package mohabbat

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func init() {
	prebuildFn = runPrebuild
}

// runPrebuild is the Go port of prebuild/src/main.rs.
// It generates rusticated target specs, builds the sysroot for each target,
// writes config.toml, and generates target/overlay.json for Go projects.
func runPrebuild(ws string) error {
	inVeg := os.Getenv("MOHABBAT_VEGETABLE_PATH") != ""
	if inVeg {
		// When running inside a vegetable (WASM guest), we assume prebuild
		// artifacts already exist in the 'target' directory, as we cannot
		// run rustc/cargo/go compilers in the restricted WASM environment.
		fmt.Println("🍆  Vegetable context: skipping prebuild steps")
		return nil
	}

	if err := buildTargetSpecs(ws); err != nil {
		return fmt.Errorf("target spec generation: %w", err)
	}
	goroot, rootSource, err := resolveGoroot(ws)
	if err != nil {
		return fmt.Errorf("resolving GOROOT: %w", err)
	}
	if err := generateGoOverlay(ws, goroot); err != nil {
		return fmt.Errorf("overlay generation: %w", err)
	}
	fmt.Println("🍆  SDK " + rootSource + " at " + goroot)
	fmt.Println("🍆  Prebuild complete.")
	return nil
}

// buildTargetSpecs generates rusticated JSON target specs, builds the sysroot
// for each target, and writes target/rusticated-spec/config.toml.
func buildTargetSpecs(ws string) error {
	// Get host triple from rustc.
	out, err := exec.Command("rustc", "-vV").Output()
	if err != nil {
		return fmt.Errorf("rustc -vV failed: %w", err)
	}
	host := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "host: ") {
			host = strings.TrimSpace(strings.TrimPrefix(line, "host: "))
		}
	}
	if host == "" {
		return fmt.Errorf("rustc -vV did not report host triple")
	}

	baseTargets := [][2]string{
		{"x86_64-pc-windows-gnullvm", "x86_64-rusticated-windows-gnullvm"},
		{"x86_64-unknown-linux-gnu", "x86_64-rusticated-linux"},
		{"aarch64-pc-windows-gnullvm", "aarch64-rusticated-windows-gnullvm"},
		{"aarch64-unknown-linux-gnu", "aarch64-rusticated-linux"},
		{"wasm32-unknown-unknown", "wasm32-rusticated-unknown-unknown"},
	}

	specDir := filepath.Join(ws, "target", "rusticated-spec")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		return err
	}
	// Clear the config to avoid tainting the build-std below.
	if err := os.WriteFile(filepath.Join(specDir, "config.toml"), nil, 0o644); err != nil {
		return err
	}

	// Compute rust_target_path (forward-slash, no \\?\ prefix).
	absSpecDir, err := filepath.EvalSymlinks(specDir)
	if err != nil {
		absSpecDir = specDir
	}
	absSpecDir = cleanWindowsPath(absSpecDir)
	rustTargetPath := filepath.ToSlash(absSpecDir)

	var configTOML strings.Builder
	var builtTargets []string

	for _, bt := range baseTargets {
		baseName := bt[0]
		customName := bt[1]

		fmt.Printf("🍆  Processing target: %s -> %s\n", baseName, customName)

		// Get target spec JSON from rustc.
		specOut, err := exec.Command("rustc", "-Z", "unstable-options",
			"--print", "target-spec-json", "--target", baseName).Output()
		if err != nil {
			fmt.Printf("🍆    Skipping %s (rustc error)\n", baseName)
			continue
		}
		var spec map[string]interface{}
		if err := json.Unmarshal(specOut, &spec); err != nil {
			fmt.Printf("🍆    Skipping %s (JSON parse error: %v)\n", baseName, err)
			continue
		}

		isWindowsMSVC := strings.Contains(baseName, "-windows-msvc")
		isWindowsGNU := strings.Contains(baseName, "-windows-gnu") ||
			strings.Contains(baseName, "-windows-gnullvm")

		spec["panic-strategy"] = "abort"

		if strings.Contains(baseName, "-linux-") {
			spec["os"] = "linux"
			spec["position-independent-executables"] = true
			spec["relocation-model"] = "pic"
			extendPreLinkArgs(spec, "gnu-lld", []string{"-pie"})
			extendPreLinkArgs(spec, "gnu-lld-cc", []string{"-pie"})
		}

		// Set target-family based on base_target.
		var families []interface{}
		switch {
		case strings.Contains(baseName, "-linux-"):
			families = []interface{}{"unix", "rusticated"}
		case strings.Contains(baseName, "-darwin") || strings.Contains(baseName, "-freebsd"):
			families = []interface{}{"unix", "rusticated"}
		case strings.Contains(baseName, "-windows-"):
			families = []interface{}{"windows", "rusticated"}
		case strings.HasPrefix(baseName, "wasm32-"):
			families = []interface{}{"wasm", "rusticated"}
		default:
			families = []interface{}{"rusticated"}
		}
		spec["target-family"] = families

		if isWindowsMSVC {
			extendPreLinkArgs(spec, "msvc", []string{
				"/NOLOGO", "/NXCOMPAT", "/DYNAMICBASE",
				"/ENTRY:mainCRTStartup", "/SUBSYSTEM:CONSOLE",
				"/FORCE:MULTIPLE", "/NODEFAULTLIB",
			})
			extendPreLinkArgs(spec, "lld-link", []string{
				"/NOLOGO", "/NXCOMPAT", "/DYNAMICBASE",
				"/ENTRY:mainCRTStartup", "/SUBSYSTEM:CONSOLE",
				"/FORCE:MULTIPLE", "/NODEFAULTLIB",
			})
		}

		if isWindowsGNU {
			spec["late-link-args"] = map[string]interface{}{}
			archArg := "i386pep"
			if strings.HasPrefix(baseName, "aarch64") {
				archArg = "arm64pe"
			}
			extendPreLinkArgs(spec, "gnu", []string{"-m", archArg, "--entry=mainCRTStartup", "--subsystem=console"})
			extendPreLinkArgs(spec, "gnu-cc", []string{"-nolibc", "--unwindlib=none", "-m", archArg, "-Wl,--entry=mainCRTStartup", "-Wl,--subsystem=console"})
			extendPreLinkArgs(spec, "gnu-lld", []string{"-m", archArg, "--entry=mainCRTStartup", "--subsystem=console"})
			extendPreLinkArgs(spec, "gnu-lld-cc", []string{"-nolibc", "--unwindlib=none", "-m", archArg, "-Wl,--entry=mainCRTStartup", "-Wl,--subsystem=console"})
		}

		if strings.Contains(baseName, "-linux-gnu") {
			spec["late-link-args"] = map[string]interface{}{
				"gnu":        []interface{}{"-nostdlib"},
				"gcc":        []interface{}{"-nostdlib"},
				"gnu-cc":     []interface{}{"-nostdlib"},
				"gnu-lld":    []interface{}{"-nostdlib"},
				"gnu-lld-cc": []interface{}{"-nostdlib"},
			}
			spec["no-default-libraries"] = true
			extendPreLinkArgs(spec, "gnu-lld", []string{"-nostdlib", "--no-dynamic-linker", "--build-id=none"})
			extendPreLinkArgs(spec, "gnu-lld-cc", []string{"-nostdlib", "-nodefaultlibs", "-nostartfiles", "-Wl,--build-id=none"})
			extendPreLinkArgs(spec, "gnu", []string{"-nostdlib"})
			extendPreLinkArgs(spec, "gnu-cc", []string{"-nostdlib", "-nodefaultlibs", "-nostartfiles"})
			extendPreLinkArgs(spec, "gcc", []string{"-nostdlib", "-nodefaultlibs", "-nostartfiles"})
			spec["linker-flavor"] = "gnu-lld"
		}

		spec["crt-static-respected"] = true
		spec["no-default-libraries"] = true
		if isWindowsMSVC || isWindowsGNU {
			spec["crt-static-default"] = true
		}
		if strings.Contains(baseName, "-windows-gnullvm") {
			spec["linker"] = "rust-lld"
			spec["linker-flavor"] = "gnu-lld"
		}
		if strings.Contains(baseName, "-linux-gnu") {
			spec["linker"] = "rust-lld"
			spec["linker-flavor"] = "gnu-lld"
			spec["env"] = ""
		}

		// Set metadata.std = false.
		if meta, ok := spec["metadata"]; ok {
			if metaMap, ok := meta.(map[string]interface{}); ok {
				metaMap["std"] = false
			}
		} else {
			spec["metadata"] = map[string]interface{}{"std": false}
		}

		// Write JSON spec.
		specJSON, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal spec for %s: %w", customName, err)
		}
		jsonPath := filepath.Join(specDir, customName+".json")
		if err := os.WriteFile(jsonPath, specJSON, 0o644); err != nil {
			return err
		}

		// Build sysroot for this target.
		existingRustflags := os.Getenv("RUSTFLAGS")
		rustflags := "-Zunstable-options --cfg backtrace_in_libstd"
		if existingRustflags != "" {
			rustflags = existingRustflags + " " + rustflags
		}
		if strings.Contains(baseName, "-linux-gnu") {
			rustflags += " -A explicit-builtin-cfgs-in-flags --cfg rusticated_linux"
		}

		targetArg := customName
		if !strings.Contains(customName, "wasm32") {
			absJSON, err := filepath.EvalSymlinks(jsonPath)
			if err != nil {
				absJSON = jsonPath
			}
			targetArg = filepath.ToSlash(cleanWindowsPath(absJSON))
		}

		buildCmd := exec.Command("cargo",
			"build", "-p", "rusticated", "--release",
			"-Z", "build-std=core,alloc,compiler_builtins",
			"-Z", "build-std-features=compiler-builtins-mem",
			"--config", "unstable.json-target-spec=true",
			"--target", targetArg)
		buildCmd.Env = upsertEnv(os.Environ(), "RUSTFLAGS", rustflags)
		buildCmd.Env = upsertEnv(buildCmd.Env, "RUST_TARGET_PATH", rustTargetPath)
		buildCmd.Dir = ws
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr

		fmt.Printf("🍆    Building sysroot for %s\n", customName)
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("sysroot build failed for %s: %w", customName, err)
		}

		// Collect rlib paths by searching the deps directory.
		paths := map[string]string{}
		depsDir := filepath.Join(ws, "target", customName, "release", "deps")
		entries, err := os.ReadDir(depsDir)
		if err != nil {
			return fmt.Errorf("read deps dir %s: %w", depsDir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rlib") {
				continue
			}
			filename := entry.Name()
			var crateName string
			if filename == "libstd.rlib" {
				crateName = "std"
			} else if strings.HasPrefix(filename, "lib") {
				stripped := filename[3:]
				if idx := strings.LastIndex(stripped, "-"); idx >= 0 {
					crateName = stripped[:idx]
				} else {
					crateName = strings.TrimSuffix(stripped, ".rlib")
				}
			} else {
				continue
			}
			absPath, _ := filepath.Abs(filepath.Join(depsDir, filename))
			paths[crateName] = filepath.ToSlash(cleanWindowsPath(absPath))
		}

		if _, ok := paths["std"]; !ok {
			return fmt.Errorf("missing built artifact for std in %s", customName)
		}

		// Build config.toml fragment for this target.
		absDepsDir, err := filepath.EvalSymlinks(depsDir)
		if err != nil {
			absDepsDir = depsDir
		}
		absDepsDir = filepath.ToSlash(cleanWindowsPath(absDepsDir))
		var entry strings.Builder
		fmt.Fprintf(&entry, "[target.%s]\nrustflags = [\n", customName)
		entry.WriteString("    \"-Zunstable-options\",\n")
		entry.WriteString("    \"--cfg\", \"backtrace_in_libstd\",\n")
		for _, crate := range []string{"std", "core", "alloc", "compiler_builtins"} {
			if p, ok := paths[crate]; ok {
				fmt.Fprintf(&entry, "    \"--extern\", \"%s=%s\",\n", crate, p)
			}
		}
		fmt.Fprintf(&entry, "    \"-L\", \"dependency=%s\",\n", absDepsDir)
		if strings.Contains(customName, "-linux") {
			entry.WriteString("    \"--cfg\", \"rusticated_linux\",\n")
		}
		entry.WriteString("]\n\n")
		configTOML.WriteString(entry.String())
		builtTargets = append(builtTargets, customName)
		fmt.Printf("🍆    sysroot built: %s\n", customName)
	}

	if len(builtTargets) == 0 {
		return fmt.Errorf("no rusticated targets were successfully built")
	}

	hostTarget := selectHostTarget(host, builtTargets)
	absJSON := filepath.ToSlash(cleanWindowsPath(filepath.Join(absSpecDir, hostTarget+".json")))

	var finalConfig strings.Builder
	fmt.Fprintf(&finalConfig, "[env]\nRUST_TARGET_PATH = %q\n\n", rustTargetPath)
	fmt.Fprintf(&finalConfig, "[build]\ntarget = %q\n\n", absJSON)
	finalConfig.WriteString("[unstable]\njson-target-spec = true\n\n")
	finalConfig.WriteString(configTOML.String())

	configPath := filepath.Join(specDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(finalConfig.String()), 0o644); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	fmt.Printf("🍆  Wrote %s\n", configPath)
	return nil
}

// selectHostTarget picks the best rusticated target triple for the current host.
func selectHostTarget(host string, builtTargets []string) string {
	contains := func(s string) bool {
		for _, v := range builtTargets {
			if v == s {
				return true
			}
		}
		return false
	}
	arch := strings.Split(host, "-")[0]
	switch {
	case strings.Contains(host, "-windows-msvc"):
		for _, c := range []string{
			arch + "-rusticated-windows-gnullvm",
			arch + "-rusticated-windows-gnu",
		} {
			if contains(c) {
				return c
			}
		}
	case strings.Contains(host, "-windows-gnullvm"), strings.Contains(host, "-windows-gnu"):
		t := strings.Replace(host, "-pc-", "-rusticated-", 1)
		if contains(t) {
			return t
		}
	case strings.Contains(host, "-linux-gnu"):
		t := arch + "-rusticated-linux"
		if contains(t) {
			return t
		}
	default:
		t := strings.Replace(host, "-pc-", "-rusticated-", 1)
		if contains(t) {
			return t
		}
	}
	return builtTargets[0]
}

// extendPreLinkArgs appends args to spec["pre-link-args"][flavor] (no duplicates).
func extendPreLinkArgs(spec map[string]interface{}, flavor string, args []string) {
	var preLinkMap map[string]interface{}
	if v, ok := spec["pre-link-args"]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			preLinkMap = m
		} else {
			preLinkMap = map[string]interface{}{}
		}
	} else {
		preLinkMap = map[string]interface{}{}
	}
	spec["pre-link-args"] = preLinkMap

	var arr []interface{}
	if v, ok := preLinkMap[flavor]; ok {
		if a, ok := v.([]interface{}); ok {
			arr = a
		}
	}
	for _, arg := range args {
		found := false
		for _, v := range arr {
			if v == arg {
				found = true
				break
			}
		}
		if !found {
			arr = append(arr, arg)
		}
	}
	preLinkMap[flavor] = arr
}

// cleanWindowsPath strips the \\?\ UNC prefix and converts backslashes to forward slashes.
func cleanWindowsPath(p string) string {
	p = strings.TrimPrefix(p, `\\?\`)
	return strings.ReplaceAll(p, `\`, `/`)
}

// generateGoOverlay writes target/overlay.json mapping wasip1 runtime/syscall sources
// to their rusticated counterparts in rusticated-go/.
func generateGoOverlay(ws, goroot string) error {
	overlayDir := filepath.Join(ws, "rusticated-go")
	genDir := filepath.Join(ws, "target", "overlay-gen")

	canon := func(p string) string {
		abs, err := filepath.EvalSymlinks(p)
		if err != nil {
			abs = p
		}
		return filepath.ToSlash(cleanWindowsPath(abs))
	}

	// Algorithmic patch for src/path/filepath/path.go
	if err := os.MkdirAll(filepath.Join(genDir, "path/filepath"), 0755); err != nil {
		return fmt.Errorf("failed to create gen dir: %w", err)
	}
	pathGoSrc := filepath.Join(goroot, "src/path/filepath/path.go")
	pathGoContent, err := os.ReadFile(pathGoSrc)
	if err != nil {
		return fmt.Errorf("failed to read src/path/filepath/path.go: %w", err)
	}
	pathGoStr := string(pathGoContent)
	// Add internal/filepathlite import if missing
	if !strings.Contains(pathGoStr, "\"internal/filepathlite\"") {
		pathGoStr = strings.Replace(pathGoStr, "import (", "import (\n\t\"internal/filepathlite\"", 1)
	}
	// Robustly find the const block containing Separator by name, not by exact body or whitespace.
	reSepConst := regexp.MustCompile(`(?m)^const \([^)]*\bSeparator\b[^)]*\)`)
	if !reSepConst.MatchString(pathGoStr) {
		return fmt.Errorf("could not find Separator const block in path.go")
	}
	modifiedPathGo := reSepConst.ReplaceAllStringFunc(pathGoStr, func(m string) string {
		m = strings.Replace(m, "const (", "var (", 1)
		m = strings.Replace(m, "os.PathSeparator", "filepathlite.Separator", 1)
		m = strings.Replace(m, "os.PathListSeparator", "filepathlite.ListSeparator", 1)
		return m
	})
	genPathGo := filepath.Join(genDir, "path/filepath/path.go")
	if err := os.WriteFile(genPathGo, []byte(modifiedPathGo), 0644); err != nil {
		return fmt.Errorf("failed to write patched path.go: %w", err)
	}

	// Algorithmic patch for src/net/http/fs.go
	if err := os.MkdirAll(filepath.Join(genDir, "net/http"), 0755); err != nil {
		return fmt.Errorf("failed to create gen dir: %w", err)
	}
	fsGoSrc := filepath.Join(goroot, "src/net/http/fs.go")
	fsGoContent, err := os.ReadFile(fsGoSrc)
	if err != nil {
		return fmt.Errorf("failed to read src/net/http/fs.go: %w", err)
	}
	fsGoStr := string(fsGoContent)
	targetFunc := "func mapOpenError(originalErr error, name string, sep rune, stat func(string) (fs.FileInfo, error)) error {"
	if !strings.Contains(fsGoStr, targetFunc) {
		targetFunc = strings.ReplaceAll(targetFunc, "\n", "\r\n")
	}
	if !strings.Contains(fsGoStr, targetFunc) {
		return fmt.Errorf("could not find mapOpenError signature in fs.go")
	}
	modifiedFsGo := strings.Replace(fsGoStr, targetFunc,
		strings.Replace(targetFunc, "sep rune,", "sep byte,", 1), 1)
	genFsGo := filepath.Join(genDir, "net/http/fs.go")
	if err := os.WriteFile(genFsGo, []byte(modifiedFsGo), 0644); err != nil {
		return fmt.Errorf("failed to write patched fs.go: %w", err)
	}

	// Algorithmic patch for src/internal/filepathlite/path_unix.go
	if err := os.MkdirAll(filepath.Join(genDir, "internal/filepathlite"), 0755); err != nil {
		return fmt.Errorf("failed to create gen dir: %w", err)
	}
	liteUnixSrc := filepath.Join(goroot, "src/internal/filepathlite/path_unix.go")
	liteUnixContent, err := os.ReadFile(liteUnixSrc)
	if err != nil {
		return fmt.Errorf("failed to read src/internal/filepathlite/path_unix.go: %w", err)
	}
	liteUnixStr := string(liteUnixContent)

	// 0. Remove unused imports
	liteUnixStr = regexp.MustCompile(`(?m)^import \([\s\S]*?^\)`).ReplaceAllString(liteUnixStr, "import (\n\t/* imports moved to path_nonwindows.go */\n)")

	// 1. Remove constant block (Separator and ListSeparator are now variables in path_nonwindows.go)
	liteUnixStr = regexp.MustCompile(`(?ms)^const \([^)]*\)`).ReplaceAllString(liteUnixStr, "/* constants moved to path_nonwindows.go */")

	// 2. Redirect functions to redirect into the Golden Source
	liteUnixStr = regexp.MustCompile(`(?m)^func IsPathSeparator\(c uint8\) bool \{[\s\S]*?^\}`).ReplaceAllString(liteUnixStr, "func IsPathSeparator(c uint8) bool {\n\treturn CoreIsPathSeparator(c)\n}")
	liteUnixStr = regexp.MustCompile(`(?m)^func IsAbs\(path string\) bool \{[\s\S]*?^\}`).ReplaceAllString(liteUnixStr, "func IsAbs(path string) bool {\n\treturn CoreIsAbs(path)\n}")
	// Join is not in the original SDK path_unix.go for Go 1.26.4, so we append it if replace fails or just append it.
	liteUnixStr += "\nfunc Join(elem []string) string {\n\treturn CoreJoin(elem)\n}\n"
	liteUnixStr = regexp.MustCompile(`(?m)^func volumeNameLen\(path string\) int \{[\s\S]*?^\}`).ReplaceAllString(liteUnixStr, "func volumeNameLen(path string) int {\n\treturn CoreVolumeNameLen(path)\n}")
	liteUnixStr = regexp.MustCompile(`(?m)^func isLocal\(path string\) bool \{[\s\S]*?^\}`).ReplaceAllString(liteUnixStr, "func isLocal(path string) bool {\n\treturn CoreIsLocal(path)\n}")
	liteUnixStr = regexp.MustCompile(`(?m)^func localize\(path string\) \(string, error\) \{[\s\S]*?^\}`).ReplaceAllString(liteUnixStr, "func localize(path string) (string, error) {\n\treturn CoreLocalize(path)\n}")

	genLiteGo := filepath.Join(genDir, "internal/filepathlite/path_unix.go")
	if err := os.WriteFile(genLiteGo, []byte(liteUnixStr), 0644); err != nil {
		return fmt.Errorf("failed to write patched filepathlite/path_unix.go: %w", err)
	}

	// Algorithmic patch for src/path/filepath/path_unix.go
	if err := os.MkdirAll(filepath.Join(genDir, "path/filepath"), 0755); err != nil {
		return fmt.Errorf("failed to create gen dir: %w", err)
	}
	pathUnixSrc := filepath.Join(goroot, "src/path/filepath/path_unix.go")
	pathUnixContent, err := os.ReadFile(pathUnixSrc)
	if err != nil {
		return fmt.Errorf("failed to read src/path/filepath/path_unix.go: %w", err)
	}
	// Patch join/Join to delegate to os.Join
	reJoin := regexp.MustCompile(`(?ms)^func [jJ]oin\(elem \[\]string\) string \{[\s\S]*?^\}`)
	pathUnixStr := string(pathUnixContent)
	// Add internal/filepathlite import if missing
	if !strings.Contains(pathUnixStr, "\"internal/filepathlite\"") {
		pathUnixStr = strings.Replace(pathUnixStr, "import (", "import (\n\t\"internal/filepathlite\"", 1)
	}
	// Patch path-logic functions to delegate to filepathlite
	pathUnixStr = reJoin.ReplaceAllString(pathUnixStr, "func join(elem []string) string {\n\treturn filepathlite.Join(elem)\n}")
	pathUnixStr = regexp.MustCompile(`(?m)^func IsAbs\(path string\) bool \{[\s\S]*?^\}`).ReplaceAllString(pathUnixStr, "func IsAbs(path string) bool {\n\treturn filepathlite.IsAbs(path)\n}")
	pathUnixStr = regexp.MustCompile(`(?m)^func VolumeNameLen\(path string\) int \{[\s\S]*?^\}`).ReplaceAllString(pathUnixStr, "func VolumeNameLen(path string) int {\n\treturn filepathlite.VolumeNameLen(path)\n}")
	pathUnixStr = regexp.MustCompile(`(?m)^func IsPathSeparator\(c uint8\) bool \{[\s\S]*?^\}`).ReplaceAllString(pathUnixStr, "func IsPathSeparator(c uint8) bool {\n\treturn filepathlite.IsPathSeparator(c)\n}")
	genPathUnix := filepath.Join(genDir, "path/filepath/path_unix.go")
	if err := os.WriteFile(genPathUnix, []byte(pathUnixStr), 0644); err != nil {
		return fmt.Errorf("failed to write patched filepath/path_unix.go: %w", err)
	}

	replacements := [][2]string{
		// runtime
		{"src/runtime/lock_wasip1.go", canon(filepath.Join(overlayDir, "runtime/lock_rusticated.go"))},
		{"src/runtime/os_wasip1.go", canon(filepath.Join(overlayDir, "runtime/os_rusticated.go"))},
		{"src/runtime/netpoll_wasip1.go", canon(filepath.Join(overlayDir, "runtime/netpoll_rusticated.go"))},
		{"src/runtime/stubs_wasm.go", canon(filepath.Join(overlayDir, "runtime/stubs_rusticated.go"))},
		{"src/runtime/rt0_wasip1_wasm.s", canon(filepath.Join(overlayDir, "runtime/rt0_wasip1_wasm.s"))},
		// syscall
		{"src/syscall/fs_wasip1.go", canon(filepath.Join(overlayDir, "syscall/fs_rusticated.go"))},
		{"src/syscall/syscall_wasip1.go", canon(filepath.Join(overlayDir, "syscall/syscall_rusticated.go"))},
		{"src/syscall/net_wasip1.go", canon(filepath.Join(overlayDir, "syscall/net_rusticated.go"))},
		{"src/syscall/os_wasip1.go", canon(filepath.Join(overlayDir, "syscall/os_rusticated.go"))},
		// internal/syscall/unix
		{"src/internal/syscall/unix/at_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/at_rusticated.go"))},
		{"src/internal/syscall/unix/utimes_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/utimes_rusticated.go"))},
		{"src/internal/syscall/unix/nonblocking_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/nonblocking_rusticated.go"))},
		{"src/internal/syscall/unix/fcntl_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/fcntl_rusticated.go"))},
		{"src/internal/syscall/unix/net_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/net_rusticated.go"))},
		// path
		{"src/os/path_unix.go", canon(filepath.Join(overlayDir, "os/path_unix.go"))},
		// pipe
		{"src/os/pipe_wasm.go", canon(filepath.Join(overlayDir, "os/pipe_rusticated.go"))},
		// exec
		{"src/os/exec/lp_wasm.go", canon(filepath.Join(overlayDir, "os/exec_rusticated.go"))},
		// filepath (generated patch)
		{"src/path/filepath/path.go", canon(genPathGo)},
		{"src/path/filepath/path_unix.go", canon(genPathUnix)},
		// net/http (generated patch)
		{"src/net/http/fs.go", canon(genFsGo)},
		// internal/filepathlite (generated patches)
		{"src/internal/filepathlite/path_nonwindows.go", canon(filepath.Join(overlayDir, "internal/filepathlite/path_nonwindows.go"))},
		{"src/internal/filepathlite/path_unix.go", canon(genLiteGo)},
	}

	var entries strings.Builder
	first := true
	for _, r := range replacements {
		srcPath := filepath.Join(goroot, r[0])
		if _, err := os.Stat(srcPath); err != nil {
			fmt.Fprintf(os.Stderr, "🍆  overlay: source not found: %s\n", srcPath)
			continue
		}
		srcStr := canon(srcPath)
		if !first {
			entries.WriteString(",\n")
		}
		first = false
		fmt.Fprintf(&entries, "    %q: %q", srcStr, r[1])
	}

	overlayJSON := fmt.Sprintf("{\n  \"Replace\": {\n%s\n  }\n}\n", entries.String())
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	if err := os.WriteFile(overlayPath, []byte(overlayJSON), 0o644); err != nil {
		return err
	}
	fmt.Printf("🍆  Wrote %s\n", overlayPath)
	return nil
}

