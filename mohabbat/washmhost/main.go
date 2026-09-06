package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var (
	BuildVersion  = "0.0.0-dev"
	BuildTime     = "unknown"
	BuildPlatform = "unknown"
)

func ensurePosixOutputRunnable(args []string) {
	if runtime.GOOS == "windows" {
		return
	}

	for i := 1; i+1 < len(args); i++ {
		if args[i] != "-o" {
			continue
		}
		output := args[i+1]
		f, err := os.OpenFile(output, os.O_CREATE, 0o755)
		if err == nil {
			_ = f.Close()
			_ = os.Chmod(output, 0o755)
		}
		return
	}
}

func parsePlatformOverride(raw string) (string, string, error) {
	if raw == "" {
		return "", "", nil
	}
	if raw == "amd64" || raw == "x64" {
		return runtime.GOOS, "amd64", nil
	}
	if raw == "arm64" {
		return runtime.GOOS, "arm64", nil
	}
	parts := strings.Split(raw, "-")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("invalid --platform %q", raw)
}

func splitWashmhostArgs(args []string) (platform string, satellite bool, probePath string, guestArgs []string, err error) {
	guestArgs = []string{args[0]}
	raw := args[1:]
	passThrough := false
	for i := 0; i < len(raw); i++ {
		arg := raw[i]
		if passThrough {
			guestArgs = append(guestArgs, arg)
			continue
		}
		switch arg {
		case "--":
			passThrough = true
		case "--dylib-satellite":
			satellite = true
		case "--probe-dlopen":
			if i+1 >= len(raw) {
				return "", false, "", nil, fmt.Errorf("missing argument after --probe-dlopen")
			}
			probePath = raw[i+1]
			i++
		case "--platform":
			if i+1 >= len(raw) {
				return "", false, "", nil, fmt.Errorf("missing argument after --platform")
			}
			platform = raw[i+1]
			i++
		default:
			guestArgs = append(guestArgs, arg)
		}
	}
	return platform, satellite, probePath, guestArgs, nil
}

func buildWashmhostForPlatform(goos, goarch string) (string, error) {
	wsRoot := findWorkspaceRoot()
	if wsRoot == "" {
		return "", fmt.Errorf("unable to find workspace root for relaunch")
	}
	buildDir := filepath.Join(wsRoot, "target", "mohabbat-build", "washmhost-relay")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return "", fmt.Errorf("create relaunch build dir: %w", err)
	}
	binName := "washmhost-" + goos + "-" + goarch
	if goos == "windows" {
		binName += ".exe"
	}
	outPath := filepath.Join(buildDir, binName)
	goBin := resolveGoBinary()
	buildCmd := exec.Command(goBin, "build", "-trimpath", "-o", outPath, ".")
	buildCmd.Dir = filepath.Join(wsRoot, "mohabbat", "washmhost")
	buildEnv := os.Environ()
	buildEnv = append(buildEnv, "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if goarch == "arm" {
		buildEnv = append(buildEnv, "GOARM=7")
	}
	buildCmd.Env = buildEnv
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return "", fmt.Errorf("build target washmhost %s/%s: %w", goos, goarch, err)
	}
	return outPath, nil
}

func relaunchForPlatform(goos, goarch string, satellite bool, probePath string, guestArgs []string) error {
	outPath, err := buildWashmhostForPlatform(goos, goarch)
	if err != nil {
		return err
	}

	args := make([]string, 0, len(guestArgs)+1)
	if satellite {
		args = append(args, "--dylib-satellite")
	}
	if probePath != "" {
		args = append(args, "--probe-dlopen", probePath)
	}
	if !satellite && len(guestArgs) > 1 {
		args = append(args, guestArgs[1:]...)
	}
	runCmd := exec.Command(outPath, args...)
	runCmd.Stdin = os.Stdin
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	runCmd.Env = os.Environ()
	if err := runCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}

func main() {
	platformOverride, satelliteMode, probePath, parsedArgs, err := splitWashmhostArgs(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "washmhost: %v\n", err)
		os.Exit(1)
	}
	if platformOverride != "" {
		goos, goarch, err := parsePlatformOverride(platformOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "washmhost: %v\n", err)
			os.Exit(1)
		}
		if goos != runtime.GOOS || goarch != runtime.GOARCH {
			if err := relaunchForPlatform(goos, goarch, satelliteMode, probePath, parsedArgs); err != nil {
				fmt.Fprintf(os.Stderr, "washmhost: %v\n", err)
				os.Exit(1)
			}
		}
	}
	if satelliteMode {
		runSatelliteMain()
		return
	}
	if probePath != "" {
		h, err := dlopen(probePath, RTLD_NOW|RTLD_GLOBAL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "washmhost probe dlopen failed on %s/%s: %v\n", runtime.GOOS, runtime.GOARCH, err)
			os.Exit(1)
		}
		_ = dlclose(h)
		fmt.Fprintf(os.Stdout, "washmhost probe dlopen succeeded on %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return
	}

	// Host mode: capture workspace + toolchain location before the guest can chdir.

	initWatchdog()
	// Set an environment variable for the guest to know the host's temp directory if not already set.
	if os.Getenv("MOHABBAT_HOST_TEMPDIR") == "" {
		os.Setenv("MOHABBAT_HOST_TEMPDIR", os.TempDir())
	}

	ref := os.Getenv("MOHABBAT_WASM_FD")
	if ref == "" {
		fmt.Fprintf(os.Stderr, "washmhost: MOHABBAT_WASM_FD not set\n")
		os.Exit(1)
	}

	var r io.Reader
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil {
		r = os.NewFile(uintptr(n), "wasm")
	} else {
		f, err := os.Open(ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "washmhost: failed to open payload: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		r = f
	}

	payloadBytes, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "washmhost: failed to read payload: %v\n", err)
		os.Exit(1)
	}

	argSlice := make([]string, len(parsedArgs))
	copy(argSlice, parsedArgs)
	ensurePosixOutputRunnable(argSlice)

	// Strip a leading "--" separator that `go run . -- [args]` passes through.
	if len(argSlice) > 1 && argSlice[1] == "--" {
		argSlice = append(argSlice[:1], argSlice[2:]...)
	}

	// When launched from brot:
	// - On Windows, if washmhost.exe is argSlice[0] and argSlice[1] is the vegetable, shift it.
	if len(argSlice) > 1 && strings.HasSuffix(strings.ToLower(argSlice[1]), ".bat") {
		argSlice = argSlice[1:]
	} else if _, err := strconv.ParseUint(ref, 10, 64); err != nil && !strings.HasSuffix(strings.ToLower(argSlice[0]), ".bat") {
		// ref is not a numeric FD, assume it is a path to the WASM file.
		argSlice[0] = ref
	}

	exitCode, err := RunWasm(context.Background(), payloadBytes, argSlice)
	if err != nil {
		fmt.Fprintf(os.Stderr, "washmhost: %v\n", err)
		if exitCode == 0 {
			os.Exit(1)
		}
		os.Exit(exitCode)
	}

	os.Exit(exitCode)
}
