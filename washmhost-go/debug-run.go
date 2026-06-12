//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run washmhost-go/debug-run.go <package-dir> [args...]")
		os.Exit(1)
	}

	projectDir := os.Args[1]
	projectName := filepath.Base(projectDir)

	// Determine workspace root. We assume we are run from the workspace root.
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
		os.Exit(1)
	}

	workspaceRoot := wd
	// Minimal check to see if we are likely in the right place.
	if _, err := os.Stat(filepath.Join(workspaceRoot, "washmhost-go")); err != nil {
		// Try parent if we are inside washmhost-go
		if filepath.Base(workspaceRoot) == "washmhost-go" {
			workspaceRoot = filepath.Dir(workspaceRoot)
		} else {
			fmt.Fprintf(os.Stderr, "Error: Could not find washmhost-go in current directory (%s). Please run from the workspace root.\n", wd)
			os.Exit(1)
		}
	}

	overlayPath := filepath.Join(workspaceRoot, "target", "overlay.json")
	if _, err := os.Stat(overlayPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "\n!!! ERROR: %s MISSING !!!\n", overlayPath)
		fmt.Fprintf(os.Stderr, "You need to generate the overlay first. Run:\n")
		fmt.Fprintf(os.Stderr, "  cargo run -p prebuild\n\n")
		os.Exit(1)
	}

	outputWasm := filepath.Join(workspaceRoot, "target", projectName+".wasm")

	fmt.Printf("🍆 Building Go package: %s -> %s\n", projectDir, outputWasm)

	// Normalize projectDir to absolute path to avoid confusion when changing CWD
	absProjectDir, err := filepath.Abs(projectDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting absolute path for %s: %v\n", projectDir, err)
		os.Exit(1)
	}

	cmd := exec.Command("go", "build", "-overlay", overlayPath, "-o", outputWasm, ".")
	cmd.Dir = absProjectDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n!!! GO BUILD FAILED !!!\n%v\n", err)
		os.Exit(1)
	}

	// Ensure the output wasm is runnable on Unix-like systems (though we are on Windows, good to follow Mohabbat)
	if runtime.GOOS != "windows" {
		_ = os.Chmod(outputWasm, 0o755)
	}

	fmt.Printf("🍆 Running %s under washmhost-go\n", outputWasm)

	// Run washmhost-go
	// Pass any additional arguments
	runArgs := []string{"run", ".", "payload.wasm"}
	if len(os.Args) > 2 {
		runArgs = append(runArgs, os.Args[2:]...)
	}

	runCmd := exec.Command("go", runArgs...)
	runCmd.Dir = filepath.Join(workspaceRoot, "washmhost-go")
	runCmd.Env = append(os.Environ(), "MOHABBAT_WASM_FD="+outputWasm)
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	runCmd.Stdin = os.Stdin

	if err := runCmd.Run(); err != nil {
		// If it's just a non-zero exit code from the guest, we might not want to "error loudly" here,
		// but let's follow the user's desire for friction-less debugging.
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "washmhost-go execution failed: %v\n", err)
		os.Exit(1)
	}
}
