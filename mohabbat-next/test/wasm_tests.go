package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mohabbat/mohabbat"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func (r *Runner) RunStage4WasmProjectTests(projectFilters []string, guestTestArgs []string, idleTimeout time.Duration, verbose bool) {
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

	if idleTimeout <= 0 {
		idleTimeout = 10 * time.Minute
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
				return r.execWasmUnderHost(wasmPath, t.Dir, guestTestArgs, idleTimeout)
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
					if err := r.execWasmUnderHost(wp, t.Dir, guestTestArgs, idleTimeout); err != nil {
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

func (r *Runner) execWasmUnderHost(wasmPath, workDir string, guestArgs []string, idleTimeout time.Duration) error {
	args := []string{"--wasm", wasmPath}
	if len(guestArgs) > 0 {
		args = append(args, "--")
		args = append(args, guestArgs...)
	}

	cmd := exec.Command(r.DistExe, args...)
	cmd.Dir = workDir
	return runCommandWithStdioIdleTimeout(cmd, idleTimeout, os.Stdout, os.Stderr)
}

type stdioActivityWriter struct {
	dst        io.Writer
	activity   chan<- struct{}
	started    time.Time
	lastOutput *atomic.Int64
}

func (w stdioActivityWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		outputAt := time.Since(w.started).Nanoseconds()
		for {
			previous := w.lastOutput.Load()
			if outputAt <= previous || w.lastOutput.CompareAndSwap(previous, outputAt) {
				break
			}
		}
		select {
		case w.activity <- struct{}{}:
		default:
		}
	}
	return w.dst.Write(p)
}

func runCommandWithStdioIdleTimeout(cmd *exec.Cmd, idleTimeout time.Duration, stdout, stderr io.Writer) error {
	if idleTimeout <= 0 {
		return fmt.Errorf("stdio idle timeout must be positive")
	}

	activity := make(chan struct{}, 1)
	started := time.Now()
	var lastOutput atomic.Int64
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	cmd.Stdout = stdioActivityWriter{dst: stdout, activity: activity, started: started, lastOutput: &lastOutput}
	cmd.Stderr = stdioActivityWriter{dst: stderr, activity: activity, started: started, lastOutput: &lastOutput}

	if err := cmd.Start(); err != nil {
		return err
	}

	wait := make(chan error, 1)
	go func() {
		wait <- cmd.Wait()
	}()

	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case err := <-wait:
			return err
		case <-activity:
			resetTimer(timer, remainingIdleTime(started, &lastOutput, idleTimeout))
		case <-timer.C:
			select {
			case err := <-wait:
				return err
			default:
			}
			if remaining := remainingIdleTime(started, &lastOutput, idleTimeout); remaining > 0 {
				resetTimer(timer, remaining)
				continue
			}

			killErr := cmd.Process.Kill()
			waitErr := <-wait
			if waitErr == nil {
				return nil
			}
			if killErr != nil {
				return fmt.Errorf("no stdout/stderr output for %s; failed to stop host process: %v (wait error: %w)", idleTimeout, killErr, waitErr)
			}
			return fmt.Errorf("no stdout/stderr output for %s; host process terminated: %w", idleTimeout, waitErr)
		}
	}
}

func remainingIdleTime(started time.Time, lastOutput *atomic.Int64, idleTimeout time.Duration) time.Duration {
	timeSinceOutput := time.Since(started) - time.Duration(lastOutput.Load())
	return idleTimeout - timeSinceOutput
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
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
			Reason     string `json:"reason"`
			Executable string `json:"executable"`
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
