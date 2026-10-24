package credstore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mavity/rusticated/kabibi/config"
)

func TestSetGetDeleteAndPrivatePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := Set("openai", "default", "secret-value"); err != nil {
		t.Fatal(err)
	}
	got, err := Get("openai", "default")
	if err != nil || got != "secret-value" {
		t.Fatalf("Get() = %q, %v", got, err)
	}
	dir, err := config.Directory()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "ai-credentials.json")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential permissions = %o, want 600", info.Mode().Perm())
	}
	if err := Delete("openai", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("openai", "default"); !os.IsNotExist(err) {
		t.Fatalf("Get after Delete error = %v, want os.ErrNotExist", err)
	}
}

func TestCredentialOperationsRejectEmptyIdentifiers(t *testing.T) {
	if err := Set("", "default", "secret"); err == nil {
		t.Fatal("Set accepted an empty provider")
	}
	if _, err := Get("openai", ""); err == nil {
		t.Fatal("Get accepted an empty account")
	}
	if err := Delete("", "default"); err == nil {
		t.Fatal("Delete accepted an empty provider")
	}
}

func TestCredentialStoreReportsMalformedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "kabibi"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "kabibi", "ai-credentials.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("openai", "default"); err == nil {
		t.Fatal("malformed credential file was accepted")
	}
}
