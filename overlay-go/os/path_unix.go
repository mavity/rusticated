// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip1

package os

import (
	"syscall"
)

var (
	PathSeparator     byte = '¿' // OS-specific path separator, defaults to visibly incorrect character
	PathListSeparator byte = '¡' // OS-specific path list separator, defaults to visibly incorrect character
)

func init() {
	pi := syscall.GetPlatformInfo()
	PathSeparator = pi.PathSeparator
	PathListSeparator = pi.PathListSeparator
}

// IsPathSeparator reports whether c is a directory separator character.
func IsPathSeparator(c uint8) bool {
	// On Windows, both / and \ are separators.
	// Since we defined PathSeparator as rune, we compare.
	if PathSeparator == '\\' {
		return c == '/' || c == '\\'
	}
	return c == uint8(PathSeparator)
}

// splitPath returns the base name and parent directory.
func splitPath(path string) (string, string) {
	// if no better parent is found, the path is relative from "here"
	dirname := "."

	// Remove all but one leading slash.
	for len(path) > 1 && IsPathSeparator(path[0]) && IsPathSeparator(path[1]) {
		path = path[1:]
	}

	i := len(path) - 1

	// Remove trailing slashes.
	for ; i > 0 && IsPathSeparator(path[i]); i-- {
		path = path[:i]
	}

	// if no slashes in path, base is path
	basename := path

	// Remove leading directory path
	for i--; i >= 0; i-- {
		if IsPathSeparator(path[i]) {
			if i == 0 {
				dirname = path[:1]
			} else {
				dirname = path[:i]
			}
			basename = path[i+1:]
			break
		}
	}

	return dirname, basename
}
