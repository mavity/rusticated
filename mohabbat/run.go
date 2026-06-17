package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// runUnderWashmhost runs a WASM file under washmhost.
// When running inside a vegetable (MOHABBAT_VEGETABLE_PATH is set), it extracts
// the appropriate pre-built washmhost binary from the vegetable's pool rather
// than re-compiling from source via `go run .`.
// Natively, it compiles washmhost via `go run .`.
func runUnderWashmhost(ws, wasmPath string, extraArgs []string) error {
	fmt.Printf("🍆 Running %s under washmhost\n", filepath.Base(wasmPath))
	goroot, _, _ := resolveGoroot(ws)
	// Determine host platform. Inside a vegetable runtime.GOOS is "wasip1",
	// so use the env vars that wasmhost sets for the brain.
	hostOS := os.Getenv("MOHABBAT_HOST_OS")
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	hostArch := os.Getenv("MOHABBAT_HOST_ARCH")
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}

	runArgs := []string{"run", ".", "--"}
	runArgs = append(runArgs, extraArgs...)
	cmd := exec.Command("go", runArgs...)
	cmd.Dir = filepath.Join(ws, "washmhost")
	env := os.Environ()
	env = upsertEnv(env, "MOHABBAT_WASM_FD", wasmPath)
	// Prevent GOOS/GOARCH leakage from prior WASM build steps.
	env = upsertEnv(env, "GOOS", hostOS)
	env = upsertEnv(env, "GOARCH", hostArch)
	if goroot != "" {
		env = upsertEnv(env, "GOROOT", goroot)
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("washmhost execution failed: %w", err)
	}
	return nil
}
