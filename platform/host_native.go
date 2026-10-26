//go:build !wasip1

package platform

import "runtime"

// HostOS returns the host operating system.
func HostOS() string { return runtime.GOOS }

// HostArch returns the host CPU architecture.
func HostArch() string { return runtime.GOARCH }
