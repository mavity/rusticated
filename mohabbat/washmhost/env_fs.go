package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/tetratelabs/wazero/api"
)

type AbiStat struct {
	Kind       uint32
	Mode       uint32
	Uid        uint32
	Gid        uint32
	Size       uint64
	ModifiedNs int64
	AccessedNs int64
	ChangedNs  int64
	CreatedNs  int64
	Nlink      uint64
	Inode      uint64
	Dev        uint64
	Rdev       uint64
	Blksize    int64
	Blocks     int64
	ValidMask  uint64
}

const (
	statFlagNoFollow = 1
	statKindUnknown  = 0
	statKindFile     = 1
	statKindDir      = 2
	statKindSymlink  = 3
	statKindChar     = 4
	statKindBlock    = 5
	statKindFIFO     = 6
	statKindSocket   = 7
)

const (
	statValidUID uint64 = 1 << iota
	statValidGID
	statValidAtime
	statValidMtime
	statValidCtime
	statValidBirthtime
	statValidNlink
	statValidInode
	statValidDev
	statValidRdev
	statValidBlksize
	statValidBlocks
)

type dirEntryInfo struct {
	Name string
	Kind uint32
}

type DirScan struct {
	mu        sync.Mutex
	Leftovers []byte
	Entries   []dirEntryInfo
	Names     []string
	File      *os.File
}

func createAbiStat(fi os.FileInfo) AbiStat {
	return createAbiStatAt(fi, "", false)
}

func createAbiStatAt(fi os.FileInfo, hostPath string, noFollow bool) AbiStat {
	mode, kind := statModeAndKind(fi.Mode())
	stat := AbiStat{
		Kind:       kind,
		Mode:       mode,
		Size:       uint64(fi.Size()),
		ModifiedNs: fi.ModTime().UnixNano(),
		Blksize:    4096,
	}
	stat.ValidMask |= statValidMtime | statValidBlksize
	if fi.Size() >= 0 {
		stat.Blocks = fi.Size() / 512
		if fi.Size()%512 != 0 {
			stat.Blocks++
		}
		stat.ValidMask |= statValidBlocks
	}
	populatePlatformAbiStat(fi, hostPath, noFollow, &stat)
	return stat
}

func marshalAbiStat(stat AbiStat) []byte {
	payload := make([]byte, 112)
	binary.LittleEndian.PutUint32(payload[0:4], stat.Kind)
	binary.LittleEndian.PutUint32(payload[4:8], stat.Mode)
	binary.LittleEndian.PutUint32(payload[8:12], stat.Uid)
	binary.LittleEndian.PutUint32(payload[12:16], stat.Gid)
	binary.LittleEndian.PutUint64(payload[16:24], stat.Size)
	binary.LittleEndian.PutUint64(payload[24:32], uint64(stat.ModifiedNs))
	binary.LittleEndian.PutUint64(payload[32:40], uint64(stat.AccessedNs))
	binary.LittleEndian.PutUint64(payload[40:48], uint64(stat.ChangedNs))
	binary.LittleEndian.PutUint64(payload[48:56], uint64(stat.CreatedNs))
	binary.LittleEndian.PutUint64(payload[56:64], stat.Nlink)
	binary.LittleEndian.PutUint64(payload[64:72], stat.Inode)
	binary.LittleEndian.PutUint64(payload[72:80], stat.Dev)
	binary.LittleEndian.PutUint64(payload[80:88], stat.Rdev)
	binary.LittleEndian.PutUint64(payload[88:96], uint64(stat.Blksize))
	binary.LittleEndian.PutUint64(payload[96:104], uint64(stat.Blocks))
	binary.LittleEndian.PutUint64(payload[104:112], stat.ValidMask)
	return payload
}

func statModeAndKind(mode os.FileMode) (uint32, uint32) {
	rawMode := uint32(mode.Perm())
	switch {
	case mode&os.ModeSetuid != 0:
		rawMode |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		rawMode |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		rawMode |= 0o1000
	}
	switch {
	case mode.IsDir():
		return rawMode | 0o040000, statKindDir
	case mode&os.ModeSymlink != 0:
		return rawMode | 0o120000, statKindSymlink
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return rawMode | 0o020000, statKindChar
	case mode&os.ModeDevice != 0:
		return rawMode | 0o060000, statKindBlock
	case mode&os.ModeNamedPipe != 0:
		return rawMode | 0o010000, statKindFIFO
	case mode&os.ModeSocket != 0:
		return rawMode | 0o140000, statKindSocket
	default:
		return rawMode | 0o100000, statKindFile
	}
}

func statKindFromUnixMode(mode uint32) uint32 {
	switch mode & 0o170000 {
	case 0o100000:
		return statKindFile
	case 0o040000:
		return statKindDir
	case 0o120000:
		return statKindSymlink
	case 0o020000:
		return statKindChar
	case 0o060000:
		return statKindBlock
	case 0o010000:
		return statKindFIFO
	case 0o140000:
		return statKindSocket
	default:
		return statKindUnknown
	}
}

func (h *HostEnv) sys_read(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	handle := stack[1]
	ptr := uint32(stack[2])
	lenBytes := uint32(stack[3])
	debugLog("ABI read submitted: handle=%d buffer_bytes=%d", handle, lenBytes)

	h.mu.Lock()
	fAny, ok := h.handles[handle]
	h.mu.Unlock()

	if !ok {
		debugLog("ABI read rejected: unknown handle=%d", handle)
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	f, isReader := fAny.(interface{ Read([]byte) (int, error) })
	if !isReader {
		debugLog("ABI read rejected: handle=%d is not readable", handle)
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	mem := m.Memory()
	_, memOk := mem.Read(ptr, lenBytes)
	if !memOk {
		debugLog("ABI read rejected: guest memory range invalid ptr=%d len=%d", ptr, lenBytes)
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	state := h.RegisterOp(ovPtr, fAny)
	go func() {
		tmp := make([]byte, lenBytes)
		n, err := f.Read(tmp)
		retCode := uint32(0)
		if err != nil && err != io.EOF {
			retCode = mapErrno(err)
		}
		payload := tmp[:n]
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()

			if c, ok := fAny.(interface{ SetDeadline(time.Time) error }); ok {
				_ = c.SetDeadline(time.Time{})
			}

			if retCode == 0 {
				if ok := mem.Write(ptr, payload); !ok {
					debugLog("ABI read completion: guest memory write failed handle=%d bytes=%d", handle, n)
					writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
					return
				}
			}
			debugLog("ABI read completed: handle=%d bytes=%d error=%v wasi_error=%d", handle, n, err, retCode)
			writeOverlapped(m, ovPtr, retCode, 0, uint64(n))
		}
	}()
}

func (h *HostEnv) sys_seek(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	handle := stack[1]
	offset := int64(stack[2])
	whence := int(uint32(stack[3]))

	h.mu.Lock()
	fAny, ok := h.handles[handle]
	h.mu.Unlock()

	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	seeker, isSeeker := fAny.(interface {
		Seek(int64, int) (int64, error)
	})
	if !isSeeker {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}

	state := h.RegisterOp(ovPtr, fAny)
	go func() {
		newOff, err := seeker.Seek(offset, whence)
		retCode := uint32(0)
		if err != nil {
			retCode = mapErrno(err)
		}
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, uint64(newOff))
		}
	}()
}

func (h *HostEnv) sys_handle_truncate(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	handle := stack[1]
	length := int64(stack[2])

	h.mu.Lock()
	fAny, ok := h.handles[handle]
	h.mu.Unlock()
	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	if length < 0 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	truncater, canTruncate := fAny.(interface{ Truncate(int64) error })
	if !canTruncate {
		writeOverlapped(m, ovPtr, wasiENOTSUP, 0, 0)
		return
	}

	state := h.RegisterOp(ovPtr, fAny)
	go func() {
		err := truncater.Truncate(length)
		retCode := mapErrno(err)
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, 0)
		}
	}()
}

func (h *HostEnv) sys_write(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	handle := stack[1]
	ptr := uint32(stack[2])
	lenBytes := uint32(stack[3])
	if handle == 1 || handle == 2 {
		debugLog("ABI write submitted: stream=%d bytes=%d", handle, lenBytes)
	}

	h.mu.Lock()
	fAny, ok := h.handles[handle]
	h.mu.Unlock()

	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	f, isWriter := fAny.(interface{ Write([]byte) (int, error) })
	if !isWriter {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	mem := m.Memory()
	buf, memOk := mem.Read(ptr, lenBytes)
	if !memOk {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	dataCopy := make([]byte, lenBytes)
	copy(dataCopy, buf)

	if handle == 1 || handle == 2 {
		n, err := f.Write(dataCopy)
		retCode := uint32(0)
		if err != nil {
			retCode = mapErrno(err)
		}
		debugLog("ABI write completed: stream=%d bytes=%d written=%d error=%v wasi_error=%d", handle, lenBytes, n, err, retCode)
		writeOverlapped(m, ovPtr, retCode, 0, uint64(n))
		return
	}

	state := h.RegisterOp(ovPtr, fAny)
	go func() {
		n, err := f.Write(dataCopy)
		retCode := uint32(0)
		if err != nil {
			debugLog("sys_write fail: handle=%d err=%v", handle, err)
			retCode = mapErrno(err)
		}
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()

			if c, ok := fAny.(interface{ SetDeadline(time.Time) error }); ok {
				_ = c.SetDeadline(time.Time{})
			}

			writeOverlapped(m, ovPtr, retCode, 0, uint64(n))
		}
	}()
}

func (h *HostEnv) sys_handle_close(ctx context.Context, m api.Module, stack []uint64) {
	handle := stack[0]

	h.mu.Lock()
	defer h.mu.Unlock()
	if fAny, ok := h.handles[handle]; ok {
		if scan, isScan := fAny.(*DirScan); isScan && scan.File != nil {
			_ = scan.File.Close()
		}
		if f, isCloser := fAny.(interface{ Close() error }); isCloser {
			if handle >= 3 {
				f.Close()
			}
		}
		delete(h.handles, handle)
	}
}

func (h *HostEnv) sys_path_open(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	flags := uint32(stack[3])

	mem := m.Memory()
	buf, ok := mem.Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	rawPath := string(buf)
	hostPathStr := h.resolveGuestPathForHost(rawPath)

	// WASM flag mapping (standard for Go's wasip1/js):
	// Based on Go's internal/syscall/unix and syscall packages for wasm
	// O_RDONLY = 0
	// O_WRONLY = 1
	// O_RDWR   = 2
	// O_CREATE = 0x40  (64)
	// O_EXCL   = 0x80  (128)
	// O_TRUNC  = 0x200 (512)
	// O_APPEND = 0x400 (1024)

	rdwr := (flags & 3) == 2
	writeOnly := (flags & 3) == 1
	createFlag := (flags & 0x40) != 0
	exclFlag := (flags & 0x80) != 0
	truncateFlag := (flags & 0x200) != 0
	appendFlag := (flags & 0x400) != 0

	debugLog("DEBUG HOST path_open: path=%s flags=%d RDWR=%v WRONLY=%v CREATE=%v EXCL=%v TRUNC=%v", hostPathStr, flags, rdwr, writeOnly, createFlag, exclFlag, truncateFlag)

	osFlags := 0
	if rdwr {
		osFlags |= os.O_RDWR
	} else if writeOnly {
		osFlags |= os.O_WRONLY
	} else {
		// O_RDONLY is default 0
	}
	if createFlag {
		osFlags |= os.O_CREATE
	}
	if truncateFlag {
		osFlags |= os.O_TRUNC
	}
	if appendFlag {
		osFlags |= os.O_APPEND
	}
	if exclFlag {
		osFlags |= os.O_EXCL
	}

	debugLog("DEBUG HOST path_open: osFlags=%d", osFlags)

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		f, err := os.OpenFile(hostPathStr, osFlags, 0666)
		if err != nil && !rdwr && !writeOnly && !createFlag && !truncateFlag && !appendFlag && !exclFlag {
			f, err = os.Open(hostPathStr)
		}
		retCode := uint32(0)
		extResult := uint64(0)
		if err != nil {
			debugLog("path_open fail: path=%q flags=%d osFlags=%d err=%v", hostPathStr, flags, osFlags, err)
			retCode = mapErrno(err)
		} else {
			h.mu.Lock()
			handle := h.nextHandle
			h.nextHandle++
			h.handles[handle] = f
			h.mu.Unlock()
			extResult = handle
			debugLog("path_open ok: path=%q flags=%d osFlags=%d handle=%d", hostPathStr, flags, osFlags, handle)
		}
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				if err == nil {
					f.Close()
				}
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, extResult)
		}
	}()
}

func (h *HostEnv) sys_dir_read(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	handle := stack[1]
	ptr := uint32(stack[2])
	lenBytes := uint32(stack[3])

	mem := m.Memory()
	if _, memOk := mem.Read(ptr, lenBytes); !memOk {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	debugLog("dir_read: handle=%d ptr=%d len=%d", handle, ptr, lenBytes)

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		var scan *DirScan
		var retCode uint32
		var copied int
		var payload []byte

		h.mu.Lock()
		fAny, ok := h.handles[handle]
		if !ok {
			retCode = wasiEBADF
		} else {
			switch v := fAny.(type) {
			case *os.File:
				fi, err := v.Stat()
				if err != nil {
					retCode = mapErrno(err)
				} else if !fi.IsDir() {
					retCode = wasiENOTDIR
				} else {
					// Don't read all names yet
					scan = &DirScan{File: v}
					h.handles[handle] = scan
				}
			case *DirScan:
				scan = v
			default:
				retCode = wasiEBADF
			}
		}
		h.mu.Unlock()

		if retCode == 0 && scan != nil {
			scan.mu.Lock()
			// Refill if necessary
			for len(scan.Leftovers) < int(lenBytes) {
				if len(scan.Entries) == 0 {
					// Use ReadDir to get kind information without individual stats
					des, err := scan.File.ReadDir(32)
					if err != nil {
						if err != io.EOF {
							retCode = mapErrno(err)
						}
						break
					}
					for _, de := range des {
						kind := uint32(statKindUnknown)
						if de.IsDir() {
							kind = statKindDir
						} else if de.Type()&os.ModeSymlink != 0 {
							kind = statKindSymlink
						} else {
							kind = statKindFile
						}
						scan.Entries = append(scan.Entries, dirEntryInfo{Name: de.Name(), Kind: kind})
					}
				}
				if len(scan.Entries) > 0 {
					ent := scan.Entries[0]
					scan.Entries = scan.Entries[1:]
					if ent.Name == "." || ent.Name == ".." {
						continue
					}
					// Encoding: [1 byte kind][N bytes name][0 byte terminator]
					entry := append([]byte{byte(ent.Kind)}, []byte(ent.Name)...)
					entry = append(entry, 0)
					scan.Leftovers = append(scan.Leftovers, entry...)
				} else {
					break
				}
			}

			if len(scan.Leftovers) > 0 {
				toCopy := len(scan.Leftovers)
				if toCopy > int(lenBytes) {
					toCopy = int(lenBytes)
				}
				payload = make([]byte, toCopy)
				copy(payload, scan.Leftovers[:toCopy])
				scan.Leftovers = scan.Leftovers[toCopy:]
				copied = toCopy
			}
			scan.mu.Unlock()
		}

		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()

			if retCode == 0 && copied > 0 {
				if ok := mem.Write(ptr, payload); !ok {
					retCode = wasiEINVAL
				}
			}
			debugLog("dir_read: handle=%d copied=%d retCode=%d", handle, copied, retCode)
			writeOverlapped(m, ovPtr, retCode, 0, uint64(copied))
		}
	}()
}

func posixModeToGoFileMode(mode uint32) os.FileMode {
	goMode := os.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		goMode |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		goMode |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		goMode |= os.ModeSticky
	}
	return goMode
}

func (h *HostEnv) sys_path_stat(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	flags := uint32(stack[3])
	outPtr := uint32(stack[4])
	outLen := uint32(stack[5])

	mem := m.Memory()
	buf, ok := mem.Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	hostPathStr := h.resolveGuestPathForHost(string(buf))

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		var fi os.FileInfo
		var err error
		if (flags & statFlagNoFollow) != 0 {
			fi, err = os.Lstat(hostPathStr)
		} else {
			fi, err = os.Stat(hostPathStr)
		}
		retCode := uint32(0)
		extResult := uint64(112)
		var payload []byte
		if err != nil {
			debugLog("path_stat fail: path=%q err=%v", hostPathStr, err)
			retCode = mapErrno(err)
		} else if outLen < 112 {
			retCode = wasiERANGE
		} else {
			stat := createAbiStatAt(fi, hostPathStr, (flags&statFlagNoFollow) != 0)
			payload = marshalAbiStat(stat)
		}
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()

			if retCode == 0 {
				if ok := mem.Write(outPtr, payload); !ok {
					writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
					return
				}
			}
			writeOverlapped(m, ovPtr, retCode, 0, extResult)
		}
	}()
}

func (h *HostEnv) sys_path_link(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	sourceBytes, ok1 := m.Memory().Read(uint32(stack[1]), uint32(stack[2]))
	newPathBytes, ok2 := m.Memory().Read(uint32(stack[3]), uint32(stack[4]))
	flags := uint32(stack[5])
	if !ok1 || !ok2 || flags&^uint32(1) != 0 || len(sourceBytes) == 0 || len(newPathBytes) == 0 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	source := h.resolveGuestPathForHost(string(sourceBytes))
	newPath := h.resolveGuestPathForHost(string(newPathBytes))
	state := h.RegisterOp(ovPtr, nil)
	go func() {
		var err error
		if flags&1 != 0 {
			source, err = filepath.EvalSymlinks(source)
		} else if runtime.GOOS == "windows" {
			var info os.FileInfo
			info, err = os.Lstat(source)
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				err = errors.ErrUnsupported
			}
		}
		if err == nil {
			err = os.Link(source, newPath)
		}
		retCode := mapErrno(err)
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, 0)
		}
	}()
}

func (h *HostEnv) sys_path_symlink(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	targetBytes, ok1 := m.Memory().Read(uint32(stack[1]), uint32(stack[2]))
	linkPathBytes, ok2 := m.Memory().Read(uint32(stack[3]), uint32(stack[4]))
	targetKind := uint32(stack[5])
	if !ok1 || !ok2 || len(linkPathBytes) == 0 || targetKind > 2 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	target := string(targetBytes)
	linkPath := h.resolveGuestPathForHost(string(linkPathBytes))
	state := h.RegisterOp(ovPtr, nil)
	go func() {
		err := createHostSymlink(target, linkPath, targetKind)
		retCode := mapErrno(err)
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, 0)
		}
	}()
}

func (h *HostEnv) sys_path_readlink(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathBytes, ok := m.Memory().Read(uint32(stack[1]), uint32(stack[2]))
	outPtr := uint32(stack[3])
	outLen := uint32(stack[4])
	if !ok || len(pathBytes) == 0 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	if _, ok := m.Memory().Read(outPtr, outLen); !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	hostPath := h.resolveGuestPathForHost(string(pathBytes))
	state := h.RegisterOp(ovPtr, nil)
	go func() {
		target, err := os.Readlink(hostPath)
		retCode := mapErrno(err)
		payload := []byte(target)
		result := uint64(len(payload))
		if err == nil && uint64(len(payload)) > uint64(outLen) {
			retCode = wasiERANGE
			payload = nil
		}
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			if retCode == 0 && len(payload) > 0 {
				if !m.Memory().Write(outPtr, payload) {
					writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
					return
				}
			}
			writeOverlapped(m, ovPtr, retCode, 0, result)
		}
	}()
}

func (h *HostEnv) sys_path_chmod(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	mode := uint32(stack[3])

	mem := m.Memory()
	buf, ok := mem.Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	err := os.Chmod(h.resolveGuestPathForHost(string(buf)), posixModeToGoFileMode(mode))
	writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
}

func (h *HostEnv) sys_path_remove(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	flags := uint32(stack[3])

	buf, ok := m.Memory().Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0) // EINVAL
		return
	}
	if flags != 0 && flags != 0x200 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	path := h.resolveGuestPathForHost(string(buf))
	info, err := os.Lstat(path)
	if err != nil {
		writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
		return
	}
	if flags == 0x200 && !info.IsDir() {
		writeOverlapped(m, ovPtr, wasiENOTDIR, 0, 0)
		return
	}
	if flags == 0 && info.IsDir() {
		writeOverlapped(m, ovPtr, wasiEISDIR, 0, 0)
		return
	}
	err = os.Remove(path)
	writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
}

func (h *HostEnv) sys_path_mkdir(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	mode := uint32(stack[3])

	buf, ok := m.Memory().Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0) // EINVAL
		return
	}
	err := os.Mkdir(h.resolveGuestPathForHost(string(buf)), os.FileMode(mode))
	writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
}

func (h *HostEnv) sys_path_rename(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	oldPtr := uint32(stack[1])
	oldLen := uint32(stack[2])
	newPtr := uint32(stack[3])
	newLen := uint32(stack[4])

	mem := m.Memory()
	oldBuf, ok1 := mem.Read(oldPtr, oldLen)
	newBuf, ok2 := mem.Read(newPtr, newLen)
	if !ok1 || !ok2 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0) // EINVAL
		return
	}
	err := os.Rename(h.resolveGuestPathForHost(string(oldBuf)), h.resolveGuestPathForHost(string(newBuf)))
	writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
}

func (h *HostEnv) sys_get_cwd(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	lenBytes := uint32(stack[1])

	cwd := h.currentCwd()
	if cwd == "" {
		debugLog("get_cwd fail: ptr=%d len=%d cwd empty", ptr, lenBytes)
		res := uint64(wasiEINVAL) << 32
		stack[0] = api.EncodeI64(int64(res))
		return
	}

	bytesNeeded := uint32(len(cwd))
	if ptr != 0 && lenBytes >= bytesNeeded && bytesNeeded > 0 {
		if ok := m.Memory().Write(ptr, []byte(cwd)); !ok {
			panic("get_cwd: out of bounds")
		}
	}

	res := (uint64(0) << 32) | uint64(bytesNeeded)
	debugLog("get_cwd ok: ptr=%d len=%d cwd=%q bytes=%d", ptr, lenBytes, cwd, bytesNeeded)
	stack[0] = api.EncodeI64(int64(res))
}

func (h *HostEnv) sys_set_cwd(ctx context.Context, m api.Module, stack []uint64) {
	pathPtr := uint32(stack[0])
	pathLen := uint32(stack[1])

	mem := m.Memory()
	buf, ok := mem.Read(pathPtr, pathLen)
	if !ok {
		stack[0] = uint64(wasiEINVAL)
		return
	}

	guestPath := string(buf)
	if err := h.setCwd(guestPath); err != nil {
		debugLog("set_cwd fail: guestPath=%q err=%v", guestPath, err)
		stack[0] = uint64(mapErrno(err))
		return
	}
	debugLog("set_cwd ok: guestPath=%q cwd=%q", guestPath, h.currentCwd())
	stack[0] = 0
}
