package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"mohabbat/mohabbat"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func findMohabbatNextDir() (string, error) {
	// Try current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	candidates := []string{
		filepath.Join(cwd, "mohabbat-next"),
		cwd,
		filepath.Dir(cwd),
	}

	for _, cand := range candidates {
		palDir := filepath.Join(cand, "pal")
		brotDir := filepath.Join(cand, "brot")
		if palInfo, palErr := os.Stat(palDir); palErr == nil && palInfo.IsDir() {
			if brotInfo, brotErr := os.Stat(brotDir); brotErr == nil && brotInfo.IsDir() {
				return cand, nil
			}
		}
	}

	return "", fmt.Errorf("could not locate mohabbat-next directory from %s", cwd)
}

func checkTools() error {
	tools := []string{
		"zig",
		"ld.lld",
		"llvm-objcopy",
		"llvm-readobj",
		"llvm-nm",
		"llvm-objdump",
	}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required build tool not found in PATH: %s", tool)
		}
	}
	return nil
}

func main() {
	var (
		verbose    bool
		runnerMode bool
		outputPath string
	)

	flag.BoolVar(&verbose, "v", false, "Verbose build output")
	flag.BoolVar(&verbose, "verbose", false, "Verbose build output")
	flag.BoolVar(&runnerMode, "runner", false, "Build payload-free Windows WASM runner")
	flag.StringVar(&outputPath, "o", "", "Output executable path")
	flag.StringVar(&outputPath, "out", "", "Output executable path")
	flag.Parse()

	totalStart := time.Now()
	fmt.Println("================================================================================")
	fmt.Println("🍆  Mohabbat-next Vegetable Builder (Go)")
	fmt.Println("================================================================================")

	nextDir, err := findMohabbatNextDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	repoRoot := filepath.Dir(nextDir)
	var targetName string
	if runnerMode {
		if flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "Error: -runner does not take a target argument (usage: build -runner [-o <out.exe>])")
			os.Exit(2)
		}
		if outputPath == "" {
			outputPath = filepath.Join(repoRoot, "release", "dist", "mohabbat-runner.exe")
		}
	} else {
		if flag.NArg() != 1 || strings.TrimSpace(flag.Arg(0)) == "" {
			fmt.Fprintln(os.Stderr, "Error: an application target is required (usage: build [flags] <target>)")
			os.Exit(2)
		}
		targetName = strings.TrimSpace(flag.Arg(0))
		if outputPath == "" {
			outputPath = filepath.Join(repoRoot, "release", "dist", targetName+".exe")
		}
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: resolve output path: %v\n", err)
		os.Exit(1)
	}

	workDir := filepath.Join(nextDir, ".build")
	zigBuildDir := filepath.Join(workDir, "zig")
	componentsDir := filepath.Join(workDir, "components")
	templatesDir := filepath.Join(nextDir, "build", "templates")
	unixOutDir := filepath.Join(nextDir, "brot", "unix", "out")
	windowsHostDir := filepath.Join(nextDir, "brot", "windows-host")

	for _, dir := range []string{workDir, zigBuildDir, componentsDir, unixOutDir, filepath.Dir(outputPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: create directory %s: %v\n", dir, err)
			os.Exit(1)
		}
	}

	// Step 1: Preflight
	fmt.Println("🍆  [1/5] Checking build tools and prerequisites...")
	if err := checkTools(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var brainWasmPath string
	if !runnerMode {
		brainWasmPath = filepath.Join(componentsDir, targetName+".wasm")
		targetDir := filepath.Join(repoRoot, targetName)
		fmt.Printf("🍆  Compiling Go WASM brain %s -> %s...\n", targetName, brainWasmPath)
		if err := mohabbat.BuildGoBrainWasm(repoRoot, targetDir, brainWasmPath, verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error building %s.wasm: %v\n", targetName, err)
			os.Exit(1)
		}
	}

	// Step 2: Build Zig flat components and Brot micro-launchers in parallel
	fmt.Println("🍆  [2/5] Compiling Zig flat components and Unix Brot launchers in parallel...")

	darwinStubTbd := filepath.Join(nextDir, "brot", "unix", "darwin-stubs", "libSystem.tbd")
	palAbiPath := filepath.Join(nextDir, "pal", "abi", "guest.zig")
	zwasmRootPath, err := prepareZwasmOverlay(zigBuildDir, verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error preparing zwasm source overlay: %v\n", err)
		os.Exit(1)
	}
	zwasmOptsPath := filepath.Join(nextDir, "runtime", "src", "build_options.zig")
	runtimeDir := filepath.Join(nextDir, "runtime")

	palAbiDep := ZigDep{Name: "pal_abi", Path: palAbiPath}
	zwasmDep := ZigDep{
		Name: "zwasm",
		Path: zwasmRootPath,
		Deps: []ZigDep{{Name: "build_options", Path: zwasmOptsPath}},
	}

	flatDefs := []ZigFlatConfig{
		// Movable zwasm Runtime Slice (ISA-shared)
		{
			Name:         "runtime-x86_64",
			RootFile:     filepath.Join(runtimeDir, "src", "main.zig"),
			Deps:         []ZigDep{palAbiDep, zwasmDep},
			Arch:         "x86_64",
			Optimize:     "ReleaseFast",
			LinkerScript: filepath.Join(runtimeDir, "runtime.ld"),
			EntrySymbol:  "runtime_code_start",
			MaxSize:      8 * 1024 * 1024,
			IsRuntime:    true,
			OutFileName:  "runtime-x86_64",
		},
		{
			Name:         "runtime-aarch64",
			RootFile:     filepath.Join(runtimeDir, "src", "main.zig"),
			Deps:         []ZigDep{palAbiDep, zwasmDep},
			Arch:         "aarch64",
			Optimize:     "ReleaseFast",
			LinkerScript: filepath.Join(runtimeDir, "runtime.ld"),
			EntrySymbol:  "runtime_code_start",
			MaxSize:      8 * 1024 * 1024,
			IsRuntime:    true,
			OutFileName:  "runtime-aarch64",
		},
		// Windows PAL
		{
			Name:         "platform-windows-x86_64",
			RootFile:     filepath.Join(nextDir, "pal", "windows", "platform.zig"),
			Deps:         []ZigDep{palAbiDep},
			Arch:         "x86_64",
			Optimize:     "ReleaseSmall",
			LinkerScript: filepath.Join(nextDir, "pal", "windows", "platform.ld"),
			EntrySymbol:  "windows_platform_entry",
			MaxSize:      256 * 1024,
			OutFileName:  "platform-windows-x86_64",
		},
		{
			Name:         "platform-windows-aarch64",
			RootFile:     filepath.Join(nextDir, "pal", "windows", "platform.zig"),
			Deps:         []ZigDep{palAbiDep},
			Arch:         "aarch64",
			Optimize:     "ReleaseSmall",
			LinkerScript: filepath.Join(nextDir, "pal", "windows", "platform.ld"),
			EntrySymbol:  "windows_platform_entry",
			MaxSize:      256 * 1024,
			OutFileName:  "platform-windows-aarch64",
		},
		// Linux PAL
		{
			Name:         "platform-linux-x86_64",
			RootFile:     filepath.Join(nextDir, "pal", "unix", "platform_linux.zig"),
			Deps:         []ZigDep{palAbiDep},
			Arch:         "x86_64",
			Optimize:     "ReleaseSmall",
			LinkerScript: filepath.Join(nextDir, "pal", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			MaxSize:      512 * 1024,
			OutFileName:  "platform-linux-x86_64",
		},
		{
			Name:         "platform-linux-aarch64",
			RootFile:     filepath.Join(nextDir, "pal", "unix", "platform_linux.zig"),
			Deps:         []ZigDep{palAbiDep},
			Arch:         "aarch64",
			Optimize:     "ReleaseSmall",
			LinkerScript: filepath.Join(nextDir, "pal", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			MaxSize:      512 * 1024,
			OutFileName:  "platform-linux-aarch64",
		},
		// Darwin PAL
		{
			Name:         "platform-darwin-x86_64",
			RootFile:     filepath.Join(nextDir, "pal", "unix", "platform_darwin.zig"),
			Deps:         []ZigDep{palAbiDep},
			Arch:         "x86_64",
			Optimize:     "ReleaseFast",
			LinkerScript: filepath.Join(nextDir, "pal", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			MaxSize:      64 * 1024,
			OutFileName:  "platform-darwin-x86_64",
		},
		{
			Name:         "platform-darwin-aarch64",
			RootFile:     filepath.Join(nextDir, "pal", "unix", "platform_darwin.zig"),
			Deps:         []ZigDep{palAbiDep},
			Arch:         "aarch64",
			Optimize:     "ReleaseFast",
			LinkerScript: filepath.Join(nextDir, "pal", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			MaxSize:      64 * 1024,
			OutFileName:  "platform-darwin-aarch64",
		},
	}

	brotConfigs := []ZigBrotConfig{
		{
			Name:        "brot-x86_64-linux",
			ZigTarget:   "x86_64-linux",
			OutFileName: "brot-x86_64-unknown-linux-gnu",
		},
		{
			Name:        "brot-aarch64-linux",
			ZigTarget:   "aarch64-linux",
			OutFileName: "brot-aarch64-unknown-linux-gnu",
		},
		{
			Name:        "brot-aarch64-android",
			ZigTarget:   "aarch64-linux",
			OutFileName: "brot-aarch64-linux-android",
		},
		{
			Name:        "brot-x86_64-darwin",
			ZigTarget:   "x86_64-macos",
			DarwinStub:  darwinStubTbd,
			OutFileName: "brot-x86_64-apple-darwin",
		},
		{
			Name:        "brot-aarch64-darwin",
			ZigTarget:   "aarch64-macos",
			DarwinStub:  darwinStubTbd,
			OutFileName: "brot-aarch64-apple-darwin",
		},
	}

	var (
		compileWg sync.WaitGroup
		compileMu sync.Mutex
		builtBins = make(map[string]string)
		errs      []error
	)

	// Spawn flat component compilation goroutines
	for _, def := range flatDefs {
		if runnerMode && !strings.HasPrefix(def.Name, "runtime-") && !strings.HasPrefix(def.Name, "platform-windows-") {
			continue
		}
		compileWg.Add(1)
		go func(def ZigFlatConfig) {
			defer compileWg.Done()
			binPath, err := buildZigFlatComponent(nextDir, zigBuildDir, componentsDir, def, verbose)

			compileMu.Lock()
			defer compileMu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("flat component %s: %w", def.Name, err))
				return
			}
			builtBins[def.OutFileName] = binPath

		}(def)
	}

	// Spawn Brot runner compilation goroutines
	if !runnerMode {
		for _, bcfg := range brotConfigs {
			compileWg.Add(1)
			go func(bcfg ZigBrotConfig) {
				defer compileWg.Done()
				if _, err := buildZigBrotRunner(nextDir, unixOutDir, bcfg, verbose); err != nil {
					compileMu.Lock()
					errs = append(errs, fmt.Errorf("brot runner %s: %w", bcfg.Name, err))
					compileMu.Unlock()
				}
			}(bcfg)
		}
	}

	compileWg.Wait()
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "Error: %v\n", e)
		}
		os.Exit(1)
	}

	// Step 3: Windows CLR host embeds only ZstdSharp; all native/WASM components live in the shared compressed pool.
	fmt.Println("🍆  [3/5] Preparing Windows CLR Host (shared compressed pool will be appended)...")

	if runnerMode {
		fmt.Println("🍆  [4/5] Compiling AnyCPU Windows CLR Runner (MohabbatWindowsRunner.exe)...")
		windowsRunnerExe := filepath.Join(workDir, "MohabbatWindowsRunner.exe")
		if err := buildWindowsRunnerExe(windowsHostDir, workDir, windowsRunnerExe, "anycpu", verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error building Windows runner: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("🍆  [5/5] Assembling payload-free Windows runner...")
		runnerAssets := AssemblyAssets{
			PlatformWindowsX86_64:  builtBins["platform-windows-x86_64"],
			PlatformWindowsAarch64: builtBins["platform-windows-aarch64"],
			RuntimeX86_64:          builtBins["runtime-x86_64"],
			RuntimeAarch64:         builtBins["runtime-aarch64"],
		}
		if err := assembleRunner(windowsRunnerExe, runnerAssets, outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error during runner assembly: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Step 4: Build Windows AnyCPU CLR Host
		fmt.Println("🍆  [4/5] Compiling AnyCPU Windows CLR Host (MohabbatWindowsHost.exe)...")
		windowsHostExe := filepath.Join(workDir, "MohabbatWindowsHost.exe")
		if err := buildWindowsHostExe(windowsHostDir, workDir, windowsHostExe, "anycpu", verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error building Windows host: %v\n", err)
			os.Exit(1)
		}

		// Step 5: Vegetable Assembly
		fmt.Println("🍆  [5/5] Assembling unified single-file vegetable...")

		brotNames := []string{
			"brot-x86_64-unknown-linux-gnu",
			"brot-aarch64-unknown-linux-gnu",
			"brot-aarch64-linux-android",
			"brot-x86_64-apple-darwin",
			"brot-aarch64-apple-darwin",
		}

		brotPaths := make(map[string]string)
		for _, name := range brotNames {
			p := filepath.Join(unixOutDir, name)
			if _, err := os.Stat(p); err != nil {
				fmt.Fprintf(os.Stderr, "Error: missing required Brot runner: %s\n", p)
				os.Exit(1)
			}
			brotPaths[name] = p
		}

		assemblyAssets := AssemblyAssets{
			BrotLinuxX86_64:        brotPaths["brot-x86_64-unknown-linux-gnu"],
			BrotLinuxAarch64:       brotPaths["brot-aarch64-unknown-linux-gnu"],
			BrotAndroidAarch64:     brotPaths["brot-aarch64-linux-android"],
			BrotDarwinX86_64:       brotPaths["brot-x86_64-apple-darwin"],
			BrotDarwinAarch64:      brotPaths["brot-aarch64-apple-darwin"],
			PlatformWindowsX86_64:  builtBins["platform-windows-x86_64"],
			PlatformWindowsAarch64: builtBins["platform-windows-aarch64"],
			PlatformLinuxX86_64:    builtBins["platform-linux-x86_64"],
			PlatformLinuxAarch64:   builtBins["platform-linux-aarch64"],
			PlatformDarwinX86_64:   builtBins["platform-darwin-x86_64"],
			PlatformDarwinAarch64:  builtBins["platform-darwin-aarch64"],
			RuntimeX86_64:          builtBins["runtime-x86_64"],
			RuntimeAarch64:         builtBins["runtime-aarch64"],
			BrainWasm:              brainWasmPath,
		}

		if err := assembleVegetable(windowsHostExe, assemblyAssets, templatesDir, outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error during vegetable assembly: %v\n", err)
			os.Exit(1)
		}
	}
	outputBytes, err := os.ReadFile(outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading assembled output: %v\n", err)
		os.Exit(1)
	}
	hash := sha256.Sum256(outputBytes)
	hashHex := hex.EncodeToString(hash[:])

	duration := time.Since(totalStart).Round(time.Millisecond)
	fmt.Println("================================================================================")
	fmt.Printf("🍆  SUCCESS: Assembled unified vegetable: %s\n", outputPath)
	fmt.Printf("🍆  Size:     %d bytes\n", len(outputBytes))
	fmt.Printf("🍆  SHA-256:  %s\n", strings.ToUpper(hashHex))
	fmt.Printf("🍆  Duration: %v\n", duration)
	fmt.Println("================================================================================")
}
