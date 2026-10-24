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
	mvdan.cc/sh/v3 v3.14.1
	other.com/foo v1.0.0
)

replace github.com/u-root/u-root => ./mohabbat/rusticated-jit/github.com/u-root/u-root
replace golang.org/x/sys => ../../mohabbat/rusticated-jit/golang.org/x/sys
replace mvdan.cc/sh/v3 => ../mohabbat/rusticated-jit/mvdan.cc/sh/v3
replace other.com/foo => ../local/other
`
	jitBase := "/abs/workspace/mohabbat/rusticated-jit"

	targets := parseJitTargets(gomod, jitBase)

	// Sort for deterministic checks
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].module < targets[j].module
	})

	if len(targets) != 3 {
		t.Fatalf("expected 3 targets, got %d: %v", len(targets), targets)
	}

	if targets[0].module != "github.com/u-root/u-root" {
		t.Errorf("expected module github.com/u-root/u-root, got %s", targets[0].module)
	}

	if targets[1].module != "golang.org/x/sys" {
		t.Errorf("expected module golang.org/x/sys, got %s", targets[1].module)
	}
	if targets[2].module != "mvdan.cc/sh/v3" {
		t.Errorf("expected module mvdan.cc/sh/v3, got %s", targets[2].module)
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

func TestPatchMvdanKillBuiltin(t *testing.T) {
	files := map[string][]byte{
		"interp/builtin.go": []byte("case\n\t\t\"jobs\",\n\t\t\"kill\",\n\t\t\"newgrp\",\n"),
	}
	if err := patchMvdanKillBuiltinInMemory(files); err != nil {
		t.Fatalf("patch mvdan kill builtin: %v", err)
	}
	if got := string(files["interp/builtin.go"]); strings.Contains(got, "\"kill\"") {
		t.Fatalf("kill remains in builtin list: %q", got)
	}

	for name, unexpected := range map[string]map[string][]byte{
		"missing file":    {},
		"changed source":  {"interp/builtin.go": []byte("case\n\t\t\"jobs\",\n\t\t\"newgrp\",\n")},
		"duplicate entry": {"interp/builtin.go": []byte("\t\t\"kill\",\n\t\t\"kill\",\n")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := patchMvdanKillBuiltinInMemory(unexpected); err == nil {
				t.Fatal("patch accepted unexpected mvdan source")
			}
		})
	}
}

func TestPatchMvdanUmaskBuiltin(t *testing.T) {
	files := map[string][]byte{
		"interp/builtin.go": []byte("case \"shift\":\n"),
	}
	if err := patchMvdanUmaskBuiltinInMemory(files); err != nil {
		t.Fatalf("patch mvdan umask builtin: %v", err)
	}
	if got := string(files["interp/builtin.go"]); !strings.Contains(got, "case \"umask\":\n") ||
		!strings.Contains(got, "r.runUmask(args)") {
		t.Fatalf("umask dispatch was not added: %q", got)
	}
	for _, name := range []string{
		"interp/umask.go",
		"interp/umask_unix.go",
		"interp/umask_wasip1.go",
		"interp/umask_unsupported.go",
	} {
		if len(files[name]) == 0 {
			t.Fatalf("generated source %q is empty", name)
		}
	}
	if !strings.Contains(string(files["interp/umask_wasip1.go"]), "syscall.GetUmask()") {
		t.Fatal("WASI umask getter does not use guest-local state")
	}
	if err := patchMvdanUmaskBuiltinInMemory(map[string][]byte{}); err == nil {
		t.Fatal("patch accepted missing mvdan builtin source")
	}
	if err := patchMvdanUmaskBuiltinInMemory(map[string][]byte{
		"interp/builtin.go": []byte("no shift builtin dispatch"),
	}); err == nil {
		t.Fatal("patch accepted unexpected mvdan builtin source")
	}
}

func TestPatchWasiStatModePreservesSpecialBits(t *testing.T) {
	fallback := "// WASI does not support unix-like permissions, but Go programs are likely\n" +
		"\t// to expect the permission bits to not be zero so we set defaults to help\n" +
		"\t// avoid breaking applications that are migrating to WASM.\n" +
		"\tif fs.sys.Filetype == syscall.FILETYPE_DIRECTORY {\n" +
		"\t\tfs.mode |= 0700\n" +
		"\t} else {\n" +
		"\t\tfs.mode |= 0600\n" +
		"\t}"
	got, err := patchWasiStatMode(fallback)
	if err != nil {
		t.Fatalf("patch WASI stat mode: %v", err)
	}
	for _, want := range []string{
		"FileMode(fs.sys.Mode) & 0777",
		"fs.sys.Mode&04000",
		"fs.mode |= ModeSetuid",
		"fs.sys.Mode&02000",
		"fs.mode |= ModeSetgid",
		"fs.sys.Mode&01000",
		"fs.mode |= ModeSticky",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("patched stat source does not contain %q: %s", want, got)
		}
	}
	if _, err := patchWasiStatMode("unexpected source"); err == nil {
		t.Fatal("patch accepted a changed WASI permission fallback")
	}
}

func TestAddGoGitWasip1Compatibility(t *testing.T) {
	files := map[string][]byte{}
	addGoGitWasip1Compatibility(files)
	got := string(files["worktree_rusticated_wasip1.go"])
	for _, want := range []string{
		"//go:build wasip1",
		"fillSystemInfo = func(e *index.Entry, sys interface{})",
		"sys.(*syscall.Stat_t)",
		"syscall.STAT_VALID_CTIME",
		"syscall.STAT_VALID_BIRTHTIME",
		"syscall.STAT_VALID_MTIME",
		"syscall.STAT_VALID_DEV",
		"syscall.STAT_VALID_INODE",
		"syscall.STAT_VALID_UID",
		"syscall.STAT_VALID_GID",
		"func isSymlinkWindowsNonAdmin(error) bool",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("go-git compatibility source is incomplete: %q", got)
		}
	}
}
