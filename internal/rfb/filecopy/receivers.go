package filecopy

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

func DirectoryReceiver(directory string) Receiver {
	return &directoryReceiver{directory: directory}
}

type folderMetadata struct {
	path     string
	mode     fs.FileMode
	accessed time.Time
	modified time.Time
}

type directoryReceiver struct {
	directory    string
	root         *os.Root
	originalName string
	localName    string
	folders      []folderMetadata
}

func (r *directoryReceiver) Item(item Item) (io.WriteCloser, error) {
	if r.root == nil {
		root, err := os.OpenRoot(r.directory)
		if err != nil {
			return nil, err
		}
		r.root = root
	}
	localPath, err := r.localPath(item)
	if err != nil {
		return nil, err
	}
	switch item.Kind {
	case Folder:
		err = r.root.Mkdir(localPath, 0o700)
		if err != nil {
			return nil, err
		}
		r.folders = append(r.folders, folderMetadata{
			path:     localPath,
			mode:     permissionsOr(item.Mode, 0o755),
			accessed: item.Accessed,
			modified: item.Modified,
		})
		return nil, nil
	case Link:
		return nil, r.root.Symlink(item.LinkTarget, localPath)
	default:
		var file *os.File
		file, err = r.root.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		return &directoryFile{
			File:     file,
			root:     r.root,
			path:     localPath,
			mode:     permissionsOr(item.Mode, 0o644),
			accessed: item.Accessed,
			modified: item.Modified,
		}, nil
	}
}

func (r *directoryReceiver) localPath(item Item) (string, error) {
	first, rest, nested := strings.Cut(item.Path, "/")
	if !nested {
		if r.localName != "" {
			return "", E.New("filecopy: a second top-level item ", strconv.Quote(item.Path))
		}
		name, err := r.freeName(first, item.Kind)
		if err != nil {
			return "", err
		}
		r.originalName, r.localName = first, name
		return name, nil
	}
	if first != r.originalName {
		return "", E.New("filecopy: item ", strconv.Quote(item.Path), " outside the transfer")
	}
	return r.localName + "/" + rest, nil
}

func (r *directoryReceiver) freeName(name string, kind Kind) (string, error) {
	extension := ""
	if kind == File {
		extension = path.Ext(name)
		if extension == name {
			extension = ""
		}
	}
	base := strings.TrimSuffix(name, extension)
	candidate := name
	for number := 2; ; number++ {
		_, err := r.root.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = fmt.Sprint(base, " ", number, extension)
	}
}

func (r *directoryReceiver) Finish(err error) error {
	if r.root == nil {
		return nil
	}
	defer r.root.Close()
	if err != nil {
		if r.localName != "" {
			r.root.RemoveAll(r.localName)
		}
		return nil
	}
	var result error
	for _, folder := range slices.Backward(r.folders) {
		result = E.Errors(result, r.root.Chmod(folder.path, folder.mode))
		result = E.Errors(result, chtimes(r.root, folder.path, folder.accessed, folder.modified))
	}
	return result
}

type directoryFile struct {
	*os.File
	root     *os.Root
	path     string
	mode     fs.FileMode
	accessed time.Time
	modified time.Time
}

func (f *directoryFile) Close() error {
	err := f.File.Close()
	if err != nil {
		return err
	}
	err = f.root.Chmod(f.path, f.mode)
	if err != nil {
		return err
	}
	return chtimes(f.root, f.path, f.accessed, f.modified)
}

func permissionsOr(mode fs.FileMode, fallback fs.FileMode) fs.FileMode {
	if mode.Perm() == 0 {
		return fallback
	}
	return mode.Perm()
}

func chtimes(root *os.Root, name string, accessed, modified time.Time) error {
	if modified.IsZero() {
		return nil
	}
	if accessed.IsZero() {
		accessed = modified
	}
	return root.Chtimes(name, accessed, modified)
}
