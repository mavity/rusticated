package gitcore

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthenticatedHTTPClientUsesEnvironmentToken(t *testing.T) {
	server := newAuthTestServer(t, "x-access-token", "test-token")
	client := NewAuthenticatedHTTPClient(map[string]string{"GITHUB_TOKEN": "test-token"})
	assertAuthenticatedRequest(t, client, server.URL, "x-access-token", "test-token")
}

func TestAuthenticatedHTTPClientReadsGitCredentials(t *testing.T) {
	home := t.TempDir()
	server := newAuthTestServer(t, "alice", "git-password")
	if err := os.WriteFile(
		filepath.Join(home, ".git-credentials"),
		[]byte(fmt.Sprintf("https://alice:git-password@%s\n", server.Listener.Addr().String())),
		0o600,
	); err != nil {
		t.Fatalf("write .git-credentials: %v", err)
	}
	client := NewAuthenticatedHTTPClient(map[string]string{"HOME": home})
	assertAuthenticatedRequest(t, client, server.URL, "alice", "git-password")
}

func TestAuthenticatedHTTPClientReadsMultilineNetrc(t *testing.T) {
	home := t.TempDir()
	server := newAuthTestServer(t, "bob", "netrc password")
	if err := os.WriteFile(
		filepath.Join(home, ".netrc"),
		[]byte(fmt.Sprintf("machine %s\n  login bob\n  password \"netrc password\"\n", server.Listener.Addr().String())),
		0o600,
	); err != nil {
		t.Fatalf("write .netrc: %v", err)
	}
	client := NewAuthenticatedHTTPClient(map[string]string{"HOME": home})
	assertAuthenticatedRequest(t, client, server.URL, "bob", "netrc password")
}

func TestAuthenticatedHTTPClientPreservesExistingAuthorization(t *testing.T) {
	server := newAuthTestServer(t, "existing", "header")
	client := NewAuthenticatedHTTPClient(map[string]string{"GITHUB_TOKEN": "should-not-replace"})
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	request.SetBasicAuth("existing", "header")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func newAuthTestServer(t *testing.T, wantUser, wantPassword string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != wantUser || password != wantPassword {
			http.Error(w, "incorrect authorization", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server
}

func assertAuthenticatedRequest(t *testing.T, client *http.Client, target, wantUser, wantPassword string) {
	t.Helper()
	response, err := client.Get(target)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func TestNewSSHAuthMethodLoadsConfiguredKey(t *testing.T) {
	home := t.TempDir()
	privateKeyPath := filepath.Join(home, "key")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate SSH test key: %v", err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("encode SSH test key: %v", err)
	}
	if err := os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{
		Type: "PRIVATE KEY", Bytes: encoded,
	}), 0o600); err != nil {
		t.Fatalf("write SSH test key: %v", err)
	}
	auth, err := NewSSHAuthMethod(map[string]string{
		"HOME":            home,
		"GIT_SSH_KEY":     privateKeyPath,
		"GIT_SSH_COMMAND": "ssh -o StrictHostKeyChecking=no",
	}, "git")
	if err != nil {
		t.Fatalf("load configured SSH key: %v", err)
	}
	if auth == nil {
		t.Fatal("SSH auth method is nil")
	}
}
