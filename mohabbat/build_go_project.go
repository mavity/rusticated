package mohabbat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// buildGoProjectWasm compiles a Go project to rusticated WASM.
func buildGoProjectWasm(ws, absProjectDir, outputWasm string, verbose bool) error {
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

	goModDir := findGoMod(absProjectDir)
	if goModDir == "" {
		goModDir = absProjectDir
	}

	// Apply wasip1 dependency patches: copy affected modules to a mutable
	// rusticated cache, flip their build-tag gates, and generate a temp go.mod
	// with replace directives plus overlay entries for suffix-flip aliases.
	patchResult, err := applyWasip1DepPatches(ws, goModDir, goroot)
	if err != nil {
		return fmt.Errorf("wasip1 dep patches: %w", err)
	}

	// Determination of effective overlay: merge suffix-flip extras if any.
	effectiveOverlay := overlayPath
	if len(patchResult.overlayExtra) > 0 {
		mergedPath := filepath.Join(buildDir, projectName+"-overlay.json")
		effectiveOverlay, err = mergeOverlay(overlayPath, patchResult.overlayExtra, mergedPath)
		if err != nil {
			return fmt.Errorf("merge overlay: %w", err)
		}
	}

	vlogf(verbose, "🍆 SDK %s at %s\n", rootSource, goroot)
	vlogf(verbose, "🍆  Building Go project %s -> %s\n", absProjectDir, outputWasm)
	goBin := goBinFromRoot(goroot)

	args := []string{"build", "-buildmode=c-shared",
		"-overlay", effectiveOverlay,
		"-trimpath", "-ldflags=-s -w"}
	if verbose {
		args = append(args, "-tags=verbose")
	}
	args = append(args, "-o", outputWasm, ".")
	cmd := exec.Command(goBin, args...)
	cmd.Dir = absProjectDir
	env := os.Environ()
	env = upsertEnv(env, "GOOS", "wasip1")
	env = upsertEnv(env, "GOARCH", "wasm")
	env = upsertEnv(env, "GOROOT", goroot)
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	cmd.Env = env
	if err := runCmd(cmd, "go build "+filepath.Base(absProjectDir)+" (wasm)", verbose); err != nil {
		return fmt.Errorf("go build failed for %s: %w", absProjectDir, err)
	}
	vlogf(verbose, "🍆  Post-processing %s (rename _initialize -> run)\n", outputWasm)
	return postProcessWasm(outputWasm)
}

// buildGoTestWasm compiles a Go project's tests to rusticated WASM.
func buildGoTestWasm(ws, absProjectDir, outputWasm string, verbose bool) error {
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	goroot, rootSource, err := resolveGoroot(ws)
	if err != nil {
		return fmt.Errorf("cannot resolve GOROOT: %w", err)
	}
	buildDir := filepath.Join(ws, "target", "mohabbat-build")
	projectName := filepath.Base(absProjectDir)
	goTmpDir := filepath.Join(buildDir, projectName, "gotmp-test")
	goCacheDir := filepath.Join(buildDir, projectName, "gocache-test")
	for _, d := range []string{goTmpDir, goCacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	goModDir := findGoMod(absProjectDir)
	if goModDir == "" {
		goModDir = absProjectDir
	}

	// Apply wasip1 dependency patches: copy affected modules to a mutable
	// rusticated cache, flip their build-tag gates, and generate a temp go.mod
	// with replace directives plus overlay entries for suffix-flip aliases.
	patchResult, err := applyWasip1DepPatches(ws, goModDir, goroot)
	if err != nil {
		return fmt.Errorf("wasip1 dep patches: %w", err)
	}

	// Determination of effective overlay: merge suffix-flip extras if any.
	effectiveOverlay := overlayPath
	if len(patchResult.overlayExtra) > 0 {
		mergedPath := filepath.Join(buildDir, projectName+"-test-overlay.json")
		effectiveOverlay, err = mergeOverlay(overlayPath, patchResult.overlayExtra, mergedPath)
		if err != nil {
			return fmt.Errorf("merge overlay: %w", err)
		}
	}

	vlogf(verbose, "🍆 SDK %s at %s\n", rootSource, goroot)
	vlogf(verbose, "🍆  Building Go tests %s -> %s\n", absProjectDir, outputWasm)
	goBin := goBinFromRoot(goroot)

	args := []string{"test", "-c", "-buildmode=c-shared",
		"-overlay", effectiveOverlay,
		"-trimpath", "-ldflags=-s -w"}
	if verbose {
		args = append(args, "-tags=verbose")
	}
	args = append(args, "-o", outputWasm, ".")
	cmd := exec.Command(goBin, args...)
	cmd.Dir = absProjectDir
	env := os.Environ()
	env = upsertEnv(env, "GOOS", "wasip1")
	env = upsertEnv(env, "GOARCH", "wasm")
	env = upsertEnv(env, "GOROOT", goroot)
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	cmd.Env = env
	if err := runCmd(cmd, "go test -c "+filepath.Base(absProjectDir)+" (wasm)", verbose); err != nil {
		return fmt.Errorf("go test -c failed for %s: %w", absProjectDir, err)
	}
	vlogf(verbose, "🍆  Post-processing %s (rename _initialize -> run)\n", outputWasm)
	return postProcessWasm(outputWasm)
}

// buildBrainWasm compiles mohabbat itself as the WASM brain.
func buildBrainWasm(ws, outputWasm string, verbose bool) error {
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
	vlogf(verbose, "🍆 SDK %s at %s\n", rootSource, goroot)
	vlogf(verbose, "🍆  Building brain WASM -> %s\n", outputWasm)
	goBin := goBinFromRoot(goroot)
	args := []string{"build", "-buildmode=c-shared",
		"-overlay", overlayPath,
		"-trimpath", "-ldflags=-s -w"}
	if verbose {
		args = append(args, "-tags=verbose")
	}
	args = append(args, "-o", outputWasm, ".")
	cmd := exec.Command(goBin, args...)
	cmd.Dir = ws
	env := os.Environ()
	env = upsertEnv(env, "GOOS", "wasip1")
	env = upsertEnv(env, "GOARCH", "wasm")
	env = upsertEnv(env, "GOROOT", goroot)
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	cmd.Env = env
	if err := runCmd(cmd, "go build brain (wasm)", verbose); err != nil {
		return fmt.Errorf("brain WASM build failed: %w", err)
	}
	return postProcessWasm(outputWasm)
}

// goBuildPIE reports whether the native washmhost for slot s is built as a
// position-independent executable. Go's internal linker (CGO_ENABLED=0)
// supports PIE on linux/amd64 and linux/arm64 only; linux/arm would need an
// external linker, which this pipeline does not use.
func goBuildPIE(s slot) bool {
	return s.goos == "linux" && (s.goarch == "amd64" || s.goarch == "arm64")
}

func goBuild(ws, pkgDir string, s slot, buildDir string, verbose bool) error {
	outPath := washmhostPath(buildDir, s)
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
	meta := GetBuildMetadata(ws)
	ldFlags := fmt.Sprintf("-s -w -X main.BuildVersion=%s -X main.BuildTime=%s -X main.BuildPlatform=%s",
		meta.Version, meta.Time, meta.Platform)

	tmpOut := filepath.Join(goTmpDir, "build.dat")
	args := []string{"build", "-trimpath", "-ldflags=" + ldFlags}
	if goBuildPIE(s) {
		// Static-PIE (ET_DYN): Android's linker rejects ET_EXEC with
		// "unexpected e_type: 2"; desktop Linux runs PIE just as well.
		args = append(args, "-buildmode=pie")
	}
	if verbose {
		args = append(args, "-tags=verbose")
	}
	args = append(args, "-o", tmpOut, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = filepath.Join(ws, pkgDir)
	env := os.Environ()
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOOS", s.goos)
	env = upsertEnv(env, "GOARCH", s.goarch)
	if s.goarm != "" {
		env = upsertEnv(env, "GOARM", s.goarm)
	}
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	env = upsertEnv(env, "TMP", goTmpDir)
	env = upsertEnv(env, "TEMP", goTmpDir)
	cmd.Env = env

	vlogf(verbose, "🍆    go build %s for %s -> %s\n", pkgDir, s.name, filepath.Base(outPath))
	if err := runCmd(cmd, fmt.Sprintf("go build %s for %s", pkgDir, s.name), verbose); err != nil {
		return fmt.Errorf("%s build failed for %s: %w", pkgDir, s.name, err)
	}

	buildResult, err := os.ReadFile(tmpOut)
	if err != nil {
		return fmt.Errorf("read build result %s: %w", tmpOut, err)
	}

	if len(buildResult) == 0 {
		return fmt.Errorf("%s build for %s produced 0 bytes", pkgDir, s.name)
	}

	if err := writeFileIfChangedMode(outPath, buildResult, 0o755); err != nil {
		return fmt.Errorf("write %s to %s: %w", pkgDir, outPath, err)
	}
	return nil
}
