package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mavity/rusticated/platform"
)

func TestAIConfigDefaultIsUnconfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	cfg, err := LoadAI()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Configured || cfg.ConsentGiven {
		t.Fatalf("missing config should not imply consent: %+v", cfg)
	}
}

func TestAIConfigRoundTripAndPrivatePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	want := AIConfig{
		Configured:                   true,
		Mode:                         "remote",
		Provider:                     "openrouter",
		Model:                        "openai/gpt-6-luna",
		ContextTokens:                128000,
		LocalEmbeddingConsentVersion: 1,
		ConsentGiven:                 true,
	}
	if err := SaveAI(want); err != nil {
		t.Fatal(err)
	}

	got, err := LoadAI()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("LoadAI() = %+v, want %+v", got, want)
	}
	path, err := AIConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if platform.HostOS() != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("ai.json permissions = %o, want 600", info.Mode().Perm())
	}
	if filepath.Ext(path) != ".json" {
		t.Fatalf("AI config path = %s, want .json", path)
	}
}

func TestAIConfigIgnoresLegacyEmbedderPreference(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path, err := AIConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"configured":true,"mode":"local","tier_id":"gemma-4-e2b","embedder":false,"consent_given":true}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAI()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Configured || cfg.Mode != "local" || cfg.LocalEmbeddingConsentVersion != 0 {
		t.Fatalf("legacy config migration = %+v, want configured local config without embedding consent version", cfg)
	}
	if err := SaveAI(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"embedder"`) {
		t.Fatalf("saved config retained legacy embedder preference: %s", data)
	}
}

func TestAIConfigRejectsMalformedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path, err := AIConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAI(); err == nil {
		t.Fatal("malformed config was accepted")
	}
}
