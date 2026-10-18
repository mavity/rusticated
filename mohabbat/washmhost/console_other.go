//go:build !windows

package main

func setupConsoleUtf8() func() {
	return func() {}
}
