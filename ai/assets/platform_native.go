//go:build !wasip1

package assets

import "github.com/mavity/rusticated/platform"

// HostOS returns the host operating system.
func HostOS() string { return platform.HostOS() }

// HostArch returns the host CPU architecture.
func HostArch() string { return platform.HostArch() }

// InspectRuntimePlatform returns environmental details for native Go builds.
func InspectRuntimePlatform() RuntimePlatformInfo {
	return RuntimePlatformInfo{
		OS:              platform.HostOS(),
		Arch:            platform.HostArch(),
		ExecutionMode:   "native",
		HostRuntime:     "native",
		EnvironmentKind: "desktop",
	}
}
