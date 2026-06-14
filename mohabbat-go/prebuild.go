//go:build !wasip1

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() {
	prebuildFn = runPrebuild
}

// runPrebuild is the Go port of prebuild/src/main.rs.
// It generates rusticated target specs, builds the sysroot for each target,
// writes config.toml, and generates target/overlay.json for Go projects.
func runPrebuild(ws string) error {
	if err := buildTargetSpecs(ws); err != nil {
		return fmt.Errorf("target spec generation: %w", err)
	}
	goroot, err := resolveGoroot(ws)
	if err != nil {
		return fmt.Errorf("resolving GOROOT: %w", err)
	}
	if err := generateGoOverlay(ws, goroot); err != nil {
		return fmt.Errorf("overlay generation: %w", err)
	}
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

		if strings.Contains(baseName, "-linux-gnu") {
			spec["os"] = "linux"
			spec["position-independent-executables"] = false
			spec["relocation-model"] = "static"
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
			"--target", targetArg,
			"--message-format=json")
		buildCmd.Env = upsertEnv(os.Environ(), "RUSTFLAGS", rustflags)
		buildCmd.Env = upsertEnv(buildCmd.Env, "RUST_TARGET_PATH", rustTargetPath)
		buildCmd.Dir = ws
		buildCmd.Stderr = os.Stderr
		var buildOut bytes.Buffer
		buildCmd.Stdout = &buildOut

		fmt.Printf("🍆    Building sysroot for %s\n", customName)
		if err := buildCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "🍆    sysroot build failed for %s\n", customName)
			for _, line := range strings.Split(buildOut.String(), "\n") {
				var v map[string]interface{}
				if json.Unmarshal([]byte(line), &v) == nil {
					if v["reason"] == "compiler-message" {
						if msg, ok := v["message"].(map[string]interface{}); ok {
							if rendered, ok := msg["rendered"].(string); ok {
								fmt.Fprint(os.Stderr, rendered)
							}
						}
					}
				}
			}
			return fmt.Errorf("sysroot build failed for %s", customName)
		}

		// Parse JSON output to collect rlib paths.
		paths := map[string]string{}
		depsDir := filepath.Join(ws, "target", customName, "release", "deps")
		for _, line := range strings.Split(buildOut.String(), "\n") {
			var v map[string]interface{}
			if json.Unmarshal([]byte(line), &v) != nil {
				continue
			}
			if v["reason"] != "compiler-artifact" {
				continue
			}
			filenames, _ := v["filenames"].([]interface{})
			for _, f := range filenames {
				filename, _ := f.(string)
				if !strings.HasSuffix(filename, ".rlib") {
					continue
				}
				basefile := filepath.Base(filename)
				var crateName string
				if basefile == "libstd.rlib" {
					crateName = "std"
				} else if strings.HasPrefix(basefile, "lib") {
					stripped := basefile[3:]
					if idx := strings.LastIndex(stripped, "-"); idx >= 0 {
						crateName = stripped[:idx]
					} else {
						crateName = strings.TrimSuffix(stripped, ".rlib")
					}
				} else {
					continue
				}
				absPath, err := filepath.EvalSymlinks(filename)
				if err != nil {
					absPath = filename
				}
				paths[crateName] = filepath.ToSlash(cleanWindowsPath(absPath))
			}
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
// to their rusticated counterparts in overlay-go/.
func generateGoOverlay(ws, goroot string) error {
	overlayDir := filepath.Join(ws, "overlay-go")
	canon := func(p string) string {
		abs, err := filepath.EvalSymlinks(p)
		if err != nil {
			abs = p
		}
		return filepath.ToSlash(cleanWindowsPath(abs))
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
