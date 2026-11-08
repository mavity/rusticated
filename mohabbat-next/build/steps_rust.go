package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

type FlatComponentConfig struct {
	Name         string
	Package      string
	Feature      string
	Arch         string // "x86_64" or "aarch64"
	Triple       string // "x86_64-unknown-none" or "aarch64-unknown-none"
	LinkerScript string
	EntrySymbol  string
	ArchiveGlobs []string
	OutFileName  string
}

func buildFlatComponent(probeDir, rustBuildDir, outDir string, cfg FlatComponentConfig, verbose bool) (string, error) {
	if cfg.Arch == "x86_64" && cfg.Triple == "" {
		cfg.Triple = "x86_64-unknown-none"
	} else if cfg.Arch == "aarch64" && cfg.Triple == "" {
		cfg.Triple = "aarch64-unknown-none"
	}

	workspace := filepath.Join(probeDir, "Cargo.toml")
	cargoHome := filepath.Join(rustBuildDir, "cargo-home")
	compTargetDir := filepath.Join(rustBuildDir, cfg.Name)
	if err := os.MkdirAll(cargoHome, 0o755); err != nil {
		return "", fmt.Errorf("create cargo home %s: %w", cargoHome, err)
	}

	args := []string{
		"+nightly",
		"-Z", "build-std=core",
		"build",
		"--manifest-path", workspace,
		"--package", cfg.Package,
		"--release",
		"--target", cfg.Triple,
		"--target-dir", compTargetDir,
	}
	if cfg.Feature != "" {
		args = append(args, "--features", cfg.Feature)
	}

	buildTime := time.Now().Format("Monday, 2 January 2006 15:04:05 MST -07:00")
	cmd := exec.Command("cargo", args...)
	cmd.Dir = probeDir
	cmd.Env = append(os.Environ(), "CARGO_HOME="+cargoHome, "BUILD_TIME="+buildTime)
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		defer func() {
			if cmd.ProcessState != nil && !cmd.ProcessState.Success() && errBuf.Len() > 0 {
				fmt.Fprintln(os.Stderr, errBuf.String())
			}
		}()
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cargo build for %s (%s): %w", cfg.Name, cfg.Triple, err)
	}

	depDir := filepath.Join(compTargetDir, cfg.Triple, "release", "deps")
	var archives []string
	for _, pattern := range cfg.ArchiveGlobs {
		archive, err := findLatestArchive(depDir, pattern)
		if err != nil {
			return "", fmt.Errorf("find archive %s for %s: %w", pattern, cfg.Name, err)
		}
		archives = append(archives, archive)
	}

	elfPath := filepath.Join(outDir, cfg.OutFileName+".elf")
	binPath := filepath.Join(outDir, cfg.OutFileName+".bin")

	// ld.lld -T <script> --build-id=none --gc-sections --no-undefined --start-group ... --end-group -o <elf>
	lldArgs := []string{
		"-T", cfg.LinkerScript,
		"--build-id=none",
		"--gc-sections",
		"--no-undefined",
		"--start-group",
	}
	lldArgs = append(lldArgs, archives...)
	lldArgs = append(lldArgs, "--end-group", "-o", elfPath)

	lldCmd := exec.Command("ld.lld", lldArgs...)
	if out, err := lldCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ld.lld linking for %s: %w\n%s", cfg.Name, err, string(out))
	}

	// Validate relocations
	relocOut, err := exec.Command("llvm-readobj", "--relocations", elfPath).Output()
	if err != nil {
		return "", fmt.Errorf("llvm-readobj relocations check on %s: %w", elfPath, err)
	}
	relocRe := regexp.MustCompile(`(?m)^\s*0x[0-9a-fA-F]+\s+`)
	if relocRe.Match(relocOut) {
		return "", fmt.Errorf("component %s contains relocations", cfg.Name)
	}

	// Validate TLS
	secOut, err := exec.Command("llvm-readobj", "--sections", elfPath).Output()
	if err != nil {
		return "", fmt.Errorf("llvm-readobj sections check on %s: %w", elfPath, err)
	}
	tlsRe := regexp.MustCompile(`\.(tdata|tbss)`)
	if tlsRe.Match(secOut) {
		return "", fmt.Errorf("component %s contains TLS sections", cfg.Name)
	}

	// Validate undefined symbols
	nmOut, err := exec.Command("llvm-nm", "--undefined-only", elfPath).Output()
	if err != nil {
		return "", fmt.Errorf("llvm-nm undefined-only check on %s: %w", elfPath, err)
	}
	if len(bytes.TrimSpace(nmOut)) > 0 {
		return "", fmt.Errorf("component %s has undefined symbols:\n%s", cfg.Name, string(nmOut))
	}

	// Validate system calls / traps in entry symbol
	disOut, err := exec.Command("llvm-objdump", "--disassemble-symbols="+cfg.EntrySymbol, elfPath).Output()
	if err != nil {
		return "", fmt.Errorf("llvm-objdump on %s (%s): %w", cfg.Name, cfg.EntrySymbol, err)
	}
	trapRe := regexp.MustCompile(`(?i)\b(syscall|sysenter|svc)\b|\bint\s+0x80\b`)
	if trapRe.Match(disOut) {
		return "", fmt.Errorf("component %s contains direct system call or trap instructions in %s", cfg.Name, cfg.EntrySymbol)
	}

	// Extract .payload section into raw binary
	objcopyCmd := exec.Command("llvm-objcopy", "-O", "binary", "--only-section=.payload", elfPath, binPath)
	if out, err := objcopyCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("llvm-objcopy extract payload for %s: %w\n%s", cfg.Name, err, string(out))
	}

	info, err := os.Stat(binPath)
	if err != nil {
		return "", fmt.Errorf("stat binary %s: %w", binPath, err)
	}
	if info.Size() == 0 || info.Size() > 4096 {
		return "", fmt.Errorf("component %s must fit within one 4 KiB page (got %d bytes)", cfg.Name, info.Size())
	}

	return binPath, nil
}

func findLatestArchive(dir, pattern string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read deps dir %s: %w", dir, err)
	}

	var bestPath string
	var bestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matched, err := filepath.Match(pattern, entry.Name())
		if err != nil {
			return "", err
		}
		if !matched {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if bestPath == "" || info.ModTime().After(bestTime) {
			bestPath = filepath.Join(dir, entry.Name())
			bestTime = info.ModTime()
		}
	}

	if bestPath == "" {
		return "", fmt.Errorf("could not find archive matching %s in %s", pattern, dir)
	}
	return bestPath, nil
}

const darwinTbdContent = `--- !tapi-tbd
tbd-version:     4
targets:         [ x86_64-macos, arm64-macos ]
install-name:    '/usr/lib/libSystem.B.dylib'
current-version: 1292.100.35
compatibility-version: 1
exports:
  - targets:     [ x86_64-macos, arm64-macos ]
    symbols:     [ _exit, _abort, _fork, _waitpid, _execve, _open, _read,
                   _write, _close, _lseek, _unlink, _fchmod, _getpid,
                   _mmap, _munmap, _mprotect, _sys_icache_invalidate, _pthread_jit_write_protect_np,
                   __NSGetExecutablePath,
                   _bzero, ___bzero, dyld_stub_binder,
                   _dlopen, _dlerror, _dlsym, _dlclose, _malloc, _free, _usleep ]
...
`

func ensureDarwinStubs(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir darwin-stubs: %w", err)
	}
	stubPath := filepath.Join(dir, "libSystem.tbd")
	existing, err := os.ReadFile(stubPath)
	if err == nil && string(existing) == darwinTbdContent {
		return nil
	}
	if err := os.WriteFile(stubPath, []byte(darwinTbdContent), 0o644); err != nil {
		return fmt.Errorf("write libSystem.tbd: %w", err)
	}
	return nil
}

type BrotRunnerConfig struct {
	Name        string
	Triple      string
	Rustflags   string
	SdkRoot     string
	OutFileName string
}

func buildBrotRunner(probeDir, rustBuildDir, outDir string, cfg BrotRunnerConfig, verbose bool) (string, error) {
	manifestPath := filepath.Join(probeDir, "hosts", "unix", "Cargo.toml")
	cargoHome := filepath.Join(rustBuildDir, "cargo-home")
	compTargetDir := filepath.Join(rustBuildDir, "brot-"+cfg.Name)

	if err := os.MkdirAll(cargoHome, 0o755); err != nil {
		return "", fmt.Errorf("create cargo home %s: %w", cargoHome, err)
	}

	args := []string{
		"+nightly",
		"build",
		"--manifest-path", manifestPath,
		"--release",
		"--target", cfg.Triple,
		"--target-dir", compTargetDir,
		fmt.Sprintf("--config=target.%s.rustflags=%s", cfg.Triple, cfg.Rustflags),
	}

	cmd := exec.Command("cargo", args...)
	cmd.Dir = filepath.Join(probeDir, "hosts", "unix")
	cmd.Env = append(os.Environ(), "CARGO_HOME="+cargoHome)
	if cfg.SdkRoot != "" {
		cmd.Env = append(cmd.Env, "SDKROOT="+cfg.SdkRoot)
	}

	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		defer func() {
			if cmd.ProcessState != nil && !cmd.ProcessState.Success() && errBuf.Len() > 0 {
				fmt.Fprintln(os.Stderr, errBuf.String())
			}
		}()
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cargo build brot runner %s (%s): %w", cfg.Name, cfg.Triple, err)
	}

	srcExe := filepath.Join(compTargetDir, cfg.Triple, "release", "mohabbat-next-brot")
	destExe := filepath.Join(outDir, cfg.OutFileName)

	data, err := os.ReadFile(srcExe)
	if err != nil {
		return "", fmt.Errorf("read built brot %s: %w", srcExe, err)
	}

	if err := os.WriteFile(destExe, data, 0o755); err != nil {
		return "", fmt.Errorf("write brot asset %s: %w", destExe, err)
	}

	return destExe, nil
}
