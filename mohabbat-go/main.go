package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/andybalholm/brotli"
)

// Modern Two scope: windows/amd64 and windows/arm64 only.
// Slot order is contractual Ã¢â‚¬â€ Zone A and patcher both depend on it.
var slots = []slot{
	{name: "win-amd64", goos: "windows", goarch: "amd64", winArch: "AMD64"},
	{name: "win-arm64", goos: "windows", goarch: "arm64", winArch: "ARM64"},
}

type slot struct {
	name    string
	goos    string
	goarch  string
	winArch string // matches %PROCESSOR_ARCHITECTURE%
}

const mohabbatMagic = "MOHABBAT"

// MohabbatMeta layout: 8-byte magic + 6*u64 = 56 bytes
type mohabbatMeta struct {
	PoolLen         uint64
	WashmhostOffset uint64
	WashmhostLen    uint64
	PayloadOffset   uint64
	PayloadLen      uint64
	Reserved        uint64
}

func upsertEnv(env []string, key, value string) []string {
	updated := make([]string, 0, len(env)+1)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], key) {
			continue
		}
		updated = append(updated, kv)
	}
	updated = append(updated, key+"="+value)
	return updated
}

func main() {
	var (
		workspace = flag.String("workspace", "", "workspace root (default: parent of mohabbat-go)")
		payload   = flag.String("payload", "", "path to payload .wasm (default: target/wasm32-rusticated-unknown-unknown/release/mohabbat.wasm)")
		output    = flag.String("output", "", "output mohab.bat path (default: <workspace>/mohab.bat)")
		skipBuild = flag.Bool("skip-build", false, "skip building brot-go and washmhost-go (use existing artifacts)")
	)
	flag.Parse()

	ws, err := resolveWorkspace(*workspace)
	must(err)
	defaultPayload := *payload == ""

	if *payload == "" {
		*payload = filepath.Join(ws, "target", "wasm32-rusticated-unknown-unknown", "release", "mohabbat.wasm")
	}
	if *output == "" {
		*output = filepath.Join(ws, "mohab.bat")
	}

	buildDir := filepath.Join(ws, "target", "mohabbat-go-build")
	must(os.MkdirAll(buildDir, 0o755))

	if !*skipBuild {
		fmt.Println("मोहब्बत  Building brot (cargo) and washmhost-go for Modern Two...")
		for _, s := range slots {
			must(cargoBuild(ws, "brot", s, buildDir))
			must(goBuild(ws, "washmhost-go", s, buildDir))
		}
		if defaultPayload {
			fmt.Println("मोहब्बत  Building default brain payload (mohabbat wasm)...")
			must(buildMohabbatBrain(ws))
		}
	}

	fmt.Printf("मोहब्बत  Reading payload: %s\n", *payload)
	brainBytes, err := os.ReadFile(*payload)
	if err != nil {
		die("cannot read payload %s: %v", *payload, err)
	}

	// Read built artifacts
	type artifacts struct {
		brot, washmhost []byte
	}
	per := make([]artifacts, len(slots))
	for i, s := range slots {
		brot, err := os.ReadFile(brotPath(buildDir, s))
		must(err)
		wh, err := os.ReadFile(washmhostPath(buildDir, s))
		must(err)
		per[i] = artifacts{brot: brot, washmhost: wh}
		fmt.Printf("मोहब्बत    %s: brot=%d washmhost=%d\n", s.name, len(brot), len(wh))
	}

	// Assemble pool: washmhost_0 + washmhost_1 + ... + payload
	pool := &bytes.Buffer{}
	whOffsets := make([]uint64, len(slots))
	whLens := make([]uint64, len(slots))
	for i := range slots {
		whOffsets[i] = uint64(pool.Len())
		whLens[i] = uint64(len(per[i].washmhost))
		pool.Write(per[i].washmhost)
	}
	payloadOffset := uint64(pool.Len())
	payloadLen := uint64(len(brainBytes))
	pool.Write(brainBytes)

	// Brotli compress (quality 1 matches existing Rust pipeline to avoid hangs).
	compressed := &bytes.Buffer{}
	w := brotli.NewWriterLevel(compressed, 1)
	if _, err := w.Write(pool.Bytes()); err != nil {
		die("brotli write: %v", err)
	}
	must(w.Close())
	poolLen := uint64(compressed.Len())
	fmt.Printf("मोहब्बत  Pool: raw=%d compressed=%d\n", pool.Len(), poolLen)

	// Patch MohabbatMeta inside each brot
	for i := range slots {
		meta := mohabbatMeta{
			PoolLen:         poolLen,
			WashmhostOffset: whOffsets[i],
			WashmhostLen:    whLens[i],
			PayloadOffset:   payloadOffset,
			PayloadLen:      payloadLen,
			Reserved:        0,
		}
		patched, err := patchMeta(per[i].brot, meta)
		if err != nil {
			die("patching brot for %s: %v", slots[i].name, err)
		}
		per[i].brot = patched
	}

	// Generate Zone A Ã¢â‚¬â€ done twice; offsets depend on Zone A length itself.
	zoneA := buildZoneA(per[0].brot, per[1].brot, 0, 0, 0, 0)
	off0 := len(zoneA)
	len0 := len(per[0].brot)
	off1 := off0 + len0
	len1 := len(per[1].brot)
	zoneA = buildZoneA(per[0].brot, per[1].brot, off0, len0, off1, len1)

	// Verify zone A length stable; if template produces variable-length numbers
	// the offsets may shift. Regenerate until stable.
	for retries := 0; retries < 4; retries++ {
		newOff0 := len(zoneA)
		if newOff0 == off0 {
			break
		}
		off0 = newOff0
		off1 = off0 + len0
		zoneA = buildZoneA(per[0].brot, per[1].brot, off0, len0, off1, len1)
	}

	out, err := os.Create(*output)
	must(err)
	defer out.Close()
	_, _ = out.WriteString(zoneA)
	_, _ = out.Write(per[0].brot)
	_, _ = out.Write(per[1].brot)
	_, _ = out.Write(compressed.Bytes())

	fmt.Printf("मोहब्बत  zone_a=%d (S_OFF.amd=%d) per0=%d per1=%d pool=%d\n", len(zoneA), off0, len0, len1, int(poolLen))
	fmt.Printf("मोहब्बत  Wrote %s (%d bytes)\n", *output, len(zoneA)+len0+len1+int(poolLen))
}

func resolveWorkspace(ws string) (string, error) {
	if ws != "" {
		return filepath.Abs(ws)
	}
	exe, err := os.Executable()
	if err == nil {
		// Walk up looking for sysroot.toml as the workspace root marker
		dir := filepath.Dir(exe)
		for i := 0; i < 6; i++ {
			if _, err := os.Stat(filepath.Join(dir, "sysroot.toml")); err == nil {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// Fallback: cwd
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Walk up from cwd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(cwd, "sysroot.toml")); err == nil {
			return cwd, nil
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			break
		}
		cwd = parent
	}
	return "", fmt.Errorf("could not locate workspace root (sysroot.toml not found)")
}

func cargoBuild(ws, pkgDir string, s slot, buildDir string) error {
	targetName := ""
	if s.goarch == "amd64" {
		targetName = "x86_64-rusticated-windows-msvc"
	} else if s.goarch == "arm64" {
		targetName = "aarch64-rusticated-windows-msvc"
	} else {
		return fmt.Errorf("unsupported arch %s in cargoBuild", s.goarch)
	}
	targetArg := filepath.Join(ws, "target", "rusticated-spec", targetName+".json")

	cmd := exec.Command("cargo", "build", "-vv", "--release", "--config", filepath.Join(ws, "target", "rusticated-spec", "config.toml"), "--target", targetArg)
	env := append(os.Environ(), "RUST_TARGET_PATH="+filepath.Join(ws, "target", "rusticated-spec"))
	cmd.Env = env
	cmd.Args = append(cmd.Args, "-Z", "unstable-options")

	cmd.Dir = filepath.Join(ws, pkgDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("मोहब्बत    cargo build %s for %s\n", pkgDir, s.name)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s cargo build failed for %s: %w", pkgDir, s.name, err)
	}

	// Copy the artifact to buildDir
	srcExe := filepath.Join(ws, "target", targetName, "release", "brot.exe")
	outPath := filepath.Join(buildDir, fmt.Sprintf("brot-%s.exe", s.name))
	bytes, err := os.ReadFile(srcExe)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, bytes, 0755)
}

func buildMohabbatBrain(ws string) error {
	target := "wasm32-rusticated-unknown-unknown"
	cmd := exec.Command("cargo", "build", "-vv", "-p", "mohabbat", "--release", "--config", filepath.Join(ws, "target", "rusticated-spec", "config.toml"), "--target", target)
	env := append(os.Environ(), "RUST_TARGET_PATH="+filepath.Join(ws, "target", "rusticated-spec"))

	cmd.Env = env
	cmd.Args = append(cmd.Args, "-Z", "unstable-options")
	cmd.Dir = ws
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mohabbat wasm build failed: %w", err)
	}
	return nil
}

func goBuild(ws, pkgDir string, s slot, buildDir string) error {
	outPath := filepath.Join(buildDir, fmt.Sprintf("%s-%s.exe", pkgDir, s.name))
	if err := os.Remove(outPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale output %s: %w", outPath, err)
	}
	goTmpDir := filepath.Join(buildDir, pkgDir, "gotmp", s.name)
	goCacheDir := filepath.Join(buildDir, pkgDir, "gocache", s.name)
	if err := os.MkdirAll(goTmpDir, 0o755); err != nil {
		return fmt.Errorf("create GOTMPDIR %s: %w", goTmpDir, err)
	}
	if err := os.MkdirAll(goCacheDir, 0o755); err != nil {
		return fmt.Errorf("create GOCACHE %s: %w", goCacheDir, err)
	}
	cmd := exec.Command("go", "build", "-trimpath", "-o", outPath, ".")
	cmd.Dir = filepath.Join(ws, pkgDir)
	env := os.Environ()
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOOS", s.goos)
	env = upsertEnv(env, "GOARCH", s.goarch)
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	env = upsertEnv(env, "TMP", goTmpDir)
	env = upsertEnv(env, "TEMP", goTmpDir)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("मोहब्बत    go build %s for %s -> %s\n", pkgDir, s.name, filepath.Base(outPath))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build failed for %s: %w", pkgDir, s.name, err)
	}
	return nil
}

func brotPath(buildDir string, s slot) string {
	return filepath.Join(buildDir, fmt.Sprintf("brot-%s.exe", s.name))
}

func washmhostPath(buildDir string, s slot) string {
	return filepath.Join(buildDir, fmt.Sprintf("washmhost-go-%s.exe", s.name))
}

func patchMeta(brot []byte, meta mohabbatMeta) ([]byte, error) {
	// The unpatched MohabbatMeta has Magic = "MOHABBAT" followed by 8 zero
	// bytes for PoolLen (the first u64 field). Searching for the 16-byte
	// signature avoids collisions with the "MOHABBAT_VEGETABLE_PATH" string
	// literal that brot embeds for reading the parent vegetable path.
	signature := append([]byte(mohabbatMagic), make([]byte, 8)...)
	count := bytes.Count(brot, signature)
	if count == 0 {
		return nil, fmt.Errorf("MOHABBAT signature (magic + NUL PoolLen) not found in brot")
	}
	if count > 1 {
		return nil, fmt.Errorf("MOHABBAT signature found %d times (expected exactly 1)", count)
	}

	idx := bytes.Index(brot, signature)
	out := make([]byte, len(brot))
	copy(out, brot)
	p := idx + len(mohabbatMagic)
	binary.LittleEndian.PutUint64(out[p+0:p+8], meta.PoolLen)
	binary.LittleEndian.PutUint64(out[p+8:p+16], meta.WashmhostOffset)
	binary.LittleEndian.PutUint64(out[p+16:p+24], meta.WashmhostLen)
	binary.LittleEndian.PutUint64(out[p+24:p+32], meta.PayloadOffset)
	binary.LittleEndian.PutUint64(out[p+32:p+40], meta.PayloadLen)
	binary.LittleEndian.PutUint64(out[p+40:p+48], meta.Reserved)
	return out, nil
}

// buildZoneA produces the polyglot script header for Modern Two (Windows-only).
// The sh prelude rejects POSIX; the cmd portion does arch detection and
// PowerShell-based byte-perfect extraction of the matching brot.
func buildZoneA(_, _ []byte, amdOff, amdLen, armOff, armLen int) string {
	const tmpl = `::; echo "[mohabbat] Modern Two preview: only Windows AMD64 and ARM64 are supported." >&2; exit 1
@echo off
setlocal enabledelayedexpansion
set "ME=%~f0"
set "TMP_EXE=%TEMP%\moh-!RANDOM!.exe"
set "ARCH=%PROCESSOR_ARCHITECTURE%"
if "!PROCESSOR_ARCHITEW6432!" neq "" set "ARCH=!PROCESSOR_ARCHITEW6432!"
set "S_OFF=0"
set "S_LEN=0"
if "!ARCH!"=="AMD64" (
    set "S_OFF={{AMD_OFF}}"
    set "S_LEN={{AMD_LEN}}"
) else if "!ARCH!"=="ARM64" (
    set "S_OFF={{ARM_OFF}}"
    set "S_LEN={{ARM_LEN}}"
)
if "!S_LEN!"=="0" (
    echo [mohabbat] This vegetable does not support !ARCH! on Windows.
    exit /b 1
)
powershell -NoProfile -ExecutionPolicy Bypass -Command "$a=[IO.File]::ReadAllBytes($env:ME); $b=New-Object byte[] !S_LEN!; [Array]::Copy($a, [int64]!S_OFF!, $b, 0, [int]!S_LEN!); [IO.File]::WriteAllBytes($env:TMP_EXE, $b)"
set "MOHABBAT_VEGETABLE_PATH=!ME!"
"!TMP_EXE!" %*
set "RET=!ERRORLEVEL!"
del "!TMP_EXE!"
exit /b !RET!
`
	// Force CRLF on cmd portion. The first line is the sh prelude with LF only
	// (sh tolerates CRLF too on most setups, but we keep it LF for portability).
	// Simplest: convert all to CRLF, then fix the first line if needed.
	s := strings.ReplaceAll(tmpl, "\n", "\r\n")
	s = strings.ReplaceAll(s, "{{AMD_OFF}}", fmt.Sprintf("%d", amdOff))
	s = strings.ReplaceAll(s, "{{AMD_LEN}}", fmt.Sprintf("%d", amdLen))
	s = strings.ReplaceAll(s, "{{ARM_OFF}}", fmt.Sprintf("%d", armOff))
	s = strings.ReplaceAll(s, "{{ARM_LEN}}", fmt.Sprintf("%d", armLen))
	return s
}

func must(err error) {
	if err != nil {
		die("%v", err)
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "मोहब्बत  error: "+format+"\n", a...)
	os.Exit(1)
}
