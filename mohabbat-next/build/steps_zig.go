package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type ZigFlatConfig struct {
	Name         string
	RootFile     string
	Deps         []ZigDep
	Arch         string // "x86_64" or "aarch64"
	Optimize     string // "ReleaseFast" or "ReleaseSmall"
	LinkerScript string
	EntrySymbol  string
	MaxSize      int64
	IsRuntime    bool
	OutFileName  string
}

type ZigDep struct {
	Name string
	Path string
	Deps []ZigDep
}

type ZigBrotConfig struct {
	Name        string
	ZigTarget   string
	DarwinStub  string
	OutFileName string
}

func appendZigModuleArgs(args []string, deps []ZigDep) []string {
	for _, d := range deps {
		for _, sub := range d.Deps {
			args = append(args, "--dep", sub.Name)
		}
		args = append(args, fmt.Sprintf("-M%s=%s", d.Name, d.Path))
		if len(d.Deps) > 0 {
			args = appendZigModuleArgs(args, d.Deps)
		}
	}
	return args
}

func buildZigFlatComponent(nextDir, zigBuildDir, outDir string, cfg ZigFlatConfig, verbose bool) (string, error) {
	triple := cfg.Arch + "-freestanding"
	opt := cfg.Optimize
	if opt == "" {
		opt = "ReleaseFast"
	}

	compDir := filepath.Join(zigBuildDir, cfg.Name)
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		return "", fmt.Errorf("create build dir %s: %w", compDir, err)
	}
	objPath := filepath.Join(compDir, cfg.OutFileName+".o")
	elfPath := filepath.Join(outDir, cfg.OutFileName+".elf")
	binPath := filepath.Join(outDir, cfg.OutFileName+".bin")

	args := []string{
		"build-obj",
		"-target", triple,
		"-O", opt,
		"-fPIC",
		"-fno-stack-protector",
		"-fno-omit-frame-pointer",
		"-femit-bin=" + objPath,
	}
	if cfg.Arch == "aarch64" {
		args = append(args, "-mcpu=baseline+reserve_x18")
	}
	if cfg.IsRuntime {
		args = append(args, "-fsingle-threaded")
	}
	for _, d := range cfg.Deps {
		args = append(args, "--dep", d.Name)
	}
	args = append(args, fmt.Sprintf("-Mroot=%s", cfg.RootFile))
	args = appendZigModuleArgs(args, cfg.Deps)

	cmd := exec.Command("zig", args...)
	cmd.Dir = nextDir
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("zig build-obj %s (%s): %w\n%s", cfg.Name, triple, err, string(out))
	}

	lldArgs := []string{
		"-T", cfg.LinkerScript,
		"--build-id=none",
		"--gc-sections",
		"--no-undefined",
		"-o", elfPath,
		objPath,
	}
	if out, err := exec.Command("ld.lld", lldArgs...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ld.lld linking %s: %w\n%s", cfg.Name, err, string(out))
	}

	// Validate relocations
	relocOut, err := exec.Command("llvm-readobj", "--relocations", elfPath).Output()
	if err != nil {
		return "", fmt.Errorf("llvm-readobj --relocations on %s: %w", elfPath, err)
	}
	relocRe := regexp.MustCompile(`(?m)^\s*0x[0-9a-fA-F]+\s+`)
	if !cfg.IsRuntime && relocRe.Match(relocOut) {
		return "", fmt.Errorf("component %s contains relocations:\n%s", cfg.Name, string(relocOut))
	}

	// Validate sections (no TLS, no .got/.plt/.data)
	secOut, err := exec.Command("llvm-readobj", "--sections", elfPath).Output()
	if err != nil {
		return "", fmt.Errorf("llvm-readobj --sections on %s: %w", elfPath, err)
	}
	badSecRe := regexp.MustCompile(`\.(tdata|tbss|got|plt)\b`)
	if badSecRe.Match(secOut) {
		return "", fmt.Errorf("component %s contains forbidden sections:\n%s", cfg.Name, string(secOut))
	}

	// Validate undefined symbols
	nmOut, err := exec.Command("llvm-nm", "--undefined-only", elfPath).Output()
	if err != nil {
		return "", fmt.Errorf("llvm-nm --undefined-only on %s: %w", elfPath, err)
	}
	if len(bytes.TrimSpace(nmOut)) > 0 {
		return "", fmt.Errorf("component %s has undefined symbols:\n%s", cfg.Name, string(nmOut))
	}

	// Validate no raw syscalls in entry symbol for payloads and non-Linux PALs
	if !strings.HasPrefix(cfg.Name, "platform-linux-") {
		disOut, err := exec.Command("llvm-objdump", "--disassemble-symbols="+cfg.EntrySymbol, elfPath).Output()
		if err != nil {
			return "", fmt.Errorf("llvm-objdump on %s (%s): %w", cfg.Name, cfg.EntrySymbol, err)
		}
		trapRe := regexp.MustCompile(`(?i)\b(syscall|sysenter|svc)\b|\bint\s+0x80\b`)
		if trapRe.Match(disOut) {
			return "", fmt.Errorf("component %s contains direct syscall/trap in %s", cfg.Name, cfg.EntrySymbol)
		}
	}

	if out, err := exec.Command("llvm-objcopy", "-O", "binary", "--only-section=.payload", elfPath, binPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("llvm-objcopy .payload for %s: %w\n%s", cfg.Name, err, string(out))
	}

	if cfg.IsRuntime {
		if err := appendZigRuntimeRwPrefixRelocs(binPath, cfg.LinkerScript, objPath, outDir, cfg.OutFileName); err != nil {
			return "", fmt.Errorf("append RW-prefix relocs for %s: %w", cfg.Name, err)
		}
	}

	info, err := os.Stat(binPath)
	if err != nil {
		return "", fmt.Errorf("stat binary %s: %w", binPath, err)
	}
	maxSize := cfg.MaxSize
	if maxSize <= 0 {
		maxSize = 4096
	}
	if info.Size() == 0 || info.Size() > maxSize {
		return "", fmt.Errorf("component %s must fit within %d bytes (got %d bytes)", cfg.Name, maxSize, info.Size())
	}
	return binPath, nil
}

func appendZigRuntimeRwPrefixRelocs(binPath, linkerScript, objPath, outDir, outName string) error {
	preElfPath := filepath.Join(outDir, ".pre-"+outName+".elf")
	rwInitPath := filepath.Join(outDir, ".rwinit-"+outName+".bin")
	defer os.Remove(preElfPath)
	defer os.Remove(rwInitPath)

	lldArgs := []string{
		"-T", linkerScript,
		"--build-id=none",
		"--gc-sections",
		"--no-undefined",
		"--emit-relocs",
		"-o", preElfPath,
		objPath,
	}
	if out, err := exec.Command("ld.lld", lldArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("ld.lld --emit-relocs: %w\n%s", err, string(out))
	}

	var rwInitBytes []byte
	if out, err := exec.Command("llvm-objcopy", "-O", "binary", "--only-section=.rwinit", preElfPath, rwInitPath).CombinedOutput(); err == nil {
		rwInitBytes, _ = os.ReadFile(rwInitPath)
	} else {
		return fmt.Errorf("llvm-objcopy .rwinit: %w\n%s", err, string(out))
	}
	if len(rwInitBytes) > 96*1024 {
		return fmt.Errorf(".rwinit size %d exceeds 96 KiB RW-prefix gate", len(rwInitBytes))
	}

	relocOut, err := exec.Command("llvm-readobj", "--relocations", preElfPath).Output()
	if err != nil {
		return fmt.Errorf("llvm-readobj --relocations: %w", err)
	}

	// Only collect ABS64 relocations inside .rela.rwinit (negative offsets relative to code_base)
	var offsets []int32
	inRwInit := false
	re := regexp.MustCompile(`^\s*0x([0-9a-fA-F]+)\s+(R_AARCH64_ABS64|R_X86_64_64)\s+`)
	for _, line := range strings.Split(string(relocOut), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Section (") {
			inRwInit = strings.Contains(trim, ".rela.rwinit") || strings.Contains(trim, ".rel.rwinit")
			continue
		}
		if !inRwInit {
			continue
		}
		if m := re.FindStringSubmatch(line); m != nil {
			uoff, err := strconv.ParseUint(m[1], 16, 64)
			if err != nil {
				return fmt.Errorf("parse relocation offset %q: %w", m[1], err)
			}
			soff := int64(uoff)
			if soff >= 0 || soff < -0x18000 {
				return fmt.Errorf("unexpected .rwinit relocation offset 0x%x", uoff)
			}
			offsets = append(offsets, int32(soff))
		}
	}

	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0x52454C4F))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(rwInitBytes)))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(offsets)))
	buf.Write(rwInitBytes)
	for _, off := range offsets {
		_ = binary.Write(&buf, binary.LittleEndian, off)
	}

	f, err := os.OpenFile(binPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(buf.Bytes())
	return err
}

func buildZigBrotRunner(nextDir, outDir string, cfg ZigBrotConfig, verbose bool) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create brot out dir %s: %w", outDir, err)
	}
	outPath := filepath.Join(outDir, cfg.OutFileName)
	mainZig := filepath.Join(nextDir, "brot", "unix", "src", "main.zig")

	args := []string{
		"build-exe",
		"-target", cfg.ZigTarget,
		"-O", "ReleaseSmall",
		"-fno-stack-protector",
		"-fPIE",
		"-femit-bin=" + outPath,
		mainZig,
	}
	if cfg.DarwinStub != "" {
		args = append(args, cfg.DarwinStub)
	}

	cmd := exec.Command("zig", args...)
	cmd.Dir = nextDir
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("zig build-exe %s (%s): %w\n%s", cfg.Name, cfg.ZigTarget, err, string(out))
	}
	return outPath, nil
}
