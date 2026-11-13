package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func findProbeDir() (string, string, error) {
	// Try current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}

	candidates := []string{
		filepath.Join(cwd, "mohabbat-next"),
		cwd,
		filepath.Dir(cwd),
	}

	for _, cand := range candidates {
		probeDir := filepath.Join(cand, "probe")
		if info, err := os.Stat(probeDir); err == nil && info.IsDir() {
			return cand, probeDir, nil
		}
	}

	return "", "", fmt.Errorf("could not locate mohabbat-next directory from %s", cwd)
}

func checkTools() error {
	tools := []string{
		"cargo",
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
		verbose         bool
		outputPath      string
		windowsHostOnly bool
	)

	flag.BoolVar(&verbose, "v", false, "Verbose build output")
	flag.BoolVar(&verbose, "verbose", false, "Verbose build output")
	flag.StringVar(&outputPath, "o", "", "Output executable path (defaults to release/dist/MohabbatProbe.exe)")
	flag.StringVar(&outputPath, "out", "", "Output executable path")
	flag.BoolVar(&windowsHostOnly, "windows-host-only", false, "Build only the Windows CLR test host")
	flag.Parse()

	totalStart := time.Now()
	fmt.Println("================================================================================")
	fmt.Println("🍆  Mohabbat-next Vegetable Builder (Go)")
	fmt.Println("================================================================================")

	nextDir, probeDir, err := findProbeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	repoRoot := filepath.Dir(nextDir)
	if outputPath == "" {
		outputPath = filepath.Join(repoRoot, "release", "dist", "MohabbatProbe.exe")
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: resolve output path: %v\n", err)
		os.Exit(1)
	}

	workDir := filepath.Join(nextDir, ".build")
	rustBuildDir := filepath.Join(workDir, "rust")
	componentsDir := filepath.Join(workDir, "components")
	templatesDir := filepath.Join(nextDir, "build", "templates")
	unixOutDir := filepath.Join(probeDir, "hosts", "unix", "out")
	windowsHostDir := filepath.Join(probeDir, "hosts", "windows")

	for _, dir := range []string{workDir, rustBuildDir, componentsDir, unixOutDir, filepath.Dir(outputPath)} {
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

	// Step 2: Build Rust flat components and Brot micro-launchers in parallel
	fmt.Println("🍆  [2/5] Compiling flat components and Unix Brot launchers in parallel...")

	darwinStubsDir := filepath.Join(repoRoot, "target", "darwin-stubs")
	if abs, err := filepath.Abs(darwinStubsDir); err == nil {
		darwinStubsDir = abs
	}
	if err := ensureDarwinStubs(darwinStubsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating darwin stubs: %v\n", err)
		os.Exit(1)
	}

	linuxMuslFlags := "['-C', 'panic=abort', '-C', 'relocation-model=pic', '-C', 'target-feature=+crt-static', '-C', 'link-self-contained=no', '-C', 'linker=rust-lld', '-C', 'linker-flavor=ld.lld', '-C', 'link-arg=-pie', '-C', 'link-arg=--no-dynamic-linker']"
	darwinFlags := fmt.Sprintf("['-C', 'panic=abort', '-C', 'linker=rust-lld', '-C', 'linker-flavor=darwin-lld', '-Z', 'unstable-options', '-C', 'link-arg=-L%s']", filepath.ToSlash(darwinStubsDir))
	runtimeRustflagsX64 := "-C relocation-model=pic -Zunstable-options -Zdefault-visibility=hidden -Cpanic=abort -Zlocation-detail=none -Zfmt-debug=none -Cforce-frame-pointers=yes -Cllvm-args=-enable-machine-outliner=never -Zplt=yes"
	runtimeRustflagsAArch64 := "-C relocation-model=pie -Zunstable-options -Zdefault-visibility=hidden -Cpanic=abort -Zlocation-detail=none -Zfmt-debug=none -Cforce-frame-pointers=yes -Cllvm-args=-enable-machine-outliner=never -Ctarget-feature=+reserve-x18"
	runtimeDir := filepath.Join(nextDir, "runtime")
	runtimeBuildDir, err := prepareCraneliftOverlay(runtimeDir, rustBuildDir, verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error preparing Cranelift source overlay: %v\n", err)
		os.Exit(1)
	}

	type targetDef struct {
		Name              string
		ManifestDir       string
		Package           string
		Feature           string
		NoDefaultFeatures bool
		NeedsAlloc        bool
		Rustflags         string
		Arch              string
		LinkerScript      string
		EntrySymbol       string
		ArchiveGlobs      []string
		StaticlibName     string
		MaxSize           int64
		OutName           string
	}

	flatDefs := []targetDef{
		// Hello payload
		{
			Name:         "payload-x86_64",
			Package:      "mohabbat-next-payload",
			Arch:         "x86_64",
			LinkerScript: filepath.Join(probeDir, "payload", "payload.ld"),
			EntrySymbol:  "probe_code_start",
			ArchiveGlobs: []string{"libmohabbat_next_payload-*.rlib", "libcore-*.rlib"},
			OutName:      "probe-x86_64",
		},
		{
			Name:         "payload-aarch64",
			Package:      "mohabbat-next-payload",
			Arch:         "aarch64",
			LinkerScript: filepath.Join(probeDir, "payload", "payload.ld"),
			EntrySymbol:  "probe_code_start",
			ArchiveGlobs: []string{"libmohabbat_next_payload-*.rlib", "libcore-*.rlib"},
			OutName:      "probe-aarch64",
		},
		{
			Name:         "payload-fileio-x86_64",
			Package:      "mohabbat-next-payload",
			Feature:      "file-io-test",
			MaxSize:      8192,
			Arch:         "x86_64",
			LinkerScript: filepath.Join(probeDir, "payload", "payload.ld"),
			EntrySymbol:  "probe_code_start",
			ArchiveGlobs: []string{"libmohabbat_next_payload-*.rlib", "libcore-*.rlib"},
			OutName:      "probe-fileio-x86_64",
		},
		{
			Name:         "payload-fileio-aarch64",
			Package:      "mohabbat-next-payload",
			Feature:      "file-io-test",
			MaxSize:      8192,
			Arch:         "aarch64",
			LinkerScript: filepath.Join(probeDir, "payload", "payload.ld"),
			EntrySymbol:  "probe_code_start",
			ArchiveGlobs: []string{"libmohabbat_next_payload-*.rlib", "libcore-*.rlib"},
			OutName:      "probe-fileio-aarch64",
		},
		// Movable Cranelift Runtime Slice (ISA-shared)
		{
			Name:              "runtime-x86_64",
			ManifestDir:       runtimeBuildDir,
			Feature:           "x86_64",
			NoDefaultFeatures: true,
			NeedsAlloc:        true,
			Rustflags:         runtimeRustflagsX64,
			Arch:              "x86_64",
			LinkerScript:      filepath.Join(runtimeDir, "runtime.ld"),
			EntrySymbol:       "runtime_code_start",
			StaticlibName:     "libmohabbat_next_runtime.a",
			MaxSize:           4 * 1024 * 1024,
			OutName:           "runtime-x86_64",
		},
		{
			Name:              "runtime-aarch64",
			ManifestDir:       runtimeBuildDir,
			Feature:           "aarch64",
			NoDefaultFeatures: true,
			NeedsAlloc:        true,
			Rustflags:         runtimeRustflagsAArch64,
			Arch:              "aarch64",
			LinkerScript:      filepath.Join(runtimeDir, "runtime.ld"),
			EntrySymbol:       "runtime_code_start",
			StaticlibName:     "libmohabbat_next_runtime.a",
			MaxSize:           4 * 1024 * 1024,
			OutName:           "runtime-aarch64",
		},
		// Windows PAL
		{
			Name:         "platform-windows-x86_64",
			Package:      "mohabbat-next-windows-platform",
			MaxSize:      8192,
			Arch:         "x86_64",
			LinkerScript: filepath.Join(probeDir, "platform", "windows", "platform.ld"),
			EntrySymbol:  "windows_platform_entry",
			ArchiveGlobs: []string{"libmohabbat_next_windows_platform-*.rlib", "libcore-*.rlib", "libcompiler_builtins-*.rlib"},
			OutName:      "platform-windows-x86_64",
		},
		{
			Name:         "platform-windows-aarch64",
			Package:      "mohabbat-next-windows-platform",
			MaxSize:      8192,
			Arch:         "aarch64",
			LinkerScript: filepath.Join(probeDir, "platform", "windows", "platform.ld"),
			EntrySymbol:  "windows_platform_entry",
			ArchiveGlobs: []string{"libmohabbat_next_windows_platform-*.rlib", "libcore-*.rlib", "libcompiler_builtins-*.rlib"},
			OutName:      "platform-windows-aarch64",
		},
		// Linux PAL
		{
			Name:         "platform-linux-x86_64",
			Package:      "mohabbat-next-unix-platform",
			Feature:      "linux",
			MaxSize:      8192,
			Arch:         "x86_64",
			LinkerScript: filepath.Join(probeDir, "platform", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			ArchiveGlobs: []string{"libmohabbat_next_unix_platform-*.rlib", "libcore-*.rlib", "libcompiler_builtins-*.rlib"},
			OutName:      "platform-linux-x86_64",
		},
		{
			Name:         "platform-linux-aarch64",
			Package:      "mohabbat-next-unix-platform",
			Feature:      "linux",
			MaxSize:      8192,
			Arch:         "aarch64",
			LinkerScript: filepath.Join(probeDir, "platform", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			ArchiveGlobs: []string{"libmohabbat_next_unix_platform-*.rlib", "libcore-*.rlib", "libcompiler_builtins-*.rlib"},
			OutName:      "platform-linux-aarch64",
		},
		// Darwin PAL
		{
			Name:         "platform-darwin-x86_64",
			Package:      "mohabbat-next-unix-platform",
			Feature:      "darwin",
			MaxSize:      8192,
			Arch:         "x86_64",
			LinkerScript: filepath.Join(probeDir, "platform", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			ArchiveGlobs: []string{"libmohabbat_next_unix_platform-*.rlib", "libcore-*.rlib", "libcompiler_builtins-*.rlib"},
			OutName:      "platform-darwin-x86_64",
		},
		{
			Name:         "platform-darwin-aarch64",
			Package:      "mohabbat-next-unix-platform",
			Feature:      "darwin",
			MaxSize:      8192,
			Arch:         "aarch64",
			LinkerScript: filepath.Join(probeDir, "platform", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			ArchiveGlobs: []string{"libmohabbat_next_unix_platform-*.rlib", "libcore-*.rlib", "libcompiler_builtins-*.rlib"},
			OutName:      "platform-darwin-aarch64",
		},
	}

	brotConfigs := []BrotRunnerConfig{
		{
			Name:        "brot-x86_64-linux",
			Triple:      "x86_64-unknown-linux-musl",
			Rustflags:   linuxMuslFlags,
			OutFileName: "brot-x86_64-unknown-linux-gnu",
		},
		{
			Name:        "brot-aarch64-linux",
			Triple:      "aarch64-unknown-linux-musl",
			Rustflags:   linuxMuslFlags,
			OutFileName: "brot-aarch64-unknown-linux-gnu",
		},
		{
			Name:        "brot-aarch64-android",
			Triple:      "aarch64-unknown-linux-musl",
			Rustflags:   linuxMuslFlags,
			OutFileName: "brot-aarch64-linux-android",
		},
		{
			Name:        "brot-x86_64-darwin",
			Triple:      "x86_64-apple-darwin",
			Rustflags:   darwinFlags,
			SdkRoot:     darwinStubsDir,
			OutFileName: "brot-x86_64-apple-darwin",
		},
		{
			Name:        "brot-aarch64-darwin",
			Triple:      "aarch64-apple-darwin",
			Rustflags:   darwinFlags,
			SdkRoot:     darwinStubsDir,
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
		compileWg.Add(1)
		go func(def targetDef) {
			defer compileWg.Done()
			binPath, err := buildFlatComponent(probeDir, rustBuildDir, componentsDir, FlatComponentConfig{
				Name:              def.Name,
				ManifestDir:       def.ManifestDir,
				Package:           def.Package,
				Feature:           def.Feature,
				NoDefaultFeatures: def.NoDefaultFeatures,
				NeedsAlloc:        def.NeedsAlloc,
				Rustflags:         def.Rustflags,
				Arch:              def.Arch,
				LinkerScript:      def.LinkerScript,
				EntrySymbol:       def.EntrySymbol,
				ArchiveGlobs:      def.ArchiveGlobs,
				StaticlibName:     def.StaticlibName,
				MaxSize:           def.MaxSize,
				OutFileName:       def.OutName,
			}, verbose)

			compileMu.Lock()
			defer compileMu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("flat component %s: %w", def.Name, err))
				return
			}
			builtBins[def.OutName] = binPath

			if strings.HasPrefix(def.OutName, "platform-linux") ||
				strings.HasPrefix(def.OutName, "platform-darwin") ||
				strings.HasPrefix(def.OutName, "probe-") {
				dest := filepath.Join(unixOutDir, def.OutName+".bin")
				data, err := os.ReadFile(binPath)
				if err == nil {
					_ = os.WriteFile(dest, data, 0o644)
				}
			}
		}(def)
	}

	// Spawn Brot runner compilation goroutines
	for _, bcfg := range brotConfigs {
		compileWg.Add(1)
		go func(bcfg BrotRunnerConfig) {
			defer compileWg.Done()
			if _, err := buildBrotRunner(probeDir, rustBuildDir, unixOutDir, bcfg, verbose); err != nil {
				compileMu.Lock()
				errs = append(errs, fmt.Errorf("brot runner %s: %w", bcfg.Name, err))
				compileMu.Unlock()
			}
		}(bcfg)
	}

	compileWg.Wait()
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "Error: %v\n", e)
		}
		os.Exit(1)
	}

	// Step 3: Compress Windows components for CLR embedding (in parallel with max zstd)
	fmt.Println("🍆  [3/5] Compressing Windows platform, hello, and file-I/O payloads (max Zstandard)...")
	type winCompDef struct {
		SrcBin  string
		OutZst  string
		ResName string
	}
	winComps := []winCompDef{
		{SrcBin: builtBins["probe-x86_64"], OutZst: filepath.Join(componentsDir, "hello-x86_64.zst"), ResName: "MohabbatNext.Hello.x86_64.zst"},
		{SrcBin: builtBins["probe-aarch64"], OutZst: filepath.Join(componentsDir, "hello-aarch64.zst"), ResName: "MohabbatNext.Hello.aarch64.zst"},
		{SrcBin: builtBins["probe-fileio-x86_64"], OutZst: filepath.Join(componentsDir, "fileio-x86_64.zst"), ResName: "MohabbatNext.FileIo.x86_64.zst"},
		{SrcBin: builtBins["probe-fileio-aarch64"], OutZst: filepath.Join(componentsDir, "fileio-aarch64.zst"), ResName: "MohabbatNext.FileIo.aarch64.zst"},
		{SrcBin: builtBins["platform-windows-x86_64"], OutZst: filepath.Join(componentsDir, "platform-windows-x86_64.zst"), ResName: "MohabbatNext.Platform.Windows.x86_64.zst"},
		{SrcBin: builtBins["platform-windows-aarch64"], OutZst: filepath.Join(componentsDir, "platform-windows-aarch64.zst"), ResName: "MohabbatNext.Platform.Windows.aarch64.zst"},
	}

	var (
		compWg       sync.WaitGroup
		compMu       sync.Mutex
		winResources = make([]WindowsComponentResource, len(winComps))
	)

	for idx, c := range winComps {
		compWg.Add(1)
		go func(i int, c winCompDef) {
			defer compWg.Done()
			raw, err := os.ReadFile(c.SrcBin)
			if err != nil {
				compMu.Lock()
				errs = append(errs, fmt.Errorf("read %s: %w", c.SrcBin, err))
				compMu.Unlock()
				return
			}
			compressed, err := compressZstandard(raw)
			if err != nil {
				compMu.Lock()
				errs = append(errs, fmt.Errorf("compress %s: %w", c.SrcBin, err))
				compMu.Unlock()
				return
			}
			if err := os.WriteFile(c.OutZst, compressed, 0o644); err != nil {
				compMu.Lock()
				errs = append(errs, fmt.Errorf("write %s: %w", c.OutZst, err))
				compMu.Unlock()
				return
			}
			compMu.Lock()
			winResources[i] = WindowsComponentResource{
				FilePath:     c.OutZst,
				ResourceName: c.ResName,
			}
			compMu.Unlock()
		}(idx, c)
	}

	compWg.Wait()
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "Error: %v\n", e)
		}
		os.Exit(1)
	}

	// Step 4: Build Windows AnyCPU CLR Host
	fmt.Println("🍆  [4/5] Compiling AnyCPU Windows CLR Host (MohabbatWindowsHost.exe)...")
	windowsHostExe := filepath.Join(workDir, "MohabbatWindowsHost.exe")
	if err := buildWindowsHostExe(windowsHostDir, workDir, windowsHostExe, "anycpu", winResources, verbose); err != nil {
		fmt.Fprintf(os.Stderr, "Error building Windows host: %v\n", err)
		os.Exit(1)
	}

	windowsHostX64Exe := filepath.Join(workDir, "MohabbatWindowsHost-x64.exe")
	if err := buildWindowsHostExe(windowsHostDir, workDir, windowsHostX64Exe, "x64", winResources, verbose); err != nil {
		fmt.Fprintf(os.Stderr, "Error building x64 Windows host: %v\n", err)
		os.Exit(1)
	}

	if windowsHostOnly {
		fmt.Printf("🍆  Finished. Windows-only test host: %s\n", windowsHostExe)
		return
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
		BrotLinuxX86_64:       brotPaths["brot-x86_64-unknown-linux-gnu"],
		BrotLinuxAarch64:      brotPaths["brot-aarch64-unknown-linux-gnu"],
		BrotAndroidAarch64:    brotPaths["brot-aarch64-linux-android"],
		BrotDarwinX86_64:      brotPaths["brot-x86_64-apple-darwin"],
		BrotDarwinAarch64:     brotPaths["brot-aarch64-apple-darwin"],
		PlatformLinuxX86_64:   builtBins["platform-linux-x86_64"],
		PlatformLinuxAarch64:  builtBins["platform-linux-aarch64"],
		PlatformDarwinX86_64:  builtBins["platform-darwin-x86_64"],
		PlatformDarwinAarch64: builtBins["platform-darwin-aarch64"],
		HelloX86_64:           builtBins["probe-x86_64"],
		HelloAarch64:          builtBins["probe-aarch64"],
	}

	if err := assembleVegetable(windowsHostExe, assemblyAssets, templatesDir, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error during vegetable assembly: %v\n", err)
		os.Exit(1)
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
