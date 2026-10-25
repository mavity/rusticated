//go:build !wasip1

package browser

import "fmt"

func Install() error {
	return nil
}

func Available() bool {
	return false
}

func Download() error {
	return fmt.Errorf("download is only available in the browser")
}
