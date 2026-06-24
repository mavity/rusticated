package mohabbat

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// buildRustProjectWasm compiles a Rust project to rusticated WASM.
func buildRustProjectWasm(ws, absProjectDir, outputWasm string, verbose bool) error {
	target := "wasm32-rusticated-unknown-unknown"
	projectName := filepath.Base(absProjectDir)
	fmt.Printf("🍆  Building Rust project %s -> WASM\n", absProjectDir)

	meta := GetBuildMetadata(ws)
	env := upsertEnv(os.Environ(), "RUST_TARGET_PATH", filepath.Join(ws, "target", "rusticated-spec"))
	env = upsertEnv(env, "BUILD_VERSION", meta.Version)
	env = upsertEnv(env, "BUILD_TIME", meta.Time)
	env = upsertEnv(env, "BUILD_PLATFORM", meta.Platform)

	args := []string{"build", "-p", projectName, "--release",
		"--config", filepath.Join(ws, "target", "rusticated-spec", "config.toml"),
		"--config", "unstable.json-target-spec=true",
		"--target", target,
		"-Z", "unstable-options"}
	if verbose {
		args = append(args, "--features", "verbose")
	}
	cmd := exec.Command("cargo", args...)
	cmd.Env = env
	cmd.Dir = ws
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cargo build failed for %s: %w", projectName, err)
	}
	srcWasm := filepath.Join(ws, "target", target, "release", projectName+".wasm")
	data, err := os.ReadFile(srcWasm)
	if err != nil {
		return fmt.Errorf("read built wasm %s: %w", srcWasm, err)
	}
	if err := os.WriteFile(outputWasm, data, 0o644); err != nil {
		return fmt.Errorf("write wasm %s: %w", outputWasm, err)
	}
	return nil
}

func cargoBuild(ws, pkgDir string, s slot, buildDir string, verbose bool) (string, error) {
	targetName, err := cargoTargetName(s)
	if err != nil {
		return "", err
	}
	if err := ensureRustTargetInstalled(targetName); err != nil {
		return "", err
	}

	isRusticatedTarget := strings.Contains(targetName, "rusticated")

	buildTarget := func(name string) error {
		targetArg := name
		// --bin brot selects the binary target; the lib is built as rlib only
		// since the Windows cdylib symbols are now provided by lib.rs.
		args := []string{"build", "--release", "--bin", "brot"}
		if verbose {
			args = append(args, "--features", "verbose")
		}
		if isRusticatedTarget {
			targetPath := filepath.Join(ws, "target", "rusticated-spec", name+".json")
			evalPath, err := filepath.EvalSymlinks(targetPath)
			if err == nil {
				targetPath = evalPath
			}
			targetArg = strings.ReplaceAll(strings.TrimPrefix(targetPath, `\\?\`), `\`, `/`)
			args = append(args, "--config", filepath.Join(ws, "target", "rusticated-spec", "config.toml"))
			args = append(args, "--config", "unstable.json-target-spec=true")
		}
		args = append(args, "--target", targetArg)
		if s.goos == "linux" && !isRusticatedTarget {
			args = append(args, "--config", fmt.Sprintf("target.%s.rustflags=['-C', 'link-self-contained=no', '-C', 'linker=rust-lld', '-C', 'linker-flavor=ld.lld']", name))
		}
		if s.goos == "windows" && (strings.Contains(name, "windows-gnu") || strings.Contains(name, "windows-gnullvm")) {
			// Brot is no_std/no_main and uses raw-dylib for all Win32 APIs.
			// Windows GNU/GNULLVM targets normally inject late-link-args for
			// MinGW libraries (-lmingw32, -lmsvcrt, etc.) and startup objects.
			// These don't exist on non-Windows hosts, and on Windows hosts
			// they might cause "double entry point" conflicts with brot.
			// We use rust-lld with a stub directory to satisfy the linker
			// without requiring a real MinGW environment.
			stubDir := filepath.Join(ws, "target", "brot-stubs")
			if err := ensureBrotStubs(stubDir); err != nil {
				return err
			}
			args = append(args, "--config", fmt.Sprintf("target.%s.rustflags=['-C', 'linker=rust-lld', '-C', 'linker-flavor=ld.lld', '-C', 'link-arg=-L%s']", name, stubDir))
		}
		if s.goos == "darwin" {
			// Cross-compile darwin brot using lld in Mach-O mode (darwin-lld).
			// A minimal libSystem.B.tbd stub is used to satisfy the linker without
			// requiring a full macOS SDK; at runtime macOS provides the real dylib.
			// lld (LLVM 14+) automatically embeds an ad-hoc code signature for
			// arm64 Mach-O binaries, satisfying macOS's signing requirement.
			stubDir := filepath.Join(ws, "target", "darwin-stubs")
			if err := ensureDarwinStubs(stubDir); err != nil {
				return err
			}
			stubDirSlash := filepath.ToSlash(strings.TrimPrefix(stubDir, `\\?\`))
			args = append(args, "--config", fmt.Sprintf(
				"target.%s.rustflags=['-C', 'linker=rust-lld', '-C', 'linker-flavor=darwin-lld', '-Z', 'unstable-options', '-C', 'link-arg=-L%s']",
				name, stubDirSlash))
		}

		meta := GetBuildMetadata(ws)
		cmd := exec.Command("cargo", args...)
		env := os.Environ()
		env = upsertEnv(env, "BUILD_VERSION", meta.Version)
		env = upsertEnv(env, "BUILD_TIME", meta.Time)
		env = upsertEnv(env, "BUILD_PLATFORM", meta.Platform)
		cmd.Env = env

		if isRusticatedTarget {
			cmd.Args = append(cmd.Args, "-Z", "unstable-options")
		}

		cmd.Dir = filepath.Join(ws, pkgDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Printf("🍆    cargo build %s for %s\n", pkgDir, s.name)
		return cmd.Run()
	}

	err = buildTarget(targetName)
	if err != nil {
		return "", fmt.Errorf("%s cargo build failed for %s: %w", pkgDir, s.name, err)
	}

	// Copy the artifact to buildDir
	srcExt := ""
	if s.goos == "windows" {
		srcExt = ".exe"
	}
	srcPath := filepath.Join(ws, "target", targetName, "release", "brot"+srcExt)
	outPath := brotPath(buildDir, s)
	bytes, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(outPath, bytes, 0755); err != nil {
		return "", err
	}
	return targetName, nil
}

func cargoTargetName(s slot) (string, error) {
	if s.goos == "js" {
		return "", fmt.Errorf("cargoTargetName called on js slot")
	}

	targetArch := "x86_64"
	if s.goarch == "arm64" {
		targetArch = "aarch64"
	}

	switch {
	case s.goos == "linux":
		return fmt.Sprintf("%s-unknown-linux-musl", targetArch), nil
	case s.goos == "windows":
		return fmt.Sprintf("%s-pc-windows-gnullvm", targetArch), nil
	case s.goos == "darwin":
		return fmt.Sprintf("%s-apple-darwin", targetArch), nil
	default:
		return "", fmt.Errorf("unsupported slot target %s/%s", s.goos, s.goarch)
	}
}

func ensureRustTargetInstalled(targetName string) error {
	if strings.Contains(targetName, "rusticated") {
		return nil
	}

	check := exec.Command("rustup", "target", "list", "--installed")
	var out bytes.Buffer
	check.Stdout = &out
	check.Stderr = os.Stderr
	if err := check.Run(); err != nil {
		return fmt.Errorf("failed checking installed rust targets: %w", err)
	}
	installed := out.String()
	if strings.Contains(installed, targetName+"\n") || strings.HasSuffix(installed, targetName) {
		return nil
	}

	fmt.Printf("🍆    rustup target add %s\n", targetName)
	addArgs := []string{"target", "add", targetName}
	if tc := strings.TrimSpace(os.Getenv("RUSTUP_TOOLCHAIN")); tc != "" {
		addArgs = append(addArgs, "--toolchain", tc)
	}
	add := exec.Command("rustup", addArgs...)
	add.Stdout = os.Stdout
	add.Stderr = os.Stderr
	if err := add.Run(); err != nil {
		return fmt.Errorf("failed to install rust target %s: %w", targetName, err)
	}
	return nil
}

func rustcHostTriple() (string, error) {
	cmd := exec.Command("rustc", "-vV")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed running rustc -vV: %w", err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "host: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "host: ")), nil
		}
	}
	return "", fmt.Errorf("rustc -vV did not report host triple")
}

func rustcTargetSpecAvailable(target string) bool {
	cmd := exec.Command("rustc", "-Z", "unstable-options", "--print", "target-spec-json", "--target", target)
	return cmd.Run() == nil
}
