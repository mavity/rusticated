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

	vlogf(verbose, "🍆  Running prebuild (target specs + sysroot + overlay)...\n")
	if err := runPrebuild(ws, verbose, PrebuildScopeAll); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	vlogf(verbose, "🍆  Building brotli-wasm base...\n")
	if err := buildBrotliWasm(ws, buildDir, verbose); err != nil {
		return err
	}

	vlogf(verbose, "🍆  Building brot (cargo) and washmhost for Modern Four...\n")
	if err := buildAllSlots(ws, buildDir, verbose); err != nil {
		return err
	}

	brainPath := filepath.Join(buildDir, "brain.wasm")
	vlogf(verbose, "🍆  Building brain WASM (mohabbat)...\n")
	if err := buildBrainWasm(ws, brainPath, verbose); err != nil {
		return fmt.Errorf("brain wasm build: %w", err)
	}

	vlogf(verbose, "🍆  Building node slot...\n")
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
		vlogf(verbose, "🍆  Output is .wasm, skipping vegetable assembly: %s\n", outputPath)
		absOutput, _ := filepath.Abs(outputPath)
		return buildProjectToWasm(ws, absProject, absOutput, verbose)
	}
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	vlogf(verbose, "🍆  Packaging %s -> %s\n", projectDir, outputPath)
	if err := buildProjectToWasm(ws, projectDir, wasmPath, verbose); err != nil {
		return err
	}
	vlogf(verbose, "🍆  Building brotli-wasm base...\n")
	if err := buildBrotliWasm(ws, buildDir, verbose); err != nil {
		return err
	}
	vlogf(verbose, "🍆  Building brot (cargo) and washmhost for Modern Four...\n")
	if err := buildAllSlots(ws, buildDir, verbose); err != nil {
		return err
	}
	vlogf(verbose, "🍆  Building node slot...\n")
	if err := buildNodeSlot(ws, buildDir, verbose); err != nil {
		return err
	}
	return assembleVegetable(ws, wasmPath, buildDir, outputPath)
}

// ModeDevRun is Mode 4: build a project to WASM and run it under washmhost.
func ModeDevRun(ws, projectDir string, extraArgs []string, platform string, verbose bool) error {
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
	vlogf(verbose, "🍆  Dev-run: building %s\n", absProject)
	if err := buildProjectToWasm(ws, absProject, wasmPath, verbose); err != nil {
		return err
	}
	return runUnderWashmhost(ws, wasmPath, extraArgs, platform, verbose, "", 0)
}

// ModeTest is the test pipeline: build project tests to WASM and run under washmhost.
func ModeTest(ws, projectDir string, extraArgs []string, platform string, verbose bool) error {
	testTimeout, err := parseGoTestTimeout(extraArgs)
	if err != nil {
		return err
	}

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

	wasmPath, err := filepath.Abs(filepath.Join(ws, "target", projectName+"_test.wasm"))
	if err != nil {
		wasmPath = filepath.Join(ws, "target", projectName+"_test.wasm")
	}
	vlogf(verbose, "🍆  Test-run: building tests for %s\n", absProject)

	// Run prebuild scoped only to what this project type needs (Go).
	if err := runPrebuild(ws, verbose, PrebuildScopeGoOnly); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	if err := buildGoTestWasm(ws, absProject, wasmPath, verbose); err != nil {
		return err
	}

	return runUnderWashmhost(ws, wasmPath, extraArgs, platform, verbose, absProject, testTimeout)
}

// buildAllSlots builds brot (cargo) and washmhost for all Modern Four slots.
func buildAllSlots(ws, buildDir string, verbose bool) error {
	// Step 1: Build washmhost for all slots
	if _, err := buildAllWashmhost(ws, buildDir, verbose); err != nil {
		return err
	}

	// Step 2: Build brot
	for _, s := range slots {
		if !shouldBuildSlot(s) {
			vlogf(verbose, "🍆    skip %s\n", s.name)
			continue
		}
		if s.goos == "js" {
			continue // Handled during buildNodeSlot when zone A is assembled
		}
		if _, err := cargoBuild(ws, filepath.Join("mohabbat", "brot"), s, buildDir, verbose); err != nil {
			return err
		}
	}
	return nil
}

// buildAllWashmhost builds washmhost for all slots and returns their sizes.
func buildAllWashmhost(ws, buildDir string, verbose bool) (map[string]uint64, error) {
	metadata := make(map[string]uint64)
	for _, s := range slots {
		if !shouldBuildSlot(s) {
			vlogf(verbose, "🍆    skip %s washmhost\n", s.name)
			continue
		}
		if s.goos == "js" {
			continue // washmhost not needed for js
		}
		if err := goBuild(ws, filepath.Join("mohabbat", "washmhost"), s, buildDir, verbose); err != nil {
			return nil, err
		}
		// Read the built washmhost to get its size
		whPath := washmhostPath(buildDir, s)
		whData, err := os.ReadFile(whPath)
		if err != nil {
			return nil, fmt.Errorf("read washmhost for %s: %w", s.name, err)
		}
		metadata[s.name] = uint64(len(whData))
	}
	return metadata, nil
}

// findGoMod walks up the directory tree to find the directory containing go.mod
func findGoMod(dir string) string {
	for {
		if fileExists(filepath.Join(dir, "go.mod")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// buildProjectToWasm auto-detects Go vs Rust project and builds to WASM.
func buildProjectToWasm(ws, projectDir, outputWasm string, verbose bool) error {
	// Resolve projectDir.
	absProject := projectDir
	if !filepath.IsAbs(absProject) && !(len(absProject) > 2 && absProject[1] == ':' && (absProject[2] == '/' || absProject[2] == '\\')) {
		absProject = filepath.Join(ws, projectDir)
	}

	if !fileExists(absProject) {
		return fmt.Errorf("project directory not found: %s (tried %s)", projectDir, absProject)
	}

	// Auto-detect: Go project has go.mod, Rust project has Cargo.toml.
	// Walk up to find go.mod to support building subpackages.
	isRust := absProject != ws && fileExists(filepath.Join(absProject, "Cargo.toml"))
	isGo := !isRust && findGoMod(absProject) != ""

	scope := PrebuildScopeRustWasmOnly
	if isGo {
		scope = PrebuildScopeGoOnly
	}

	// Run prebuild scoped only to what this project type needs.
	if err := runPrebuild(ws, verbose, scope); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	if isGo {
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

	if err := runCmd(cmd, "cargo build brot (wasm)", verbose); err != nil {
		return fmt.Errorf("cargo build failed for brot (wasm): %w", err)
	}

	srcWasm := filepath.Join(ws, "target", "wasm32-unknown-unknown", "release", "brot.wasm")
	data, err := os.ReadFile(srcWasm)
	if err != nil {
		return fmt.Errorf("read built brot.wasm %s: %w", srcWasm, err)
	}
	outPath := filepath.Join(buildDir, "brotli.wasm")
	if err := writeFileIfChanged(outPath, data); err != nil {
		return fmt.Errorf("write brotli.wasm %s: %w", outPath, err)
	}
	return nil
}
