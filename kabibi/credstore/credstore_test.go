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
	dir, err := config.Directory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ai-credentials.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("openai", "default"); err == nil {
		t.Fatal("malformed credential file was accepted")
	}
}

func TestModifyReadAndListStructuredOAuthCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	ctx := t.Context()
	stored, err := Modify(ctx, "anthropic", func(current *Credential) (*Credential, error) {
		if current != nil {
			t.Fatalf("expected nil initial credential, got %+v", current)
		}
		return &Credential{
			Type:      CredentialOAuth,
			Access:    "sk-ant-oat-access-1",
			Refresh:   "refresh-1",
			ExpiresAt: 123456789,
			Metadata:  map[string]any{"account": "pro"},
		}, nil
	})
	if err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	if stored == nil || stored.Access != "sk-ant-oat-access-1" || stored.Refresh != "refresh-1" {
		t.Fatalf("Modify() returned %+v", stored)
	}

	// No-op Modify when callback returns nil
	unchanged, err := Modify(ctx, "anthropic", func(current *Credential) (*Credential, error) {
		if current == nil || current.Refresh != "refresh-1" {
			t.Fatalf("unexpected current credential in Modify: %+v", current)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Modify(nil) error = %v", err)
	}
	if unchanged == nil || unchanged.Access != "sk-ant-oat-access-1" {
		t.Fatalf("Modify(nil) returned %+v", unchanged)
	}

	readBack, err := Read(ctx, "anthropic")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if readBack == nil || readBack.Type != CredentialOAuth || readBack.Metadata["account"] != "pro" {
		t.Fatalf("Read() = %+v", readBack)
	}

	infos, err := List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(infos) != 1 || infos[0].ProviderID != "anthropic" || infos[0].Type != CredentialOAuth {
		t.Fatalf("List() = %+v", infos)
	}

	if err := Remove(ctx, "anthropic"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	after, err := Read(ctx, "anthropic")
	if err != nil || after != nil {
		t.Fatalf("Read after Remove = %+v, %v", after, err)
	}
}

