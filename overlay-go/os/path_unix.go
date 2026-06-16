// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip1

package os

import (
	"internal/filepathlite"
)

var (
	PathSeparator     byte = '/'
	PathListSeparator byte = ':'
)

func init() {
	// Sync from the core source
	PathSeparator = filepathlite.Separator
	PathListSeparator = filepathlite.ListSeparator
}

// IsPathSeparator reports whether c is a directory separator character.
func IsPathSeparator(c uint8) bool {
	return filepathlite.CoreIsPathSeparator(c)
}

// IsAbs reports whether the path is absolute.
func IsAbs(path string) bool {
	return filepathlite.CoreIsAbs(path)
}

// Join is a Windows/Unix aware path joiner, used by algorithmic patches.
func Join(elem []string) string {
	return filepathlite.CoreJoin(elem)
}

// VolumeNameLen returns the length of the leading volume name on Windows (e.g. "C:").
func VolumeNameLen(path string) int {
	return filepathlite.CoreVolumeNameLen(path)
}

// splitPath returns the base name and parent directory.
func splitPath(path string) (string, string) {
	if path == "" {
		return ".", ""
	}

	i := len(path) - 1

	// Remove trailing slashes.
	for i > 0 && IsPathSeparator(path[i]) {
		i--
	}
	path = path[:i+1]

	// Find the last separator.
	i = len(path) - 1
	for i >= 0 && !IsPathSeparator(path[i]) {
		i--
	}

	if i < 0 {
		return ".", path
	}
	if i == 0 {
		return path[:1], path[1:]
	}
	return path[:i], path[i+1:]
}
