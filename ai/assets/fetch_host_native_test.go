//go:build !wasip1

package assets

import "runtime"

func testRuntimeHost() (string, string) {
	return runtime.GOOS, runtime.GOARCH
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
