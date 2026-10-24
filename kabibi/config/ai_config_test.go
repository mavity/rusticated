package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
		Configured:   true,
		Mode:         "local",
		TierID:       "gemma-4-e2b",
		Embedder:     true,
		ConsentGiven: true,
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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("ai.json permissions = %o, want 600", info.Mode().Perm())
	}
	if filepath.Ext(path) != ".json" {
		t.Fatalf("AI config path = %s, want .json", path)
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
