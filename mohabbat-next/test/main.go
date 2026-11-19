package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--internal-build-go-test-wasm" {
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "internal WASM build requires a package directory and output path")
			os.Exit(2)
		}
		os.Exit(buildGoTestWasm(os.Args[2], os.Args[3]))
	}

	var (
		projectFilter string
		idleTimeout   time.Duration
		suiteTimeout  time.Duration
		skipBuild     bool
		skipNative    bool
		skipWasm      bool
		skipRust      bool
		skipZig       bool
	)
	flag.StringVar(&projectFilter, "p", "", "Comma-separated project/package filter")
	flag.DurationVar(&idleTimeout, "stdio-idle-timeout", 3*time.Minute, "Fail a runner after this long without output")
	flag.DurationVar(&suiteTimeout, "suite-timeout", 30*time.Minute, "Maximum duration of one test suite")
	flag.BoolVar(&skipBuild, "skip-build", false, "Reuse an existing Mohabbat-Next test host")
	flag.BoolVar(&skipNative, "skip-native", false, "Skip native Go tests")
	flag.BoolVar(&skipWasm, "skip-wasm", false, "Skip Rusticated WASM tests")
	flag.BoolVar(&skipRust, "skip-rust", false, "Skip Rust WASM tests")
	flag.BoolVar(&skipZig, "skip-zig", false, "Skip Zig tests")
	flag.Parse()

	root, err := findWorkspaceRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	filter := parseFilter(projectFilter)
	run, err := newTestRun(root, idleTimeout, suiteTimeout, filter)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("🧪 Mohabbat-Next test run: %s\n", run.logDir)

	if !skipBuild && (!skipWasm || !skipRust) {
		run.buildHost()
	}
	if !skipNative {
		run.runNativeGo()
	}
	if !skipWasm {
		run.runWasmGo()
	}
	if !skipRust {
		run.runRustWasm()
	}
	if !skipZig {
		run.runZig()
	}

	if err := run.finish(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func findWorkspaceRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if fileExists(filepath.Join(dir, "go.mod")) &&
			fileExists(filepath.Join(dir, "mohabbat-next", "pal", "abi", "guest.zig")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", fmt.Errorf("could not locate workspace root from %s", cwd)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func parseFilter(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var result []string
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			result = append(result, filepath.ToSlash(entry))
		}
	}
	return result
}
