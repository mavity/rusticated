package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/andybalholm/brotli"
)

// Modern Four scope: linux/amd64, linux/arm64, windows/amd64, windows/arm64.
// Slot order is contractual - Zone A and patcher both depend on it.
var slots = []slot{
	{name: "linux-amd64", goos: "linux", goarch: "amd64", shCase: "x86_64-Linux"},
	{name: "linux-arm64", goos: "linux", goarch: "arm64", shCase: "aarch64-Linux"},
	{name: "win-amd64", goos: "windows", goarch: "amd64", winArch: "AMD64"},
	{name: "win-arm64", goos: "windows", goarch: "arm64", winArch: "ARM64"},
}

type slot struct {
	name    string
	goos    string
	goarch  string
	shCase  string // matches "$(uname -m)-$(uname -s)"
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

func formatSize(n int64) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	l := len(s)
	for i, c := range s {
		out = append(out, byte(c))
		if (l-i-1)%3 == 0 && i != l-1 {
			out = append(out, ',')
		}
	}
	return string(out)
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

	selectedTargets := map[string]string{}
	if !*skipBuild {
		fmt.Println("🍆  Building brot (cargo) and washmhost-go for Modern Four...")
		for _, s := range slots {
			if !shouldBuildSlot(s) {
				fmt.Printf("🍆    skip %s on host %s\n", s.name, runtime.GOOS)
				continue
			}
			targetName, err := cargoBuild(ws, "brot", s, buildDir)
			must(err)
			selectedTargets[s.name] = targetName
			must(goBuild(ws, "washmhost-go", s, buildDir))
		}
		if defaultPayload {
			fmt.Println("🍆  Building default brain payload (mohabbat wasm)...")
			must(buildMohabbatBrain(ws))
		}
	}

	fmt.Printf("🍆  Reading payload: %s\n", *payload)
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
		if !shouldBuildSlot(s) {
			fmt.Printf("🍆    %s: disabled for this host\n", s.name)
			continue
		}
		brot, err := os.ReadFile(brotPath(buildDir, s))
		must(err)
		wh, err := os.ReadFile(washmhostPath(buildDir, s))
		must(err)
		per[i] = artifacts{brot: brot, washmhost: wh}
		displayName := s.name
		if targetName, ok := selectedTargets[s.name]; ok {
			displayName = targetName
		}
		fmt.Printf("🍆    %s: brot=%s washmhost=%s\n", displayName, formatSize(int64(len(brot))), formatSize(int64(len(wh))))
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

	// Brotli compress with maximum compression settings.
	compressed := &bytes.Buffer{}
	w := brotli.NewWriterOptions(compressed, brotli.WriterOptions{
		Quality: 11,
		LGWin:   24,
	})
	if _, err := w.Write(pool.Bytes()); err != nil {
		die("brotli write: %v", err)
	}
	must(w.Close())
	poolLen := uint64(compressed.Len())
	fmt.Printf("🍆  Pool: raw=%s compressed=%s\n", formatSize(int64(pool.Len())), formatSize(int64(poolLen)))

	// Patch MohabbatMeta inside each brot
	for i := range slots {
		if len(per[i].brot) == 0 {
			continue
		}
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

	// Compute Zone A/Zone B offsets as a fixed-point because Zone A length
	// depends on numeric offsets embedded in it.
	lengths := make([]int, len(slots))
	offsets := make([]int, len(slots))
	for i := range slots {
		lengths[i] = len(per[i].brot)
	}

	zoneA := ""
	for retries := 0; retries < 8; retries++ {
		zoneA = buildZoneA(offsets, lengths)
		next := len(zoneA)
		newOffsets := make([]int, len(slots))
		for i := range slots {
			newOffsets[i] = next
			next += lengths[i]
		}

		stable := true
		for i := range slots {
			if newOffsets[i] != offsets[i] {
				stable = false
				break
			}
		}
		offsets = newOffsets
		if stable {
			zoneA = buildZoneA(offsets, lengths)
			break
		}
	}

	out, err := os.Create(*output)
	must(err)
	defer out.Close()
	_, _ = out.WriteString(zoneA)
	for i := range slots {
		_, _ = out.Write(per[i].brot)
	}
	_, _ = out.Write(compressed.Bytes())
	if runtime.GOOS != "windows" {
		if err := os.Chmod(*output, 0o755); err != nil {
			die("chmod output %s: %v", *output, err)
		}
	}

	totalZoneB := 0
	for _, n := range lengths {
		totalZoneB += n
	}
	fmt.Printf("🍆  zone_a=%s zone_b=%s pool=%s\n", formatSize(int64(len(zoneA))), formatSize(int64(totalZoneB)), formatSize(int64(poolLen)))
	fmt.Printf("🍆  Wrote %s (%s bytes)\n", *output, formatSize(int64(len(zoneA)+totalZoneB+int(poolLen))))
	if err := ensureBatOnPath("mohab.bat", *output); err != nil {
		fmt.Printf("🍆  warn: %v\n", err)
	}
	if err := ensureBatOnPath("demo.bat", filepath.Join(ws, "demo.bat")); err != nil {
		fmt.Printf("🍆  warn: %v\n", err)
	}
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

func cargoBuild(ws, pkgDir string, s slot, buildDir string) (string, error) {
	targetName, err := cargoTargetName(s)
	if err != nil {
		return "", err
	}
	if err := ensureRustTargetInstalled(targetName); err != nil {
		return "", err
	}

	isRusticatedTarget := strings.Contains(targetName, "rusticated")

	buildTarget := func(name string) error {
		targetArg := name
		args := []string{"build", "--release"}
		if isRusticatedTarget {
			targetPath := filepath.Join(ws, "target", "rusticated-spec", name+".json")
			evalPath, err := filepath.EvalSymlinks(targetPath)
			if err == nil {
				targetPath = evalPath
			}
			targetArg = strings.ReplaceAll(strings.TrimPrefix(targetPath, `\\?\`), `\`, `/`)
			args = append(args, "--config", filepath.Join(ws, "target", "rusticated-spec", "config.toml"))
			args = append(args, "--config", "unstable.json-target-spec=true")
		}
		args = append(args, "--target", targetArg)
		if s.goos == "linux" && !isRusticatedTarget {
			args = append(args, "--config", fmt.Sprintf("target.%s.rustflags=['-C', 'link-self-contained=no', '-C', 'linker=rust-lld', '-C', 'linker-flavor=ld.lld']", name))
		}
		if s.goos == "windows" && (strings.Contains(name, "windows-gnu") || strings.Contains(name, "windows-gnullvm")) {
			// brot is no_std/no_main and uses raw-dylib exclusively; we block
			// the default libraries (MinGW runtime) that rustc injects by default,
			// as they are often missing on ARM64 hosts. We provide a standalone
			// entry point in the source to avoid any dependency on crt2.o.
			args = append(args, "--config", fmt.Sprintf("target.%s.rustflags=['-C', 'link-arg=-nostdlib', '-C', 'link-arg=-nodefaultlibs']", name))
		}
		cmd := exec.Command("cargo", args...)
		env := os.Environ()
		cmd.Env = env
		if isRusticatedTarget {
			cmd.Args = append(cmd.Args, "-Z", "unstable-options")
		}

		cmd.Dir = filepath.Join(ws, pkgDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Printf("🍆    cargo build %s for %s\n", pkgDir, s.name)
		return cmd.Run()
	}

	err = buildTarget(targetName)
	/*
		if err != nil && isRusticatedTarget && (strings.Contains(targetName, "windows-gnullvm") || strings.Contains(targetName, "windows-gnu")) {
			fallbackTarget := strings.Replace(targetName, "windows-gnullvm", "windows-msvc", 1)
			if fallbackTarget == targetName {
				fallbackTarget = strings.Replace(targetName, "windows-gnu", "windows-msvc", 1)
			}
			if fallbackTarget != targetName {
				fmt.Printf("🍆    fallback build target from %s to %s\n", targetName, fallbackTarget)
				if err := ensureRustTargetInstalled(fallbackTarget); err != nil {
					return "", err
				}
				err = buildTarget(fallbackTarget)
				targetName = fallbackTarget
			}
		}
	*/
	if err != nil {
		return "", fmt.Errorf("%s cargo build failed for %s: %w", pkgDir, s.name, err)
	}

	// Copy the artifact to buildDir
	srcExt := ""
	if s.goos == "windows" {
		srcExt = ".exe"
	}
	srcPath := filepath.Join(ws, "target", targetName, "release", "brot"+srcExt)
	outPath := brotPath(buildDir, s)
	bytes, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(outPath, bytes, 0755); err != nil {
		return "", err
	}
	return targetName, nil
}

func buildMohabbatBrain(ws string) error {
	target := "wasm32-rusticated-unknown-unknown"
	cmd := exec.Command("cargo", "build", "-p", "mohabbat", "--release", "--config", filepath.Join(ws, "target", "rusticated-spec", "config.toml"), "--config", "unstable.json-target-spec=true", "--target", target)

	cmd.Env = os.Environ()
	cmd.Env = upsertEnv(cmd.Env, "RUST_TARGET_PATH", filepath.Join(ws, "target", "rusticated-spec"))
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
	outPath := washmhostPath(buildDir, s)
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

	// Option B: Use a temporary .dat file for the build instead of the default
	// a.out.exe to avoid aggressive Windows Defender scanning.
	// Note: go build -o - is avoided here because on some Windows environments
	// it incorrectly creates a literal file named "-" instead of streaming.
	tmpOut := filepath.Join(goTmpDir, "build.dat")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", tmpOut, ".")
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

	fmt.Printf("🍆    go build %s for %s -> %s\n", pkgDir, s.name, filepath.Base(outPath))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build failed for %s: %w", pkgDir, s.name, err)
	}

	buildResult, err := os.ReadFile(tmpOut)
	if err != nil {
		return fmt.Errorf("read build result %s: %w", tmpOut, err)
	}

	if len(buildResult) == 0 {
		return fmt.Errorf("%s build for %s produced 0 bytes", pkgDir, s.name)
	}

	if err := os.WriteFile(outPath, buildResult, 0755); err != nil {
		return fmt.Errorf("write %s to %s: %w", pkgDir, outPath, err)
	}
	return nil
}

func brotPath(buildDir string, s slot) string {
	return filepath.Join(buildDir, fmt.Sprintf("brot-%s%s", s.name, artifactExt(s.goos)))
}

func washmhostPath(buildDir string, s slot) string {
	return filepath.Join(buildDir, fmt.Sprintf("washmhost-go-%s%s", s.name, artifactExt(s.goos)))
}

func artifactExt(goos string) string {
	if goos == "windows" {
		// Use .dat instead of .exe for the stored artifact to bypass
		// aggressive Windows Defender real-time scanning during the build process.
		return ".dat"
	}
	return ""
}

func shouldBuildSlot(s slot) bool {
	// Build all supported slots on any host. Windows targets are cross-compiled
	// from non-Windows hosts using rusticated target specs and Go cross-build.
	return true
}

func ensureBatOnPath(commandName, targetPath string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	absoluteTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolve path for %s: %w", commandName, err)
	}
	pathDirs := filepath.SplitList(os.Getenv("PATH"))
	if len(pathDirs) == 0 {
		return nil
	}

	for _, dir := range pathDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		linkPath := filepath.Join(dir, commandName)
		_ = os.Remove(linkPath)
		wrapper := "#!/usr/bin/env bash\n" +
			"exec bash \"" + absoluteTarget + "\" \"$@\"\n"
		if err := os.WriteFile(linkPath, []byte(wrapper), 0o755); err == nil {
			fmt.Printf("🍆  PATH shim: %s -> %s\n", linkPath, absoluteTarget)
			return nil
		}
	}
	return fmt.Errorf("could not place %s in any PATH directory; use ./%s", commandName, commandName)
}

func rustcHostTriple() (string, error) {
	cmd := exec.Command("rustc", "-vV")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed running rustc -vV: %w", err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "host: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "host: ")), nil
		}
	}
	return "", fmt.Errorf("rustc -vV did not report host triple")
}

func rustcTargetSpecAvailable(target string) bool {
	cmd := exec.Command("rustc", "-Z", "unstable-options", "--print", "target-spec-json", "--target", target)
	return cmd.Run() == nil
}

func cargoTargetName(s slot) (string, error) {
	targetArch := "x86_64"
	if s.goarch == "arm64" {
		targetArch = "aarch64"
	}

	switch {
	case s.goos == "linux":
		return fmt.Sprintf("%s-unknown-linux-musl", targetArch), nil
	case s.goos == "windows":
		return fmt.Sprintf("%s-pc-windows-gnullvm", targetArch), nil
	case s.goos == "darwin":
		return fmt.Sprintf("%s-apple-darwin", targetArch), nil
	default:
		return "", fmt.Errorf("unsupported slot target %s/%s", s.goos, s.goarch)
	}
}

func ensureRustTargetInstalled(targetName string) error {
	if strings.Contains(targetName, "rusticated") {
		return nil
	}

	check := exec.Command("rustup", "target", "list", "--installed")
	var out bytes.Buffer
	check.Stdout = &out
	check.Stderr = os.Stderr
	if err := check.Run(); err != nil {
		return fmt.Errorf("failed checking installed rust targets: %w", err)
	}
	installed := out.String()
	if strings.Contains(installed, targetName+"\n") || strings.HasSuffix(installed, targetName) {
		return nil
	}

	fmt.Printf("🍆    rustup target add %s\n", targetName)
	addArgs := []string{"target", "add", targetName}
	if tc := strings.TrimSpace(os.Getenv("RUSTUP_TOOLCHAIN")); tc != "" {
		addArgs = append(addArgs, "--toolchain", tc)
	}
	add := exec.Command("rustup", addArgs...)
	add.Stdout = os.Stdout
	add.Stderr = os.Stderr
	if err := add.Run(); err != nil {
		return fmt.Errorf("failed to install rust target %s: %w", targetName, err)
	}
	return nil
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

// buildZoneA produces the polyglot script header for Modern Four.
func buildZoneA(offsets, lengths []int) string {
	const tmpl = ":; ME=\"$(readlink -f \"$0\" 2>/dev/null || realpath \"$0\" 2>/dev/null || printf \"%s\" \"$0\")\"; S_OFF=0; S_LEN=0; case \"$(uname -m)-$(uname -s)\" in x86_64-Linux) S_OFF={{LINUX_AMD_OFF}}; S_LEN={{LINUX_AMD_LEN}} ;; aarch64-Linux) S_OFF={{LINUX_ARM_OFF}}; S_LEN={{LINUX_ARM_LEN}} ;; esac; [ \"$S_LEN\" = \"0\" ] && { echo \"[mohabbat] Unsupported arch/os\"; exit 1; }; TMP_EXE=\"/tmp/moh-$$-$(date +%s)\"; dd if=\"$ME\" bs=1 skip=\"$S_OFF\" count=\"$S_LEN\" of=\"$TMP_EXE\" 2>/dev/null; chmod +x \"$TMP_EXE\"; \"$TMP_EXE\" \"$ME\" \"$@\"; RET=$?; rm \"$TMP_EXE\"; exit $RET\n" +
		"@echo off\r\n" +
		"setlocal enabledelayedexpansion\r\n" +
		"set \"ME=%~f0\"\r\n" +
		"set \"TMP_EXE=%TEMP%\\moh-!RANDOM!.exe\"\r\n" +
		"set \"ARCH=%PROCESSOR_ARCHITECTURE%\"\r\n" +
		"if \"!PROCESSOR_ARCHITEW6432!\" neq \"\" set \"ARCH=!PROCESSOR_ARCHITEW6432!\"\r\n" +
		"set \"S_OFF=0\"\r\n" +
		"set \"S_LEN=0\"\r\n" +
		"if \"!ARCH!\"==\"AMD64\" (\r\n" +
		"	set \"S_OFF={{WIN_AMD_OFF}}\"\r\n" +
		"	set \"S_LEN={{WIN_AMD_LEN}}\"\r\n" +
		") else if \"!ARCH!\"==\"ARM64\" (\r\n" +
		"	set \"S_OFF={{WIN_ARM_OFF}}\"\r\n" +
		"	set \"S_LEN={{WIN_ARM_LEN}}\"\r\n" +
		")\r\n" +
		"if \"!S_LEN!\"==\"0\" (\r\n" +
		"    echo 🍆 This vegetable does not support !ARCH! on Windows.\r\n" +
		"    exit /b 1\r\n" +
		")\r\n" +
		"powershell -NoProfile -ExecutionPolicy Bypass -Command \"$a=[IO.File]::ReadAllBytes($env:ME); $b=New-Object byte[] !S_LEN!; [Array]::Copy($a, [int64]!S_OFF!, $b, 0, [int]!S_LEN!); [IO.File]::WriteAllBytes($env:TMP_EXE, $b)\"\r\n" +
		"set \"MOHABBAT_VEGETABLE_PATH=!ME!\"\r\n" +
		"\"!TMP_EXE!\" %*\r\n" +
		"set \"RET=!ERRORLEVEL!\"\r\n" +
		"del \"!TMP_EXE!\"\r\n" +
		"exit /b !RET!\r\n"
	idx := map[string]int{}
	for i, s := range slots {
		idx[s.name] = i
	}
	linuxAMD := idx["linux-amd64"]
	linuxARM := idx["linux-arm64"]
	winAMD := idx["win-amd64"]
	winARM := idx["win-arm64"]

	s := tmpl
	s = strings.ReplaceAll(s, "{{LINUX_AMD_OFF}}", fmt.Sprintf("%d", offsets[linuxAMD]))
	s = strings.ReplaceAll(s, "{{LINUX_AMD_LEN}}", fmt.Sprintf("%d", lengths[linuxAMD]))
	s = strings.ReplaceAll(s, "{{LINUX_ARM_OFF}}", fmt.Sprintf("%d", offsets[linuxARM]))
	s = strings.ReplaceAll(s, "{{LINUX_ARM_LEN}}", fmt.Sprintf("%d", lengths[linuxARM]))
	s = strings.ReplaceAll(s, "{{WIN_AMD_OFF}}", fmt.Sprintf("%d", offsets[winAMD]))
	s = strings.ReplaceAll(s, "{{WIN_AMD_LEN}}", fmt.Sprintf("%d", lengths[winAMD]))
	s = strings.ReplaceAll(s, "{{WIN_ARM_OFF}}", fmt.Sprintf("%d", offsets[winARM]))
	s = strings.ReplaceAll(s, "{{WIN_ARM_LEN}}", fmt.Sprintf("%d", lengths[winARM]))
	return s
}

func must(err error) {
	if err != nil {
		die("%v", err)
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "🍆  error: "+format+"\n", a...)
	os.Exit(1)
}
