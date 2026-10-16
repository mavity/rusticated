//go:build windows

package shell

func isRoot() bool {
	// On Windows, the typical user is not root / UID 0
	return false
}
