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

func testRuntimePlatform() RuntimePlatformInfo {
	osName, arch := testRuntimeHost()
	return RuntimePlatformInfo{OS: osName, Arch: arch, ExecutionMode: "mohabbat"}
}

func testRuntimeWheelFilename() string {
	osName, arch := testRuntimeHost()
	return runtimeWheelFilename(osName, arch)
}

func runtimeWheelFilename(osName, arch string) string {
	switch osName + "/" + arch {
	case "windows/amd64":
		return "litert_lm_api-0.19.0-py3-none-win_amd64.whl"
	case "windows/arm64":
		return "litert_lm_api-0.19.0-py3-none-win_amd64.whl"
	case "linux/amd64":
		return "litert_lm_api-0.19.0-py3-none-manylinux_2_27_x86_64.whl"
	case "linux/arm64":
		return "litert_lm_api-0.19.0-py3-none-manylinux_2_27_aarch64.whl"
	case "darwin/arm64":
		return "litert_lm_api-0.19.0-py3-none-macosx_12_0_arm64.whl"
	default:
		return ""
	}
}
