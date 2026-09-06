package mohabbat

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// DoRefill is Mode 2: juice bottle refill.
// When running inside a vegetable, it reads the current vegetable,
// decompresses the pool, builds a new payload for projectDir, splices it in,
// recompresses, and updates Zone A header metadata without rebuilding or
// modifying the native launchers.
func DoRefill(ws, projectDir, vegPath, outputPath string, verbose bool) error {
	fmt.Printf("🍆  Refill: %s -> %s\n", projectDir, outputPath)

	// 1. Read the vegetable file.
	vegData, err := os.ReadFile(vegPath)
	if err != nil {
		return fmt.Errorf("read vegetable %s: %w", vegPath, err)
	}
	fileLen := len(vegData)

	// 2. Parse layout metadata from Zone A script header.
	poolLen, payloadOffset, payloadLen, err := parseVegetableMeta(vegData)
	if err != nil {
		return fmt.Errorf("parse vegetable metadata: %w", err)
	}

	// 3. Pool is at the tail of the file: vegData[poolStart:].
	poolStart := fileLen - int(poolLen)
	if poolStart < 0 {
		return fmt.Errorf("invalid PoolLen %d > fileLen %d", poolLen, fileLen)
	}

	// 4. Decompress pool.
	poolReader := brotli.NewReader(bytes.NewReader(vegData[poolStart:]))
	poolBytes, err := io.ReadAll(poolReader)
	if err != nil {
		return fmt.Errorf("decompress pool: %w", err)
	}
	fmt.Printf("🍆  Pool decompressed: %s -> %s\n",
		formatSize(int64(poolLen)), formatSize(int64(len(poolBytes))))

	// 5. Build new payload WASM for projectDir.
	projectName := filepath.Base(projectDir)
	wasmPath := filepath.Join(ws, "target", projectName+".wasm")
	if err := buildProjectToWasm(ws, projectDir, wasmPath, verbose); err != nil {
		return fmt.Errorf("build payload: %w", err)
	}
	newPayload, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("read new payload: %w", err)
	}
	fmt.Printf("🍆  New payload: %s (old: %s)\n",
		formatSize(int64(len(newPayload))), formatSize(int64(payloadLen)))

	// 6. Build new pool: keep washmhost sections, replace payload.
	payloadOff := int(payloadOffset)
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
		formatSize(int64(poolLen)), formatSize(int64(newPoolLen)))

	// 8. Build output: Zone A+B (with updated Zone A header) + new compressed pool.
	zoneAB := make([]byte, poolStart)
	copy(zoneAB, vegData[:poolStart])

	// Update POSIX script variables (exact length preserved)
	zoneAB = replaceExactPadded(zoneAB, `:; P_LEN="`, fmt.Sprintf("%-20d", newPoolLen))
	zoneAB = replaceExactPadded(zoneAB, `:; PLD_LEN="`, fmt.Sprintf("%-20d", len(newPayload)))

	// Update Windows batch variables (exact length preserved)
	zoneAB = replaceExactPadded(zoneAB, `set "P_LEN=`, fmt.Sprintf("%-20d", newPoolLen))
	zoneAB = replaceExactPadded(zoneAB, `set "PLD_LEN=`, fmt.Sprintf("%-20d", len(newPayload)))

	// Update Node starter variables if present
	zoneAB = replaceExactPadded(zoneAB, `const poolLen = "`, fmt.Sprintf("%-17d", newPoolLen))
	zoneAB = replaceExactPadded(zoneAB, `const payloadLen = "`, fmt.Sprintf("%-21d", len(newPayload)))

	// 9. Write output (0755 so it is executable on Unix).
	outData := append(zoneAB, newCompressed.Bytes()...)
	if err := os.WriteFile(outputPath, outData, 0o755); err != nil {
		return fmt.Errorf("write refilled vegetable: %w", err)
	}
	fmt.Printf("🍆  Wrote refilled vegetable: %s (%s bytes)\n",
		outputPath, formatSize(int64(len(outData))))
	return nil
}

func parseVegetableMeta(vegData []byte) (poolLen, payloadOffset, payloadLen uint64, err error) {
	headerLimit := 10000
	if len(vegData) < headerLimit {
		headerLimit = len(vegData)
	}
	header := vegData[:headerLimit]

	extract := func(prefix string) (uint64, error) {
		idx := bytes.Index(header, []byte(prefix))
		if idx == -1 {
			return 0, fmt.Errorf("marker %q not found in header", prefix)
		}
		start := idx + len(prefix)
		end := bytes.IndexAny(header[start:], "\"\r\n")
		if end == -1 {
			return 0, fmt.Errorf("unterminated value for %q", prefix)
		}
		valStr := strings.TrimSpace(string(header[start : start+end]))
		val, parseErr := strconv.ParseUint(valStr, 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parsing %q value %q: %w", prefix, valStr, parseErr)
		}
		return val, nil
	}

	poolLen, err = extract(`P_LEN="`)
	if err != nil {
		return 0, 0, 0, err
	}
	payloadOffset, err = extract(`PLD_OFF="`)
	if err != nil {
		return 0, 0, 0, err
	}
	payloadLen, err = extract(`PLD_LEN="`)
	if err != nil {
		return 0, 0, 0, err
	}
	return poolLen, payloadOffset, payloadLen, nil
}

func replaceExactPadded(data []byte, prefix, newVal string) []byte {
	idx := bytes.Index(data, []byte(prefix))
	if idx == -1 {
		return data
	}
	start := idx + len(prefix)
	if start+len(newVal) <= len(data) {
		copy(data[start:start+len(newVal)], []byte(newVal))
	}
	return data
}
