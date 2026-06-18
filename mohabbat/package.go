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

