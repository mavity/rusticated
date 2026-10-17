package shell

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrianliechti/go-git"
)

// osFS implements git.FS on top of the host or WASM OS filesystem.
// It maps io/fs names ("dir/file", "." for root) back to real OS paths relative to baseDir.
type osFS struct {
	baseDir string // Clean absolute path of the root
}

var _ git.FS = (*osFS)(nil)

func newOSFS(baseDir string) *osFS {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		abs = filepath.Clean(baseDir)
	}
	return &osFS{baseDir: abs}
}

// toOSPath converts an io/fs relative name (or "." for root) to a host filesystem path.
func (o *osFS) toOSPath(name string) string {
	if name == "." || name == "" {
		return o.baseDir
	}
	cleanRel := filepath.FromSlash(strings.TrimPrefix(name, "/"))
	return filepath.Join(o.baseDir, cleanRel)
}

func (o *osFS) Open(name string) (fs.File, error) {
	return os.Open(o.toOSPath(name))
}

func (o *osFS) OpenFile(name string, flag int, perm fs.FileMode) (git.File, error) {
	osPath := o.toOSPath(name)
	if flag&os.O_CREATE != 0 {
		if err := os.MkdirAll(filepath.Dir(osPath), 0777); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(osPath, flag, perm)
	if err != nil {
		return nil, err
	}
	return &osFile{File: f}, nil
}

func (o *osFS) Mkdir(name string, perm fs.FileMode) error {
	return os.Mkdir(o.toOSPath(name), perm)
}

func (o *osFS) Remove(name string) error {
	return os.Remove(o.toOSPath(name))
}

func (o *osFS) Rename(oldName, newName string) error {
	newPath := o.toOSPath(newName)
	if err := os.MkdirAll(filepath.Dir(newPath), 0777); err != nil {
		return err
	}
	return os.Rename(o.toOSPath(oldName), newPath)
}

type osFile struct {
	*os.File
}

var (
	_ git.File       = (*osFile)(nil)
	_ io.Seeker      = (*osFile)(nil)
	_ io.ReaderAt    = (*osFile)(nil)
	_ fs.ReadDirFile = (*osFile)(nil)
)
