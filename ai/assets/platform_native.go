//go:build !wasip1

package assets

import "runtime"

// HostOS returns the native host operating system.
func HostOS() string { return runtime.GOOS }

// HostArch returns the native host CPU architecture.
func HostArch() string { return runtime.GOARCH }

// InspectRuntimePlatform returns environmental details for native Go builds.
func InspectRuntimePlatform() RuntimePlatformInfo {
	return RuntimePlatformInfo{
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		ExecutionMode:   "native",
		HostRuntime:     "native",
		EnvironmentKind: "desktop",
	}
}
