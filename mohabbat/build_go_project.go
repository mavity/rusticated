package mohabbat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// buildGoProjectWasm compiles a Go project to rusticated WASM.
func buildGoProjectWasm(ws, absProjectDir, outputWasm string) error {
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	goroot, rootSource, err := resolveGoroot(ws)
	if err != nil {
		return fmt.Errorf("cannot resolve GOROOT: %w", err)
	}
	buildDir := filepath.Join(ws, "target", "mohabbat-build")
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

// buildBrainWasm compiles mohabbat itself as the WASM brain.
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
	cmd.Dir = ws
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
