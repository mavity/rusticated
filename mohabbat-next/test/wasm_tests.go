package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"mohabbat/mohabbat"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type wasmTestTarget struct {
	Project string // top-level project name (e.g. "demo-go", "ai", "kabibi", "u-root", "demo")
	PkgRel  string // repo-relative package path (e.g. "ai/assets", "u-root/cmds/core/echo")
	Kind    string // "go" or "rust"
	Dir     string // absolute directory path
}

var excludedTopDirs = map[string]bool{
	"mohabbat":        true,
	"mohabbat-next":   true,
	"release":         true,
	"target":          true,
	"rusticated-go":   true,
	"rusticated-rust": true,
	"external":        true,
}

func discoverWasmTestTargets(repoRoot string) ([]wasmTestTarget, error) {
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return nil, err
	}

	var targets []wasmTestTarget
	seenGoDirs := make(map[string]bool)
	seenRustDirs := make(map[string]bool)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || excludedTopDirs[name] {
			continue
		}
		topDir := filepath.Join(repoRoot, name)

		err := filepath.WalkDir(topDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				base := d.Name()
				if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" ||
					base == "target" || base == "rusticated-jit" || base == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}

			dir := filepath.Dir(path)
			if strings.HasSuffix(d.Name(), "_test.go") {
				if seenGoDirs[dir] {
					return nil
				}
				seenGoDirs[dir] = true
				rel, _ := filepath.Rel(repoRoot, dir)
				targets = append(targets, wasmTestTarget{
					Project: name,
					PkgRel:  filepath.ToSlash(rel),
					Kind:    "go",
					Dir:     dir,
				})
				return nil
			}

			if d.Name() == "Cargo.toml" && !seenRustDirs[dir] {
				// Skip Cargo.toml in directories that are Go modules (e.g. dload/)
				if _, goModErr := os.Stat(filepath.Join(dir, "go.mod")); goModErr == nil {
					return nil
				}
				seenRustDirs[dir] = true
				rel, _ := filepath.Rel(repoRoot, dir)
				targets = append(targets, wasmTestTarget{
					Project: name,
					PkgRel:  filepath.ToSlash(rel),
					Kind:    "rust",
					Dir:     dir,
				})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// Include mohabbat-next/wasm-abi-test if present
	wasmAbiDir := filepath.Join(repoRoot, "mohabbat-next", "wasm-abi-test")
	if info, statErr := os.Stat(wasmAbiDir); statErr == nil && info.IsDir() {
		targets = append(targets, wasmTestTarget{
			Project: "wasm-abi-test",
			PkgRel:  "mohabbat-next/wasm-abi-test",
			Kind:    "go",
			Dir:     wasmAbiDir,
		})
	}

	return targets, nil
}

func rustCrateHasTests(crateDir string) bool {
	found := false
	_ = filepath.WalkDir(crateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "target" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".rs") {
			if raw, readErr := os.ReadFile(path); readErr == nil {
				s := string(raw)
				if strings.Contains(s, "#[test]") || strings.Contains(s, "#[cfg(test)]") {
					found = true
				}
			}
		}
		return nil
	})
	return found
}

func matchesProjectFilter(t wasmTestTarget, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		f = strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(f)), "/")
		if f == "" {
			continue
		}
		if t.Project == f || t.PkgRel == f || strings.HasPrefix(t.PkgRel, f+"/") {
			return true
		}
	}
	return false
}

func (r *Runner) RunStage4WasmProjectTests(projectFilters []string, guestTestArgs []string, perPkgTimeout time.Duration, verbose bool) {
	fmt.Fprintln(os.Stdout, "\n================================================================================")
	fmt.Fprintln(os.Stdout, "🧪  Stage 4: Rusticated WASM Project Tests (Go & Rust)")
	fmt.Fprintln(os.Stdout, "================================================================================")

	repoRoot := filepath.Dir(r.NextDir)
	targets, err := discoverWasmTestTargets(repoRoot)
	if err != nil {
		r.RunTest("wasm/discover", "repo-projects", func() error { return err })
		return
	}

	wasmOutDir := filepath.Join(r.BuildDir, "wasm-tests")
	if err := os.MkdirAll(wasmOutDir, 0o755); err != nil {
		r.RunTest("wasm/setup", "mkdir", func() error { return err })
		return
	}

	if perPkgTimeout <= 0 {
		perPkgTimeout = 30 * time.Second
	}

	matched := 0
	for _, t := range targets {
		t := t
		if !matchesProjectFilter(t, projectFilters) {
			continue
		}
		matched++

		suite := "wasm/" + t.Kind
		safeName := strings.ReplaceAll(t.PkgRel, "/", "_")

		if t.Kind == "go" {
			r.RunTest(suite, t.PkgRel, func() error {
				wasmPath := filepath.Join(wasmOutDir, safeName+"_test.wasm")
				if err := mohabbat.BuildGoTestWasm(repoRoot, t.Dir, wasmPath, verbose); err != nil {
					return err
				}
				return r.execWasmUnderHost(wasmPath, t.Dir, guestTestArgs, perPkgTimeout)
			})
		} else {
			r.RunTest(suite, t.PkgRel, func() error {
				wasmPaths, err := buildRustTestWasm(repoRoot, t.Dir, r.BuildDir)
				if err != nil {
					return err
				}
				if len(wasmPaths) == 0 {
					return fmt.Errorf("no test wasm artifacts produced for %s", t.PkgRel)
				}
				for _, wp := range wasmPaths {
					if err := r.execWasmUnderHost(wp, t.Dir, guestTestArgs, perPkgTimeout); err != nil {
						return err
					}
				}
				return nil
			})
		}
	}

	if matched == 0 {
		r.RecordDisabled("wasm/filter", strings.Join(projectFilters, ","), "no matching Go or Rust test packages found")
	}
}

func (r *Runner) execWasmUnderHost(wasmPath, workDir string, guestArgs []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{"--wasm", wasmPath}
	if len(guestArgs) > 0 {
		args = append(args, "--")
		args = append(args, guestArgs...)
	}

	cmd := exec.CommandContext(ctx, r.DistExe, args...)
	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timed out after %s", timeout)
		}
		return err
	}
	return nil
}

func buildRustTestWasm(repoRoot, crateDir, buildDir string) ([]string, error) {
	targetDir := filepath.Join(buildDir, "rust-tests")
	manifest := filepath.Join(crateDir, "Cargo.toml")
	cmd := exec.Command(
		"cargo", "+nightly",
		"-Z", "build-std=core,alloc,std,panic_abort",
		"test",
		"--manifest-path", manifest,
		"--target", "wasm32-wasip1",
		"--target-dir", targetDir,
		"--no-run",
		"--message-format=json",
	)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("cargo test --no-run failed: %w\n%s", err, string(ee.Stderr))
		}
		return nil, fmt.Errorf("cargo test --no-run failed: %w", err)
	}

	var wasmFiles []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var msg struct {
			Reason     string   `json:"reason"`
			Executable string   `json:"executable"`
			Target     struct {
				Kind []string `json:"kind"`
			} `json:"target"`
			Profile struct {
				Test bool `json:"test"`
			} `json:"profile"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if msg.Reason == "compiler-artifact" && msg.Profile.Test && msg.Executable != "" {
			if strings.HasSuffix(msg.Executable, ".wasm") {
				wasmFiles = append(wasmFiles, msg.Executable)
			}
		}
	}
	return wasmFiles, nil
}
