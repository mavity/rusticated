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
		fixtureMode     bool
	)

	flag.BoolVar(&verbose, "v", false, "Verbose build output")
	flag.BoolVar(&verbose, "verbose", false, "Verbose build output")
	flag.StringVar(&outputPath, "o", "", "Output executable path (defaults to probe/hosts/windows/out/MohabbatProbe.exe)")
	flag.StringVar(&outputPath, "out", "", "Output executable path")
	flag.BoolVar(&windowsHostOnly, "windows-host-only", false, "Build only the Windows CLR test host")
	flag.BoolVar(&fixtureMode, "fixture", false, "Allow substituting missing Unix Brot runners with available runner for local validation")
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

	if outputPath == "" {
		outputPath = filepath.Join(probeDir, "hosts", "windows", "out", "MohabbatProbe.exe")
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

	// Step 2: Build Rust flat components
	fmt.Println("🍆  [2/5] Building flat payload and PAL components...")

	type targetDef struct {
		Name         string
		Package      string
		Feature      string
		Arch         string
		LinkerScript string
		EntrySymbol  string
		ArchiveGlobs []string
		OutName      string
	}

	flatDefs := []targetDef{
		// Hello payload
		{
			Name:         "payload-x86_64",
			Package:      "mohabbat-next-payload",
			Arch:         "x86_64",
			LinkerScript: filepath.Join(probeDir, "payload", "payload.ld"),
			EntrySymbol:  "probe_entry",
			ArchiveGlobs: []string{"libmohabbat_next_payload-*.rlib", "libcore-*.rlib"},
			OutName:      "probe-x86_64",
		},
		{
			Name:         "payload-aarch64",
			Package:      "mohabbat-next-payload",
			Arch:         "aarch64",
			LinkerScript: filepath.Join(probeDir, "payload", "payload.ld"),
			EntrySymbol:  "probe_entry",
			ArchiveGlobs: []string{"libmohabbat_next_payload-*.rlib", "libcore-*.rlib"},
			OutName:      "probe-aarch64",
		},
		// Windows PAL
		{
			Name:         "platform-windows-x86_64",
			Package:      "mohabbat-next-windows-platform",
			Arch:         "x86_64",
			LinkerScript: filepath.Join(probeDir, "platform", "windows", "platform.ld"),
			EntrySymbol:  "windows_platform_entry",
			ArchiveGlobs: []string{"libmohabbat_next_windows_platform-*.rlib", "libcore-*.rlib", "libcompiler_builtins-*.rlib"},
			OutName:      "platform-windows-x86_64",
		},
		{
			Name:         "platform-windows-aarch64",
			Package:      "mohabbat-next-windows-platform",
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
			Arch:         "aarch64",
			LinkerScript: filepath.Join(probeDir, "platform", "unix", "platform.ld"),
			EntrySymbol:  "unix_platform_entry",
			ArchiveGlobs: []string{"libmohabbat_next_unix_platform-*.rlib", "libcore-*.rlib", "libcompiler_builtins-*.rlib"},
			OutName:      "platform-darwin-aarch64",
		},
	}

	builtBins := make(map[string]string)
	for _, def := range flatDefs {
		fmt.Printf("🍆    Building %s...\n", def.Name)
		binPath, err := buildFlatComponent(probeDir, rustBuildDir, componentsDir, FlatComponentConfig{
			Name:         def.Name,
			Package:      def.Package,
			Feature:      def.Feature,
			Arch:         def.Arch,
			LinkerScript: def.LinkerScript,
			EntrySymbol:  def.EntrySymbol,
			ArchiveGlobs: def.ArchiveGlobs,
			OutFileName:  def.OutName,
		}, verbose)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error building %s: %v\n", def.Name, err)
			os.Exit(1)
		}
		builtBins[def.OutName] = binPath

		// Mirror to unix assets directory if it's a unix or shared payload
		if strings.HasPrefix(def.OutName, "platform-linux") ||
			strings.HasPrefix(def.OutName, "platform-darwin") ||
			strings.HasPrefix(def.OutName, "probe-") {
			dest := filepath.Join(unixOutDir, def.OutName+".bin")
			data, err := os.ReadFile(binPath)
			if err == nil {
				_ = os.WriteFile(dest, data, 0o644)
			}
		}
	}

	// Step 3: Compress Windows components for CLR embedding
	fmt.Println("🍆  [3/5] Compressing Windows platform and hello payloads (Zstandard)...")
	type winCompDef struct {
		SrcBin   string
		OutZst   string
		ResName  string
	}
	winComps := []winCompDef{
		{SrcBin: builtBins["probe-x86_64"], OutZst: filepath.Join(componentsDir, "hello-x86_64.zst"), ResName: "MohabbatNext.Hello.x86_64.zst"},
		{SrcBin: builtBins["probe-aarch64"], OutZst: filepath.Join(componentsDir, "hello-aarch64.zst"), ResName: "MohabbatNext.Hello.aarch64.zst"},
		{SrcBin: builtBins["platform-windows-x86_64"], OutZst: filepath.Join(componentsDir, "platform-windows-x86_64.zst"), ResName: "MohabbatNext.Platform.Windows.x86_64.zst"},
		{SrcBin: builtBins["platform-windows-aarch64"], OutZst: filepath.Join(componentsDir, "platform-windows-aarch64.zst"), ResName: "MohabbatNext.Platform.Windows.aarch64.zst"},
	}

	var winResources []WindowsComponentResource
	for _, c := range winComps {
		raw, err := os.ReadFile(c.SrcBin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", c.SrcBin, err)
			os.Exit(1)
		}
		compressed, err := compressZstandard(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error compressing %s: %v\n", c.SrcBin, err)
			os.Exit(1)
		}
		if err := os.WriteFile(c.OutZst, compressed, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", c.OutZst, err)
			os.Exit(1)
		}
		winResources = append(winResources, WindowsComponentResource{
			FilePath:     c.OutZst,
			ResourceName: c.ResName,
		})
	}

	// Step 4: Build Windows AnyCPU CLR Host
	fmt.Println("🍆  [4/5] Compiling AnyCPU Windows CLR Host (MohabbatWindowsHost.exe)...")
	windowsHostExe := filepath.Join(workDir, "MohabbatWindowsHost.exe")
	if err := buildWindowsHostExe(windowsHostDir, workDir, windowsHostExe, winResources, verbose); err != nil {
		fmt.Fprintf(os.Stderr, "Error building Windows host: %v\n", err)
		os.Exit(1)
	}

	if windowsHostOnly {
		fmt.Printf("🍆  Finished. Windows-only test host: %s\n", windowsHostExe)
		return
	}

	// Step 5: Vegetable Assembly
	fmt.Println("🍆  [5/5] Assembling unified single-file vegetable...")

	// Verify required Brot runners
	brotNames := []string{
		"brot-x86_64-unknown-linux-gnu",
		"brot-aarch64-unknown-linux-gnu",
		"brot-aarch64-linux-android",
		"brot-x86_64-apple-darwin",
		"brot-aarch64-apple-darwin",
	}

	brotPaths := make(map[string]string)
	linuxArmRunner := filepath.Join(unixOutDir, "brot-aarch64-unknown-linux-gnu")

	for _, name := range brotNames {
		p := filepath.Join(unixOutDir, name)
		if _, err := os.Stat(p); err == nil {
			brotPaths[name] = p
		} else {
			if fixtureMode {
				if _, err := os.Stat(linuxArmRunner); err != nil {
					fmt.Fprintf(os.Stderr, "Error: fixture mode requires Linux AArch64 runner at %s\n", linuxArmRunner)
					os.Exit(1)
				}
				fmt.Printf("🍆    [fixture] Substituting %s -> %s\n", linuxArmRunner, name)
				brotPaths[name] = linuxArmRunner
			} else {
				fmt.Fprintf(os.Stderr, "Error: missing required Brot runner: %s\n(Use --fixture to assemble a local validation fixture)\n", p)
				os.Exit(1)
			}
		}
	}

	assemblyAssets := AssemblyAssets{
		BrotLinuxX86_64:     brotPaths["brot-x86_64-unknown-linux-gnu"],
		BrotLinuxAarch64:    brotPaths["brot-aarch64-unknown-linux-gnu"],
		BrotAndroidAarch64:  brotPaths["brot-aarch64-linux-android"],
		BrotDarwinX86_64:    brotPaths["brot-x86_64-apple-darwin"],
		BrotDarwinAarch64:   brotPaths["brot-aarch64-apple-darwin"],
		PlatformLinuxX86_64: builtBins["platform-linux-x86_64"],
		PlatformLinuxAarch64: builtBins["platform-linux-aarch64"],
		PlatformDarwinX86_64: builtBins["platform-darwin-x86_64"],
		PlatformDarwinAarch64: builtBins["platform-darwin-aarch64"],
		HelloX86_64:         builtBins["probe-x86_64"],
		HelloAarch64:        builtBins["probe-aarch64"],
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
