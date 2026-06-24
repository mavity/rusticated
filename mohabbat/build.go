package mohabbat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ModeBuild is Mode 1: full build pipeline.
// On native, runs prebuild (target specs + sysroot + overlay) via prebuildFn.
// Inside a WASM vegetable, falls back to subprocess if artifacts are missing.
func ModeBuild(ws string, verbose bool) error {
	buildDir := filepath.Join(ws, "target", "mohabbat-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	fmt.Println("🍆  Running prebuild (target specs + sysroot + overlay)...")
	if err := runPrebuild(ws); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	fmt.Println("🍆  Building brotli-wasm base...")
	if err := buildBrotliWasm(ws, buildDir, verbose); err != nil {
		return err
	}

	fmt.Println("🍆  Building brot (cargo) and washmhost for Modern Four...")
	if err := buildAllSlots(ws, buildDir, verbose); err != nil {
		return err
	}

	brainPath := filepath.Join(buildDir, "brain.wasm")
	fmt.Println("🍆  Building brain WASM (mohabbat)...")
	if err := buildBrainWasm(ws, brainPath, verbose); err != nil {
		return fmt.Errorf("brain wasm build: %w", err)
	}

	fmt.Println("🍆  Building node slot...")
	if err := buildNodeSlot(ws, buildDir, verbose); err != nil {
		return err
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

// ModePackage is Mode 3: build a project's payload and assemble a fresh vegetable.
func ModePackage(ws, projectDir, outputPath string, verbose bool) error {
	buildDir := filepath.Join(ws, "target", "mohabbat-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		absProject = projectDir
	}
	projectName := filepath.Base(absProject)
	if projectName == "." || projectName == "" {
		projectName = filepath.Base(ws)
	}

	if strings.HasSuffix(outputPath, ".wasm") {
		fmt.Printf("🍆  Output is .wasm, skipping vegetable assembly: %s\n", outputPath)
		absOutput, _ := filepath.Abs(outputPath)
		return buildProjectToWasm(ws, absProject, absOutput, verbose)
	}
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	fmt.Printf("🍆  Packaging %s -> %s\n", projectDir, outputPath)
	if err := buildProjectToWasm(ws, projectDir, wasmPath, verbose); err != nil {
		return err
	}
	fmt.Println("🍆  Building brotli-wasm base...")
	if err := buildBrotliWasm(ws, buildDir, verbose); err != nil {
		return err
	}
	fmt.Println("🍆  Building brot (cargo) and washmhost for Modern Four...")
	if err := buildAllSlots(ws, buildDir, verbose); err != nil {
		return err
	}
	fmt.Println("🍆  Building node slot...")
	if err := buildNodeSlot(ws, buildDir, verbose); err != nil {
		return err
	}
	return assembleVegetable(ws, wasmPath, buildDir, outputPath)
}

// ModeDevRun is Mode 4: build a project to WASM and run it under washmhost.
func ModeDevRun(ws, projectDir string, extraArgs []string, verbose bool) error {
	absProject, err := filepath.Abs(projectDir)
	if err != nil || (!filepath.IsAbs(absProject) && !fileExists(absProject)) {
		if !filepath.IsAbs(projectDir) {
			absProject = filepath.Join(ws, projectDir)
		} else {
			absProject = projectDir
		}
	}
	projectName := filepath.Base(absProject)
	if projectName == "." || projectName == "" {
		projectName = filepath.Base(ws)
	}

	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	fmt.Printf("🍆  Dev-run: building %s\n", absProject)
	if err := buildProjectToWasm(ws, absProject, wasmPath, verbose); err != nil {
		return err
	}
	return runUnderWashmhost(ws, wasmPath, extraArgs)
}

// buildAllSlots builds brot (cargo) and washmhost for all Modern Four slots.
func buildAllSlots(ws, buildDir string, verbose bool) error {
	for _, s := range slots {
		if !shouldBuildSlot(s) {
			fmt.Printf("🍆    skip %s\n", s.name)
			continue
		}
		if s.goos == "js" {
			continue // Handled during buildNodeSlot when zone A is assembled
		}
		if _, err := cargoBuild(ws, filepath.Join("mohabbat", "brot"), s, buildDir, verbose); err != nil {
			return err
		}
		if err := goBuild(ws, filepath.Join("mohabbat", "washmhost"), s, buildDir, verbose); err != nil {
			return err
		}
	}
	return nil
}

// buildProjectToWasm auto-detects Go vs Rust project and builds to WASM.
func buildProjectToWasm(ws, projectDir, outputWasm string, verbose bool) error {
	// Unconditionally run prebuild to ensure target specs and overlay.json are up to date.
	if err := runPrebuild(ws); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	// Resolve projectDir.
	absProject := projectDir
	if !filepath.IsAbs(absProject) && !(len(absProject) > 2 && absProject[1] == ':' && (absProject[2] == '/' || absProject[2] == '\\')) {
		absProject = filepath.Join(ws, projectDir)
	}

	if !fileExists(absProject) {
		return fmt.Errorf("project directory not found: %s (tried %s)", projectDir, absProject)
	}
	// Auto-detect: Go project has go.mod, Rust project has Cargo.toml.
	if fileExists(filepath.Join(absProject, "go.mod")) {
		return buildGoProjectWasm(ws, absProject, outputWasm, verbose)
	}
	return buildRustProjectWasm(ws, absProject, outputWasm, verbose)
}

func shouldBuildSlot(s slot) bool {
	// Build all supported slots on any host. Windows targets are cross-compiled
	// from non-Windows hosts using rusticated target specs and Go cross-build.
	return true
}

func buildBrotliWasm(ws, buildDir string, verbose bool) error {
	pkgDir := "mohabbat/brot"

	meta := GetBuildMetadata(ws)
	env := os.Environ()
	env = upsertEnv(env, "BUILD_VERSION", meta.Version)
	env = upsertEnv(env, "BUILD_TIME", meta.Time)
	env = upsertEnv(env, "BUILD_PLATFORM", meta.Platform)

	args := []string{"build", "--release", "--lib",
		"--config", "unstable.json-target-spec=true",
		"--target", "wasm32-unknown-unknown"}

	cmd := exec.Command("cargo", args...)
	cmd.Dir = filepath.Join(ws, pkgDir)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cargo build failed for brot (wasm): %w", err)
	}

	srcWasm := filepath.Join(ws, "target", "wasm32-unknown-unknown", "release", "brot.wasm")
	data, err := os.ReadFile(srcWasm)
	if err != nil {
		return fmt.Errorf("read built brot.wasm %s: %w", srcWasm, err)
	}
	outPath := filepath.Join(buildDir, "brotli.wasm")
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write brotli.wasm %s: %w", outPath, err)
	}
	return nil
}
