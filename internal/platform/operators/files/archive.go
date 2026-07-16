package files

import (
	"archive/tar"
	"archive/zip"

	"compress/gzip"
	"context"

	"errors"
	"fmt"

	"io"
	"io/fs"
	"math"

	"os"
	"path"

	"sort"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

func (o *HostOperator) Archive(_ context.Context, scope sitefs.Scope, rawPaths []string, rawTarget string) (ArchiveResult, error) {
	root, target, err := o.resolveMutable(scope, rawTarget)
	if err != nil {
		return ArchiveResult{}, err
	}
	defer root.Close()
	if !strings.HasSuffix(target, ".tar.gz") {
		return ArchiveResult{}, invalid("The archive target must end in .tar.gz.")
	}
	if len(rawPaths) == 0 {
		return ArchiveResult{}, invalid("At least one path is required to build an archive.")
	}
	if _, err := root.Lstat(target); err == nil {
		return ArchiveResult{}, existsConflict("An entry already exists at the target path.")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ArchiveResult{}, pathError(err, "The target path is not reachable.")
	}
	sources := make([]string, 0, len(rawPaths))
	for _, raw := range rawPaths {
		cleaned, err := sitefs.CleanRelative(raw)
		if err != nil {
			return ArchiveResult{}, invalid("The path is not allowed: " + err.Error() + ".")
		}
		if cleaned == "." {
			return ArchiveResult{}, invalid("The site root cannot be archived; choose specific paths.")
		}
		if _, err := root.Lstat(cleaned); err != nil {
			return ArchiveResult{}, pathError(err, fmt.Sprintf("The path %q does not exist.", cleaned))
		}
		sources = append(sources, cleaned)
	}
	staging := path.Join(path.Dir(target), ".nexa-stage-"+randomID())
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return ArchiveResult{}, pathError(err, "The target directory does not exist.")
	}
	discard := func(cause error) (ArchiveResult, error) {
		file.Close()
		_ = root.Remove(staging)
		return ArchiveResult{}, cause
	}
	compressor := gzip.NewWriter(file)
	writer := tar.NewWriter(compressor)
	budget := newBudget("archive", archiveMaxEntries, archiveMaxBytes)
	result := &ArchiveResult{}
	for _, source := range sources {
		if err := o.archiveTree(root, writer, source, true, budget, result); err != nil {
			return discard(err)
		}
	}
	if err := writer.Close(); err != nil {
		return discard(err)
	}
	if err := compressor.Close(); err != nil {
		return discard(err)
	}
	if err := file.Sync(); err != nil {
		return discard(err)
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(staging)
		return ArchiveResult{}, err
	}
	if err := root.Rename(staging, target); err != nil {
		_ = root.Remove(staging)
		return ArchiveResult{}, pathError(err, "The target directory does not exist.")
	}
	if err := o.chown(scope, target); err != nil {
		return ArchiveResult{}, err
	}
	info, err := root.Lstat(target)
	if err != nil {
		return ArchiveResult{}, err
	}
	result.Entry = entryFor(path.Base(target), info)
	return *result, nil
}

func (o *HostOperator) archiveTree(root *os.Root, writer *tar.Writer, source string, named bool, budget *budget, result *ArchiveResult) error {
	info, err := root.Lstat(source)
	if err != nil {
		return pathError(err, fmt.Sprintf("The path %q does not exist.", source))
	}
	switch {
	case info.Mode().IsRegular():
		return o.archiveFile(root, writer, source, info, budget, result)
	case info.IsDir():
		if err := budget.spendEntry(); err != nil {
			return err
		}
		header := &tar.Header{Name: source + "/", Typeflag: tar.TypeDir, Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		result.Entries++
		directory, err := root.Open(source)
		if err != nil {
			return err
		}
		children, err := directory.ReadDir(-1)
		directory.Close()
		if err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			// Panel staging and upload files are transient; keep them out.
			if strings.HasPrefix(child.Name(), ".nexa-") {
				continue
			}
			childInfo, err := child.Info()
			if err != nil {
				continue
			}
			if !childInfo.Mode().IsRegular() && !childInfo.IsDir() {
				continue
			}
			if err := o.archiveTree(root, writer, path.Join(source, child.Name()), false, budget, result); err != nil {
				return err
			}
		}
		return nil
	default:
		if named {
			return invalid("Only regular files and directories can be archived.")
		}
		return nil
	}
}

func (o *HostOperator) archiveFile(root *os.Root, writer *tar.Writer, source string, info fs.FileInfo, budget *budget, result *ArchiveResult) error {
	if err := budget.spendEntry(); err != nil {
		return err
	}
	if err := budget.spendBytes(info.Size()); err != nil {
		return err
	}
	header := &tar.Header{Name: source, Typeflag: tar.TypeReg, Size: info.Size(), Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	file, err := root.Open(source)
	if err != nil {
		return pathError(err, fmt.Sprintf("The path %q does not exist.", source))
	}
	defer file.Close()
	if _, err := io.CopyN(writer, file, info.Size()); err != nil {
		return fmt.Errorf("archive %s: the file changed while it was being archived", source)
	}
	result.Entries++
	result.Bytes += info.Size()
	return nil
}

func (o *HostOperator) Extract(_ context.Context, scope sitefs.Scope, rawPath, rawTargetDir string) (ExtractResult, error) {
	root, source, err := o.resolve(scope, rawPath)
	if err != nil {
		return ExtractResult{}, err
	}
	defer root.Close()
	targetDir, err := relativeMutable(rawTargetDir)
	if err != nil {
		return ExtractResult{}, err
	}
	file, info, err := openRegular(root, source)
	if err != nil {
		return ExtractResult{}, err
	}
	defer file.Close()
	if err := o.makeDirectories(scope, root, targetDir); err != nil {
		return ExtractResult{}, err
	}
	name := strings.ToLower(source)
	switch {
	case strings.HasSuffix(name, ".zip"):
		return o.extractZip(scope, root, file, info.Size(), targetDir)
	case strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz"):
		return o.extractTar(scope, root, file, targetDir)
	default:
		return ExtractResult{}, invalid("Only .zip, .tar.gz, and .tgz archives can be extracted.")
	}
}

func (o *HostOperator) extractTar(scope sitefs.Scope, root *os.Root, file io.Reader, targetDir string) (ExtractResult, error) {
	decompressor, err := gzip.NewReader(file)
	if err != nil {
		return ExtractResult{}, invalid("The archive is not a valid gzip stream.")
	}
	defer decompressor.Close()
	reader := tar.NewReader(decompressor)
	budget := newBudget("extraction", extractMaxEntries, extractMaxBytes)
	result := ExtractResult{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ExtractResult{}, invalid("The archive is corrupt and cannot be extracted.")
		}
		cleaned, err := sitefs.CleanRelative(header.Name)
		if err != nil || cleaned == "." {
			result.Skipped++
			continue
		}
		destination := path.Join(targetDir, cleaned)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := budget.spendEntry(); err != nil {
				return ExtractResult{}, err
			}
			if err := o.makeDirectories(scope, root, destination); err != nil {
				return ExtractResult{}, err
			}
			result.Written++
		case tar.TypeReg:
			if err := budget.spendEntry(); err != nil {
				return ExtractResult{}, err
			}
			if err := budget.spendBytes(header.Size); err != nil {
				return ExtractResult{}, err
			}
			if err := o.writeExtracted(scope, root, destination, reader, header.Size, fs.FileMode(header.Mode).Perm()); err != nil {
				if errors.Is(err, errSkipEntry) {
					result.Skipped++
					continue
				}
				return ExtractResult{}, err
			}
			result.Written++
		default:
			result.Skipped++
		}
	}
	return result, nil
}

func (o *HostOperator) extractZip(scope sitefs.Scope, root *os.Root, file *os.File, size int64, targetDir string) (ExtractResult, error) {
	reader, err := zip.NewReader(file, size)
	if err != nil {
		return ExtractResult{}, invalid("The archive is not a valid zip file.")
	}
	budget := newBudget("extraction", extractMaxEntries, extractMaxBytes)
	result := ExtractResult{}
	for _, entry := range reader.File {
		cleaned, err := sitefs.CleanRelative(entry.Name)
		if err != nil || cleaned == "." {
			result.Skipped++
			continue
		}
		destination := path.Join(targetDir, cleaned)
		mode := entry.Mode()
		switch {
		case mode.IsDir() || strings.HasSuffix(entry.Name, "/"):
			if err := budget.spendEntry(); err != nil {
				return ExtractResult{}, err
			}
			if err := o.makeDirectories(scope, root, destination); err != nil {
				return ExtractResult{}, err
			}
			result.Written++
		case mode.IsRegular():
			if entry.UncompressedSize64 > math.MaxInt64 {
				return ExtractResult{}, invalid("The extraction exceeds the size limit.")
			}
			declared := int64(entry.UncompressedSize64)
			if err := budget.spendEntry(); err != nil {
				return ExtractResult{}, err
			}
			if err := budget.spendBytes(declared); err != nil {
				return ExtractResult{}, err
			}
			content, err := entry.Open()
			if err != nil {
				return ExtractResult{}, invalid("The archive is corrupt and cannot be extracted.")
			}
			err = o.writeExtracted(scope, root, destination, content, declared, mode.Perm())
			content.Close()
			if err != nil {
				if errors.Is(err, errSkipEntry) {
					result.Skipped++
					continue
				}
				return ExtractResult{}, err
			}
			result.Written++
		default:
			result.Skipped++
		}
	}
	return result, nil
}

var errSkipEntry = errors.New("skip archive entry")

func (o *HostOperator) writeExtracted(scope sitefs.Scope, root *os.Root, destination string, content io.Reader, declared int64, perm fs.FileMode) error {
	if declared < 0 {
		return invalid("The archive is corrupt and cannot be extracted.")
	}
	if parent := path.Dir(destination); parent != "." {
		if err := o.makeDirectories(scope, root, parent); err != nil {
			return err
		}
	}
	existing, err := root.Lstat(destination)
	if err == nil {
		if !existing.Mode().IsRegular() {
			return errSkipEntry
		}
		if err := root.Remove(destination); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return pathError(err, "The target path is not reachable.")
	}
	if perm&0o777 == 0 {
		perm = fileMode
	}
	file, err := root.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm&0o777)
	if err != nil {
		return pathError(err, "The target directory does not exist.")
	}
	written, err := io.Copy(file, io.LimitReader(content, declared+1))
	if err != nil {
		file.Close()
		_ = root.Remove(destination)
		return err
	}
	if written > declared {
		file.Close()
		_ = root.Remove(destination)
		return invalid("The archive contents exceed their declared size.")
	}
	if err := file.Close(); err != nil {
		return err
	}
	return o.chown(scope, destination)
}
