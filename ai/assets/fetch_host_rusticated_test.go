//go:build wasip1

package assets

import "syscall"

func testRuntimeHost() (string, string) {
	info := syscall.GetPlatformInfo()
	if info == nil {
		return "", ""
	}

	osName := ""
	switch info.OSKind {
	case 1:
		osName = "windows"
	case 2:
		osName = "linux"
	case 3:
		osName = "darwin"
	}

	arch := ""
	switch info.CPUType {
	case 1:
		arch = "amd64"
	case 2:
		arch = "arm64"
	}
	return osName, arch
}

func testRuntimeWheelFilename() string {
	osName, arch := testRuntimeHost()
	return runtimeWheelFilename(osName, arch)
}

func runtimeWheelFilename(osName, arch string) string {
	switch osName + "/" + arch {
	case "windows/amd64":
		return "litert_lm_api-1.0-cp311-cp311-win_amd64.whl"
	case "windows/arm64":
		return "litert_lm_api-1.0-cp311-cp311-win_arm64.whl"
	case "linux/amd64":
		return "litert_lm_api-1.0-cp311-cp311-manylinux_2_17_x86_64.whl"
	case "linux/arm64":
		return "litert_lm_api-1.0-cp311-cp311-manylinux_2_17_aarch64.whl"
	case "darwin/amd64":
		return "litert_lm_api-1.0-cp311-cp311-macosx_10_9_x86_64.whl"
	case "darwin/arm64":
		return "litert_lm_api-1.0-cp311-cp311-macosx_14_0_arm64.whl"
	default:
		return ""
	}
}
