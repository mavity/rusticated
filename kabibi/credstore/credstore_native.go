//go:build !wasip1

package credstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mavity/rusticated/kabibi/config"
	"github.com/mavity/rusticated/kabibi/internal/privatefile"
)

func load() (map[string]string, error) {
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

func save(entries map[string]string) error {
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
	if err := privatefile.Restrict(tmp); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
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
