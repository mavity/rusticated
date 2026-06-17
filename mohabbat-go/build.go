package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// modeBuild is Mode 1: full build pipeline.
// On native, runs prebuild (target specs + sysroot + overlay) via prebuildFn.
// Inside a WASM vegetable, falls back to subprocess if artifacts are missing.
func modeBuild(ws string) error {
	buildDir := filepath.Join(ws, "target", "mohabbat-go-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	fmt.Println("🍆  Running prebuild (target specs + sysroot + overlay)...")
	if err := runPrebuild(ws); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	fmt.Println("🍆  Building brot (cargo) and washmhost-go for Modern Four...")
	if err := buildAllSlots(ws, buildDir); err != nil {
		return err
	}

	brainPath := filepath.Join(buildDir, "brain.wasm")
	fmt.Println("🍆  Building brain WASM (mohabbat-go)...")
	if err := buildBrainWasm(ws, brainPath); err != nil {
		return fmt.Errorf("brain wasm build: %w", err)
	}

	outputPath := filepath.Join(ws, "mohab.bat")
	if err := assembleVegetable(ws, brainPath, buildDir, outputPath); err != nil {
		return err
	}

	if err := ensureBatOnPath("mohab.bat", outputPath); err != nil {
		fmt.Printf("🍆  warn: %v\n", err)
	}
	return nil
}

// modePackage is Mode 3: build a project's payload and assemble a fresh vegetable.
func modePackage(ws, projectDir, outputPath string) error {
	buildDir := filepath.Join(ws, "target", "mohabbat-go-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	projectName := filepath.Base(projectDir)
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	fmt.Printf("🍆  Packaging %s -> %s\n", projectDir, outputPath)
	if err := buildProjectToWasm(ws, projectDir, wasmPath); err != nil {
		return err
	}
	fmt.Println("🍆  Building brot (cargo) and washmhost-go for Modern Four...")
	if err := buildAllSlots(ws, buildDir); err != nil {
		return err
	}
	return assembleVegetable(ws, wasmPath, buildDir, outputPath)
}

// modeDevRun is Mode 4: build a project to WASM and run it under washmhost-go.
func modeDevRun(ws, projectDir string, extraArgs []string) error {
	projectName := filepath.Base(projectDir)
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	fmt.Printf("🍆  Dev-run: building %s\n", projectDir)
	if err := buildProjectToWasm(ws, projectDir, wasmPath); err != nil {
		return err
	}
	return runUnderWashmhost(ws, wasmPath, extraArgs)
}

// buildAllSlots builds brot (cargo) and washmhost-go for all Modern Four slots.
func buildAllSlots(ws, buildDir string) error {
	for _, s := range slots {
		if !shouldBuildSlot(s) {
			fmt.Printf("🍆    skip %s\n", s.name)
			continue
		}
		if _, err := cargoBuild(ws, "brot", s, buildDir); err != nil {
			return err
		}
		if err := goBuild(ws, "washmhost-go", s, buildDir); err != nil {
			return err
		}
	}
	return nil
}

// buildProjectToWasm auto-detects Go vs Rust project and builds to WASM.
func buildProjectToWasm(ws, projectDir, outputWasm string) error {
	vegPath := os.Getenv("MOHABBAT_VEGETABLE_PATH")
	inVeg := vegPath != ""

	// Unconditionally run prebuild to ensure target specs and overlay.json are up to date.
	if err := runPrebuild(ws); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	// Resolve projectDir: first relative to CWD, then relative to workspace root.
	absProject, err := filepath.Abs(projectDir)
	if err != nil || !fileExists(absProject) {
		absProject = filepath.Join(ws, projectDir)
	}

	// Double-check if we are in a vegetable and the "projectDir" is actually the CWD-absolute path of the vegetable.
	if inVeg {
		vAbs, _ := filepath.Abs(vegPath)
		pAbs, _ := filepath.Abs(absProject)
		if strings.EqualFold(vAbs, pAbs) {
			// This was the vegetable path, ignore it if we are looking for a project.
			absProject = ""
		}
	}

	if absProject == "" || !fileExists(absProject) {
		return fmt.Errorf("project directory not found: %s", projectDir)
	}
	// Auto-detect: Go project has go.mod, Rust project has Cargo.toml.
	if fileExists(filepath.Join(absProject, "go.mod")) {
		return buildGoProjectWasm(ws, absProject, outputWasm)
	}
	return buildRustProjectWasm(ws, absProject, outputWasm)
}

// buildGoProjectWasm compiles a Go project to rusticated WASM.
func buildGoProjectWasm(ws, absProjectDir, outputWasm string) error {
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	goroot, rootSource, err := resolveGoroot(ws)
	if err != nil {
		return fmt.Errorf("cannot resolve GOROOT: %w", err)
	}
	buildDir := filepath.Join(ws, "target", "mohabbat-go-build")
	projectName := filepath.Base(absProjectDir)
	goTmpDir := filepath.Join(buildDir, projectName, "gotmp")
	goCacheDir := filepath.Join(buildDir, projectName, "gocache")
	for _, d := range []string{goTmpDir, goCacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	fmt.Println("🍆 SDK " + rootSource + " at " + goroot)
	fmt.Printf("🍆  Building Go project %s -> %s\n", absProjectDir, outputWasm)
	goBin := goBinFromRoot(goroot)
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared",
		"-overlay", overlayPath,
		"-trimpath", "-ldflags=-s -w",
		"-o", outputWasm, ".")
	cmd.Dir = absProjectDir
	env := os.Environ()
	env = upsertEnv(env, "GOOS", "wasip1")
	env = upsertEnv(env, "GOARCH", "wasm")
	env = upsertEnv(env, "GOROOT", goroot)
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build failed for %s: %w", absProjectDir, err)
	}
	fmt.Printf("🍆  Post-processing %s (rename _initialize -> run)\n", outputWasm)
	return postProcessWasm(outputWasm)
}

// buildRustProjectWasm compiles a Rust project to rusticated WASM.
func buildRustProjectWasm(ws, absProjectDir, outputWasm string) error {
	target := "wasm32-rusticated-unknown-unknown"
	projectName := filepath.Base(absProjectDir)
	fmt.Printf("🍆  Building Rust project %s -> WASM\n", absProjectDir)
	cmd := exec.Command("cargo", "build", "-p", projectName, "--release",
		"--config", filepath.Join(ws, "target", "rusticated-spec", "config.toml"),
		"--config", "unstable.json-target-spec=true",
		"--target", target,
		"-Z", "unstable-options")
	cmd.Env = upsertEnv(os.Environ(), "RUST_TARGET_PATH", filepath.Join(ws, "target", "rusticated-spec"))
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

// buildBrainWasm compiles mohabbat-go itself as the WASM brain.
func buildBrainWasm(ws, outputWasm string) error {
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	goroot, rootSource, err := resolveGoroot(ws)
	if err != nil {
		return fmt.Errorf("cannot resolve GOROOT for brain build: %w", err)
	}
	buildDir := filepath.Dir(outputWasm)
	goTmpDir := filepath.Join(buildDir, "brain-gotmp")
	goCacheDir := filepath.Join(buildDir, "brain-gocache")
	for _, d := range []string{goTmpDir, goCacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	fmt.Println("🍆 SDK " + rootSource + " at " + goroot)
	fmt.Printf("🍆  Building brain WASM -> %s\n", outputWasm)
	goBin := goBinFromRoot(goroot)
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared",
		"-overlay", overlayPath,
		"-trimpath", "-ldflags=-s -w",
		"-o", outputWasm, ".")
	cmd.Dir = filepath.Join(ws, "mohabbat-go")
	env := os.Environ()
	env = upsertEnv(env, "GOOS", "wasip1")
	env = upsertEnv(env, "GOARCH", "wasm")
	env = upsertEnv(env, "GOROOT", goroot)
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("brain WASM build failed: %w", err)
	}
	return postProcessWasm(outputWasm)
}

func cargoBuild(ws, pkgDir string, s slot, buildDir string) (string, error) {
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
		args := []string{"build", "--release"}
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
		cmd := exec.Command("cargo", args...)
		env := os.Environ()
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

func goBuild(ws, pkgDir string, s slot, buildDir string) error {
	outPath := washmhostPath(buildDir, s)
	if err := os.Remove(outPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale output %s: %w", outPath, err)
	}
	goTmpDir := filepath.Join(buildDir, pkgDir, "gotmp", s.name)
	goCacheDir := filepath.Join(buildDir, pkgDir, "gocache", s.name)
	if err := os.MkdirAll(goTmpDir, 0o755); err != nil {
		return fmt.Errorf("create GOTMPDIR %s: %w", goTmpDir, err)
	}
	if err := os.MkdirAll(goCacheDir, 0o755); err != nil {
		return fmt.Errorf("create GOCACHE %s: %w", goCacheDir, err)
	}

	// Option B: Use a temporary .dat file for the build instead of the default
	// a.out.exe to avoid aggressive Windows Defender scanning.
	// Note: go build -o - is avoided here because on some Windows environments
	// it incorrectly creates a literal file named "-" instead of streaming.
	tmpOut := filepath.Join(goTmpDir, "build.dat")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", tmpOut, ".")
	cmd.Dir = filepath.Join(ws, pkgDir)
	env := os.Environ()
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOOS", s.goos)
	env = upsertEnv(env, "GOARCH", s.goarch)
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	env = upsertEnv(env, "TMP", goTmpDir)
	env = upsertEnv(env, "TEMP", goTmpDir)
	cmd.Env = env

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("🍆    go build %s for %s -> %s\n", pkgDir, s.name, filepath.Base(outPath))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build failed for %s: %w", pkgDir, s.name, err)
	}

	buildResult, err := os.ReadFile(tmpOut)
	if err != nil {
		return fmt.Errorf("read build result %s: %w", tmpOut, err)
	}

	if len(buildResult) == 0 {
		return fmt.Errorf("%s build for %s produced 0 bytes", pkgDir, s.name)
	}

	if err := os.WriteFile(outPath, buildResult, 0755); err != nil {
		return fmt.Errorf("write %s to %s: %w", pkgDir, outPath, err)
	}
	return nil
}

// postProcessWasm renames the _initialize export to run in a WASM binary.
func postProcessWasm(wasmPath string) error {
	data, err := os.ReadFile(wasmPath)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("invalid wasm file: too small")
	}
	pos := 8
	var newData []byte
	newData = append(newData, data[:8]...)
	for pos < len(data) {
		sectionID := data[pos]
		pos++
		size, n, err := readVarUint32(data[pos:])
		if err != nil {
			return fmt.Errorf("wasm section size: %w", err)
		}
		pos += n
		sectionEnd := pos + int(size)
		if sectionEnd > len(data) {
			return fmt.Errorf("wasm section overflows file")
		}
		if sectionID == 7 { // Export section
			exportData := data[pos:sectionEnd]
			count, n2, err := readVarUint32(exportData)
			if err != nil {
				return fmt.Errorf("export count: %w", err)
			}
			var newExportSec []byte
			newExportSec = append(newExportSec, encodeVarUint32(count)...)
			p := n2
			found := false
			for i := uint32(0); i < count; i++ {
				nameLen, n3, err := readVarUint32(exportData[p:])
				if err != nil {
					return fmt.Errorf("export name len: %w", err)
				}
				p += n3
				name := string(exportData[p : p+int(nameLen)])
				p += int(nameLen)
				kind := exportData[p]
				p++
				idx, n4, err := readVarUint32(exportData[p:])
				if err != nil {
					return fmt.Errorf("export idx: %w", err)
				}
				p += n4
				if name == "_initialize" {
					name = "run"
					found = true
				}
				newExportSec = append(newExportSec, encodeVarUint32(uint32(len(name)))...)
				newExportSec = append(newExportSec, name...)
				newExportSec = append(newExportSec, kind)
				newExportSec = append(newExportSec, encodeVarUint32(idx)...)
			}
			if found {
				newData = append(newData, sectionID)
				newData = append(newData, encodeVarUint32(uint32(len(newExportSec)))...)
				newData = append(newData, newExportSec...)
			} else {
				newData = append(newData, data[pos-n-1:sectionEnd]...)
			}
		} else {
			newData = append(newData, data[pos-n-1:sectionEnd]...)
		}
		pos = sectionEnd
	}
	return os.WriteFile(wasmPath, newData, 0o644)
}

func readVarUint32(data []byte) (uint32, int, error) {
	var res uint32
	var shift uint
	for i, b := range data {
		res |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			return res, i + 1, nil
		}
		shift += 7
		if shift >= 32 {
			break
		}
	}
	return 0, 0, fmt.Errorf("invalid leb128")
}

func encodeVarUint32(v uint32) []byte {
	var res []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			res = append(res, b|0x80)
		} else {
			res = append(res, b)
			break
		}
	}
	return res
}

func cargoTargetName(s slot) (string, error) {
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

func shouldBuildSlot(s slot) bool {
	// Build all supported slots on any host. Windows targets are cross-compiled
	// from non-Windows hosts using rusticated target specs and Go cross-build.
	return true
}
