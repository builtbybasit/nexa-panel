package logs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// HostOperator serves bounded reads of the files beneath a site's logs/
// directory. It never mutates anything and never loads more than one
// MaxBytes window (plus one sentinel byte) into memory.
type HostOperator struct {
	SiteRoot string
	now      func() time.Time
}

var _ Operator = (*HostOperator)(nil)

func NewHostOperator(siteRoot string) (*HostOperator, error) {
	if siteRoot == "" {
		siteRoot = "/srv/nexa/sites"
	}
	if !filepath.IsAbs(siteRoot) {
		return nil, errors.New("logs site root must be absolute")
	}
	return &HostOperator{SiteRoot: filepath.Clean(siteRoot), now: time.Now}, nil
}

var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// cleanName confines a client-supplied log name to the logs/ directory: it
// must be relative, free of traversal, and every segment must be a plain
// visible file name (hidden entries are never served).
func cleanName(raw string) (string, error) {
	cleaned, err := sitefs.CleanRelative(raw)
	if err != nil {
		return "", invalid("The log name is not allowed: " + err.Error() + ".")
	}
	if cleaned == "." {
		return "", invalid("A log file name is required.")
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if !segmentPattern.MatchString(segment) {
			return "", invalid("Log names may only contain letters, digits, dots, dashes, and underscores, and may not be hidden.")
		}
	}
	return cleaned, nil
}

// openScope validates the scope and confines all resolution beneath the site
// root. Callers must Close the returned root.
func (o *HostOperator) openScope(scope sitefs.Scope) (*os.Root, error) {
	if err := sitefs.Validate(scope, o.SiteRoot); err != nil {
		return nil, invalid("The site scope is invalid: " + err.Error() + ".")
	}
	root, err := sitefs.OpenRoot(scope)
	if err != nil {
		return nil, notFound("The site directory is not present on this node.")
	}
	return root, nil
}

func (o *HostOperator) List(_ context.Context, scope sitefs.Scope) (Listing, error) {
	root, err := o.openScope(scope)
	if err != nil {
		return Listing{}, err
	}
	defer root.Close()
	listing := Listing{Items: []Entry{}}
	if _, err := root.Lstat("logs"); errors.Is(err, fs.ErrNotExist) {
		// A site that has not written logs yet simply has nothing to show.
		return listing, nil
	}
	var walk func(directory, prefix string)
	walk = func(directory, prefix string) {
		handle, err := root.Open(directory)
		if err != nil {
			return
		}
		children, err := handle.ReadDir(-1)
		handle.Close()
		if err != nil {
			return
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if len(listing.Items) == ListMaxEntries {
				return
			}
			switch {
			case child.Type().IsDir():
				walk(path.Join(directory, child.Name()), prefix+child.Name()+"/")
			case child.Type().IsRegular():
				info, err := child.Info()
				if err != nil {
					continue
				}
				listing.Items = append(listing.Items, Entry{Name: prefix + child.Name(), Size: info.Size(), ModifiedAt: info.ModTime().UTC()})
			}
		}
	}
	walk("logs", "")
	return listing, nil
}

func (o *HostOperator) Read(_ context.Context, scope sitefs.Scope, request ReadRequest) (ReadResult, error) {
	name, err := cleanName(request.Name)
	if err != nil {
		return ReadResult{}, err
	}
	if request.FromOffset != nil && *request.FromOffset < 0 {
		return ReadResult{}, invalid("The read offset must not be negative.")
	}
	maxBytes := clamp(request.MaxBytes, ReadDefaultMaxBytes, ReadMaxBytes)
	tailLines := clamp(request.TailLines, ReadDefaultTail, ReadMaxTail)
	root, err := o.openScope(scope)
	if err != nil {
		return ReadResult{}, err
	}
	defer root.Close()
	file, info, err := openLog(root, name)
	if err != nil {
		return ReadResult{}, err
	}
	defer file.Close()
	if request.FromOffset == nil {
		return tailRead(file, info.Size(), maxBytes, tailLines, request.Filter)
	}
	return incrementalRead(file, info.Size(), *request.FromOffset, maxBytes, request.Filter)
}

func (o *HostOperator) Download(_ context.Context, scope sitefs.Scope, rawName string) (io.ReadCloser, Entry, error) {
	name, err := cleanName(rawName)
	if err != nil {
		return nil, Entry{}, err
	}
	root, err := o.openScope(scope)
	if err != nil {
		return nil, Entry{}, err
	}
	defer root.Close()
	file, info, err := openLog(root, name)
	if err != nil {
		return nil, Entry{}, err
	}
	return file, Entry{Name: name, Size: info.Size(), ModifiedAt: info.ModTime().UTC()}, nil
}

// tailRead returns the last complete lines of the file: the last maxBytes
// window is read, any leading partial line is dropped, and the newest
// tailLines matching lines are kept. NextOffset is the current file size so
// a follow-up incremental read resumes exactly where the tail stopped.
func tailRead(file io.ReaderAt, size int64, maxBytes, tailLines int, filter string) (ReadResult, error) {
	result := ReadResult{Lines: []string{}, NextOffset: size, Size: size}
	start := size - int64(maxBytes)
	if start < 0 {
		start = 0
	}
	result.Truncated = start > 0
	if size == 0 {
		return result, nil
	}
	// One sentinel byte before the window tells a partial first line (which
	// must be dropped) apart from a window starting exactly on a line.
	readFrom := start
	if start > 0 {
		readFrom = start - 1
	}
	data := make([]byte, size-readFrom)
	if _, err := io.ReadFull(io.NewSectionReader(file, readFrom, size-readFrom), data); err != nil {
		return ReadResult{}, err
	}
	if start > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			// The whole window is the tail of one oversized line.
			return result, nil
		}
		data = data[newline+1:]
	}
	lines := filterLines(splitLines(data), filter)
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	result.Lines = lines
	return result, nil
}

// incrementalRead returns the complete lines in [offset, offset+maxBytes).
// NextOffset advances past the last complete line only, so a partial line
// still being written is re-read once it gains its newline. A file now
// smaller than the offset was rotated: the caller restarts from zero.
func incrementalRead(file io.ReaderAt, size, offset int64, maxBytes int, filter string) (ReadResult, error) {
	result := ReadResult{Lines: []string{}, NextOffset: offset, Size: size}
	if size < offset {
		result.Rotated = true
		result.NextOffset = 0
		return result, nil
	}
	window := size - offset
	if window > int64(maxBytes) {
		window = int64(maxBytes)
		result.Truncated = true
	}
	if window == 0 {
		return result, nil
	}
	data := make([]byte, window)
	if _, err := io.ReadFull(io.NewSectionReader(file, offset, window), data); err != nil {
		return ReadResult{}, err
	}
	newline := bytes.LastIndexByte(data, '\n')
	if newline < 0 {
		return result, nil
	}
	result.NextOffset = offset + int64(newline) + 1
	result.Lines = filterLines(splitLines(data[:newline+1]), filter)
	return result, nil
}

// splitLines splits on \n, tolerating CRLF line endings. A trailing piece
// without a newline is kept as a (partial) line; callers that must not see
// partial lines cut the buffer at the last newline first.
func splitLines(data []byte) []string {
	lines := []string{}
	if len(data) == 0 {
		return lines
	}
	pieces := strings.Split(string(data), "\n")
	if pieces[len(pieces)-1] == "" {
		pieces = pieces[:len(pieces)-1]
	}
	for _, piece := range pieces {
		lines = append(lines, strings.TrimSuffix(piece, "\r"))
	}
	return lines
}

func filterLines(lines []string, filter string) []string {
	if filter == "" {
		return lines
	}
	needle := strings.ToLower(filter)
	matched := []string{}
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), needle) {
			matched = append(matched, line)
		}
	}
	return matched
}

func clamp(value, fallback, ceiling int) int {
	if value <= 0 {
		return fallback
	}
	if value > ceiling {
		return ceiling
	}
	return value
}

// openLog opens a log file for reading, refusing symbolic links and any
// other non-regular entry both before and after the open.
func openLog(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	relative := path.Join("logs", name)
	info, err := root.Lstat(relative)
	if err != nil {
		return nil, nil, pathError(err, "The requested log file does not exist.")
	}
	if !info.Mode().IsRegular() {
		return nil, nil, invalid("Only regular log files can be read.")
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, nil, pathError(err, "The requested log file does not exist.")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		file.Close()
		return nil, nil, invalid("Only regular log files can be read.")
	}
	return file, opened, nil
}

func pathError(err error, missing string) error {
	var operationErr *OperationError
	if errors.As(err, &operationErr) {
		return operationErr
	}
	if errors.Is(err, fs.ErrNotExist) {
		return notFound(missing)
	}
	if strings.Contains(err.Error(), "escapes from parent") {
		return invalid("The path escapes the site root.")
	}
	return err
}
