//go:build ignore

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func main() {
	// 1. Resolve Compiler Binaries via PATH
	var gccName, gxxName string
	switch runtime.GOOS {
	case "windows":
		gccName = "gcc.exe"
		gxxName = "g++.exe"
	default:
		gccName = "gcc"
		gxxName = "g++"
	}

	gccPath, err := exec.LookPath(gccName)
	if err != nil {
		log.Fatalf("Required C compiler (%s) not found in system PATH!", gccName)
	}
	gxxPath, err := exec.LookPath(gxxName)
	if err != nil {
		log.Fatalf("Required C++ compiler (%s) not found in system PATH!", gxxName)
	}

	// 2. Establish persistent dependency directory strictly inside kabibi-go
	currentDir, _ := os.Getwd()
	baseDir := currentDir
	// Walk up to find the true workspace root (has Cargo.toml or kabibi-go)
	for {
		if _, err := os.Stat(filepath.Join(baseDir, "kabibi-go")); err == nil {
			break
		}
		parent := filepath.Dir(baseDir)
		if parent == baseDir {
			baseDir = currentDir // Fallback
			break
		}
		baseDir = parent
	}

	kabibiDir := filepath.Join(baseDir, "kabibi-go")
	depsDir := filepath.Join(kabibiDir, ".deps")
	repoDir := filepath.Join(depsDir, "LiteRT-LM")

	_ = os.MkdirAll(depsDir, 0755)

	// 2.5. Fix Environment for Windows (Ensure Git Bash tools are available for Bazel patches)
	if runtime.GOOS == "windows" {
		gitBin := `C:\Program Files\Git\bin`
		gitUsrBin := `C:\Program Files\Git\usr\bin`
		path := os.Getenv("PATH")
		if !strings.Contains(strings.ToLower(path), strings.ToLower(gitBin)) {
			path = gitBin + string(os.PathListSeparator) + gitUsrBin + string(os.PathListSeparator) + path
			os.Setenv("PATH", path)
		}
		os.Setenv("BAZEL_SH", filepath.Join(gitBin, "bash.exe"))
	}

	// 3. Conditional Shallow Clone
	if _, err := os.Stat(filepath.Join(repoDir, "WORKSPACE")); os.IsNotExist(err) {
		fmt.Println("Cloning LiteRT-LM repository (depth=10) into tracking workspace...")
		_ = os.RemoveAll(repoDir) // Clean up if it was empty/partial
		cloneCmd := exec.Command("git", "clone", "--depth=10", "https://github.com/google-ai-edge/LiteRT-LM.git", repoDir)
		cloneCmd.Stdout = os.Stdout
		cloneCmd.Stderr = os.Stderr
		if err := cloneCmd.Run(); err != nil {
			log.Fatalf("Failed cloning upstream source code tree: %v", err)
		}
	}

	// 3.5. Automated Environment Patching for Windows ARM64
	if runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
		fmt.Println("Applying ARM64 Windows compatibility patches to LiteRT-LM...")

		workspacePath := filepath.Join(repoDir, "WORKSPACE")
		workspaceContent, err := os.ReadFile(workspacePath)
		if err == nil {
			content := string(workspaceContent)

			// Add ARM64 Windows to extra_target_triples in rust_register_toolchains
			if !strings.Contains(content, "aarch64-pc-windows-msvc") {
				// Find rust_register_toolchains and add it there
				content = strings.Replace(content,
					"\"aarch64-linux-android\",",
					"\"aarch64-pc-windows-msvc\",\n        \"aarch64-linux-android\",", 1)
			}

			// Fix cxxbridge_cmd_deps to support ARM64 Windows
			if strings.Contains(content, "cxxbridge_cmd_deps") && !strings.Contains(content, "name = \"cxxbridge_cmd_deps\",\n    supported_platform_triples") {
				content = strings.Replace(content,
					"name = \"cxxbridge_cmd_deps\",",
					"name = \"cxxbridge_cmd_deps\",\n    supported_platform_triples = [\"x86_64-pc-windows-msvc\", \"aarch64-pc-windows-msvc\"],", 1)
				fmt.Println("Applied cxxbridge_cmd_deps patch")
			}

			_ = os.WriteFile(workspacePath, []byte(content), 0644)
		}

		bazelrcPath := filepath.Join(repoDir, ".bazelrc")
		bazelrcContent, err := os.ReadFile(bazelrcPath)
		if err == nil {
			content := string(bazelrcContent)
			// 1. Remove AVX2 requirement for ARM64 Windows
			if strings.Contains(content, "build:windows --copt=/arch:AVX2") {
				content = strings.Replace(content, "build:windows --copt=/arch:AVX2", "# build:windows --copt=/arch:AVX2 (disabled for ARM64)", -1)
			}
			// 2. Disable problematic warnings import
			if strings.Contains(content, "try-import %workspace%/warnings.bazelrc") {
				content = strings.Replace(content, "try-import %workspace%/warnings.bazelrc", "# try-import %workspace%/warnings.bazelrc (disabled for ARM64)", -1)
			}
			// 3. Add ARM64 specific defines for common library issues
			if !strings.Contains(content, "FARMHASH_NO_BUILTIN_EXPECT") {
				content += "\n# ARM64 Windows Fixes\n"
				content += "build:windows --copt=/Drestrict=__restrict\n"
				content += "build:windows --copt=/DFARMHASH_NO_BUILTIN_EXPECT\n"
				content += "build:windows --copt=/D__builtin_expect(x,y)=x\n"
				content += "build:windows --copt=/W0\n"
			}

			fmt.Println("Applied .bazelrc ARM64 compatibility patches")
			_ = os.WriteFile(bazelrcPath, []byte(content), 0644)
		}

		patchPath := filepath.Join(repoDir, "PATCH.rules_rust")
		patchContent, err := os.ReadFile(patchPath)
		if err == nil {
			content := string(patchContent)
			if !strings.Contains(content, "aarch64-pc-windows-msvc") {
				// Add the line to the patch
				content = strings.Replace(content,
					"\"aarch64-linux-android\",",
					"\"aarch64-linux-android\",\n+    \"aarch64-pc-windows-msvc\",", 1)
				// Update the patch header (+28,9 -> +28,10)
				content = strings.Replace(content, "@@ -28,6 +28,9 @@", "@@ -28,6 +28,10 @@", 1)
				_ = os.WriteFile(patchPath, []byte(content), 0644)
				fmt.Println("Applied PATCH.rules_rust patch")
			}
		}
	}

	// 4. STOP-GAP CARGO CEILING IN .DEPS (Only create if missing)
	stopGapCargo := filepath.Join(depsDir, "Cargo.toml")
	if _, err := os.Stat(stopGapCargo); os.IsNotExist(err) {
		fmt.Println("Deploying workspace ceiling stop-gap to kabibi-go/.deps/Cargo.toml...")
		boundaryConfig := "[workspace]\nmembers = []\n"
		if err := os.WriteFile(stopGapCargo, []byte(boundaryConfig), 0644); err != nil {
			log.Fatalf("Failed to write isolation workspace stop-gap file: %v", err)
		}
	}

	// 5. Anchor the Bazel user output root inside a short path to avoid MAX_PATH issues
	absCacheDir := `C:\kb`
	_ = os.MkdirAll(absCacheDir, 0755)

	// 6. Clean Environment setup
	var cleanEnv []string
	for _, env := range os.Environ() {
		upperEnv := strings.ToUpper(env)
		if !strings.HasPrefix(upperEnv, "CARGO_") && !strings.HasPrefix(upperEnv, "RUST") {
			cleanEnv = append(cleanEnv, env)
		}
	}
	cleanEnv = append(cleanEnv, "RUSTUP_TOOLCHAIN=nightly-aarch64-pc-windows-gnullvm")

	// 7. Execute Build targeting the C API artifact using CMake (GNU toolchain)

	// 8. Execute Build targeting the C API artifact using CMake (GNU toolchain)
	fmt.Println("Evaluating toolchain runtime dependencies (C API) via CMake...")

	buildDir := filepath.Join(repoDir, "b")
	_ = os.RemoveAll(buildDir)
	_ = os.MkdirAll(buildDir, 0755)

	// Configure with toolchain args for sub-project
	// Use semicolons to pass a list to CMake
	toolchainArgs := "-DCMAKE_C_COMPILER=" + gccPath +
		";-DCMAKE_CXX_COMPILER=" + gxxPath +
		";-DCMAKE_CXX_STANDARD=20" +
		";-DLITERTLM_RUST_TARGET=aarch64-pc-windows-gnullvm" +
		";-DRust_TOOLCHAIN=nightly-aarch64-pc-windows-gnullvm" +
		";-DRust_CARGO_TARGET=aarch64-pc-windows-gnullvm"

	cmakeArgs := []string{
		"-S", repoDir,
		"-B", buildDir,
		"-G", "MinGW Makefiles",
		"-DCMAKE_C_COMPILER=" + gccPath,
		"-DCMAKE_CXX_COMPILER=" + gxxPath,
		"-DLITERT_LM_BUILD_C_API=ON",
		"-DCMAKE_BUILD_TYPE=Release",
		"-DLITERTLM_TOOLCHAIN_ARGS=" + toolchainArgs,
	}

	configCmd := exec.Command("cmake", cmakeArgs...)
	configCmd.Stdout = os.Stdout
	configCmd.Stderr = os.Stderr
	configCmd.Env = cleanEnv

	fmt.Println("Running CMake configuration...")
	if err := configCmd.Run(); err != nil {
		log.Fatalf("CMake configuration failed: %v", err)
	}

	// Build
	buildCmd := exec.Command("cmake", "--build", buildDir, "--config", "Release")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	buildCmd.Env = cleanEnv

	fmt.Println("Running CMake build...")
	if err := buildCmd.Run(); err != nil {
		log.Fatalf("CMake build failed: %v", err)
	}

	// 9. Compute external direct paths for CGO integration
	cgoIncludeDir := kabibiDir
	cgoLibDir := filepath.Join(kabibiDir, "lib")
	_ = os.MkdirAll(cgoLibDir, 0755)

	// Copy the library - find the built artifacts
	// On MinGW/GNU it could be .a, .dll, or .dll.a
	var artifacts []string
	if runtime.GOOS == "windows" {
		artifacts = []string{"liblitertlm_c.a", "litertlm_c.dll", "liblitertlm_c.dll.a"}
	} else {
		artifacts = []string{"liblitertlm_c.a", "liblitertlm_c.so"}
	}

	found := false
	for _, art := range artifacts {
		src := filepath.Join(buildDir, "c", art)
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(cgoLibDir, art)
			fmt.Printf("Deploying artifact: %s -> %s\n", src, dst)
			input, err := os.ReadFile(src)
			if err != nil {
				log.Fatalf("Failed to read artifact %s: %v", art, err)
			}
			if err := os.WriteFile(dst, input, 0644); err != nil {
				log.Fatalf("Failed to deploy artifact %s: %v", art, err)
			}
			found = true
		}
	}

	if !found {
		log.Fatalf("No build artifacts found in %s", filepath.Join(buildDir, "c"))
	}

	// 10. Inject target parameters into the CGO context
	os.Setenv("CGO_ENABLED", "1")
	os.Setenv("CC", gccPath)
	os.Setenv("CXX", gxxPath)

	// We add both the kabibiDir (for litertlm_c_api.h) and repoDir (for internal includes)
	os.Setenv("CGO_CFLAGS", "-I"+cgoIncludeDir+" -I"+repoDir)
	os.Setenv("CGO_LDFLAGS", "-L"+cgoLibDir+" -llitertlm_c -lstdc++ -lm")

	// 10.5 Ensure dependencies are available
	fmt.Println("Fetching Go dependencies...")
	getCmd := exec.Command("go", "get", "github.com/vladimirvivien/litertlm-go")
	getCmd.Dir = kabibiDir
	getCmd.Run()

	// 11. Execute package binary wrapper context
	cmd := exec.Command("go", "run", ".")
	// If we're already in kabibi-go, don't try to chdir into it
	if _, err := os.Stat("main.go"); err == nil {
		cmd.Dir = "."
	} else {
		cmd.Dir = "kabibi-go"
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		log.Fatalf("Build runtime execution error: %v", err)
	}
}

func patchCache(cacheDir string) {
	// Find the external directory - search non-recursively first for efficiency
	externalDir := ""
	entries, _ := os.ReadDir(cacheDir)
	for _, entry := range entries {
		if entry.IsDir() {
			extPath := filepath.Join(cacheDir, entry.Name(), "external")
			if _, err := os.Stat(filepath.Join(extPath, "org_tensorflow")); err == nil {
				externalDir = extPath
				fmt.Printf("Found external directory: %s\n", externalDir)
				break
			}
		}
	}

	if externalDir == "" {
		fmt.Println("External directory not found in cache. This is normal if it's a fresh build.")
		return
	}

	fmt.Println("Applying targeted MSVC compatibility patches to cache...")

	// Specific files we know need patching to avoid massive scans
	criticalFiles := []string{
		filepath.Join(externalDir, "XNNPACK", "build_defs.bzl"),
	}

	for _, path := range criticalFiles {
		if content, err := os.ReadFile(path); err == nil {
			s := string(content)
			changed := false

			// Remove GCC-specific flags that MSVC doesn't understand
			toRemove := []string{
				"-Wimplicit-fallthrough",
				"-std=c99",
				"\"-std=c99\",",
				"\"-Wimplicit-fallthrough\",",
				", \"-Wimplicit-fallthrough\"",
				", \"-std=c99\"",
			}

			for _, r := range toRemove {
				if strings.Contains(s, r) {
					s = strings.ReplaceAll(s, r, "")
					changed = true
				}
			}

			if strings.Contains(s, "/Wno-implicit-fallthrough") {
				s = strings.ReplaceAll(s, "/Wno-implicit-fallthrough", "")
				changed = true
			}
			if changed {
				fmt.Printf("Patching %s...\n", path)
				_ = os.WriteFile(path, []byte(s), 0644)
			}
		}
	}

	// For the rest, only scan source files in specific directories
	targets := []struct {
		name string
		subs []string
	}{
		{"cpuinfo", []string{"src", "include"}},
		// {"XNNPACK", []string{"src", "include"}}, // Too many files, skip for now unless needed
	}

	for _, target := range targets {
		targetPath := filepath.Join(externalDir, target.name)
		for _, sub := range target.subs {
			subPath := filepath.Join(targetPath, sub)
			if _, err := os.Stat(subPath); err != nil {
				continue
			}

			fmt.Printf("Scanning %s/%s for syntax fixes...\n", target.name, sub)
			filepath.Walk(subPath, func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					ext := filepath.Ext(path)
					if ext == ".c" || ext == ".h" {
						content, err := os.ReadFile(path)
						if err == nil {
							s := string(content)
							changed := false

							if strings.Contains(s, "[restrict static") {
								re := regexp.MustCompile(`\[restrict\s+static\s+[^\]]+\]`)
								if re.MatchString(s) {
									s = re.ReplaceAllString(s, "[]")
									changed = true
								}
							}

							if changed {
								_ = os.WriteFile(path, []byte(s), 0644)
							}
						}
					}
				}
				return nil
			})
		}
	}
}
