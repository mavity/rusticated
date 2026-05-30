package main

import (
	"fmt"
	"io"
	"os"

	"github.com/andybalholm/brotli"
)

// readPool reads the washmhost and the payload from the parent vegetable specified by MOHABBAT_VEGETABLE_PATH.
func readPool() ([]byte, []byte, error) {
	vegPath := os.Getenv("MOHABBAT_VEGETABLE_PATH")
	if vegPath == "" {
		return nil, nil, fmt.Errorf("MOHABBAT_VEGETABLE_PATH environment variable not set")
	}

	f, err := os.Open(vegPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open parent vegetable: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to stat parent vegetable: %w", err)
	}

	size := info.Size()
	poolLen := int64(mohabbatMeta.PoolLen)
	if size < poolLen {
		return nil, nil, fmt.Errorf("file size %d is smaller than pool len %d", size, poolLen)
	}

	if _, err := f.Seek(size-poolLen, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("failed to seek to pool: %w", err)
	}

	decoder := brotli.NewReader(f)
	poolBytes, err := io.ReadAll(decoder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decompress pool: %w", err)
	}

	whOffset := mohabbatMeta.WashmhostOffset
	whLen := mohabbatMeta.WashmhostLen
	if uint64(len(poolBytes)) < whOffset+whLen {
		return nil, nil, fmt.Errorf("washmhost bounds out of range")
	}
	washmhostBytes := poolBytes[whOffset : whOffset+whLen]

	payloadOffset := mohabbatMeta.PayloadOffset
	payloadLen := mohabbatMeta.PayloadLen
	if uint64(len(poolBytes)) < payloadOffset+payloadLen {
		return nil, nil, fmt.Errorf("payload bounds out of range")
	}
	payloadBytes := poolBytes[payloadOffset : payloadOffset+payloadLen]

	return washmhostBytes, payloadBytes, nil
}
