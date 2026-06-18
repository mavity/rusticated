package mohabbat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/andybalholm/brotli"
)

// DoRefill is Mode 2: juice bottle refill.
// When running inside a vegetable (MOHABBAT_VEGETABLE_PATH is set), it reads
// the current vegetable, decompresses the pool, builds a new payload for
// projectDir, splices it in, recompresses, patches all meta structs, and
// writes the result to outputPath without rebuilding the native launchers.
func DoRefill(ws, projectDir, vegPath, outputPath string) error {
	fmt.Printf("🍆  Refill: %s -> %s\n", projectDir, outputPath)

	// 1. Read the vegetable file.
	vegData, err := os.ReadFile(vegPath)
	if err != nil {
		return fmt.Errorf("read vegetable %s: %w", vegPath, err)
	}
	fileLen := len(vegData)

	// 2. Find all valid MOHABBAT meta occurrences.
	//    Each brot executable embeds one meta struct right after the "MOHABBAT"
	//    magic bytes.  False positives (e.g. the "MOHABBAT_VEGETABLE_PATH"
	//    string literal inside the brot binary) are filtered out because their
	//    PoolLen value would be astronomically large (bytes of the "_VEGETABLE…"
	//    string interpreted as a little-endian u64).
	type metaOcc struct {
		offset int
		meta   mohabbatMeta
	}
	var occs []metaOcc
	magic := []byte(mohabbatMagic)
	const metaBodySize = 48 // 6 × uint64

	for i := 0; i+len(magic)+metaBodySize <= fileLen; i++ {
		if !bytes.Equal(vegData[i:i+len(magic)], magic) {
			continue
		}
		p := i + len(magic)
		poolLen := binary.LittleEndian.Uint64(vegData[p : p+8])
		// Reject: zero PoolLen (unpatched brot), and values larger than the file.
		if poolLen == 0 || uint64(fileLen) < poolLen {
			continue
		}
		// The reserved field Must be zero for a well-formed meta.
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
		occs = append(occs, metaOcc{offset: i, meta: m})
	}

	if len(occs) == 0 {
		return fmt.Errorf("no valid MOHABBAT meta found in vegetable %s", vegPath)
	}

	// Warn on inconsistency (should never happen with well-formed vegetables).
	base := occs[0].meta
	for _, occ := range occs[1:] {
		if occ.meta.PoolLen != base.PoolLen ||
			occ.meta.PayloadOffset != base.PayloadOffset ||
			occ.meta.PayloadLen != base.PayloadLen {
			fmt.Printf("🍆  warn: inconsistent meta across brot slots; using first occurrence\n")
			break
		}
	}

	// 3. Pool is at the tail of the file: vegData[poolStart:].
	poolStart := fileLen - int(base.PoolLen)
	if poolStart < 0 {
		return fmt.Errorf("invalid PoolLen %d > fileLen %d", base.PoolLen, fileLen)
	}

	// 4. Decompress pool.
	poolReader := brotli.NewReader(bytes.NewReader(vegData[poolStart:]))
	poolBytes, err := io.ReadAll(poolReader)
	if err != nil {
		return fmt.Errorf("decompress pool: %w", err)
	}
	fmt.Printf("🍆  Pool decompressed: %s -> %s\n",
		formatSize(int64(base.PoolLen)), formatSize(int64(len(poolBytes))))

	// 5. Build new payload WASM for projectDir.
	projectName := filepath.Base(projectDir)
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	if err := buildProjectToWasm(ws, projectDir, wasmPath); err != nil {
		return fmt.Errorf("build payload: %w", err)
	}
	newPayload, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("read new payload: %w", err)
	}
	fmt.Printf("🍆  New payload: %s (old: %s)\n",
		formatSize(int64(len(newPayload))), formatSize(int64(base.PayloadLen)))

	// 6. Build new pool: keep washmhost sections, replace payload.
	//    Pool layout: [washmhost_0][washmhost_1][washmhost_2][washmhost_3][payload]
	payloadOff := int(base.PayloadOffset)
	if payloadOff > len(poolBytes) {
		return fmt.Errorf("PayloadOffset %d exceeds decompressed pool size %d",
			payloadOff, len(poolBytes))
	}
	newPool := make([]byte, payloadOff+len(newPayload))
	copy(newPool, poolBytes[:payloadOff])
	copy(newPool[payloadOff:], newPayload)

	// 7. Recompress with maximum settings.
	newCompressed := &bytes.Buffer{}
	bw := brotli.NewWriterOptions(newCompressed, brotli.WriterOptions{Quality: 11, LGWin: 24})
	if _, err := bw.Write(newPool); err != nil {
		return fmt.Errorf("brotli write: %w", err)
	}
	if err := bw.Close(); err != nil {
		return fmt.Errorf("brotli close: %w", err)
	}
	newPoolLen := uint64(newCompressed.Len())
	fmt.Printf("🍆  Refill pool: old=%s new=%s\n",
		formatSize(int64(base.PoolLen)), formatSize(int64(newPoolLen)))

	// 8. Build output: Zone A+B (with patched metas) + new compressed pool.
	zoneAB := make([]byte, poolStart)
	copy(zoneAB, vegData[:poolStart])

	// Patch each meta occurrence in place.
	for _, occ := range occs {
		p := occ.offset + len(magic)
		// PoolLen → new compressed pool length.
		binary.LittleEndian.PutUint64(zoneAB[p:p+8], newPoolLen)
		// WashmhostOffset and WashmhostLen are per-slot and unchanged.
		// PayloadOffset is unchanged (payload still starts at the same position).
		binary.LittleEndian.PutUint64(zoneAB[p+24:p+32], base.PayloadOffset)
		// PayloadLen → new payload size.
		binary.LittleEndian.PutUint64(zoneAB[p+32:p+40], uint64(len(newPayload)))
		// Reserved stays 0.
	}

	// 9. Write output (0755 so it is executable on Unix).
	outData := append(zoneAB, newCompressed.Bytes()...)
	if err := os.WriteFile(outputPath, outData, 0o755); err != nil {
		return fmt.Errorf("write refilled vegetable: %w", err)
	}
	fmt.Printf("🍆  Wrote refilled vegetable: %s (%s bytes)\n",
		outputPath, formatSize(int64(len(outData))))
	return nil
}

