package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestZstandardRoundTrip(t *testing.T) {
	original := bytes.Repeat([]byte("Mohabbat-Next movable binary payload test "), 64)
	compressed, err := compressZstandard(original)
	if err != nil {
		t.Fatalf("compressZstandard failed: %v", err)
	}
	if len(compressed) == 0 || len(compressed) >= len(original) {
		t.Fatalf("expected compressed size < %d, got %d", len(original), len(compressed))
	}
}

func TestBuildSharedPoolRequiresBrainWasm(t *testing.T) {
	if _, err := buildSharedPool(AssemblyAssets{}); err == nil {
		t.Fatal("expected shared pool construction to require an application brain")
	}
}

func TestValidatePE(t *testing.T) {
	// Valid minimal MZ + PE\0\0 header at offset 0x80
	valid := make([]byte, 256)
	valid[0] = 'M'
	valid[1] = 'Z'
	binary.LittleEndian.PutUint32(valid[0x3c:0x40], 0x80)
	copy(valid[0x80:], []byte{'P', 'E', 0, 0})

	peOff, err := validatePE(valid)
	if err != nil {
		t.Fatalf("validatePE failed on valid header: %v", err)
	}
	if peOff != 0x80 {
		t.Fatalf("expected peOffset 0x80, got 0x%x", peOff)
	}

	// Reject corrupted MZ magic
	badMZ := append([]byte(nil), valid...)
	badMZ[0] = 'X'
	if _, err := validatePE(badMZ); err == nil {
		t.Fatalf("expected validatePE to reject invalid MZ signature")
	}

	// Reject corrupted PE signature
	badPE := append([]byte(nil), valid...)
	badPE[0x80] = 'X'
	if _, err := validatePE(badPE); err == nil {
		t.Fatalf("expected validatePE to reject invalid PE signature")
	}
}

func TestDosStubAndBootstrapTemplates(t *testing.T) {
	nextDir, err := findMohabbatNextDir()
	if err != nil {
		t.Fatalf("findMohabbatNextDir: %v", err)
	}
	templatesDir := filepath.Join(nextDir, "build", "templates")

	dosPath := filepath.Join(templatesDir, "dos_stub.txt.in")
	dosTmpl, err := template.ParseFiles(dosPath)
	if err != nil {
		t.Fatalf("parse dos_stub.txt.in: %v", err)
	}

	type DosData struct {
		BootstrapOffset int64
	}
	var buf bytes.Buffer
	if err := dosTmpl.Execute(&buf, DosData{BootstrapOffset: 1075420}); err != nil {
		t.Fatalf("execute dos_stub.txt.in: %v", err)
	}
	rendered := bytes.ReplaceAll(buf.Bytes(), []byte("\r\n"), []byte("\n"))
	if len(rendered) > 59 {
		t.Fatalf("rendered DOS stub exceeds 59-byte e_lfanew limit (%d bytes): %q", len(rendered), string(rendered))
	}
	if bytes.IndexByte(rendered, '\r') != -1 {
		t.Fatalf("rendered DOS stub contains CR")
	}
}

func TestAssembleVegetableBashHandoff(t *testing.T) {
	nextDir, err := findMohabbatNextDir()
	if err != nil {
		t.Fatalf("findMohabbatNextDir: %v", err)
	}

	workDir := t.TempDir()
	hostExe := filepath.Join(workDir, "host.exe")
	host := make([]byte, 256)
	host[0], host[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(host[0x3c:0x40], 0x80)
	copy(host[0x80:], []byte{'P', 'E', 0, 0})
	if err := os.WriteFile(hostExe, host, 0o644); err != nil {
		t.Fatalf("write synthetic host PE: %v", err)
	}

	assetsDir := filepath.Join(workDir, "assets")
	if err := os.Mkdir(assetsDir, 0o755); err != nil {
		t.Fatalf("create assets directory: %v", err)
	}
	writeAsset := func(name string) string {
		t.Helper()
		path := filepath.Join(assetsDir, name)
		if err := os.WriteFile(path, []byte("fixture-"+name), 0o644); err != nil {
			t.Fatalf("write fixture asset %s: %v", name, err)
		}
		return path
	}

	assets := AssemblyAssets{
		BrotLinuxX86_64:        writeAsset("brot-linux-x86_64"),
		BrotLinuxAarch64:       writeAsset("brot-linux-aarch64"),
		BrotAndroidAarch64:     writeAsset("brot-android-aarch64"),
		BrotDarwinX86_64:       writeAsset("brot-darwin-x86_64"),
		BrotDarwinAarch64:      writeAsset("brot-darwin-aarch64"),
		PlatformWindowsX86_64:  writeAsset("platform-windows-x86_64"),
		PlatformWindowsAarch64: writeAsset("platform-windows-aarch64"),
		PlatformLinuxX86_64:    writeAsset("platform-linux-x86_64"),
		PlatformLinuxAarch64:   writeAsset("platform-linux-aarch64"),
		PlatformDarwinX86_64:   writeAsset("platform-darwin-x86_64"),
		PlatformDarwinAarch64:  writeAsset("platform-darwin-aarch64"),
		RuntimeX86_64:          writeAsset("runtime-x86_64"),
		RuntimeAarch64:         writeAsset("runtime-aarch64"),
		BrainWasm:              writeAsset("brain.wasm"),
	}

	templatesDir := filepath.Join(workDir, "templates")
	if err := os.Mkdir(templatesDir, 0o755); err != nil {
		t.Fatalf("create templates directory: %v", err)
	}
	dosStub, err := os.ReadFile(filepath.Join(nextDir, "build", "templates", "dos_stub.txt.in"))
	if err != nil {
		t.Fatalf("read DOS stub template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templatesDir, "dos_stub.txt.in"), dosStub, 0o644); err != nil {
		t.Fatalf("write DOS stub template: %v", err)
	}
	bootstrap := "[ -f \"$0\" ] || exit 21\n" +
		"[ \"$1\" = \"polyglot-argument\" ] || exit 22\n" +
		"printf 'synthetic-bootstrap-ok\\n'\n" +
		"exit 23\n"
	if err := os.WriteFile(filepath.Join(templatesDir, "bootstrap.sh.in"), []byte(bootstrap), 0o644); err != nil {
		t.Fatalf("write synthetic bootstrap: %v", err)
	}

	bundle := filepath.Join(workDir, "synthetic.exe")
	if err := assembleVegetable(hostExe, assets, templatesDir, bundle); err != nil {
		t.Fatalf("assemble synthetic vegetable: %v", err)
	}
	assembled, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatalf("read assembled synthetic vegetable: %v", err)
	}
	peOffset, err := validatePE(assembled)
	if err != nil {
		t.Fatalf("validate assembled PE: %v", err)
	}
	if peOffset != 0x80 {
		t.Fatalf("expected PE offset 0x80, got %#x", peOffset)
	}
	if !bytes.HasPrefix(assembled, []byte("MZ=1;eval \"$(tail -c +")) {
		t.Fatalf("assembled DOS stub does not contain the shell trampoline: %q", assembled[:59])
	}

	var command string
	var args []string
	if runtime.GOOS == "windows" {
		wsl, err := exec.LookPath("wsl.exe")
		if err != nil {
			t.Skipf("Bash handoff execution requires WSL on Windows: %v", err)
		}
		volume := filepath.VolumeName(bundle)
		if volume == "" {
			t.Fatalf("synthetic bundle has no Windows drive: %s", bundle)
		}
		wslBundle := "/mnt/" + strings.ToLower(strings.TrimSuffix(volume, ":")) +
			filepath.ToSlash(strings.TrimPrefix(bundle, volume))
		command = wsl
		args = []string{"-e", "bash", wslBundle, "polyglot-argument"}
	} else {
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Skipf("Bash handoff execution requires Bash: %v", err)
		}
		command = bash
		args = []string{bundle, "polyglot-argument"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("synthetic Bash handoff timed out: %v; output: %s", ctx.Err(), output)
	}
	if err == nil {
		t.Fatalf("synthetic Bash handoff returned success; expected exit 23; output: %s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run synthetic Bash handoff: %v; output: %s", err, output)
	}
	if exitErr.ExitCode() != 23 {
		t.Fatalf("synthetic Bash handoff exited %d, expected 23; output: %s", exitErr.ExitCode(), output)
	}
	if !bytes.Contains(output, []byte("synthetic-bootstrap-ok\n")) {
		t.Fatalf("synthetic bootstrap output mismatch: %q", output)
	}
}

func TestBuildRunnerSharedPoolWithoutBrainWasm(t *testing.T) {
	workDir := t.TempDir()
	writeAsset := func(name string) string {
		t.Helper()
		p := filepath.Join(workDir, name)
		if err := os.WriteFile(p, []byte("payload-"+name), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
		return p
	}

	assets := AssemblyAssets{
		PlatformWindowsX86_64:  writeAsset("platform-windows-x86_64"),
		PlatformWindowsAarch64: writeAsset("platform-windows-aarch64"),
		RuntimeX86_64:          writeAsset("runtime-x86_64"),
		RuntimeAarch64:         writeAsset("runtime-aarch64"),
	}

	compressed, err := buildRunnerSharedPool(assets)
	if err != nil {
		t.Fatalf("buildRunnerSharedPool failed: %v", err)
	}

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatalf("create zstd reader: %v", err)
	}
	defer dec.Close()

	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatalf("decompress runner pool: %v", err)
	}
	if len(raw) < 16 || string(raw[:8]) != "MHBTPOOL" {
		t.Fatalf("invalid MHBTPOOL header: %q", raw[:min(len(raw), 16)])
	}
	if version := binary.LittleEndian.Uint32(raw[8:12]); version != 1 {
		t.Fatalf("expected pool version 1, got %d", version)
	}
	count := binary.LittleEndian.Uint32(raw[12:16])
	if count != 4 {
		t.Fatalf("expected 4 entries in runner pool, got %d", count)
	}

	got := make(map[string]string, count)
	for i := uint32(0); i < count; i++ {
		tocOff := 16 + int(i)*40
		name := strings.TrimRight(string(raw[tocOff:tocOff+32]), "\x00")
		off := binary.LittleEndian.Uint32(raw[tocOff+32 : tocOff+36])
		length := binary.LittleEndian.Uint32(raw[tocOff+36 : tocOff+40])
		got[name] = string(raw[off : off+length])
	}

	for _, name := range []string{"platform-windows-x86_64", "platform-windows-aarch64", "runtime-x86_64", "runtime-aarch64"} {
		if got[name] != "payload-"+name {
			t.Fatalf("entry %s = %q, want %q", name, got[name], "payload-"+name)
		}
	}
	if _, exists := got["brain.wasm"]; exists {
		t.Fatal("runner pool must not contain brain.wasm")
	}

	if _, err := buildRunnerSharedPool(AssemblyAssets{}); err == nil {
		t.Fatal("expected buildRunnerSharedPool to fail when required assets are missing")
	}
}

func TestAssembleRunner(t *testing.T) {
	workDir := t.TempDir()
	runnerExe := filepath.Join(workDir, "runner-base.exe")
	host := make([]byte, 256)
	host[0], host[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(host[0x3c:0x40], 0x80)
	copy(host[0x80:], []byte{'P', 'E', 0, 0})
	if err := os.WriteFile(runnerExe, host, 0o644); err != nil {
		t.Fatalf("write synthetic runner PE: %v", err)
	}

	writeAsset := func(name string) string {
		t.Helper()
		p := filepath.Join(workDir, name)
		if err := os.WriteFile(p, []byte("runner-"+name), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
		return p
	}

	assets := AssemblyAssets{
		PlatformWindowsX86_64:  writeAsset("platform-windows-x86_64"),
		PlatformWindowsAarch64: writeAsset("platform-windows-aarch64"),
		RuntimeX86_64:          writeAsset("runtime-x86_64"),
		RuntimeAarch64:         writeAsset("runtime-aarch64"),
	}

	outExe := filepath.Join(workDir, "mohabbat-runner.exe")
	if err := assembleRunner(runnerExe, assets, outExe); err != nil {
		t.Fatalf("assembleRunner failed: %v", err)
	}

	assembled, err := os.ReadFile(outExe)
	if err != nil {
		t.Fatalf("read assembled runner: %v", err)
	}
	if _, err := validatePE(assembled); err != nil {
		t.Fatalf("validate assembled runner PE: %v", err)
	}
	if len(assembled) < len(host)+24 {
		t.Fatalf("assembled runner too small: %d", len(assembled))
	}

	trailer := assembled[len(assembled)-24:]
	if string(trailer[16:24]) != "MHBTTAIL" {
		t.Fatalf("missing MHBTTAIL trailer: %q", trailer[16:24])
	}
	poolOffset := int(binary.LittleEndian.Uint64(trailer[0:8]))
	poolLength := int(binary.LittleEndian.Uint64(trailer[8:16]))
	if poolOffset != len(host) || poolOffset+poolLength != len(assembled)-24 {
		t.Fatalf("unexpected pool bounds: offset=%d len=%d total=%d", poolOffset, poolLength, len(assembled))
	}

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatalf("create zstd reader: %v", err)
	}
	defer dec.Close()

	rawPool, err := dec.DecodeAll(assembled[poolOffset:poolOffset+poolLength], nil)
	if err != nil {
		t.Fatalf("decompress appended runner pool: %v", err)
	}
	if string(rawPool[:8]) != "MHBTPOOL" {
		t.Fatalf("invalid appended pool magic: %q", rawPool[:8])
	}
}
