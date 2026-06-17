package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// modeBuild is Mode 1: full build pipeline.
// On native, runs prebuild (target specs + sysroot + overlay) via prebuildFn.
// Inside a WASM vegetable, falls back to subprocess if artifacts are missing.
func modeBuild(ws string) error {
	buildDir := filepath.Join(ws, "target", "mohabbat-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	fmt.Println("🍆  Running prebuild (target specs + sysroot + overlay)...")
	if err := runPrebuild(ws); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	fmt.Println("🍆  Building brot (cargo) and washmhost for Modern Four...")
	if err := buildAllSlots(ws, buildDir); err != nil {
		return err
	}

	brainPath := filepath.Join(buildDir, "brain.wasm")
	fmt.Println("🍆  Building brain WASM (mohabbat)...")
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
	buildDir := filepath.Join(ws, "target", "mohabbat-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	projectName := filepath.Base(projectDir)
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	fmt.Printf("🍆  Packaging %s -> %s\n", projectDir, outputPath)
	if err := buildProjectToWasm(ws, projectDir, wasmPath); err != nil {
		return err
	}
	fmt.Println("🍆  Building brot (cargo) and washmhost for Modern Four...")
	if err := buildAllSlots(ws, buildDir); err != nil {
		return err
	}
	return assembleVegetable(ws, wasmPath, buildDir, outputPath)
}

// modeDevRun is Mode 4: build a project to WASM and run it under washmhost.
func modeDevRun(ws, projectDir string, extraArgs []string) error {
	projectName := filepath.Base(projectDir)
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	fmt.Printf("🍆  Dev-run: building %s\n", projectDir)
	if err := buildProjectToWasm(ws, projectDir, wasmPath); err != nil {
		return err
	}
	return runUnderWashmhost(ws, wasmPath, extraArgs)
}

// buildAllSlots builds brot (cargo) and washmhost for all Modern Four slots.
func buildAllSlots(ws, buildDir string) error {
	for _, s := range slots {
		if !shouldBuildSlot(s) {
			fmt.Printf("🍆    skip %s\n", s.name)
			continue
		}
		if _, err := cargoBuild(ws, "brot", s, buildDir); err != nil {
			return err
		}
		if err := goBuild(ws, "washmhost", s, buildDir); err != nil {
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

func shouldBuildSlot(s slot) bool {
	// Build all supported slots on any host. Windows targets are cross-compiled
	// from non-Windows hosts using rusticated target specs and Go cross-build.
	return true
}
