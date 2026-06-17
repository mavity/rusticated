package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
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

// prebuildFn is set by prebuild.go on native (!wasip1) builds via init().
// On WASM builds it remains nil; modeBuild falls back to subprocess invocation.
var prebuildFn func(ws string) error

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
	vegPath := os.Getenv("MOHABBAT_VEGETABLE_PATH")
	inVeg := vegPath != ""

	if inVeg {
		// We used to override shell temp vars here, but that caused permission issues in 'target'.
		// Now we rely on the host-provided /tmp mapping.
	}

	// Parse args manually: [project] [-o out] [-r [args...]]
	rawArgs := os.Args[1:]
	projectDir := ""
	outputPath := ""
	runMode := false
	var runArgs []string

	for i := 0; i < len(rawArgs); {
		// If in a vegetable, skip the vegetable path itself if it appears in args.
		arg := rawArgs[i]
		if inVeg && (arg == vegPath || (runtime.GOOS == "windows" && strings.EqualFold(arg, vegPath))) {
			i++
			continue
		}

		switch arg {
		case "-r":
			runMode = true
			runArgs = rawArgs[i+1:]
			i = len(rawArgs)
		case "-o":
			if i+1 < len(rawArgs) {
				outputPath = rawArgs[i+1]
				i += 2
			} else {
				die("missing argument after -o")
			}
		default:
			if projectDir == "" && !strings.HasPrefix(arg, "-") {
				projectDir = arg
			}
			i++
		}
	}

	ws, err := resolveWorkspace("")
	must(err)

	// Heuristic: if -r was used and projectDir remains empty, check if first runArg is a project.
	if projectDir == "" && runMode && len(runArgs) > 0 {
		if isProject(ws, runArgs[0]) {
			projectDir = runArgs[0]
			runArgs = runArgs[1:]
		}
	}

	switch {
	case runMode:
		// Mode 4: build project to WASM + run immediately under washmhost-go.
		// Defaults to current directory if no projectDir was specified.
		if projectDir == "" {
			projectDir = "."
		}
		must(modeDevRun(ws, projectDir, runArgs))
	case projectDir != "" && outputPath != "" && inVeg:
		// Mode 2: juice bottle refill (running as WASM brain inside a vegetable)
		must(doRefill(ws, projectDir, vegPath, outputPath))
	case projectDir != "" && outputPath != "":
		// Mode 3: native fresh assembly with arbitrary payload
		must(modePackage(ws, projectDir, outputPath))
	default:
		// Mode 1: full build pipeline
		must(modeBuild(ws))
	}
}

// modeBuild is Mode 1: full build pipeline.
// On native, runs prebuild (target specs + sysroot + overlay) via prebuildFn.
// Inside a WASM vegetable, falls back to subprocess if artifacts are missing.
func modeBuild(ws string) error {
	buildDir := filepath.Join(ws, "target", "mohabbat-go-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	fmt.Println("🍆  Running prebuild (target specs + sysroot + overlay)...")
	if err := runPrebuild(ws); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	fmt.Println("🍆  Building brot (cargo) and washmhost-go for Modern Four...")
	if err := buildAllSlots(ws, buildDir); err != nil {
		return err
	}

	brainPath := filepath.Join(buildDir, "brain.wasm")
	fmt.Println("🍆  Building brain WASM (mohabbat-go)...")
	if err := buildBrainWasm(ws, brainPath); err != nil {
		return fmt.Errorf("brain wasm build: %w", err)
	}

	outputPath := filepath.Join(ws, "mohab.bat")
	if err := assembleVegetable(ws, brainPath, buildDir, outputPath); err != nil {
		return err
	}

	if err := ensureBatOnPath("mohab.bat", outputPath); err != nil {
		fmt.Printf("🍆  warn: %v\n", err)
	}
	return nil
}

// modePackage is Mode 3: build a project's payload and assemble a fresh vegetable.
func modePackage(ws, projectDir, outputPath string) error {
	buildDir := filepath.Join(ws, "target", "mohabbat-go-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	projectName := filepath.Base(projectDir)
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	fmt.Printf("🍆  Packaging %s -> %s\n", projectDir, outputPath)
	if err := buildProjectToWasm(ws, projectDir, wasmPath); err != nil {
		return err
	}
	fmt.Println("🍆  Building brot (cargo) and washmhost-go for Modern Four...")
	if err := buildAllSlots(ws, buildDir); err != nil {
		return err
	}
	return assembleVegetable(ws, wasmPath, buildDir, outputPath)
}

// modeDevRun is Mode 4: build a project to WASM and run it under washmhost-go.
func modeDevRun(ws, projectDir string, extraArgs []string) error {
	projectName := filepath.Base(projectDir)
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	fmt.Printf("🍆  Dev-run: building %s\n", projectDir)
	if err := buildProjectToWasm(ws, projectDir, wasmPath); err != nil {
		return err
	}
	return runUnderWashmhost(ws, wasmPath, extraArgs)
}

// buildAllSlots builds brot (cargo) and washmhost-go for all Modern Four slots.
func buildAllSlots(ws, buildDir string) error {
	for _, s := range slots {
		if !shouldBuildSlot(s) {
			fmt.Printf("🍆    skip %s\n", s.name)
			continue
		}
		if _, err := cargoBuild(ws, "brot", s, buildDir); err != nil {
			return err
		}
		if err := goBuild(ws, "washmhost-go", s, buildDir); err != nil {
			return err
		}
	}
	return nil
}

// assembleVegetable reads built artifacts and packages them into a vegetable .bat file.
func assembleVegetable(ws, brainPath, buildDir, outputPath string) error {
	fmt.Printf("🍆  Reading brain payload: %s\n", brainPath)
	brainBytes, err := os.ReadFile(brainPath)
	if err != nil {
		return fmt.Errorf("cannot read brain %s: %w", brainPath, err)
	}
	type artifacts struct {
		brot, washmhost []byte
	}
	per := make([]artifacts, len(slots))
	for i, s := range slots {
		if !shouldBuildSlot(s) {
			fmt.Printf("🍆    %s: disabled for this host\n", s.name)
			continue
		}
		brotData, err := os.ReadFile(brotPath(buildDir, s))
		if err != nil {
			return fmt.Errorf("read brot for %s: %w", s.name, err)
		}
		wh, err := os.ReadFile(washmhostPath(buildDir, s))
		if err != nil {
			return fmt.Errorf("read washmhost for %s: %w", s.name, err)
		}
		per[i] = artifacts{brot: brotData, washmhost: wh}
		fmt.Printf("🍆    %s: brot=%s washmhost=%s\n", s.name, formatSize(int64(len(brotData))), formatSize(int64(len(wh))))
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
	bw := brotli.NewWriterOptions(compressed, brotli.WriterOptions{
		Quality: 11,
		LGWin:   24,
	})
	if _, err := bw.Write(pool.Bytes()); err != nil {
		return fmt.Errorf("brotli write: %w", err)
	}
	if err := bw.Close(); err != nil {
		return fmt.Errorf("brotli close: %w", err)
	}
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
			return fmt.Errorf("patching brot for %s: %w", slots[i].name, err)
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

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output %s: %w", outputPath, err)
	}
	defer outFile.Close()
	_, _ = outFile.WriteString(zoneA)
	for i := range slots {
		_, _ = outFile.Write(per[i].brot)
	}
	_, _ = outFile.Write(compressed.Bytes())
	if runtime.GOOS != "windows" {
		if err := os.Chmod(outputPath, 0o755); err != nil {
			return fmt.Errorf("chmod %s: %w", outputPath, err)
		}
	}
	totalZoneB := 0
	for _, n := range lengths {
		totalZoneB += n
	}
	fmt.Printf("🍆  zone_a=%s zone_b=%s pool=%s\n", formatSize(int64(len(zoneA))), formatSize(int64(totalZoneB)), formatSize(int64(poolLen)))
	fmt.Printf("🍆  Wrote %s (%s bytes)\n", outputPath, formatSize(int64(len(zoneA)+totalZoneB+int(poolLen))))
	return nil
}

// buildProjectToWasm auto-detects Go vs Rust project and builds to WASM.
func buildProjectToWasm(ws, projectDir, outputWasm string) error {
	vegPath := os.Getenv("MOHABBAT_VEGETABLE_PATH")
	inVeg := vegPath != ""

	// Unconditionally run prebuild to ensure target specs and overlay.json are up to date.
	if err := runPrebuild(ws); err != nil {
		return fmt.Errorf("prebuild: %w", err)
	}

	// Resolve projectDir: first relative to CWD, then relative to workspace root.
	absProject, err := filepath.Abs(projectDir)
	if err != nil || !fileExists(absProject) {
		absProject = filepath.Join(ws, projectDir)
	}

	// Double-check if we are in a vegetable and the "projectDir" is actually the CWD-absolute path of the vegetable.
	if inVeg {
		vAbs, _ := filepath.Abs(vegPath)
		pAbs, _ := filepath.Abs(absProject)
		if strings.EqualFold(vAbs, pAbs) {
			// This was the vegetable path, ignore it if we are looking for a project.
			absProject = ""
		}
	}

	if absProject == "" || !fileExists(absProject) {
		return fmt.Errorf("project directory not found: %s", projectDir)
	}
	// Auto-detect: Go project has go.mod, Rust project has Cargo.toml.
	if fileExists(filepath.Join(absProject, "go.mod")) {
		return buildGoProjectWasm(ws, absProject, outputWasm)
	}
	return buildRustProjectWasm(ws, absProject, outputWasm)
}

// buildGoProjectWasm compiles a Go project to rusticated WASM.
func buildGoProjectWasm(ws, absProjectDir, outputWasm string) error {
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	goroot, err := resolveGoroot(ws)
	if err != nil {
		return fmt.Errorf("cannot resolve GOROOT: %w", err)
	}
	buildDir := filepath.Join(ws, "target", "mohabbat-go-build")
	projectName := filepath.Base(absProjectDir)
	goTmpDir := filepath.Join(buildDir, projectName, "gotmp")
	goCacheDir := filepath.Join(buildDir, projectName, "gocache")
	for _, d := range []string{goTmpDir, goCacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	fmt.Printf("🍆  Building Go project %s -> %s\n", absProjectDir, outputWasm)
	goBin := goBinFromRoot(goroot)
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared",
		"-overlay", overlayPath,
		"-trimpath", "-ldflags=-s -w",
		"-o", outputWasm, ".")
	cmd.Dir = absProjectDir
	env := os.Environ()
	env = upsertEnv(env, "GOOS", "wasip1")
	env = upsertEnv(env, "GOARCH", "wasm")
	env = upsertEnv(env, "GOROOT", goroot)
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build failed for %s: %w", absProjectDir, err)
	}
	fmt.Printf("🍆  Post-processing %s (rename _initialize -> run)\n", outputWasm)
	return postProcessWasm(outputWasm)
}

// buildRustProjectWasm compiles a Rust project to rusticated WASM.
func buildRustProjectWasm(ws, absProjectDir, outputWasm string) error {
	target := "wasm32-rusticated-unknown-unknown"
	projectName := filepath.Base(absProjectDir)
	fmt.Printf("🍆  Building Rust project %s -> WASM\n", absProjectDir)
	cmd := exec.Command("cargo", "build", "-p", projectName, "--release",
		"--config", filepath.Join(ws, "target", "rusticated-spec", "config.toml"),
		"--config", "unstable.json-target-spec=true",
		"--target", target,
		"-Z", "unstable-options")
	cmd.Env = upsertEnv(os.Environ(), "RUST_TARGET_PATH", filepath.Join(ws, "target", "rusticated-spec"))
	cmd.Dir = ws
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cargo build failed for %s: %w", projectName, err)
	}
	srcWasm := filepath.Join(ws, "target", target, "release", projectName+".wasm")
	data, err := os.ReadFile(srcWasm)
	if err != nil {
		return fmt.Errorf("read built wasm %s: %w", srcWasm, err)
	}
	if err := os.WriteFile(outputWasm, data, 0o644); err != nil {
		return fmt.Errorf("write wasm %s: %w", outputWasm, err)
	}
	return nil
}

// buildBrainWasm compiles mohabbat-go itself as the WASM brain.
func buildBrainWasm(ws, outputWasm string) error {
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	goroot, err := resolveGoroot(ws)
	if err != nil {
		return fmt.Errorf("cannot resolve GOROOT for brain build: %w", err)
	}
	buildDir := filepath.Dir(outputWasm)
	goTmpDir := filepath.Join(buildDir, "brain-gotmp")
	goCacheDir := filepath.Join(buildDir, "brain-gocache")
	for _, d := range []string{goTmpDir, goCacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	fmt.Printf("🍆  Building brain WASM -> %s\n", outputWasm)
	goBin := goBinFromRoot(goroot)
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared",
		"-overlay", overlayPath,
		"-trimpath", "-ldflags=-s -w",
		"-o", outputWasm, ".")
	cmd.Dir = filepath.Join(ws, "mohabbat-go")
	env := os.Environ()
	env = upsertEnv(env, "GOOS", "wasip1")
	env = upsertEnv(env, "GOARCH", "wasm")
	env = upsertEnv(env, "GOROOT", goroot)
	env = upsertEnv(env, "CGO_ENABLED", "0")
	env = upsertEnv(env, "GOTMPDIR", goTmpDir)
	env = upsertEnv(env, "GOCACHE", goCacheDir)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("brain WASM build failed: %w", err)
	}
	return postProcessWasm(outputWasm)
}

// runUnderWashmhost runs a WASM file under washmhost-go.
// When running inside a vegetable (MOHABBAT_VEGETABLE_PATH is set), it extracts
// the appropriate pre-built washmhost binary from the vegetable's pool rather
// than re-compiling from source via `go run .`.
// Natively, it compiles washmhost-go via `go run .`.
func runUnderWashmhost(ws, wasmPath string, extraArgs []string) error {
	fmt.Printf("🍆  Running %s under washmhost-go\n", filepath.Base(wasmPath))

	vegPath := os.Getenv("MOHABBAT_VEGETABLE_PATH")
	if vegPath != "" {
		return runUnderWashmhostFromVeg(vegPath, wasmPath, extraArgs)
	}
	return runUnderWashmhostNative(ws, wasmPath, extraArgs)
}

// runUnderWashmhostNative compiles washmhost-go from source and runs the payload.
func runUnderWashmhostNative(ws, wasmPath string, extraArgs []string) error {
	goroot, _ := resolveGoroot(ws)
	runArgs := []string{"run", ".", "--"}
	runArgs = append(runArgs, extraArgs...)
	cmd := exec.Command("go", runArgs...)
	cmd.Dir = filepath.Join(ws, "washmhost-go")
	env := os.Environ()
	env = upsertEnv(env, "MOHABBAT_WASM_FD", wasmPath)
	// Prevent GOOS/GOARCH leakage from prior WASM build steps.
	env = upsertEnv(env, "GOOS", runtime.GOOS)
	env = upsertEnv(env, "GOARCH", runtime.GOARCH)
	if goroot != "" {
		env = upsertEnv(env, "GOROOT", goroot)
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("washmhost execution failed: %w", err)
	}
	return nil
}

// runUnderWashmhostFromVeg extracts the platform washmhost binary from the
// vegetable's compressed pool and runs it directly, bypassing source compilation.
func runUnderWashmhostFromVeg(vegPath, wasmPath string, extraArgs []string) error {
	vegData, err := os.ReadFile(vegPath)
	if err != nil {
		return fmt.Errorf("read vegetable for washmhost extraction: %w", err)
	}
	fileLen := len(vegData)

	// Find first valid meta — gives us pool location + washmhost offsets.
	magic := []byte(mohabbatMagic)
	const metaBodySize = 48
	var meta *mohabbatMeta
	for i := 0; i+len(magic)+metaBodySize <= fileLen; i++ {
		if !bytes.Equal(vegData[i:i+len(magic)], magic) {
			continue
		}
		p := i + len(magic)
		poolLen := binary.LittleEndian.Uint64(vegData[p : p+8])
		if poolLen == 0 || uint64(fileLen) < poolLen {
			continue
		}
		reserved := binary.LittleEndian.Uint64(vegData[p+40 : p+48])
		if reserved != 0 {
			continue
		}
		m := mohabbatMeta{
			PoolLen:         poolLen,
			WashmhostOffset: binary.LittleEndian.Uint64(vegData[p+8 : p+16]),
			WashmhostLen:    binary.LittleEndian.Uint64(vegData[p+16 : p+24]),
			PayloadOffset:   binary.LittleEndian.Uint64(vegData[p+24 : p+32]),
			PayloadLen:      binary.LittleEndian.Uint64(vegData[p+32 : p+40]),
		}
		meta = &m
		break
	}
	if meta == nil {
		return fmt.Errorf("no valid MOHABBAT meta found in vegetable %s — cannot extract washmhost", vegPath)
	}

	poolStart := fileLen - int(meta.PoolLen)
	if poolStart < 0 {
		return fmt.Errorf("invalid PoolLen in vegetable")
	}

	poolReader := brotli.NewReader(bytes.NewReader(vegData[poolStart:]))
	var poolBuf bytes.Buffer
	if _, err := poolBuf.ReadFrom(poolReader); err != nil {
		return fmt.Errorf("decompress pool for washmhost: %w", err)
	}
	pool := poolBuf.Bytes()

	hostOS := os.Getenv("MOHABBAT_HOST_OS")
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	hostArch := os.Getenv("MOHABBAT_HOST_ARCH")
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}

	// Find the washmhost entry for the current platform.
	// Slot order is contractual: linux-amd64, linux-arm64, win-amd64, win-arm64.
	var targetSlotIdx int = -1
	for i, s := range slots {
		if s.goos == hostOS && s.goarch == hostArch {
			targetSlotIdx = i
			break
		}
	}
	if targetSlotIdx < 0 {
		return fmt.Errorf("no washmhost slot for %s/%s in vegetable", hostOS, hostArch)
	}

	// Re-read the per-slot washmhost offset+length from the matching brot in Zone B.
	// Each brot has its own meta with its slot's WashmhostOffset+Len.
	// We already have meta from the first occurrence, which belongs to slot 0.
	// We need to find the meta for targetSlotIdx. Walk all meta occurrences.
	type slotMeta struct {
		whOffset, whLen uint64
	}
	slotMetas := make([]slotMeta, len(slots))
	occIdx := 0
	for i := 0; i+len(magic)+metaBodySize <= fileLen && occIdx < len(slots); i++ {
		if !bytes.Equal(vegData[i:i+len(magic)], magic) {
			continue
		}
		p := i + len(magic)
		poolLen := binary.LittleEndian.Uint64(vegData[p : p+8])
		if poolLen == 0 || uint64(fileLen) < poolLen {
			continue
		}
		reserved := binary.LittleEndian.Uint64(vegData[p+40 : p+48])
		if reserved != 0 {
			continue
		}
		if occIdx < len(slots) {
			slotMetas[occIdx] = slotMeta{
				whOffset: binary.LittleEndian.Uint64(vegData[p+8 : p+16]),
				whLen:    binary.LittleEndian.Uint64(vegData[p+16 : p+24]),
			}
			occIdx++
		}
	}

	sm := slotMetas[targetSlotIdx]
	if sm.whLen == 0 || uint64(len(pool)) < sm.whOffset+sm.whLen {
		return fmt.Errorf("washmhost bytes out of range in pool (offset=%d len=%d pool=%d)",
			sm.whOffset, sm.whLen, len(pool))
	}
	washmhostBytes := pool[sm.whOffset : sm.whOffset+sm.whLen]

	// Write washmhost to a temp file and run it.
	tempDir := os.TempDir()
	if tempDir == "" || tempDir == "." {
		tempDir = "target"
	}
	_ = os.MkdirAll(tempDir, 0755)
	// We might be running inside washmhost (as a vegetable), so tempDir might be /tmp.
	// washmhost maps /tmp to host's actual temp dir.
	ext := ""
	if hostOS == "windows" {
		ext = ".exe"
	}
	tmpFile, err := os.CreateTemp(tempDir, "mohabbat-wh-*"+ext)
	if err != nil {
		return fmt.Errorf("create temp washmhost in %s: %w", tempDir, err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.Write(washmhostBytes); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp washmhost: %w", err)
	}
	tmpFile.Close()
	if hostOS != "windows" {
		_ = os.Chmod(tmpPath, 0o755)
	}

	runCmd := exec.Command(tmpPath, append([]string{vegPath}, extraArgs...)...)
	runCmd.Env = upsertEnv(os.Environ(), "MOHABBAT_WASM_FD", wasmPath)
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	runCmd.Stdin = os.Stdin
	if err := runCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("washmhost (from veg) execution failed: %w", err)
	}
	return nil
}

// resolveGoroot finds the correct GOROOT using a priority chain that does not
// require the `go` binary to be in PATH — critical for vegetable (WASM brain) mode.
func resolveGoroot(ws string) (string, error) {
	// Priority 0: extract from overlay.json if it already exists.
	// overlay.json keys are absolute paths of the form {GOROOT}/src/...,
	// so the GOROOT can be inferred without running any subprocess.
	if goroot := gorootFromOverlay(ws); goroot != "" {
		return goroot, nil
	}

	// Priority 1: GOROOT env var already set (parent process may have it).
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		if _, err := os.Stat(goroot); err == nil {
			return goroot, nil
		}
	}

	// Determine go version from go.mod.
	ver := ""
	goModPath := filepath.Join(ws, "mohabbat-go", "go.mod")
	if f, err := os.Open(goModPath); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "go ") {
				ver = strings.TrimSpace(strings.TrimPrefix(line, "go "))
				break
			}
		}
		f.Close()
	}

	if ver != "" {
		// Priority 2: $HOME/sdk/go{ver} — check multiple home sources because
		// os.UserHomeDir() may fail inside the WASM sandbox.
		homes := uniqueStrings([]string{
			func() string { h, _ := os.UserHomeDir(); return h }(),
			os.Getenv("USERPROFILE"),
			os.Getenv("HOME"),
		})
		for _, home := range homes {
			if home == "" {
				continue
			}
			sdkPath := filepath.Join(home, "sdk", "go"+ver)
			if _, err := os.Stat(sdkPath); err == nil {
				return sdkPath, nil
			}
		}
		// Priority 3: run `go{ver} env GOROOT`
		if out, err := exec.Command("go"+ver, "env", "GOROOT").Output(); err == nil {
			p := strings.TrimSpace(string(out))
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
		// Priority 4: `go env GOROOT`
		if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
			p := strings.TrimSpace(string(out))
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	// Last resort: plain `go env GOROOT`
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOROOT failed: %w", err)
	}
	p := strings.TrimSpace(string(out))
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("GOROOT %q does not exist", p)
	}
	return p, nil
}

// gorootFromOverlay reads target/overlay.json and extracts GOROOT from the
// source paths embedded in it. The keys are absolute paths of the form
// {GOROOT}/src/runtime/os_wasip1.go — so GOROOT is everything before /src/.
// This requires no subprocess and works inside the WASM vegetable sandbox.
func gorootFromOverlay(ws string) string {
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	data, err := os.ReadFile(overlayPath)
	if err != nil {
		return ""
	}
	var v struct {
		Replace map[string]string `json:"Replace"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return ""
	}
	for src := range v.Replace {
		// Normalize to forward slashes for consistent searching.
		srcFwd := filepath.ToSlash(src)
		if idx := strings.Index(srcFwd, "/src/runtime/"); idx >= 0 {
			candidate := filepath.FromSlash(srcFwd[:idx])
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}

// goBinFromRoot returns the absolute path to the `go` binary inside goroot.
// Falls back to "go" (PATH lookup) if the binary doesn't exist there.
// Probes both "go.exe" and "go" because runtime.GOOS is "wasip1" when
// this code runs as the WASM brain, not the actual host OS.
func goBinFromRoot(goroot string) string {
	if goroot == "" {
		return "go"
	}
	// Try .exe first (Windows host), then no extension (Linux/macOS host).
	for _, ext := range []string{".exe", ""} {
		bin := filepath.Join(goroot, "bin", "go"+ext)
		if _, err := os.Stat(bin); err == nil {
			return bin
		}
	}
	return "go"
}

// uniqueStrings returns a slice with duplicates removed, preserving order.
func uniqueStrings(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// postProcessWasm renames the _initialize export to run in a WASM binary.
func postProcessWasm(wasmPath string) error {
	data, err := os.ReadFile(wasmPath)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("invalid wasm file: too small")
	}
	pos := 8
	var newData []byte
	newData = append(newData, data[:8]...)
	for pos < len(data) {
		sectionID := data[pos]
		pos++
		size, n, err := readVarUint32(data[pos:])
		if err != nil {
			return fmt.Errorf("wasm section size: %w", err)
		}
		pos += n
		sectionEnd := pos + int(size)
		if sectionEnd > len(data) {
			return fmt.Errorf("wasm section overflows file")
		}
		if sectionID == 7 { // Export section
			exportData := data[pos:sectionEnd]
			count, n2, err := readVarUint32(exportData)
			if err != nil {
				return fmt.Errorf("export count: %w", err)
			}
			var newExportSec []byte
			newExportSec = append(newExportSec, encodeVarUint32(count)...)
			p := n2
			found := false
			for i := uint32(0); i < count; i++ {
				nameLen, n3, err := readVarUint32(exportData[p:])
				if err != nil {
					return fmt.Errorf("export name len: %w", err)
				}
				p += n3
				name := string(exportData[p : p+int(nameLen)])
				p += int(nameLen)
				kind := exportData[p]
				p++
				idx, n4, err := readVarUint32(exportData[p:])
				if err != nil {
					return fmt.Errorf("export idx: %w", err)
				}
				p += n4
				if name == "_initialize" {
					name = "run"
					found = true
				}
				newExportSec = append(newExportSec, encodeVarUint32(uint32(len(name)))...)
				newExportSec = append(newExportSec, name...)
				newExportSec = append(newExportSec, kind)
				newExportSec = append(newExportSec, encodeVarUint32(idx)...)
			}
			if found {
				newData = append(newData, sectionID)
				newData = append(newData, encodeVarUint32(uint32(len(newExportSec)))...)
				newData = append(newData, newExportSec...)
			} else {
				newData = append(newData, data[pos-n-1:sectionEnd]...)
			}
		} else {
			newData = append(newData, data[pos-n-1:sectionEnd]...)
		}
		pos = sectionEnd
	}
	return os.WriteFile(wasmPath, newData, 0o644)
}

func readVarUint32(data []byte) (uint32, int, error) {
	var res uint32
	var shift uint
	for i, b := range data {
		res |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			return res, i + 1, nil
		}
		shift += 7
		if shift >= 32 {
			break
		}
	}
	return 0, 0, fmt.Errorf("invalid leb128")
}

func encodeVarUint32(v uint32) []byte {
	var res []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			res = append(res, b|0x80)
		} else {
			res = append(res, b)
			break
		}
	}
	return res
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isProject(ws, dir string) bool {
	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(ws, dir)
	}
	if !fileExists(abs) {
		return false
	}
	return fileExists(filepath.Join(abs, "go.mod")) || fileExists(filepath.Join(abs, "Cargo.toml"))
}

func resolveWorkspace(ws string) (string, error) {
	if ws != "" {
		return filepath.Abs(ws)
	}
	// Highest priority: when running inside a vegetable, MOHABBAT_VEGETABLE_PATH
	// points to the .bat file itself. Its directory is (or contains) the workspace root.
	if vegPath := os.Getenv("MOHABBAT_VEGETABLE_PATH"); vegPath != "" {
		dir := filepath.Dir(vegPath)
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
	const tmpl = ":; ME=\"$(readlink -f \"$0\" 2>/dev/null || realpath \"$0\" 2>/dev/null || printf \"%s\" \"$0\")\"; S_OFF=0; S_LEN=0; case \"$(uname -m)-$(uname -s)\" in x86_64-Linux) S_OFF={{LINUX_AMD_OFF}}; S_LEN={{LINUX_AMD_LEN}} ;; aarch64-Linux) S_OFF={{LINUX_ARM_OFF}}; S_LEN={{LINUX_ARM_LEN}} ;; esac; [ \"$S_LEN\" = \"0\" ] && { echo \"[mohabbat] Unsupported arch/os \"; exit 1; }; TMP_DIR=\"${TMPDIR:-/tmp}\"; [ -d \"./target\" ] && TMP_DIR=\"./target\"; TMP_EXE=\"$TMP_DIR/moh-$$\"; dd if=\"$ME\" bs=1 skip=\"$S_OFF\" count=\"$S_LEN\" of=\"$TMP_EXE\" 2>/dev/null; chmod +x \"$TMP_EXE\"; \"$TMP_EXE\" \"$ME\" \"$@\"; RET=$?; rm \"$TMP_EXE\"; exit $RET\n" +
		"@echo off\r\n" +
		"setlocal enabledelayedexpansion\r\n" +
		"set \"ME=%~f0\"\r\n" +
		"set \"TMP_DIR=!TEMP!\"\r\n" +
		"if exist \".\\target\" set \"TMP_DIR=.\\target\"\r\n" +
		"set \"TMP_EXE=!TMP_DIR!\\moh-!RANDOM!.exe\"\r\n" +
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
