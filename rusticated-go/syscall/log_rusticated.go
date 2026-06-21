//go:build !verbose

package syscall

func debugPrintln(args ...interface{}) {
	// Disabled in normal builds
}
