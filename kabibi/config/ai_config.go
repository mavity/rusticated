package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mavity/rusticated/kabibi/internal/privatefile"
)

type AIConfig struct {
	Configured   bool   `json:"configured"`
	Mode         string `json:"mode,omitempty"`
	TierID       string `json:"tier_id,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	Embedder     bool   `json:"embedder,omitempty"`
	ConsentGiven bool   `json:"consent_given,omitempty"`
}

func Directory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("user home directory is empty")
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Local", "kabibi"), nil
	}
	return filepath.Join(home, ".config", "kabibi"), nil
}

func AIConfigPath() (string, error) {
	dir, err := Directory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ai.json"), nil
}

func LoadAI() (AIConfig, error) {
	path, err := AIConfigPath()
	if err != nil {
		return AIConfig{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AIConfig{}, nil
		}
		return AIConfig{}, fmt.Errorf("read AI config: %w", err)
	}
	var cfg AIConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AIConfig{}, fmt.Errorf("decode AI config %s: %w", path, err)
	}
	return cfg, nil
}

func SaveAI(cfg AIConfig) error {
	path, err := AIConfigPath()
	if err != nil {
		return err
	}
	if err := writePrivateJSON(path, cfg); err != nil {
		return fmt.Errorf("save AI config: %w", err)
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	if err := privatefile.Restrict(tmp); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
