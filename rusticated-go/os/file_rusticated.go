//go:build wasip1

package os

import (
	"errors"
	"syscall"
)

// UserHomeDir returns the host user home directory provided once by the ABI.
func UserHomeDir() (string, error) {
	pi := syscall.GetPlatformInfo()
	if pi == nil || pi.UserHomeDir == "" {
		return "", errors.New("user home dir is not defined")
	}
	return pi.UserHomeDir, nil
}

// TempDir returns the host temp directory provided once by the ABI.
func TempDir() string {
	pi := syscall.GetPlatformInfo()
	if pi == nil || pi.TempDir == "" {
		return ""
	}
	return pi.TempDir
}
