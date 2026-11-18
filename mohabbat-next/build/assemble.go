package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/klauspost/compress/zstd"
)

func writeFileIfChanged(path string, data []byte, perm os.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

type ComponentRange struct {
	Offset int64
	Length int64
}

func compressZstandard(data []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, fmt.Errorf("create zstd encoder: %w", err)
	}
	defer enc.Close()

	compressed := enc.EncodeAll(data, nil)

	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("create zstd decoder: %w", err)
	}
	defer dec.Close()

	restored, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("zstd round-trip decode failed: %w", err)
	}
	if !bytes.Equal(data, restored) {
		return nil, fmt.Errorf("zstd round-trip check failed: restored bytes do not match original")
	}
	return compressed, nil
}

func validatePE(data []byte) (int, error) {
	if len(data) < 64 || data[0] != 'M' || data[1] != 'Z' {
		return 0, fmt.Errorf("executable is not a valid MZ executable")
	}
	peOffset := int(binary.LittleEndian.Uint32(data[0x3c:0x40]))
	if peOffset < 64 || peOffset > len(data)-4 {
		return 0, fmt.Errorf("invalid PE header offset: %d", peOffset)
	}
	if data[peOffset] != 'P' || data[peOffset+1] != 'E' || data[peOffset+2] != 0 || data[peOffset+3] != 0 {
		return 0, fmt.Errorf("PE signature not found at offset %d", peOffset)
	}
	return peOffset, nil
}

type AssemblyAssets struct {
	BrotLinuxX86_64        string
	BrotLinuxAarch64       string
	BrotAndroidAarch64     string
	BrotDarwinX86_64       string
	BrotDarwinAarch64      string
	PlatformWindowsX86_64  string
	PlatformWindowsAarch64 string
	PlatformLinuxX86_64    string
	PlatformLinuxAarch64   string
	PlatformDarwinX86_64   string
	PlatformDarwinAarch64  string
	RuntimeX86_64          string
	RuntimeAarch64         string
	BrainWasm              string
}

func buildSharedPool(assets AssemblyAssets) ([]byte, error) {
	if assets.BrainWasm == "" {
		return nil, fmt.Errorf("application brain WASM is required")
	}

	type poolItem struct {
		name string
		path string
	}
	items := []poolItem{
		{name: "platform-windows-x86_64", path: assets.PlatformWindowsX86_64},
		{name: "platform-windows-aarch64", path: assets.PlatformWindowsAarch64},
		{name: "platform-linux-x86_64", path: assets.PlatformLinuxX86_64},
		{name: "platform-linux-aarch64", path: assets.PlatformLinuxAarch64},
		{name: "platform-darwin-x86_64", path: assets.PlatformDarwinX86_64},
		{name: "platform-darwin-aarch64", path: assets.PlatformDarwinAarch64},
		{name: "runtime-x86_64", path: assets.RuntimeX86_64},
		{name: "runtime-aarch64", path: assets.RuntimeAarch64},
		{name: "brain.wasm", path: assets.BrainWasm},
	}

	const headerSize = 16
	const tocEntrySize = 40
	tocSize := len(items) * tocEntrySize
	payloadOffset := headerSize + tocSize

	rawPayloads := make([][]byte, len(items))
	totalSize := payloadOffset
	for i, item := range items {
		if len(item.name) > 32 {
			return nil, fmt.Errorf("pool entry name exceeds 32 bytes: %s", item.name)
		}
		data, err := os.ReadFile(item.path)
		if err != nil {
			return nil, fmt.Errorf("read pool asset %s (%s): %w", item.name, item.path, err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("pool asset is empty: %s (%s)", item.name, item.path)
		}
		rawPayloads[i] = data
		totalSize += len(data)
	}

	poolRaw := make([]byte, totalSize)
	copy(poolRaw[0:8], "MHBTPOOL")
	binary.LittleEndian.PutUint32(poolRaw[8:12], 1)
	binary.LittleEndian.PutUint32(poolRaw[12:16], uint32(len(items)))

	cur := payloadOffset
	for i, item := range items {
		tocOff := headerSize + i*tocEntrySize
		copy(poolRaw[tocOff:tocOff+32], item.name)
		binary.LittleEndian.PutUint32(poolRaw[tocOff+32:tocOff+36], uint32(cur))
		binary.LittleEndian.PutUint32(poolRaw[tocOff+36:tocOff+40], uint32(len(rawPayloads[i])))
		copy(poolRaw[cur:cur+len(rawPayloads[i])], rawPayloads[i])
		cur += len(rawPayloads[i])
	}

	return compressZstandard(poolRaw)
}

func assembleVegetable(windowsHostExe string, assets AssemblyAssets, templatesDir string, outputExe string) error {
	winExeBytes, err := os.ReadFile(windowsHostExe)
	if err != nil {
		return fmt.Errorf("read Windows host executable %s: %w", windowsHostExe, err)
	}
	if _, err := validatePE(winExeBytes); err != nil {
		return fmt.Errorf("validate base Windows PE: %w", err)
	}

	bundle := new(bytes.Buffer)
	if _, err := bundle.Write(winExeBytes); err != nil {
		return err
	}

	cache := make(map[string]ComponentRange)

	addAsset := func(filePath string, compress bool) (ComponentRange, error) {
		key := fmt.Sprintf("%s|%t", filepath.Clean(filePath), compress)
		if cr, exists := cache[key]; exists {
			return cr, nil
		}
		raw, err := os.ReadFile(filePath)
		if err != nil {
			return ComponentRange{}, fmt.Errorf("read asset %s: %w", filePath, err)
		}
		if len(raw) == 0 {
			return ComponentRange{}, fmt.Errorf("asset is empty: %s", filePath)
		}

		var payload []byte
		if compress {
			const maxCompressedComponentBytes = 128 * 1024 * 1024
			if len(raw) > maxCompressedComponentBytes {
				return ComponentRange{}, fmt.Errorf("uncompressed component exceeds 128 MiB: %s (%d bytes)", filePath, len(raw))
			}
			compressed, err := compressZstandard(raw)
			if err != nil {
				return ComponentRange{}, fmt.Errorf("compress %s: %w", filePath, err)
			}
			if len(compressed) > maxCompressedComponentBytes {
				return ComponentRange{}, fmt.Errorf("compressed component exceeds 128 MiB: %s (%d bytes)", filePath, len(compressed))
			}
			payload = compressed
		} else {
			if len(raw) > 16*1024*1024 {
				return ComponentRange{}, fmt.Errorf("brot executable exceeds 16 MiB: %s (%d bytes)", filePath, len(raw))
			}
			payload = raw
		}

		offset := int64(bundle.Len())
		length := int64(len(payload))
		if _, err := bundle.Write(payload); err != nil {
			return ComponentRange{}, err
		}

		cr := ComponentRange{Offset: offset, Length: length}
		cache[key] = cr
		return cr, nil
	}

	// 1. Raw Brot launchers
	brotLinuxX86_64, err := addAsset(assets.BrotLinuxX86_64, false)
	if err != nil {
		return err
	}
	brotLinuxAarch64, err := addAsset(assets.BrotLinuxAarch64, false)
	if err != nil {
		return err
	}
	brotAndroidAarch64, err := addAsset(assets.BrotAndroidAarch64, false)
	if err != nil {
		return err
	}
	brotDarwinX86_64, err := addAsset(assets.BrotDarwinX86_64, false)
	if err != nil {
		return err
	}
	brotDarwinAarch64, err := addAsset(assets.BrotDarwinAarch64, false)
	if err != nil {
		return err
	}

	// 2. Single shared compressed pool blob (all PALs, runtimes, and the required application brain.wasm)
	poolCompressed, err := buildSharedPool(assets)
	if err != nil {
		return err
	}
	poolOffset := int64(bundle.Len())
	poolLength := int64(len(poolCompressed))
	if _, err := bundle.Write(poolCompressed); err != nil {
		return err
	}

	bootstrapOffset := int64(bundle.Len())

	// 4. Render bootstrap.sh.in
	type BootstrapData struct {
		BrotLinuxX86_64Offset    int64
		BrotLinuxX86_64Length    int64
		BrotLinuxAarch64Offset   int64
		BrotLinuxAarch64Length   int64
		BrotAndroidAarch64Offset int64
		BrotAndroidAarch64Length int64
		BrotDarwinX86_64Offset   int64
		BrotDarwinX86_64Length   int64
		BrotDarwinAarch64Offset  int64
		BrotDarwinAarch64Length  int64
		PoolOffset               int64
		PoolLength               int64
	}

	bData := BootstrapData{
		BrotLinuxX86_64Offset:    brotLinuxX86_64.Offset,
		BrotLinuxX86_64Length:    brotLinuxX86_64.Length,
		BrotLinuxAarch64Offset:   brotLinuxAarch64.Offset,
		BrotLinuxAarch64Length:   brotLinuxAarch64.Length,
		BrotAndroidAarch64Offset: brotAndroidAarch64.Offset,
		BrotAndroidAarch64Length: brotAndroidAarch64.Length,
		BrotDarwinX86_64Offset:   brotDarwinX86_64.Offset,
		BrotDarwinX86_64Length:   brotDarwinX86_64.Length,
		BrotDarwinAarch64Offset:  brotDarwinAarch64.Offset,
		BrotDarwinAarch64Length:  brotDarwinAarch64.Length,
		PoolOffset:               poolOffset,
		PoolLength:               poolLength,
	}

	bootTmplPath := filepath.Join(templatesDir, "bootstrap.sh.in")
	bootTmplBytes, err := os.ReadFile(bootTmplPath)
	if err != nil {
		return fmt.Errorf("read bootstrap template %s: %w", bootTmplPath, err)
	}

	// Normalize CRLF to LF
	bootTmplStr := string(bytes.ReplaceAll(bootTmplBytes, []byte("\r\n"), []byte("\n")))
	bootTmpl, err := template.New("bootstrap").Parse(bootTmplStr)
	if err != nil {
		return fmt.Errorf("parse bootstrap template: %w", err)
	}

	var renderedBoot bytes.Buffer
	if err := bootTmpl.Execute(&renderedBoot, bData); err != nil {
		return fmt.Errorf("execute bootstrap template: %w", err)
	}

	normBoot := bytes.ReplaceAll(renderedBoot.Bytes(), []byte("\r\n"), []byte("\n"))
	if bytes.IndexByte(normBoot, '\r') != -1 {
		return fmt.Errorf("bootstrap script contains forbidden CR (0x0D) characters")
	}
	if _, err := bundle.Write(normBoot); err != nil {
		return err
	}

	// Append 24-byte EOF trailer so Windows locates the same shared pool blob directly.
	var trailer [24]byte
	binary.LittleEndian.PutUint64(trailer[0:8], uint64(poolOffset))
	binary.LittleEndian.PutUint64(trailer[8:16], uint64(poolLength))
	copy(trailer[16:24], "MHBTTAIL")
	if _, err := bundle.Write(trailer[:]); err != nil {
		return err
	}

	// 5. Render dos_stub.txt.in
	dosTmplPath := filepath.Join(templatesDir, "dos_stub.txt.in")
	dosTmplBytes, err := os.ReadFile(dosTmplPath)
	if err != nil {
		return fmt.Errorf("read dos stub template %s: %w", dosTmplPath, err)
	}

	dosTmplStr := strings.TrimRight(string(bytes.ReplaceAll(dosTmplBytes, []byte("\r\n"), []byte("\n"))), "\r\n") + "\n"
	bootTmplDos, err := template.New("dos_stub").Parse(dosTmplStr)
	if err != nil {
		return fmt.Errorf("parse dos stub template: %w", err)
	}

	type DosData struct {
		BootstrapOffset int64
	}
	var renderedDos bytes.Buffer
	if err := bootTmplDos.Execute(&renderedDos, DosData{BootstrapOffset: bootstrapOffset + 1}); err != nil {
		return fmt.Errorf("execute dos stub template: %w", err)
	}

	dosBytes := bytes.ReplaceAll(renderedDos.Bytes(), []byte("\r\n"), []byte("\n"))
	if bytes.IndexByte(dosBytes, '\r') != -1 {
		return fmt.Errorf("DOS stub contains forbidden CR (0x0D) characters")
	}
	if len(dosBytes) > 59 {
		return fmt.Errorf("DOS stub command exceeds 59 bytes (got %d): %q", len(dosBytes), string(dosBytes))
	}

	// Patch bundle at offset 0
	completeBytes := bundle.Bytes()
	copy(completeBytes[0:], dosBytes)

	// Validate PE header integrity after patching
	if _, err := validatePE(completeBytes); err != nil {
		return fmt.Errorf("validate PE after patching DOS stub: %w", err)
	}

	if err := writeFileIfChanged(outputExe, completeBytes, 0o755); err != nil {
		return fmt.Errorf("write output executable %s: %w", outputExe, err)
	}

	return nil
}
