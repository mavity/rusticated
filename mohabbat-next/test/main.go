package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	// Stage 1: Always Build First (Unbuffered)
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

	// Stage 2: Package-Level Unit Tests (Go, Rust, C#)
	runner.RunStage2UnitTests()

	// Stage 3: Holistic Multi-Platform & Multi-Arch Acceptance Matrix
	runner.RunStage3ArtifactIntegrityTests()
	runner.RunStage3PlatformMatrix()
	runner.RunStage3CraneliftRuntimeTests()

	runner.PrintSummary(time.Since(totalStart))
	if runner.FailCount > 0 {
		os.Exit(1)
	}
}
