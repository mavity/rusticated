//go:build wasip1

package exec

import (
	"errors"
	"strings"
)

var ErrNotFound = errors.New("rusticated: executable not found in $PATH")

func lookPath(file string) (string, error) {
	if strings.Contains(file, "/") || strings.Contains(file, "\\") {
		return file, nil
	}
	return file, nil
}

func lookExtensions(path, dir string) (string, error) {
	return path, nil
}
