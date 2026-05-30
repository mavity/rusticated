//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procVirtualAlloc   = kernel32.NewProc("VirtualAlloc")
	procVirtualProtect = kernel32.NewProc("VirtualProtect")
	procGetProcAddress = kernel32.NewProc("GetProcAddress")
	procLoadLibraryA   = kernel32.NewProc("LoadLibraryA")
)

const (
	MEM_COMMIT             = 0x1000
	MEM_RESERVE            = 0x2000
	PAGE_NOACCESS          = 0x01
	PAGE_READONLY          = 0x02
	PAGE_READWRITE         = 0x04
	PAGE_WRITECOPY         = 0x08
	PAGE_EXECUTE           = 0x10
	PAGE_EXECUTE_READ      = 0x20
	PAGE_EXECUTE_READWRITE = 0x40
	PAGE_EXECUTE_WRITECOPY = 0x80

	IMAGE_SCN_MEM_DISCARDABLE = 0x02000000
	IMAGE_SCN_MEM_EXECUTE     = 0x20000000
	IMAGE_SCN_MEM_READ        = 0x40000000
	IMAGE_SCN_MEM_WRITE       = 0x80000000

	IMAGE_DIRECTORY_ENTRY_IMPORT    = 1
	IMAGE_DIRECTORY_ENTRY_BASERELOC = 5
)

func runWashmhost(washmhostBytes []byte, payloadBytes []byte) (int, error) {
	if len(washmhostBytes) < 0x40 {
		return -1, fmt.Errorf("invalid DOS header")
	}

	lfanew := binary.LittleEndian.Uint32(washmhostBytes[0x3C:])
	if len(washmhostBytes) < int(lfanew)+24 {
		return -1, fmt.Errorf("invalid NT headers")
	}

	sig := binary.LittleEndian.Uint32(washmhostBytes[lfanew:])
	if sig != 0x00004550 { // "PE\0\0"
		return -1, fmt.Errorf("invalid PE signature")
	}

	machine := binary.LittleEndian.Uint16(washmhostBytes[lfanew+4:])
	if machine != 0x8664 && machine != 0xaa64 { // AMD64 and ARM64
		return -1, fmt.Errorf("unsupported machine type: %x", machine)
	}

	numSections := binary.LittleEndian.Uint16(washmhostBytes[lfanew+6:])
	sizeOfOptionalHeader := binary.LittleEndian.Uint16(washmhostBytes[lfanew+20:])
	optionalHeaderOffset := lfanew + 24

	magic := binary.LittleEndian.Uint16(washmhostBytes[optionalHeaderOffset:])
	if magic != 0x20B { // PE32+
		return -1, fmt.Errorf("expected PE32+ (64-bit)")
	}

	sizeOfImage := binary.LittleEndian.Uint32(washmhostBytes[optionalHeaderOffset+56:])
	sizeOfHeaders := binary.LittleEndian.Uint32(washmhostBytes[optionalHeaderOffset+60:])
	preferredBase := binary.LittleEndian.Uint64(washmhostBytes[optionalHeaderOffset+24:])
	addressOfEntryPoint := binary.LittleEndian.Uint32(washmhostBytes[optionalHeaderOffset+16:])

	numDataDirs := binary.LittleEndian.Uint32(washmhostBytes[optionalHeaderOffset+108:])
	dataDirsOffset := optionalHeaderOffset + 112

	getDataDir := func(idx uint32) (uint32, uint32) {
		if idx >= numDataDirs {
			return 0, 0
		}
		offset := dataDirsOffset + idx*8
		va := binary.LittleEndian.Uint32(washmhostBytes[offset:])
		size := binary.LittleEndian.Uint32(washmhostBytes[offset+4:])
		return va, size
	}

	imageBase, _, _ := procVirtualAlloc.Call(uintptr(preferredBase), uintptr(sizeOfImage), MEM_RESERVE|MEM_COMMIT, PAGE_READWRITE)
	if imageBase == 0 {
		imageBase, _, _ = procVirtualAlloc.Call(0, uintptr(sizeOfImage), MEM_RESERVE|MEM_COMMIT, PAGE_READWRITE)
		if imageBase == 0 {
			return -1, fmt.Errorf("failed to allocate memory for image")
		}
	}
	baseAddr := uintptr(imageBase)

	deref := func(offset uint32) *byte {
		return (*byte)(unsafe.Pointer(uintptr(imageBase) + uintptr(offset)))
	}
	sliceData := func(offset, length uint32) []byte {
		return unsafe.Slice(deref(offset), length)
	}

	// Copy Headers
	headerDest := unsafe.Slice((*byte)(unsafe.Pointer(baseAddr)), sizeOfHeaders)
	copy(headerDest, washmhostBytes[:sizeOfHeaders])

	// Copy Sections
	sectionTableOffset := optionalHeaderOffset + uint32(sizeOfOptionalHeader)
	for i := 0; i < int(numSections); i++ {
		secOff := sectionTableOffset + uint32(i*40)
		virtualSize := binary.LittleEndian.Uint32(washmhostBytes[secOff+8:])
		virtualAddr := binary.LittleEndian.Uint32(washmhostBytes[secOff+12:])
		sizeOfRawData := binary.LittleEndian.Uint32(washmhostBytes[secOff+16:])
		pointerToRawData := binary.LittleEndian.Uint32(washmhostBytes[secOff+20:])

		if sizeOfRawData > 0 {
			copySize := sizeOfRawData
			if virtualSize > 0 && virtualSize < sizeOfRawData {
				copySize = virtualSize
			}
			dest := sliceData(virtualAddr, copySize)
			copy(dest, washmhostBytes[pointerToRawData:pointerToRawData+copySize])
		}
	}

	// Relocations
	relocationDelta := int64(baseAddr) - int64(preferredBase)
	if relocationDelta != 0 {
		relocsVA, relocsSize := getDataDir(IMAGE_DIRECTORY_ENTRY_BASERELOC)
		if relocsVA != 0 && relocsSize > 0 {
			relocEnd := relocsVA + relocsSize
			curr := relocsVA
			for curr < relocEnd {
				pageRVA := binary.LittleEndian.Uint32(sliceData(curr, 4))
				blockSize := binary.LittleEndian.Uint32(sliceData(curr+4, 4))
				if blockSize == 0 {
					break
				}
				numEntries := (blockSize - 8) / 2
				entries := sliceData(curr+8, numEntries*2)
				for j := uint32(0); j < numEntries; j++ {
					entry := binary.LittleEndian.Uint16(entries[j*2:])
					relType := entry >> 12
					offset := entry & 0x0FFF
					if relType == 10 { // IMAGE_REL_BASED_DIR64
						targetPtr := (*uint64)(unsafe.Pointer(deref(pageRVA + uint32(offset))))
						*targetPtr = uint64(int64(*targetPtr) + relocationDelta)
					}
				}
				curr += blockSize
			}
		}
	}

	// Imports
	importsVA, importsSize := getDataDir(IMAGE_DIRECTORY_ENTRY_IMPORT)
	if importsVA != 0 && importsSize > 0 {
		curr := importsVA
		for {
			nameRVA := binary.LittleEndian.Uint32(sliceData(curr+12, 4))
			if nameRVA == 0 {
				break
			}

			dllNamePtr := deref(nameRVA)
			dllNameRaw := make([]byte, 0, 128)
			for i := 0; i < 128; i++ {
				c := *(*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(dllNamePtr)) + uintptr(i)))
				if c == 0 {
					break
				}
				dllNameRaw = append(dllNameRaw, c)
			}
			dllNameRaw = append(dllNameRaw, 0)

			hLib, _, _ := procLoadLibraryA.Call(uintptr(unsafe.Pointer(&dllNameRaw[0])))
			if hLib == 0 {
				return -1, fmt.Errorf("failed to load import library: %s", string(dllNameRaw))
			}

			funcRef := binary.LittleEndian.Uint32(sliceData(curr+16, 4))
			origThunk := binary.LittleEndian.Uint32(sliceData(curr, 4))
			if origThunk == 0 {
				origThunk = funcRef
			}

			thunkCurr := origThunk
			funcCurr := funcRef
			for {
				thunkData := binary.LittleEndian.Uint64(sliceData(thunkCurr, 8))
				if thunkData == 0 {
					break
				}

				var procAddr uintptr
				if (thunkData & 0x8000000000000000) != 0 {
					ordinal := thunkData & 0xFFFF
					procAddr, _, _ = procGetProcAddress.Call(hLib, uintptr(ordinal))
				} else {
					nameThunkRVA := uint32(thunkData & 0x7FFFFFFF)
					importNamePtr := deref(nameThunkRVA + 2) // skip hint
					procAddr, _, _ = procGetProcAddress.Call(hLib, uintptr(unsafe.Pointer(importNamePtr)))
				}

				if procAddr == 0 {
					return -1, fmt.Errorf("failed to resolve import in %s", string(dllNameRaw))
				}

				targetPtr := (*uint64)(unsafe.Pointer(deref(funcCurr)))
				*targetPtr = uint64(procAddr)

				thunkCurr += 8
				funcCurr += 8
			}

			curr += 20
		}
	}

	// Protect sections
	for i := 0; i < int(numSections); i++ {
		secOff := sectionTableOffset + uint32(i*40)
		virtualSize := binary.LittleEndian.Uint32(washmhostBytes[secOff+8:])
		virtualAddr := binary.LittleEndian.Uint32(washmhostBytes[secOff+12:])
		characteristics := binary.LittleEndian.Uint32(washmhostBytes[secOff+36:])

		if virtualSize == 0 {
			continue
		}

		var protect uint32 = PAGE_NOACCESS
		executable := (characteristics & IMAGE_SCN_MEM_EXECUTE) != 0
		readable := (characteristics & IMAGE_SCN_MEM_READ) != 0
		writable := (characteristics & IMAGE_SCN_MEM_WRITE) != 0

		if executable {
			if readable && writable {
				protect = PAGE_EXECUTE_READWRITE
			} else if readable {
				protect = PAGE_EXECUTE_READ
			} else if writable {
				protect = PAGE_EXECUTE_WRITECOPY
			} else {
				protect = PAGE_EXECUTE
			}
		} else {
			if readable && writable {
				protect = PAGE_READWRITE
			} else if readable {
				protect = PAGE_READONLY
			} else if writable {
				protect = PAGE_WRITECOPY
			}
		}

		var oldProtect uint32
		procVirtualProtect.Call(baseAddr+uintptr(virtualAddr), uintptr(virtualSize), uintptr(protect), uintptr(unsafe.Pointer(&oldProtect)))
	}

	if addressOfEntryPoint == 0 {
		return -1, fmt.Errorf("entry point not found in loaded executable")
	}
	
	// Pass payload to the embedded Go executable via Environment
	var ptr uintptr
	if len(payloadBytes) > 0 {
		ptr = uintptr(unsafe.Pointer(&payloadBytes[0]))
	}
	os.Setenv("WASHMHOST_PAYLOAD_PTR", fmt.Sprintf("%d", ptr))
	os.Setenv("WASHMHOST_PAYLOAD_LEN", fmt.Sprintf("%d", len(payloadBytes)))

	entryAddr := baseAddr + uintptr(addressOfEntryPoint)

	// Since the embedded binary is a full Go EXE, its entry point takes 0 args and calls exit() or returns.
	// But it will do its own OS-level setup because it's an EXE entrypoint (the Go runtime init).
	syscall.SyscallN(entryAddr)

	return 0, nil
}
