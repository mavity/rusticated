//go:build wasip1

package browser

import (
	"fmt"
	"strconv"

	"ai"
	"ai/assets"
)

func Install() error {
	if assets.HostOS() != "browser" {
		return nil
	}
	if _, err := ai.EvalJS(mainJS); err != nil {
		return fmt.Errorf("install Kabibi browser runtime: %w", err)
	}
	mode, err := ai.EvalJS(`globalThis.KabibiBrowser && globalThis.KabibiBrowser.isSuperDemoEnabled(globalThis) ? "super-demo" : "default"`)
	if err != nil {
		return fmt.Errorf("read Kabibi browser mode: %w", err)
	}
	if mode == "super-demo" {
		if _, err := ai.EvalJS(superDemoJS); err != nil {
			return fmt.Errorf("install Kabibi super-demo runtime: %w", err)
		}
	}
	if _, err := ai.EvalJS(`globalThis.KabibiBrowser.bootFromGuest()`); err != nil {
		return fmt.Errorf("boot Kabibi browser terminal: %w", err)
	}
	return nil
}

func Available() bool {
	return assets.HostOS() == "browser"
}

func Download() error {
	if !Available() {
		return fmt.Errorf("download is only available in the browser")
	}
	if _, err := ai.EvalJS(`globalThis.KabibiBrowser.downloadLoadedArtifact()`); err != nil {
		return fmt.Errorf("download Kabibi browser artifact: %w", err)
	}
	return nil
}

// MountRequestKey is sent by the browser shortcut to open the guest TUI mount dialog.
const MountRequestKey = "\uE000"

func SelectMountDirectory() (string, error) {
	if !Available() {
		return "", fmt.Errorf("directory mounting is only available in the browser")
	}
	name, err := ai.EvalJS(`globalThis.KabibiBrowser.selectMountDirectory()`)
	if err != nil {
		return "", fmt.Errorf("select browser directory: %w", err)
	}
	return name, nil
}

func SelectedMountDirectoryName() (string, error) {
	if !Available() {
		return "", fmt.Errorf("directory mounting is only available in the browser")
	}
	name, err := ai.EvalJS(`globalThis.KabibiBrowser.selectedMountDirectoryName()`)
	if err != nil {
		return "", fmt.Errorf("read selected browser directory: %w", err)
	}
	return name, nil
}

func MountDirectory(path string) error {
	if !Available() {
		return fmt.Errorf("directory mounting is only available in the browser")
	}
	if _, err := ai.EvalJS(`globalThis.KabibiBrowser.mountSelectedDirectory(` + strconv.Quote(path) + `)`); err != nil {
		return fmt.Errorf("mount browser directory at %q: %w", path, err)
	}
	return nil
}

func CancelMountSelection() error {
	if !Available() {
		return fmt.Errorf("directory mounting is only available in the browser")
	}
	if _, err := ai.EvalJS(`globalThis.KabibiBrowser.cancelMountSelection()`); err != nil {
		return fmt.Errorf("cancel browser directory selection: %w", err)
	}
	return nil
}
