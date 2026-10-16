//go:build !windows

package shell

import "os"

func isRoot() bool {
	return os.Geteuid() == 0
}
