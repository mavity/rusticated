package mohabbat

import (
	"fmt"
	"os"
)

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
			sectionEnd = sectionEnd + 0 // NO-OP
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

