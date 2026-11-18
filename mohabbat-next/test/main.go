package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func findMohabbatNextDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	candidates := []string{
		filepath.Join(cwd, "mohabbat-next"),
		cwd,
		filepath.Dir(cwd),
	}

	for _, cand := range candidates {
		palDir := filepath.Join(cand, "pal")
		brotDir := filepath.Join(cand, "brot")
		if palInfo, palErr := os.Stat(palDir); palErr == nil && palInfo.IsDir() {
			if brotInfo, brotErr := os.Stat(brotDir); brotErr == nil && brotInfo.IsDir() {
				return cand, nil
			}
		}
	}

	return "", fmt.Errorf("could not locate mohabbat-next directory from %s", cwd)
}

func main() {
	var (
		projectFilter   string
		onlyWasm        bool
		skipWasm        bool
		testRun         string
		testVerbose     bool
		testIdleTimeout time.Duration
		verbose         bool
	)
	flag.StringVar(&projectFilter, "p", "", "Comma-separated project or package filter for WASM tests (e.g. demo-go,ai,u-root,demo)")
	flag.StringVar(&projectFilter, "project", "", "Comma-separated project or package filter for WASM tests")
	flag.BoolVar(&onlyWasm, "only-wasm", false, "Run only Stage 1 build + Stage 4 WASM project tests")
	flag.BoolVar(&skipWasm, "skip-wasm", false, "Skip Stage 4 WASM project tests")
	flag.StringVar(&testRun, "run", "", "Forwarded as -test.run=<pattern> to WASM Go tests")
	flag.StringVar(&testRun, "test.run", "", "Forwarded as -test.run=<pattern> to WASM Go tests")
	flag.BoolVar(&testVerbose, "test.v", true, "Forward --test.v to WASM Go tests")
	flag.DurationVar(&testIdleTimeout, "stdio-idle-timeout", 10*time.Minute, "Fail WASM tests after this long without stdout or stderr output")
	flag.BoolVar(&verbose, "v", false, "Verbose build output")
	flag.Parse()

	var projectFilters []string
	if projectFilter != "" {
		for _, p := range strings.Split(projectFilter, ",") {
			if p = strings.TrimSpace(p); p != "" {
				projectFilters = append(projectFilters, p)
			}
		}
	}
	var guestTestArgs []string
	if testVerbose {
		guestTestArgs = append(guestTestArgs, "--test.v")
	}
	if testRun != "" {
		guestTestArgs = append(guestTestArgs, "--test.run="+testRun)
	}
	for _, arg := range flag.Args() {
		if strings.HasPrefix(arg, "-test.") || strings.HasPrefix(arg, "--test.") {
			guestTestArgs = append(guestTestArgs, normalizeGuestTestFlag(arg))
		} else if !strings.HasPrefix(arg, "-") {
			projectFilters = append(projectFilters, arg)
		}
	}

	totalStart := time.Now()
	fmt.Fprintln(os.Stdout, "================================================================================")
	fmt.Fprintln(os.Stdout, "🧪  Mohabbat-Next Unified Build & Test Runner (Go)")
	fmt.Fprintln(os.Stdout, "================================================================================")

	nextDir, err := findMohabbatNextDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	distExe := filepath.Join(nextDir, "brot", "windows-host", "out", "MohabbatNext.exe")
	runner := NewRunner(nextDir, distExe)

	// Stage 1: Always Build First (Unbuffered, incremental via writeFileIfChanged)
	fmt.Fprintln(os.Stdout, "\n================================================================================")
	fmt.Fprintln(os.Stdout, "🔨  Stage 1: Building All Artifacts (Unbuffered)")
	fmt.Fprintln(os.Stdout, "================================================================================")
	runner.RunTest("build/all", "go run ./build", func() error {
		cmd := exec.Command("go", "run", "./build", "-o", distExe)
		cmd.Dir = nextDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})
	if runner.FailCount > 0 {
		runner.PrintSummary(time.Since(totalStart))
		os.Exit(1)
	}

	if !onlyWasm && len(projectFilters) == 0 {
		// Stage 2: Package-Level Unit Tests (Go, Zig, C#)
		runner.RunStage2UnitTests()

		// Stage 3: Holistic Multi-Platform & Multi-Arch Acceptance Matrix
		runner.RunStage3ArtifactIntegrityTests()
		runner.RunStage3PlatformMatrix()
		runner.RunStage3NativeAbiConformanceTests(testIdleTimeout)
		runner.RunStage3ZwasmRuntimeTests()
	}

	if !skipWasm {
		// Stage 4: Rusticated WASM Project Tests (all projects by default, or filtered)
		runner.RunStage4WasmProjectTests(projectFilters, guestTestArgs, testIdleTimeout, verbose)
	}

	runner.PrintSummary(time.Since(totalStart))
	if runner.FailCount > 0 {
		os.Exit(1)
	}
}

func normalizeGuestTestFlag(arg string) string {
	if strings.HasPrefix(arg, "--test.") {
		return arg
	}
	if strings.HasPrefix(arg, "-test.") {
		return "-" + arg
	}
	return arg
}
