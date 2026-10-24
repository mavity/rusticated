//go:build !windows && !wasip1

package privatefile

import (
	"fmt"
	"os"
)

func Restrict(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set private file permissions: %w", err)
	}
	return nil
}
