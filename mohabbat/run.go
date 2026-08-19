package mohabbat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// runUnderWashmhost runs a WASM file under washmhost.
// When running inside a vegetable (MOHABBAT_VEGETABLE_PATH is set), it extracts
// the appropriate pre-built washmhost binary from the vegetable's pool rather
// than re-compiling from source via `go run .`.
// Natively, it compiles washmhost via `go run .`.
func runUnderWashmhost(ws, wasmPath string, extraArgs []string, platform string, verbose bool) error {
	fmt.Printf("🍆 Running %s under washmhost", filepath.Base(wasmPath))
	if platform != "" {
		fmt.Printf(" (dev-run platform %s)", platform)
	}
	fmt.Println()

	if platform == "node" {
		runArgs := []string{filepath.Join(ws, "node-host", "index.js"), wasmPath}
		runArgs = append(runArgs, extraArgs...)
		cmd := exec.Command("node", runArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()
	}

	goroot, _, _ := resolveGoroot(ws)
	// Determine host platform. Inside a vegetable runtime.GOOS is "wasip1",
	// so use the env vars that wasmhost sets for the brain, falling back to runtime defaults.
	nativeOS := os.Getenv("MOHABBAT_HOST_OS")
	if nativeOS == "" {
		nativeOS = runtime.GOOS
	}
	nativeArch := os.Getenv("MOHABBAT_HOST_ARCH")
	if nativeArch == "" {
		nativeArch = runtime.GOARCH
	}

	hostOS := nativeOS
	hostArch := nativeArch

	if platform != "" && platform != "node" {
		if platform == "x64" || platform == "amd64" {
			hostArch = "amd64"
		} else if platform == "arm64" {
			hostArch = "arm64"
		} else {
			parts := strings.Split(platform, "-")
			if len(parts) == 2 {
				hostOS = parts[0]
				hostArch = parts[1]
			}
		}
	}

	metadata := GetBuildMetadata(ws)
	ldflags := fmt.Sprintf("-X main.BuildVersion=%s -X main.BuildTime=%s -X main.BuildPlatform=%s",
		metadata.Version, metadata.Time, metadata.Platform)

	runArgs := []string{"run"}
	if verbose {
		runArgs = append(runArgs, "-tags=verbose")
	}
	runArgs = append(runArgs, fmt.Sprintf("-ldflags=%s", ldflags), ".", "--")
	runArgs = append(runArgs, extraArgs...)
	cmd := exec.Command("go", runArgs...)
	cmd.Dir = filepath.Join(ws, "mohabbat", "washmhost")
	env := os.Environ()
	env = upsertEnv(env, "MOHABBAT_WASM_FD", wasmPath)
	env = upsertEnv(env, "MOHABBAT_HOST_OS", hostOS)
	env = upsertEnv(env, "MOHABBAT_HOST_ARCH", hostArch)
	// Prevent GOOS/GOARCH leakage from prior WASM build steps.
	env = upsertEnv(env, "GOOS", nativeOS)
	env = upsertEnv(env, "GOARCH", nativeArch)
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
