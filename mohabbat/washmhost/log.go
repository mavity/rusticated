//go:build !verbose

package main

func debugLog(format string, a ...any) {
	// Disabled in normal builds
}

func debugLogf(format string, a ...any) {
	// Disabled in normal builds
}

func debugPrintln(a ...any) {
	// Disabled in normal builds
}
