package shell

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/adrianliechti/go-git"
)

// osFS implements git.FS on top of the host or WASM OS filesystem.
// It maps io/fs names ("dir/file", "." for root) back to real OS paths relative to baseDir.
type osFS struct {
	baseDir string // Clean absolute path of the root
}

var (
	_ git.FS       = (*osFS)(nil)
	_ fs.StatFS    = (*osFS)(nil)
	_ fs.ReadDirFS = (*osFS)(nil)
)

func newOSFS(baseDir string) *osFS {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		abs = filepath.Clean(baseDir)
	}
	return &osFS{baseDir: abs}
}

func newHostFS(workingDir string) (*osFS, string, error) {
	absDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve git working directory %q: %w", workingDir, err)
	}
	absDir = filepath.Clean(absDir)

	baseDir := string(filepath.Separator)
	virtualDir := filepath.ToSlash(absDir)
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(absDir)
		if volume == "" {
			return nil, "", fmt.Errorf("git working directory %q has no Windows volume", absDir)
		}
		baseDir = volume + string(filepath.Separator)
		relative := strings.TrimPrefix(absDir, volume)
		virtualDir = "/" + strings.Trim(filepath.ToSlash(relative), "/")
	}
	return &osFS{baseDir: baseDir}, path.Clean("/" + strings.TrimPrefix(virtualDir, "/")), nil
}

func (o *osFS) virtualPath(name string) string {
	normalized := strings.ReplaceAll(name, `\`, "/")
	return path.Clean("/" + strings.TrimPrefix(normalized, "/"))
}

func (o *osFS) toVirtualPath(hostPath string) (string, error) {
	absPath, err := filepath.Abs(hostPath)
	if err != nil {
		return "", fmt.Errorf("resolve Git filesystem path %q: %w", hostPath, err)
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.VolumeName(absPath), filepath.VolumeName(o.baseDir)) {
		return "", fmt.Errorf("Git path %q is outside filesystem volume %q", absPath, filepath.VolumeName(o.baseDir))
	}
	relative, err := filepath.Rel(o.baseDir, absPath)
	if err != nil {
		return "", fmt.Errorf("map Git path %q: %w", absPath, err)
	}
	return o.virtualPath(filepath.ToSlash(relative)), nil
}

// toOSPath converts an io/fs relative name (or "." for root) to a host filesystem path.
func (o *osFS) toOSPath(name string) string {
	virtual := o.virtualPath(name)
	if virtual == "/" {
		return o.baseDir
	}
	cleanRel := filepath.FromSlash(strings.TrimPrefix(virtual, "/"))
	return filepath.Join(o.baseDir, cleanRel)
}

func (o *osFS) Open(name string) (fs.File, error) {
	return os.Open(o.toOSPath(name))
}

func (o *osFS) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(o.toOSPath(name))
}

func (o *osFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(o.toOSPath(name))
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
