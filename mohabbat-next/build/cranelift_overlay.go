package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	craneliftOverlayName   = "cranelift-codegen-overlay"
	craneliftWorkspaceName = "runtime-cranelift-overlay"
)

type craneliftSourcePatch struct {
	name        string
	pattern     string
	replacement string
}

var contextIsaPatches = []craneliftSourcePatch{
	{
		name:        "Context::compile_and_emit ISA argument",
		pattern:     `(?s)(pub fn compile_and_emit\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
	{
		name:        "Context::compile_stencil ISA argument",
		pattern:     `(?s)(pub fn compile_stencil\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
	{
		name:        "Context::optimize ISA argument",
		pattern:     `(?s)(pub fn optimize\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
	{
		name:        "Context::compile ISA argument",
		pattern:     `(?s)(pub fn compile\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
	{
		name:        "Context::canonicalize_nans ISA argument",
		pattern:     `(?s)(pub fn canonicalize_nans\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
}

var calleeIsaPatch = craneliftSourcePatch{
	name:        "Callee::new ISA argument",
	pattern:     `(?s)(pub fn new\s*\(\s*f:\s*&ir::Function,\s*isa:\s*)&dyn TargetIsa`,
	replacement: `$1&impl TargetIsa`,
}

var flagsOrIsaPatch = craneliftSourcePatch{
	name:    "FlagsOrIsa generic concrete-ISA conversion",
	pattern: `impl<'a> From<&'a dyn TargetIsa> for FlagsOrIsa<'a> \{`,
	replacement: `impl<'a, T: TargetIsa> From<&'a T> for FlagsOrIsa<'a> {
    fn from(isa: &'a T) -> FlagsOrIsa<'a> {
        FlagsOrIsa {
            flags: isa.flags(),
            isa: Some(isa),
        }
    }
}

impl<'a> From<&'a dyn TargetIsa> for FlagsOrIsa<'a> {`,
}

var compileHookPatch = craneliftSourcePatch{
	name:    "Context::compile_with_hook compatibility wrapper",
	pattern: `(?s)(    /// Compile the function,\r?\n    ///\r?\n    /// Run the function through all the passes necessary to generate)`,
	replacement: `    /// Compile the function and report completion to a diagnostic hook.
    pub fn compile_with_hook(
        &mut self,
        isa: &impl TargetIsa,
        ctrl_plane: &mut ControlPlane,
        mut on_phase: impl FnMut(u8),
    ) -> CompileResult<'_, &CompiledCode> {
        let result = self.compile(isa, ctrl_plane);
        if result.is_ok() {
            on_phase(1);
        }
        result
    }

$1`,
}

var x64BackendVisibilityPatch = craneliftSourcePatch{
	name:        "X64Backend visibility for concrete runtime construction",
	pattern:     `pub\(crate\) struct X64Backend`,
	replacement: `pub struct X64Backend`,
}

var x64ConstructorVisibilityPatch = craneliftSourcePatch{
	name:        "X64Backend::new_with_flags visibility",
	pattern:     `(?m)^    fn new_with_flags\(`,
	replacement: `    pub fn new_with_flags(`,
}

var hashbrownManifestPatch = craneliftSourcePatch{
	name:        "disable hashbrown default hasher in freestanding Cranelift",
	pattern:     `(?s)(\[dependencies\.hashbrown\]\r?\nversion = "0\.17"\r?\n)features = \["default-hasher"\]\r?\n`,
	replacement: `$1`,
}

var noStdHashAliasesPatch = craneliftSourcePatch{
	name:    "use Fx hashing for freestanding Cranelift maps",
	pattern: `(?s)#\[cfg\(not\(feature = "std"\)\)\]\r?\nuse hashbrown::\{HashMap, HashSet, hash_map\};`,
	replacement: `#[cfg(not(feature = "std"))]
use hashbrown::{HashMap as HashBrownMap, HashSet as HashBrownSet, hash_map};
#[cfg(not(feature = "std"))]
type HashMap<K, V, S = rustc_hash::FxBuildHasher> = HashBrownMap<K, V, S>;
#[cfg(not(feature = "std"))]
type HashSet<V, S = rustc_hash::FxBuildHasher> = HashBrownSet<V, S>;`,
}

var hashMapNewPatch = craneliftSourcePatch{
	name:        "construct freestanding hash map with Fx hasher",
	pattern:     `HashMap::new\(\)`,
	replacement: `HashMap::with_hasher(rustc_hash::FxBuildHasher::default())`,
}

var hashMapCapacityPatch = craneliftSourcePatch{
	name:        "construct freestanding hash map with capacity and Fx hasher",
	pattern:     `HashMap::with_capacity\(expected_num_constants\)`,
	replacement: `HashMap::with_capacity_and_hasher(expected_num_constants, rustc_hash::FxBuildHasher::default())`,
}

var dynamicTypeHashMapCapacityPatch = craneliftSourcePatch{
	name:        "construct dynamic type map with capacity and Fx hasher",
	pattern:     `HashMap::with_capacity\(f\.dfg\.dynamic_types\.len\(\)\)`,
	replacement: `HashMap::with_capacity_and_hasher(f.dfg.dynamic_types.len(), rustc_hash::FxBuildHasher::default())`,
}

var cargoPackageVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)

func prepareCraneliftOverlay(runtimeDir, rustBuildDir string, verbose bool) (workspaceDir string, retErr error) {
	lockPath := filepath.Join(runtimeDir, "Cargo.lock")
	lockData, err := os.ReadFile(lockPath)
	if err != nil {
		return "", fmt.Errorf("read Cranelift lockfile %s: %w", lockPath, err)
	}
	version, err := lockedCraneliftVersion(string(lockData))
	if err != nil {
		return "", fmt.Errorf("resolve locked Cranelift version: %w", err)
	}

	cargoHome := filepath.Join(rustBuildDir, "cargo-home")
	if err := os.MkdirAll(cargoHome, 0o755); err != nil {
		return "", fmt.Errorf("create Cargo home %s: %w", cargoHome, err)
	}
	sourceDir, err := fetchCraneliftSource(runtimeDir, version, cargoHome, verbose)
	if err != nil {
		return "", err
	}

	manifestPath := filepath.Join(sourceDir, "Cargo.toml")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read fetched Cranelift manifest %s: %w", manifestPath, err)
	}
	sourceVersion, err := cargoPackageVersion(string(manifestData))
	if err != nil {
		return "", fmt.Errorf("read fetched Cranelift package version: %w", err)
	}
	if sourceVersion != version {
		return "", fmt.Errorf("fetched Cranelift source version %s does not match Cargo.lock version %s", sourceVersion, version)
	}

	overlayDir := filepath.Join(rustBuildDir, craneliftOverlayName)
	stagingDir := overlayDir + ".staging"
	if err := os.RemoveAll(stagingDir); err != nil {
		return "", fmt.Errorf("remove stale Cranelift staging directory %s: %w", stagingDir, err)
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("create Cranelift staging directory %s: %w", stagingDir, err)
	}
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove Cranelift staging directory %s: %w", stagingDir, err))
		}
	}()

	if err := copyCraneliftSource(sourceDir, stagingDir); err != nil {
		return "", fmt.Errorf("copy Cranelift source into overlay: %w", err)
	}
	if err := applyCraneliftIsaPatches(stagingDir); err != nil {
		return "", fmt.Errorf("patch Cranelift source overlay: %w", err)
	}
	if err := os.RemoveAll(overlayDir); err != nil {
		return "", fmt.Errorf("remove previous Cranelift overlay %s: %w", overlayDir, err)
	}
	if err := os.Rename(stagingDir, overlayDir); err != nil {
		return "", fmt.Errorf("publish Cranelift overlay %s: %w", overlayDir, err)
	}

	workspaceDir, err = prepareCraneliftRuntimeWorkspace(runtimeDir, rustBuildDir, overlayDir)
	if err != nil {
		return "", err
	}
	if verbose {
		fmt.Printf("🍆  Prepared Cranelift %s source overlay at %s\n", version, overlayDir)
	}
	return workspaceDir, nil
}

func fetchCraneliftSource(runtimeDir, version, cargoHome string, verbose bool) (string, error) {
	manifestPath := filepath.Join(runtimeDir, "Cargo.toml")
	cmd := exec.Command("cargo", "+nightly", "fetch", "--locked", "--manifest-path", manifestPath)
	cmd.Dir = runtimeDir
	cmd.Env = append(os.Environ(), "CARGO_HOME="+cargoHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cargo fetch Cranelift %s: %w\n%s", version, err, strings.TrimSpace(string(out)))
	}
	if verbose && len(out) > 0 {
		fmt.Print(string(out))
	}

	pattern := filepath.Join(cargoHome, "registry", "src", "index.crates.io-*", "cranelift-codegen-"+version)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("find fetched Cranelift source using %s: %w", pattern, err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one fetched Cranelift source for version %s, found %d under %s", version, len(matches), pattern)
	}
	return matches[0], nil
}

func prepareCraneliftRuntimeWorkspace(runtimeDir, rustBuildDir, overlayDir string) (string, error) {
	manifestPath := filepath.Join(runtimeDir, "Cargo.toml")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read runtime manifest %s: %w", manifestPath, err)
	}
	manifest := string(manifestData)
	sourcePath := filepath.ToSlash(filepath.Join(runtimeDir, "src", "lib.rs"))
	manifest, err = replaceLiteralExactlyOnce(
		manifestPath,
		manifest,
		`path = "src/lib.rs"`,
		fmt.Sprintf("path = %q", sourcePath),
	)
	if err != nil {
		return "", fmt.Errorf("prepare runtime manifest overlay: %w", err)
	}
	if strings.Contains(manifest, "[patch.crates-io]") {
		return "", fmt.Errorf("runtime manifest still contains a tracked [patch.crates-io] override")
	}
	manifest += fmt.Sprintf(
		"\n[patch.crates-io]\ncranelift-codegen = { path = %q }\n",
		filepath.ToSlash(overlayDir),
	)

	workspaceDir := filepath.Join(rustBuildDir, craneliftWorkspaceName)
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return "", fmt.Errorf("create generated runtime workspace %s: %w", workspaceDir, err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "Cargo.toml"), []byte(manifest), 0o644); err != nil {
		return "", fmt.Errorf("write generated runtime manifest: %w", err)
	}
	lockData, err := os.ReadFile(filepath.Join(runtimeDir, "Cargo.lock"))
	if err != nil {
		return "", fmt.Errorf("read runtime lockfile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "Cargo.lock"), lockData, 0o644); err != nil {
		return "", fmt.Errorf("write generated runtime lockfile: %w", err)
	}
	return workspaceDir, nil
}

func lockedCraneliftVersion(lock string) (string, error) {
	var version string
	for _, block := range strings.Split(lock, "[[package]]") {
		name := ""
		blockVersion := ""
		for _, line := range strings.Split(block, "\n") {
			fields := strings.SplitN(strings.TrimSpace(line), "=", 2)
			if len(fields) != 2 {
				continue
			}
			value := strings.Trim(strings.TrimSpace(fields[1]), `"`)
			switch strings.TrimSpace(fields[0]) {
			case "name":
				name = value
			case "version":
				blockVersion = value
			}
		}
		if name != "cranelift-codegen" {
			continue
		}
		if version != "" {
			return "", fmt.Errorf("Cargo.lock contains multiple cranelift-codegen packages")
		}
		version = blockVersion
	}
	if version == "" {
		return "", fmt.Errorf("Cargo.lock has no cranelift-codegen package")
	}
	if !cargoPackageVersionPattern.MatchString(version) {
		return "", fmt.Errorf("Cargo.lock has invalid cranelift-codegen version %q", version)
	}
	return version, nil
}

func cargoPackageVersion(manifest string) (string, error) {
	inPackage := false
	for _, line := range strings.Split(manifest, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inPackage = trimmed == "[package]"
			continue
		}
		if !inPackage {
			continue
		}
		fields := strings.SplitN(trimmed, "=", 2)
		if len(fields) != 2 || strings.TrimSpace(fields[0]) != "version" {
			continue
		}
		version := strings.Trim(strings.TrimSpace(fields[1]), `"`)
		if !cargoPackageVersionPattern.MatchString(version) {
			return "", fmt.Errorf("invalid package version %q", version)
		}
		return version, nil
	}
	return "", fmt.Errorf("manifest has no package version")
}

func copyCraneliftSource(sourceDir, destinationDir string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("resolve source path %s: %w", path, err)
		}
		destination := filepath.Join(destinationDir, relative)
		if entry.IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", destination, err)
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in fetched Cranelift source: %s", path)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular Cranelift source entry: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		mode := info.Mode().Perm()
		if mode&0o200 == 0 {
			mode |= 0o200
		}
		if err := os.WriteFile(destination, data, mode); err != nil {
			return fmt.Errorf("write %s: %w", destination, err)
		}
		return nil
	})
}

func applyCraneliftIsaPatches(crateDir string) error {
	contextPath := filepath.Join(crateDir, "src", "context.rs")
	contextData, err := os.ReadFile(contextPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", contextPath, err)
	}
	contextSource := string(contextData)
	for _, patch := range contextIsaPatches {
		contextSource, err = replaceRegexExactlyOnce(contextPath, contextSource, patch)
		if err != nil {
			return err
		}
	}

	abiPath := filepath.Join(crateDir, "src", "machinst", "abi.rs")
	abiData, err := os.ReadFile(abiPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", abiPath, err)
	}
	abiSource, err := replaceRegexExactlyOnce(abiPath, string(abiData), calleeIsaPatch)
	if err != nil {
		return err
	}

	settingsPath := filepath.Join(crateDir, "src", "settings.rs")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}
	settingsSource, err := replaceRegexExactlyOnce(settingsPath, string(settingsData), flagsOrIsaPatch)
	if err != nil {
		return err
	}
	contextSource, err = replaceRegexExactlyOnce(contextPath, contextSource, compileHookPatch)
	if err != nil {
		return err
	}

	x64Path := filepath.Join(crateDir, "src", "isa", "x64", "mod.rs")
	x64Data, err := os.ReadFile(x64Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", x64Path, err)
	}
	x64Source, err := replaceRegexExactlyOnce(x64Path, string(x64Data), x64BackendVisibilityPatch)
	if err != nil {
		return err
	}
	x64Source, err = replaceRegexExactlyOnce(x64Path, x64Source, x64ConstructorVisibilityPatch)
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(crateDir, "Cargo.toml")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", manifestPath, err)
	}
	manifestSource, err := replaceRegexExactlyOnce(manifestPath, string(manifestData), hashbrownManifestPatch)
	if err != nil {
		return err
	}

	libPath := filepath.Join(crateDir, "src", "lib.rs")
	libData, err := os.ReadFile(libPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", libPath, err)
	}
	libSource, err := replaceRegexExactlyOnce(libPath, string(libData), noStdHashAliasesPatch)
	if err != nil {
		return err
	}

	if err := os.WriteFile(contextPath, []byte(contextSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", contextPath, err)
	}
	if err := os.WriteFile(abiPath, []byte(abiSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", abiPath, err)
	}
	if err := os.WriteFile(settingsPath, []byte(settingsSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", settingsPath, err)
	}
	if err := os.WriteFile(x64Path, []byte(x64Source), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", x64Path, err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifestSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", manifestPath, err)
	}
	if err := os.WriteFile(libPath, []byte(libSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", libPath, err)
	}

	hashMapPatches := []struct {
		relativePath string
		patch        craneliftSourcePatch
		count        int
	}{
		{filepath.Join("src", "machinst", "vcode.rs"), hashMapNewPatch, 3},
		{filepath.Join("src", "machinst", "vcode.rs"), hashMapCapacityPatch, 1},
		{filepath.Join("src", "machinst", "abi.rs"), dynamicTypeHashMapCapacityPatch, 1},
		{filepath.Join("src", "ir", "memflags.rs"), hashMapNewPatch, 2},
		{filepath.Join("src", "verifier", "mod.rs"), hashMapNewPatch, 1},
	}
	for _, filePatch := range hashMapPatches {
		path := filepath.Join(crateDir, filePatch.relativePath)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		patched, err := replaceRegexExpected(path, string(data), filePatch.patch, filePatch.count)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}

	return nil
}

func replaceRegexExactlyOnce(file, source string, patch craneliftSourcePatch) (string, error) {
	return replaceRegexExpected(file, source, patch, 1)
}

func replaceRegexExpected(file, source string, patch craneliftSourcePatch, expected int) (string, error) {
	re, err := regexp.Compile(patch.pattern)
	if err != nil {
		return "", fmt.Errorf("compile patch pattern %q: %w", patch.name, err)
	}
	matches := re.FindAllStringIndex(source, expected+1)
	if len(matches) != expected {
		return "", fmt.Errorf("%s: expected %d matches in %s, found %d", patch.name, expected, file, len(matches))
	}
	return re.ReplaceAllString(source, patch.replacement), nil
}

func replaceLiteralExactlyOnce(file, source, old, replacement string) (string, error) {
	count := strings.Count(source, old)
	if count != 1 {
		return "", fmt.Errorf("%s: expected exactly one occurrence in %s, found %d", old, file, count)
	}
	return strings.Replace(source, old, replacement, 1), nil
}
