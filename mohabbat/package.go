package mohabbat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/andybalholm/brotli"
)

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
		if slots[i].goos == "js" {
			continue // Handled via template string replacements below
		}
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

	idx := map[string]int{}
	for i, s := range slots {
		idx[s.name] = i
	}
	nodeIdx := idx["node"]

	var rawStarter string
	var brotliWasmBytes []byte
	if len(per[nodeIdx].brot) > 0 {
		wasmMagic := []byte{0x00, 0x61, 0x73, 0x6d}
		widx := bytes.Index(per[nodeIdx].brot, wasmMagic)
		if widx != -1 {
			rawStarter = string(per[nodeIdx].brot[:widx])
			brotliWasmBytes = per[nodeIdx].brot[widx:]
		}
	}

	lengths := make([]int, len(slots))
	for i := range slots {
		lengths[i] = len(per[i].brot)
	}
	offsets := make([]int, len(slots))

	zoneA := ""
	for retries := 0; retries < 8; retries++ {
		if rawStarter != "" {
			s := rawStarter
			s = strings.ReplaceAll(s, "\"{{NODE_POOL_LEN}}\"", fmt.Sprintf("%-17d", poolLen))
			s = strings.ReplaceAll(s, "\"{{NODE_WASHMHOST_OFF}}\"", fmt.Sprintf("%-23d", whOffsets[nodeIdx]))
			s = strings.ReplaceAll(s, "\"{{NODE_WASHMHOST_LEN}}\"", fmt.Sprintf("%-23d", whLens[nodeIdx]))
			s = strings.ReplaceAll(s, "\"{{NODE_PAYLOAD_OFF}}\"", fmt.Sprintf("%-21d", payloadOffset))
			s = strings.ReplaceAll(s, "\"{{NODE_PAYLOAD_LEN}}\"", fmt.Sprintf("%-21d", payloadLen))

			dummyS := s
			dummyS = strings.ReplaceAll(dummyS, "\"{{NODE_WASM_OFF}}\"", "01234567890123456")
			dummyS = strings.ReplaceAll(dummyS, "\"{{NODE_WASM_LEN}}\"", "01234567890123456")
			jsLen := len(dummyS)

			wasmOff := offsets[nodeIdx] + jsLen
			wasmLen := len(brotliWasmBytes)
			s = strings.ReplaceAll(s, "\"{{NODE_WASM_OFF}}\"", fmt.Sprintf("%-17d", wasmOff))
			s = strings.ReplaceAll(s, "\"{{NODE_WASM_LEN}}\"", fmt.Sprintf("%-17d", wasmLen))

			per[nodeIdx].brot = append([]byte(s), brotliWasmBytes...)
			lengths[nodeIdx] = len(per[nodeIdx].brot)
			slots[nodeIdx].jsTextLen = len(s)
		}

		zoneA = buildZoneA(offsets, lengths, slots[nodeIdx].jsTextLen)
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
			// One final update of node payload to lock in correct absolute wasm offsets
			if rawStarter != "" {
				s := rawStarter
				s = strings.ReplaceAll(s, "\"{{NODE_POOL_LEN}}\"", fmt.Sprintf("%-17d", poolLen))
				s = strings.ReplaceAll(s, "\"{{NODE_WASHMHOST_OFF}}\"", fmt.Sprintf("%-23d", whOffsets[nodeIdx]))
				s = strings.ReplaceAll(s, "\"{{NODE_WASHMHOST_LEN}}\"", fmt.Sprintf("%-23d", whLens[nodeIdx]))
				s = strings.ReplaceAll(s, "\"{{NODE_PAYLOAD_OFF}}\"", fmt.Sprintf("%-21d", payloadOffset))
				s = strings.ReplaceAll(s, "\"{{NODE_PAYLOAD_LEN}}\"", fmt.Sprintf("%-21d", payloadLen))

				dummyS := s
				dummyS = strings.ReplaceAll(dummyS, "\"{{NODE_WASM_OFF}}\"", "01234567890123456")
				dummyS = strings.ReplaceAll(dummyS, "\"{{NODE_WASM_LEN}}\"", "01234567890123456")
				jsLen := len(dummyS)

				wasmOff := offsets[nodeIdx] + jsLen
				wasmLen := len(brotliWasmBytes)
				s = strings.ReplaceAll(s, "\"{{NODE_WASM_OFF}}\"", fmt.Sprintf("%-17d", wasmOff))
				s = strings.ReplaceAll(s, "\"{{NODE_WASM_LEN}}\"", fmt.Sprintf("%-17d", wasmLen))

				per[nodeIdx].brot = append([]byte(s), brotliWasmBytes...)
				slots[nodeIdx].jsTextLen = len(s)
			}
			zoneA = buildZoneA(offsets, lengths, slots[nodeIdx].jsTextLen)
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
	fmt.Printf("[mohabbat]  zone_a=%s zone_b=%s pool=%s\n", formatSize(int64(len(zoneA))), formatSize(int64(totalZoneB)), formatSize(int64(poolLen)))
	fmt.Printf("[mohabbat]  Wrote %s (%s bytes)\n", outputPath, formatSize(int64(len(zoneA)+totalZoneB+int(poolLen))))
	return nil
}

// buildZoneA produces the polyglot script header for Modern Four.
func buildZoneA(offsets, lengths []int, nodeJsLen int) string {
	const tmplPOSIX = `:; ME="$(readlink -f "$0" 2>/dev/null || realpath "$0" 2>/dev/null || printf "%s" "$0")"; S_OFF=0; S_LEN=0
:; case "$(uname -m)-$(uname -s)" in
:; x86_64-Linux) S_OFF={{LINUX_AMD_OFF}}; S_LEN={{LINUX_AMD_LEN}} ;;
:; aarch64-Linux) S_OFF={{LINUX_ARM_OFF}}; S_LEN={{LINUX_ARM_LEN}} ;;
:; esac
:; USE_NODE=0
:; [ -n "$MOHABBAT_USE_NODE" ] && USE_NODE=1
:; [ "$S_LEN" = "0" ] && USE_NODE=1
:; if [ "$USE_NODE" = "1" ]; then
:;   find_node() {
:;     command -v node >/dev/null 2>&1 && node -e "process.exit(parseInt(process.version.slice(1))<18)" 2>/dev/null && NODE_BIN="$(command -v node)" && return 0
:;     for cand in /usr/local/bin/node /opt/homebrew/bin/node /usr/bin/node "$HOME"/.nvm/versions/node/v*/bin/node "$HOME"/.fnm/node-versions/*/installation/bin/node /usr/local/n/versions/node/*/bin/node "$HOME"/.volta/bin/node; do
:;       [ -x "$cand" ] && "$cand" -e "process.exit(parseInt(process.version.slice(1))<18)" 2>/dev/null && NODE_BIN="$cand" && return 0
:;     done
:;     return 1
:;   }
:;   if find_node; then
:;     export MOHABBAT_VEGETABLE_PATH="$ME"
:;     dd if="$ME" bs=1 skip="{{NODE_OFF}}" count="{{NODE_JS_LEN}}" 2>/dev/null | "$NODE_BIN" - "$ME" "$@"
:;     exit $?
:;   fi
:; fi
:; [ "$S_LEN" = "0" ] && { echo "[mohabbat] Unsupported platform and node not available"; exit 1; }
:; TMP_DIR="${TMPDIR:-/tmp}"; [ -d "./target" ] && TMP_DIR="./target"; TMP_EXE="$TMP_DIR/moh-$$"; dd if="$ME" bs=1 skip="$S_OFF" count="$S_LEN" of="$TMP_EXE" 2>/dev/null; chmod +x "$TMP_EXE"; "$TMP_EXE" "$ME" "$@"; RET=$?; rm "$TMP_EXE"; exit $RET
`
	const tmplWIN = `@echo off
setlocal enabledelayedexpansion
set "ME=%~f0"
set "TMP_DIR=!TEMP!"
if exist ".\target" set "TMP_DIR=.\target"
set "TMP_EXE=!TMP_DIR!\moh-!RANDOM!.exe"
set "ARCH=%PROCESSOR_ARCHITECTURE%"
if "!PROCESSOR_ARCHITEW6432!" neq "" set "ARCH=!PROCESSOR_ARCHITEW6432!"
set "S_OFF=0"
set "S_LEN=0"
if "!ARCH!"=="AMD64" (
	set "S_OFF={{WIN_AMD_OFF}}"
	set "S_LEN={{WIN_AMD_LEN}}"
) else if "!ARCH!"=="ARM64" (
	set "S_OFF={{WIN_ARM_OFF}}"
	set "S_LEN={{WIN_ARM_LEN}}"
)
set "USE_NODE=0"
if defined MOHABBAT_USE_NODE set "USE_NODE=1"
if "!S_LEN!"=="0" set "USE_NODE=1"
if "!USE_NODE!"=="1" (
  set "NODE_BIN="
  for %%c in ("node.exe") do (
    if not "%%~$PATH:c"=="" set "NODE_BIN=%%~$PATH:c"
  )
  if "!NODE_BIN!"=="" (
    for %%c in ("%ProgramFiles%\nodejs\node.exe" "%ProgramFiles(x86)%\nodejs\node.exe" "%ChocolateyInstall%\bin\node.exe" "%UserProfile%\.volta\bin\node.exe") do (
      if exist "%%~c" set "NODE_BIN=%%~c"
    )
  )
  if "!NODE_BIN!"=="" (
    for /D %%d in ("%AppData%\nvm\v*") do if exist "%%d\node.exe" set "NODE_BIN=%%d\node.exe"
  )
  if "!NODE_BIN!"=="" (
    for /D %%d in ("%UserProfile%\.fnm\node-versions\*") do if exist "%%d\installation\node.exe" set "NODE_BIN=%%d\installation\node.exe"
  )
  if not "!NODE_BIN!"=="" (
    "!NODE_BIN!" -v >nul 2>&1
    if !errorlevel! equ 0 (
      set "MOHABBAT_VEGETABLE_PATH=!ME!"
      powershell -NoProfile -ExecutionPolicy Bypass -Command "& { $a=[IO.File]::ReadAllBytes($env:ME); $b=[Text.Encoding]::UTF8.GetString($a, {{NODE_OFF}}, {{NODE_JS_LEN}}); $n=$env:NODE_BIN; $m=$env:ME; $b | & $n - $env:ME $args; exit $LASTEXITCODE }" -- %*
      exit /b !errorlevel!
    )
  )
)
if "!S_LEN!"=="0" (
    echo [mohabbat] This vegetable does not support !ARCH! on Windows and node is not available.
    exit /b 1
)
powershell -NoProfile -ExecutionPolicy Bypass -Command "$a=[IO.File]::ReadAllBytes($env:ME); $b=New-Object byte[] !S_LEN!; [Array]::Copy($a, [int64]!S_OFF!, $b, 0, [int]!S_LEN!); [IO.File]::WriteAllBytes($env:TMP_EXE, $b)"
"!TMP_EXE!" "!ME!" %*
set "RET=!ERRORLEVEL!"
if exist "!TMP_EXE!" del "!TMP_EXE!"
exit /b !RET!
`
	idx := map[string]int{}
	for i, s := range slots {
		idx[s.name] = i
	}
	node := -1
	linuxAMD := -1
	linuxARM := -1
	winAMD := -1
	winARM := -1
	if i, ok := idx["node"]; ok {
		node = i
	}
	if i, ok := idx["linux-amd64"]; ok {
		linuxAMD = i
	}
	if i, ok := idx["linux-arm64"]; ok {
		linuxARM = i
	}
	if i, ok := idx["win-amd64"]; ok {
		winAMD = i
	}
	if i, ok := idx["win-arm64"]; ok {
		winARM = i
	}

	s := tmplPOSIX + tmplWIN
	s = strings.ReplaceAll(s, "\n", "\r\n")

	replace := func(s, key string, i int, vals []int) string {
		val := 0
		if i >= 0 && i < len(vals) {
			val = vals[i]
		}
		return strings.ReplaceAll(s, key, fmt.Sprintf("%d", val))
	}

	s = replace(s, "{{NODE_OFF}}", node, offsets)
	s = replace(s, "{{NODE_LEN}}", node, lengths)
	s = replace(s, "{{NODE_JS_LEN}}", node, []int{nodeJsLen})

	s = replace(s, "{{LINUX_AMD_OFF}}", linuxAMD, offsets)
	s = replace(s, "{{LINUX_AMD_LEN}}", linuxAMD, lengths)
	s = replace(s, "{{LINUX_ARM_OFF}}", linuxARM, offsets)
	s = replace(s, "{{LINUX_ARM_LEN}}", linuxARM, lengths)
	s = replace(s, "{{WIN_AMD_OFF}}", winAMD, offsets)
	s = replace(s, "{{WIN_AMD_LEN}}", winAMD, lengths)
	s = replace(s, "{{WIN_ARM_OFF}}", winARM, offsets)
	s = replace(s, "{{WIN_ARM_LEN}}", winARM, lengths)
	return s
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

// ensureBrotStubs creates empty import library stubs (.a files) for MinGW
// target dependencies (like mingw32, moldname, mingwex, msvcrt) inside the
// provided directory. This allows cross-linking a no_std raw-dylib Windows
// binary on macOS/Linux without requiring a true MinGW sysroot installation.
func ensureBrotStubs(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir brot-stubs: %w", err)
	}
	// empty COFF archive header snippet that acts as a valid empty .a file
	emptyArchive := []byte("!<arch>\n")
	libs := []string{
		"libmingw32.a",
		"libmoldname.a",
		"libmingwex.a",
		"libmsvcrt.a",
		"libadvapi32.a",
		"libshell32.a",
		"libuser32.a",
		"libkernel32.a",
	}
	for _, lib := range libs {
		if err := os.WriteFile(filepath.Join(dir, lib), emptyArchive, 0o644); err != nil {
			return fmt.Errorf("write stub %s: %w", lib, err)
		}
	}
	return nil
}

func brotPath(buildDir string, s slot) string {
	return filepath.Join(buildDir, fmt.Sprintf("brot-%s%s", s.name, artifactExt(s.goos)))
}

func washmhostPath(buildDir string, s slot) string {
	return filepath.Join(buildDir, fmt.Sprintf("washmhost-%s%s", s.name, artifactExt(s.goos)))
}

func artifactExt(goos string) string {
	if goos == "js" {
		return ".js"
	}
	if goos == "windows" {
		// Use .dat instead of .exe for the stored artifact to bypass
		// aggressive Windows Defender real-time scanning during the build process.
		return ".dat"
	}
	return ""
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
