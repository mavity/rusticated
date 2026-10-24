package gitfs

import (
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/helper/chroot"
	"github.com/go-git/go-billy/v5/osfs"
)

type pathAwareOS struct {
	*osfs.ChrootOS
}

func New(root string) billy.Filesystem {
	return chroot.New(&pathAwareOS{ChrootOS: &osfs.ChrootOS{}}, root)
}

func (fs *pathAwareOS) Join(elem ...string) string {
	if len(elem) == 0 {
		return "."
	}

	joined := elem[0]
	for _, part := range elem[1:] {
		if filepath.IsAbs(part) || strings.HasPrefix(part, "/") || strings.HasPrefix(part, `\`) {
			part = strings.TrimLeft(part, `/\`)
		}
		if part == "" {
			continue
		}
		if joined == "" {
			joined = part
		} else {
			joined = filepath.Join(joined, part)
		}
	}
	return filepath.Clean(joined)
}
