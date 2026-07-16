package files

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"

	"io"
	"io/fs"

	"os"
	"path"

	"regexp"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

const uploadPrefix = "tmp/.nexa-upload-"

var uploadIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type uploadMeta struct {
	Target    string    `json:"target"`
	Size      int64     `json:"size"`
	Overwrite bool      `json:"overwrite"`
	CreatedAt time.Time `json:"createdAt"`
}

func (o *HostOperator) UploadBegin(_ context.Context, scope sitefs.Scope, rawPath string, size int64, overwrite bool) (Upload, error) {
	root, target, err := o.resolveMutable(scope, rawPath)
	if err != nil {
		return Upload{}, err
	}
	defer root.Close()
	if size < 0 || size > uploadMaxBytes {
		return Upload{}, invalid("Upload size must be between 0 bytes and 2 GiB.")
	}
	if err := checkUploadTarget(root, target, overwrite); err != nil {
		return Upload{}, err
	}
	o.cleanStaleUploads(root)
	if _, err := root.Lstat("tmp"); errors.Is(err, fs.ErrNotExist) {
		if err := root.Mkdir("tmp", 0o700); err != nil {
			return Upload{}, err
		}
		if err := o.chown(scope, "tmp"); err != nil {
			return Upload{}, err
		}
	}
	id := randomID()
	staging := uploadPrefix + id
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Upload{}, err
	}
	if err := file.Close(); err != nil {
		return Upload{}, err
	}
	if err := o.chown(scope, staging); err != nil {
		return Upload{}, err
	}
	encoded, err := json.Marshal(uploadMeta{Target: target, Size: size, Overwrite: overwrite, CreatedAt: o.now().UTC()})
	if err != nil {
		return Upload{}, err
	}
	if err := root.WriteFile(staging+".meta", encoded, 0o600); err != nil {
		_ = root.Remove(staging)
		return Upload{}, err
	}
	if err := o.chown(scope, staging+".meta"); err != nil {
		return Upload{}, err
	}
	return Upload{UploadID: id}, nil
}

func (o *HostOperator) UploadChunk(_ context.Context, scope sitefs.Scope, uploadID string, offset int64, body io.Reader) error {
	root, _, err := o.resolve(scope, ".")
	if err != nil {
		return err
	}
	defer root.Close()
	staging, meta, err := loadUpload(root, uploadID)
	if err != nil {
		return err
	}
	if offset < 0 {
		return invalid("The chunk offset must not be negative.")
	}
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return notFound("The upload session does not exist or has expired.")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != offset {
		return &OperationError{Code: CodeOffsetConflict, Message: fmt.Sprintf("The upload is at offset %d; resend the chunk from there.", info.Size()), Offset: info.Size()}
	}
	remaining := meta.Size - offset
	written, err := io.Copy(file, io.LimitReader(body, min(remaining, ChunkMaxBytes)))
	if err != nil {
		return err
	}
	var probe [1]byte
	if extra, _ := io.ReadFull(body, probe[:]); extra > 0 {
		// A rejected chunk leaves no trace so the client can retry from the
		// reported offset.
		_ = file.Truncate(offset)
		if written >= remaining {
			return invalid("The upload exceeds its declared size.")
		}
		return invalid("Upload chunks must be 8 MiB or smaller.")
	}
	return nil
}

func (o *HostOperator) UploadCommit(_ context.Context, scope sitefs.Scope, uploadID string) (Entry, error) {
	root, _, err := o.resolve(scope, ".")
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	staging, meta, err := loadUpload(root, uploadID)
	if err != nil {
		return Entry{}, err
	}
	if err := mutable(meta.Target); err != nil {
		return Entry{}, err
	}
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return Entry{}, notFound("The upload session does not exist or has expired.")
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return Entry{}, err
	}
	if info.Size() != meta.Size {
		file.Close()
		return Entry{}, invalid(fmt.Sprintf("The upload holds %d of %d declared bytes; upload the rest before committing.", info.Size(), meta.Size))
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return Entry{}, err
	}
	if err := file.Close(); err != nil {
		return Entry{}, err
	}
	if err := checkUploadTarget(root, meta.Target, meta.Overwrite); err != nil {
		return Entry{}, err
	}
	if err := root.Chmod(staging, fileMode); err != nil {
		return Entry{}, err
	}
	if err := root.Rename(staging, meta.Target); err != nil {
		return Entry{}, pathError(err, "The target directory no longer exists.")
	}
	_ = root.Remove(staging + ".meta")
	if err := o.chown(scope, meta.Target); err != nil {
		return Entry{}, err
	}
	final, err := root.Lstat(meta.Target)
	if err != nil {
		return Entry{}, err
	}
	return entryFor(path.Base(meta.Target), final), nil
}

func (o *HostOperator) UploadAbort(_ context.Context, scope sitefs.Scope, uploadID string) error {
	root, _, err := o.resolve(scope, ".")
	if err != nil {
		return err
	}
	defer root.Close()
	if !uploadIDPattern.MatchString(uploadID) {
		return invalid("The upload ID is invalid.")
	}
	staging := uploadPrefix + uploadID
	if err := root.Remove(staging); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := root.Remove(staging + ".meta"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (o *HostOperator) Download(_ context.Context, scope sitefs.Scope, rawPath string) (io.ReadCloser, Entry, error) {
	root, relative, err := o.resolve(scope, rawPath)
	if err != nil {
		return nil, Entry{}, err
	}
	defer root.Close()
	file, info, err := openRegular(root, relative)
	if err != nil {
		return nil, Entry{}, err
	}
	return file, entryFor(path.Base(relative), info), nil
}

func checkUploadTarget(root *os.Root, target string, overwrite bool) error {
	info, err := root.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return pathError(err, "The target path is not reachable.")
	}
	if err := requireRegular(info, "replaced by an upload"); err != nil {
		return err
	}
	if !overwrite {
		return existsConflict("A file already exists at the target path.")
	}
	return nil
}

func loadUpload(root *os.Root, uploadID string) (string, uploadMeta, error) {
	if !uploadIDPattern.MatchString(uploadID) {
		return "", uploadMeta{}, invalid("The upload ID is invalid.")
	}
	staging := uploadPrefix + uploadID
	encoded, err := root.ReadFile(staging + ".meta")
	if err != nil {
		return "", uploadMeta{}, notFound("The upload session does not exist or has expired.")
	}
	var meta uploadMeta
	if err := json.Unmarshal(encoded, &meta); err != nil {
		return "", uploadMeta{}, notFound("The upload session does not exist or has expired.")
	}
	return staging, meta, nil
}

func (o *HostOperator) cleanStaleUploads(root *os.Root) {
	tmp, err := root.Open("tmp")
	if err != nil {
		return
	}
	defer tmp.Close()
	children, err := tmp.ReadDir(-1)
	if err != nil {
		return
	}
	cutoff := o.now().Add(-uploadStaleAfter)
	for _, child := range children {
		if !strings.HasPrefix(child.Name(), ".nexa-upload-") {
			continue
		}
		info, err := child.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = root.Remove("tmp/" + child.Name())
	}
}
