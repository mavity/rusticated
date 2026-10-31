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

const MountRequestKey = "\uE000"

func SelectMountDirectory() (string, error) {
	return "", fmt.Errorf("directory mounting is only available in the browser")
}

func SelectedMountDirectoryName() (string, error) {
	return "", fmt.Errorf("directory mounting is only available in the browser")
}

func MountDirectory(string) error {
	return fmt.Errorf("directory mounting is only available in the browser")
}

func CancelMountSelection() error {
	return fmt.Errorf("directory mounting is only available in the browser")
}
