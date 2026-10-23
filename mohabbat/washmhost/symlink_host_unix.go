//go:build !windows

package main

import "os"

func createHostSymlink(target, linkPath string, targetKind uint32) error {
	return os.Symlink(target, linkPath)
}
