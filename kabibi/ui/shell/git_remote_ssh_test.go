package shell

import (
	"path/filepath"
	"testing"
)

func TestIsSSHRemoteURLRecognizesURIAndSCPForms(t *testing.T) {
	for _, remoteURL := range []string{
		"ssh://git@github.com/owner/repo.git",
		"git@github.com:owner/repo.git",
	} {
		if !isSSHRemoteURL(remoteURL) {
			t.Errorf("isSSHRemoteURL(%q) = false, want true", remoteURL)
		}
	}
	for _, remoteURL := range []string{
		"https://github.com/owner/repo.git",
		"file:///tmp/repo",
	} {
		if isSSHRemoteURL(remoteURL) {
			t.Errorf("isSSHRemoteURL(%q) = true, want false", remoteURL)
		}
	}
}

func TestParseSSHCloneArgsHandlesCommonOptions(t *testing.T) {
	invocation := gitInvocation{
		args:         []string{"clone", "--bare", "--single-branch", "--branch", "main", "--depth", "3", "git@github.com:owner/repo.git", "local"},
		effectiveDir: filepath.Clean(`C:\workspace`),
	}
	remoteURL, destination, bare, branch, depth, singleBranch, ok := parseSSHCloneArgs(invocation)
	if !ok {
		t.Fatal("parseSSHCloneArgs rejected supported options")
	}
	if remoteURL != "git@github.com:owner/repo.git" || destination != filepath.Join(invocation.effectiveDir, "local") ||
		!bare || branch != "main" || depth != 3 || !singleBranch {
		t.Fatalf("parsed clone options = (%q, %q, %v, %q, %d, %v)", remoteURL, destination, bare, branch, depth, singleBranch)
	}
}

func TestJoinShellArgumentsPreservesGitArgumentBoundaries(t *testing.T) {
	got := joinShellArguments([]string{"git", "clone", "git@github.com:owner/repo.git", "directory with spaces"})
	want := "'git' 'clone' 'git@github.com:owner/repo.git' 'directory with spaces'"
	if got != want {
		t.Fatalf("quoted Git command = %q, want %q", got, want)
	}
}
