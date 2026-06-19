package main

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	ModifiedNs uint64
	AccessedNs uint64
	CreatedNs  uint64
	Nlink      uint64
	Inode      uint64
}

const (
	statFlagNoFollow = 1
	statKindUnknown  = 0
	statKindFile     = 1
	statKindDir      = 2
	statKindSymlink  = 3
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
	kind := uint32(statKindUnknown)
	if fi.IsDir() {
		kind = statKindDir
	} else if fi.Mode()&os.ModeSymlink != 0 {
		kind = statKindSymlink
	} else {
		kind = statKindFile
	}

	mode := uint32(fi.Mode().Perm())
	if fi.IsDir() {
		mode |= 0o040000
	} else if fi.Mode()&os.ModeSymlink != 0 {
		mode |= 0o120000
	} else {
		mode |= 0o100000
	}

	ns := uint64(fi.ModTime().UnixNano())
	return AbiStat{
		Kind:       kind,
		Mode:       mode,
		Uid:        0,
		Gid:        0,
		Size:       uint64(fi.Size()),
		ModifiedNs: ns,
		AccessedNs: ns,
		CreatedNs:  ns,
		Nlink:      1,
		Inode:      0,
	}
}

func marshalAbiStat(stat AbiStat) []byte {
	payload := make([]byte, 64)
	binary.LittleEndian.PutUint32(payload[0:4], stat.Kind)
	binary.LittleEndian.PutUint32(payload[4:8], stat.Mode)
	binary.LittleEndian.PutUint32(payload[8:12], stat.Uid)
	binary.LittleEndian.PutUint32(payload[12:16], stat.Gid)
	binary.LittleEndian.PutUint64(payload[16:24], stat.Size)
	binary.LittleEndian.PutUint64(payload[24:32], stat.ModifiedNs)
	binary.LittleEndian.PutUint64(payload[32:40], stat.AccessedNs)
	binary.LittleEndian.PutUint64(payload[40:48], stat.CreatedNs)
	binary.LittleEndian.PutUint64(payload[48:56], stat.Nlink)
	binary.LittleEndian.PutUint64(payload[56:64], stat.Inode)
	return payload
}

func (h *HostEnv) sys_read(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	handle := stack[1]
	ptr := uint32(stack[2])
	lenBytes := uint32(stack[3])

	h.mu.Lock()
	fAny, ok := h.handles[handle]
	h.mu.Unlock()

	if !ok {
		writeOverlapped(m, ovPtr, 9, 0, 0) // EBADF
		return
	}
	f, isReader := fAny.(interface{ Read([]byte) (int, error) })
	if !isReader {
		writeOverlapped(m, ovPtr, 9, 0, 0) // EBADF
		return
	}
	mem := m.Memory()
	_, memOk := mem.Read(ptr, lenBytes)
	if !memOk {
		writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
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
					writeOverlapped(m, ovPtr, 22, 0, 0)
					return
				}
			}
			writeOverlapped(m, ovPtr, retCode, 0, uint64(n))
		}
	}()
}

func (h *HostEnv) sys_write(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	handle := stack[1]
	ptr := uint32(stack[2])
	lenBytes := uint32(stack[3])

	h.mu.Lock()
	fAny, ok := h.handles[handle]
	h.mu.Unlock()

	if !ok {
		writeOverlapped(m, ovPtr, 9, 0, 0) // EBADF
		return
	}
	f, isWriter := fAny.(interface{ Write([]byte) (int, error) })
	if !isWriter {
		writeOverlapped(m, ovPtr, 9, 0, 0) // EBADF
		return
	}
	mem := m.Memory()
	buf, memOk := mem.Read(ptr, lenBytes)
	if !memOk {
		writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
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

func (h *HostEnv) translatePath(p string) string {
	// Normalize all slashes to / first to simplify prefix matching
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/tmp/") {
		return filepath.Join(os.TempDir(), p[5:])
	}
	if p == "/tmp" {
		return os.TempDir()
	}
	// Also handle cases where it might be relative or workspace-absolute.
	// In this simple host, we treat "/" as same as "" (relative to project root).
	// But let's keep it simple for now as most paths are either /tmp or relative.
	return filepath.FromSlash(p)
}

func (h *HostEnv) sys_path_open(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	flags := uint32(stack[3])

	mem := m.Memory()
	buf, ok := mem.Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
		return
	}
	rawPath := string(buf)
	pathStr := h.translatePath(rawPath)

	// Special-case /dev/tty BEFORE translation: on Windows filepath.FromSlash
	// would convert it to \dev\tty which would never match after translation.
	// Map it to stdin (the controlling terminal) so that bubbletea's
	// openInputTTY() succeeds.
	if rawPath == "/dev/tty" {
		h.mu.Lock()
		handle := h.nextHandle
		h.nextHandle++
		h.handles[handle] = os.Stdin
		h.mu.Unlock()
		state := h.RegisterOp(ovPtr, nil)
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				h.mu.Lock()
				delete(h.handles, handle)
				h.mu.Unlock()
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, 0, 0, handle)
		}
		return
	}

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

	debugLog("DEBUG HOST path_open: path=%s flags=%d RDWR=%v WRONLY=%v CREATE=%v EXCL=%v TRUNC=%v", pathStr, flags, rdwr, writeOnly, createFlag, exclFlag, truncateFlag)

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
		f, err := os.OpenFile(pathStr, osFlags, 0666)
		if err != nil && !rdwr && !writeOnly && !createFlag && !truncateFlag && !appendFlag && !exclFlag {
			f, err = os.Open(pathStr)
		}
		retCode := uint32(0)
		extResult := uint64(0)
		if err != nil {
			debugLog("path_open fail: path=%q flags=%d osFlags=%d err=%v", pathStr, flags, osFlags, err)
			retCode = mapErrno(err)
		} else {
			h.mu.Lock()
			handle := h.nextHandle
			h.nextHandle++
			h.handles[handle] = f
			h.mu.Unlock()
			extResult = handle
			debugLog("path_open ok: path=%q flags=%d osFlags=%d handle=%d", pathStr, flags, osFlags, handle)
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
		writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
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
			retCode = 9 // EBADF
		} else {
			switch v := fAny.(type) {
			case *os.File:
				fi, err := v.Stat()
				if err != nil {
					retCode = mapErrno(err)
				} else if !fi.IsDir() {
					retCode = 20 // ENOTDIR
				} else {
					// Don't read all names yet
					scan = &DirScan{File: v}
					h.handles[handle] = scan
				}
			case *DirScan:
				scan = v
			default:
				retCode = 9 // EBADF
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
					retCode = 22 // EINVAL
				}
			}
			debugLog("dir_read: handle=%d copied=%d retCode=%d", handle, copied, retCode)
			writeOverlapped(m, ovPtr, retCode, 0, uint64(copied))
		}
	}()
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
		writeOverlapped(m, ovPtr, 22, 0, 0)
		return
	}
	pathStr := h.translatePath(string(buf))
	debugLog("path_stat: path=%q flags=%d", pathStr, flags)

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		var fi os.FileInfo
		var err error
		if (flags & statFlagNoFollow) != 0 {
			fi, err = os.Lstat(pathStr)
		} else {
			fi, err = os.Stat(pathStr)
		}
		retCode := uint32(0)
		extResult := uint64(64)
		var payload []byte
		if err != nil {
			debugLog("path_stat fail: path=%q err=%v", pathStr, err)
			retCode = mapErrno(err)
		} else if outLen < 64 {
			retCode = 34 // ERANGE
		} else {
			payload = marshalAbiStat(createAbiStat(fi))
			debugLog("path_stat ok: path=%q kind=%d mode=%o", pathStr, createAbiStat(fi).Kind, createAbiStat(fi).Mode)
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
					writeOverlapped(m, ovPtr, 22, 0, 0)
					return
				}
			}
			writeOverlapped(m, ovPtr, retCode, 0, extResult)
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
		writeOverlapped(m, ovPtr, 22, 0, 0)
		return
	}

	err := os.Chmod(h.translatePath(string(buf)), os.FileMode(mode))
	writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
}

func (h *HostEnv) sys_path_remove(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])

	buf, ok := m.Memory().Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
		return
	}
	err := os.Remove(h.translatePath(string(buf)))
	writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
}

func (h *HostEnv) sys_path_mkdir(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	mode := uint32(stack[3])

	buf, ok := m.Memory().Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
		return
	}
	err := os.Mkdir(h.translatePath(string(buf)), os.FileMode(mode))
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
		writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
		return
	}
	err := os.Rename(h.translatePath(string(oldBuf)), h.translatePath(string(newBuf)))
	writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
}

func (h *HostEnv) sys_get_cwd(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	lenBytes := uint32(stack[1])

	cwd, err := resolveUsableCwd()
	if err != nil {
		debugLog("get_cwd fail: ptr=%d len=%d err=%v", ptr, lenBytes, err)
		res := uint64(mapErrno(err)) << 32
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
		stack[0] = uint64(22) // EINVAL
		return
	}

	err := os.Chdir(string(buf))
	if err != nil {
		debugLog("set_cwd fail: path=%q err=%v", string(buf), err)
		stack[0] = uint64(mapErrno(err))
		return
	}

	if cwd, err := os.Getwd(); err == nil {
		_ = os.Setenv("PWD", cwd)
		debugLog("set_cwd ok: path=%q cwd=%q", string(buf), cwd)
	}
	stack[0] = 0
}
