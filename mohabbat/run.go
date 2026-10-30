package mohabbat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const washmhostGuestArgsEnv = "MOHABBAT_WASHMHOST_GUEST_ARGS"
const washmhostTestTimeoutEnv = "MOHABBAT_WASHMHOST_TEST_TIMEOUT"

// runUnderWashmhost runs a WASM file under washmhost.
// When running inside a vegetable (MOHABBAT_VEGETABLE_PATH is set), it extracts
// the appropriate pre-built washmhost binary from the vegetable's pool rather
// than re-compiling from source via `go run .`.
// Natively, it compiles washmhost via `go run .`.
func runUnderWashmhost(ws, wasmPath string, extraArgs []string, platform string, verbose bool, workingDir string, testTimeout time.Duration) error {
	cwd, err := resolveRunWorkingDirectory(workingDir)
	if err != nil {
		return err
	}

	if verbose {
		fmt.Printf("🍆 Running %s under washmhost", filepath.Base(wasmPath))
		if platform != "" {
			fmt.Printf(" (dev-run platform %s)", platform)
		}
		fmt.Println()
	}

	if platform == "node" {
		runArgs := []string{filepath.Join(ws, "mohabbat", "washmhost", "js", "node", "index.js"), wasmPath}
		runArgs = append(runArgs, extraArgs...)
		ctx := context.Background()
		cancel := func() {}
		if testTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, testTimeout)
		}
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", runArgs...)
		cmd.Dir = cwd
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("guest test exceeded -test.timeout=%s: %w", testTimeout, ctx.Err())
			}
			return err
		}
		return nil
	}

	goroot, _, _ := resolveGoroot(ws)
	platformArg := ""

	if platform != "" && platform != "node" {
		if platform == "x64" || platform == "amd64" {
			platformArg = "amd64"
		} else if platform == "arm64" {
			platformArg = "arm64"
		} else {
			platformArg = platform
		}
	}

	metadata := GetBuildMetadata(ws)
	ldflags := fmt.Sprintf("-X main.BuildVersion=%s -X main.BuildTime=%s -X main.BuildPlatform=%s",
		metadata.Version, metadata.Time, metadata.Platform)

	runArgs := []string{"run"}
	if verbose {
		runArgs = append(runArgs, "-tags=verbose")
	}
	runArgs = append(runArgs, fmt.Sprintf("-ldflags=%s", ldflags), ".")
	if platformArg != "" {
		runArgs = append(runArgs, "--platform", platformArg)
	}
	runArgs = append(runArgs, "--cwd", cwd)
	runArgs = append(runArgs, "--")
	goBin := "go"
	if goroot != "" {
		goBin = goBinFromRoot(goroot)
	}
	cmd := exec.Command(goBin, runArgs...)
	cmd.Dir = filepath.Join(ws, "mohabbat", "washmhost")
	env := os.Environ()
	env = upsertEnv(env, "MOHABBAT_WASM_FD", wasmPath)
	testTimeoutValue := ""
	if testTimeout > 0 {
		testTimeoutValue = testTimeout.String()
	}
	env = upsertEnv(env, washmhostTestTimeoutEnv, testTimeoutValue)
	if extraArgs == nil {
		extraArgs = []string{}
	}
	encodedArgs, err := json.Marshal(extraArgs)
	if err != nil {
		return fmt.Errorf("encode guest arguments: %w", err)
	}
	env = upsertEnv(env, washmhostGuestArgsEnv, string(encodedArgs))
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

func parseGoTestTimeout(args []string) (time.Duration, error) {
	var raw string
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-test.timeout" || arg == "--test.timeout":
			if i+1 >= len(args) {
				return 0, fmt.Errorf("missing value for %s", arg)
			}
			raw = args[i+1]
			found = true
			i++
		case strings.HasPrefix(arg, "-test.timeout="):
			raw = strings.TrimPrefix(arg, "-test.timeout=")
			found = true
		case strings.HasPrefix(arg, "--test.timeout="):
			raw = strings.TrimPrefix(arg, "--test.timeout=")
			found = true
		}
	}
	if !found {
		return 0, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid -test.timeout %q: %w", raw, err)
	}
	return timeout, nil
}

func resolveRunWorkingDirectory(workingDir string) (string, error) {
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current working directory: %w", err)
		}
		return cwd, nil
	}

	cwd, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", workingDir, err)
	}
	return cwd, nil
}
