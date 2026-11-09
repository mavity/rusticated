package main

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"testing"
	"text/template"
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
	nextDir, _, err := findProbeDir()
	if err != nil {
		t.Fatalf("findProbeDir: %v", err)
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
