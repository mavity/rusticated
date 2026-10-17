package mohabbat

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestParseJitTargets(t *testing.T) {
	gomod := `module foo

go 1.20

require (
	github.com/u-root/u-root v0.12.0
	golang.org/x/sys v0.26.0
	other.com/foo v1.0.0
)

replace github.com/u-root/u-root => ./mohabbat/rusticated-jit/github.com/u-root/u-root
replace golang.org/x/sys => ../../mohabbat/rusticated-jit/golang.org/x/sys
replace other.com/foo => ../local/other
`
	jitBase := "/abs/workspace/mohabbat/rusticated-jit"

	targets := parseJitTargets(gomod, jitBase)

	// Sort for deterministic checks
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].module < targets[j].module
	})

	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d: %v", len(targets), targets)
	}

	if targets[0].module != "github.com/u-root/u-root" {
		t.Errorf("expected module github.com/u-root/u-root, got %s", targets[0].module)
	}

	if targets[1].module != "golang.org/x/sys" {
		t.Errorf("expected module golang.org/x/sys, got %s", targets[1].module)
	}
}

func TestModVersionFromGomod(t *testing.T) {
	gomod := `module foo // hello

go 1.20

require (
	github.com/u-root/u-root v0.12.0 // indirect
	golang.org/x/sys v0.26.0-dev.1
)`

	v1, ok1 := modVersionFromGomod(gomod, "github.com/u-root/u-root")
	if !ok1 || v1 != "v0.12.0" {
		t.Errorf("expected v0.12.0, got %s (ok=%v)", v1, ok1)
	}

	v2, ok2 := modVersionFromGomod(gomod, "golang.org/x/sys")
	if !ok2 || v2 != "v0.26.0-dev.1" {
		t.Errorf("expected v0.26.0-dev.1, got %s (ok=%v)", v2, ok2)
	}

	_, ok3 := modVersionFromGomod(gomod, "not/found")
	if ok3 {
		t.Errorf("expected not found to be false")
	}
}

func TestEncodeModPath(t *testing.T) {
	cases := []struct {
		in  string
		out string
	}{
		{"github.com/u-root/u-root", "github.com/u-root/u-root"},
		{"golang.org/x/sys", "golang.org/x/sys"},
		{"github.com/SomeOne/Repo", "github.com/!some!one/!repo"},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := encodeModPath(c.in)
			if got != c.out {
				t.Errorf("expected %s, got %s", c.out, got)
			}
		})
	}
}

func TestXSysCPUFilesAreNotFlippedForWasip1(t *testing.T) {
	files := map[string][]byte{
		"cpu/cpu_linux.go":         []byte("//go:build !386 && !amd64\n\npackage cpu\n"),
		"cpu/cpu_linux_mips64x.go": []byte("//go:build linux && (mips64 || mips64le)\n\npackage cpu\n"),
		"cpu/cpu_wasm.go":          []byte("//go:build wasm\n\npackage cpu\n"),
	}
	flips := applyGateFlipsInMemory("golang.org/x/sys", files)
	if len(flips) != 0 {
		t.Fatalf("unexpected x/sys/cpu suffix aliases: %v", flips)
	}
	if strings.Contains(string(files["cpu/cpu_linux_mips64x.go"]), "wasip1") {
		t.Fatal("Linux architecture-specific CPU file was enabled for wasip1")
	}
	if _, exists := files["cpu/cpu_wasip1.go"]; exists {
		t.Fatal("Linux CPU file unexpectedly gained a wasip1 alias")
	}
}

func TestRemoveStaleXSysCPUAliases(t *testing.T) {
	jitDir := t.TempDir()
	source := []byte("//go:build !386 && !amd64\n\npackage cpu\n")
	files := map[string][]byte{"cpu/cpu_linux.go": source}
	aliasPath := filepath.Join(jitDir, "cpu", "cpu_wasip1.go")
	if err := os.MkdirAll(filepath.Dir(aliasPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aliasPath, source, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleXSysCPUAliases(jitDir, files); err != nil {
		t.Fatalf("remove stale alias: %v", err)
	}
	if _, err := os.Stat(aliasPath); !os.IsNotExist(err) {
		t.Fatalf("stale alias still exists: %v", err)
	}
}

func TestPatchFilepathSecurejoinForByteSeparator(t *testing.T) {
	files := map[string][]byte{
		"join.go": []byte("strings.IndexRune(remainingPath, filepath.Separator)"),
	}
	if err := patchFilepathSecurejoinInMemory(files); err != nil {
		t.Fatalf("patch securejoin source: %v", err)
	}
	if got := string(files["join.go"]); got != "strings.IndexByte(remainingPath, filepath.Separator)" {
		t.Fatalf("patched join.go = %q", got)
	}
	if err := patchFilepathSecurejoinInMemory(map[string][]byte{"join.go": []byte("no separator scan")}); err == nil {
		t.Fatal("patch accepted an unexpected securejoin source")
	}
}

func TestAddGoGitWasip1Compatibility(t *testing.T) {
	files := map[string][]byte{}
	addGoGitWasip1Compatibility(files)
	if got := string(files["worktree_rusticated_wasip1.go"]); !strings.Contains(got, "//go:build wasip1") ||
		!strings.Contains(got, "func isSymlinkWindowsNonAdmin(error) bool") {
		t.Fatalf("go-git compatibility source is incomplete: %q", got)
	}
}
