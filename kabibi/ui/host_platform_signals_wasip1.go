//go:build wasip1

package ui

// WASI runtimes do not provide OS signal delivery.
func initSignalHandlers(*Host) func() {
	return func() {}
}
