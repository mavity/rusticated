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
	vlogf(verbose, "🍆  Building Rust project %s -> WASM\n", absProjectDir)

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
	if err := runCmd(cmd, "cargo build "+projectName+" (wasm)", verbose); err != nil {
		return fmt.Errorf("cargo build failed for %s: %w", projectName, err)
	}
	srcWasm := filepath.Join(ws, "target", target, "release", projectName+".wasm")
	data, err := os.ReadFile(srcWasm)
	if err != nil {
		return fmt.Errorf("read built wasm %s: %w", srcWasm, err)
	}
	if err := writeFileIfChanged(outputWasm, data); err != nil {
		return fmt.Errorf("write wasm %s: %w", outputWasm, err)
	}
	return nil
}

func cargoBuild(ws, pkgDir string, s slot, buildDir string, verbose bool) (string, error) {
	targetName, err := cargoTargetName(s)
	if err != nil {
		return "", err
	}

	isRusticatedTarget := strings.Contains(targetName, "rusticated")
	absPkgDir := filepath.Join(ws, pkgDir)

	// buildTarget runs the cargo build. overlaySysroot is empty on the normal
	// (first) attempt and is only set on a failure-driven retry, where it is
	// appended as an additive `--sysroot` to the target's rustflags.
	buildTarget := func(name, overlaySysroot string) error {
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

		// Assemble the per-target rustflags as a single list so that the overlay
		// `--sysroot` (retry only) is additive to the existing linker flags
		// rather than overriding them (cargo takes the last rustflags config).
		var rustflags []string
		if s.goos == "linux" && !isRusticatedTarget {
			rustflags = append(rustflags, "'-C'", "'link-self-contained=no'", "'-C'", "'linker=rust-lld'", "'-C'", "'linker-flavor=ld.lld'")
			// Static-PIE (ET_DYN, no PT_INTERP). Android's linker rejects
			// ET_EXEC; brot's _start applies the relocations itself.
			rustflags = append(rustflags,
				"'-C'", "'relocation-model=pic'",
				"'-C'", "'target-feature=+crt-static'",
				"'-C'", "'link-arg=-pie'",
				"'-C'", "'link-arg=--no-dynamic-linker'")
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
			rustflags = append(rustflags, "'-C'", "'linker=rust-lld'", "'-C'", "'linker-flavor=ld.lld'", "'-C'", fmt.Sprintf("'link-arg=-L%s'", stubDir))
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
			rustflags = append(rustflags, "'-C'", "'linker=rust-lld'", "'-C'", "'linker-flavor=darwin-lld'", "'-Z'", "'unstable-options'", "'-C'", fmt.Sprintf("'link-arg=-L%s'", stubDirSlash))
		}
		if overlaySysroot != "" {
			rustflags = append(rustflags, "'--sysroot'", fmt.Sprintf("'%s'", overlaySysroot))
		}
		if len(rustflags) > 0 {
			args = append(args, "--config", fmt.Sprintf("target.%s.rustflags=[%s]", name, strings.Join(rustflags, ", ")))
		}

		meta := GetBuildMetadata(ws)
		cmd := exec.Command("cargo", args...)
		env := os.Environ()
		env = upsertEnv(env, "BUILD_VERSION", meta.Version)
		env = upsertEnv(env, "BUILD_TIME", meta.Time)
		env = upsertEnv(env, "BUILD_PLATFORM", meta.Platform)
		// Set SDKROOT to the stubs directory for darwin cross-compilation so lld
		// doesn't call xcrun to locate the macOS SDK (unavailable on non-Mac hosts).
		if s.goos == "darwin" {
			env = upsertEnv(env, "SDKROOT", filepath.Join(ws, "target", "darwin-stubs"))
		}
		cmd.Env = env

		if isRusticatedTarget {
			cmd.Args = append(cmd.Args, "-Z", "unstable-options")
		}

		cmd.Dir = absPkgDir
		vlogf(verbose, "🍆    cargo build %s for %s\n", pkgDir, s.name)
		return runCmd(cmd, fmt.Sprintf("cargo build %s for %s", pkgDir, s.name), verbose)
	}

	// Normal attempt — identical to the historical behavior (no overlay, no
	// pre-flight probe). Only if it fails do we investigate and remediate.
	err = buildTarget(targetName, "")
	if err != nil {
		// Classify: is the failure caused by a genuinely missing target std?
		if !isRusticatedTarget && !rustTargetStdPresent(absPkgDir, targetName) {
			overlaySysroot, remErr := remediateMissingTarget(absPkgDir, targetName)
			if remErr != nil {
				return "", fmt.Errorf("%s cargo build failed for %s (target %q missing, fallback failed: %v)", pkgDir, s.name, targetName, remErr)
			}
			// Retry exactly once. overlaySysroot is "" when rustup supplied the
			// target (nothing extra needed) or a path when the overlay was built.
			if err = buildTarget(targetName, overlaySysroot); err == nil {
				goto copyArtifact
			}
		}
		return "", fmt.Errorf("%s cargo build failed for %s: %w", pkgDir, s.name, err)
	}

copyArtifact:

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
	if err := writeFileIfChangedMode(outPath, bytes, 0o755); err != nil {
		return "", err
	}
	return targetName, nil
}

func cargoTargetName(s slot) (string, error) {
	if s.goos == "js" {
		return "", fmt.Errorf("cargoTargetName called on js slot")
	}

	if s.goarch == "arm" && s.goos == "linux" {
		return "armv7-unknown-linux-musleabihf", nil
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

// remediateMissingTarget is invoked only after a brot build has already failed
// and the failure was classified as a genuinely missing target std. It tries
// rustup first (if present) and falls back to composing a project-local overlay
// sysroot that supplies the target. It returns an overlay sysroot path to pass
// to the retried build via `--sysroot`, or "" when rustup satisfied the target
// (nothing extra needed on retry).
func remediateMissingTarget(dir, targetName string) (string, error) {
	if hasRustup() {
		fmt.Printf("🍆    rustup target add %s\n", targetName)
		addArgs := []string{"target", "add", targetName}
		if tc := strings.TrimSpace(os.Getenv("RUSTUP_TOOLCHAIN")); tc != "" {
			addArgs = append(addArgs, "--toolchain", tc)
		}
		add := exec.Command("rustup", addArgs...)
		add.Stdout = os.Stdout
		add.Stderr = os.Stderr
		if add.Run() == nil {
			return "", nil
		}
		fmt.Printf("🍆    rustup target add failed; composing overlay sysroot for %s\n", targetName)
	} else {
		fmt.Printf("🍆    rustup not found; composing overlay sysroot for %s\n", targetName)
	}

	tc, err := resolveToolchain(dir)
	if err != nil {
		return "", fmt.Errorf("resolve toolchain: %w", err)
	}
	overlayDir := filepath.Join("target", "rust-sysroot-overlay")
	overlayPath, err := composeOverlay(tc, overlayDir, []string{targetName})
	if err != nil {
		return "", fmt.Errorf("compose overlay sysroot: %w", err)
	}
	return overlayPath, nil
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
