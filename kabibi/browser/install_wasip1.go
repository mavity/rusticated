//go:build wasip1

package browser

import (
	"fmt"

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
