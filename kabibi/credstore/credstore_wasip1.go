//go:build wasip1

package credstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"ai"
	"ai/assets"
	"github.com/mavity/rusticated/kabibi/config"
)

const browserCredentialStorageKey = "kabibi.ai.credentials.v1"

func load() (map[string]string, error) {
	if assets.HostOS() != "browser" {
		return loadFileStore()
	}
	raw, err := ai.EvalJS("JSON.stringify(localStorage.getItem(" + strconv.Quote(browserCredentialStorageKey) + ") || \"\")")
	if err != nil {
		return nil, fmt.Errorf("read browser AI credentials from localStorage: %w", err)
	}
	var stored string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, fmt.Errorf("decode browser AI credential value: %w", err)
	}
	entries := make(map[string]string)
	if stored == "" {
		return entries, nil
	}
	if err := json.Unmarshal([]byte(stored), &entries); err != nil {
		return nil, fmt.Errorf("decode browser AI credentials: %w", err)
	}
	if entries == nil {
		entries = make(map[string]string)
	}
	return entries, nil
}

func save(entries map[string]string) error {
	if assets.HostOS() != "browser" {
		return saveFileStore(entries)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode browser AI credentials: %w", err)
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("browser AI credentials exceed 1 MiB")
	}
	script := "JSON.stringify((localStorage.setItem(" + strconv.Quote(browserCredentialStorageKey) + ", " +
		strconv.Quote(string(data)) + "), \"ok\"))"
	if _, err := ai.EvalJS(script); err != nil {
		return fmt.Errorf("save browser AI credentials to localStorage: %w", err)
	}
	return nil
}

func loadFileStore() (map[string]string, error) {
	file, err := credentialPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("read AI credentials: %w", err)
	}
	var entries map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode AI credentials: %w", err)
	}
	if entries == nil {
		entries = make(map[string]string)
	}
	return entries, nil
}

func saveFileStore(entries map[string]string) error {
	file, err := credentialPath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode AI credentials: %w", err)
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	tmp := file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write temporary credentials: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temporary credentials: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync temporary credentials: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temporary credentials: %w", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace credentials: %w", err)
	}
	return nil
}

func credentialPath() (string, error) {
	dir, err := config.Directory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ai-credentials.json"), nil
}
