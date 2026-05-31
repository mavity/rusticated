package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"golang.org/x/term"
)

type HostEnv struct {
	mu           sync.Mutex
	timers       map[uint32]time.Time
	handles      map[uint64]interface{}
	nextHandle   uint64
	fileOpsQueue chan func()
	ttyRawState  *term.State
	ttyRawFd     int
}

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

type DirScan struct {
	Leftovers []byte
	Names     []string
	File      *os.File
}

func NewHostEnv() *HostEnv {
	env := &HostEnv{
		timers:       make(map[uint32]time.Time),
		handles:      make(map[uint64]interface{}),
		nextHandle:   3, // 0,1,2 reserved
		fileOpsQueue: make(chan func(), 100),
	}
	env.handles[0] = os.Stdin
	env.handles[1] = os.Stdout
	env.handles[2] = os.Stderr
	return env
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

func mapErrno(err error) uint32 {
	if err == nil {
		return 0
	}
	if os.IsNotExist(err) {
		return 2 // ENOENT
	}
	if os.IsPermission(err) {
		return 13 // EACCES
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	return 5 // EIO
}

func resolveUsableCwd() (string, error) {
	if cwd, err := os.Getwd(); err == nil {
		return cwd, nil
	}

	if veg := os.Getenv("MOHABBAT_VEGETABLE_PATH"); veg != "" {
		dir := filepath.Dir(veg)
		if err := os.Chdir(dir); err == nil {
			if cwd, err := os.Getwd(); err == nil {
				return cwd, nil
			}
		}
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if err := os.Chdir(dir); err == nil {
			if cwd, err := os.Getwd(); err == nil {
				return cwd, nil
			}
		}
	}

	return "", fmt.Errorf("unable to resolve cwd")
}

func debugLog(format string, args ...interface{}) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "washmhost-debug.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s ", time.Now().Format("15:04:05.000"))
	_, _ = fmt.Fprintf(f, format, args...)
	_, _ = f.Write([]byte("\n"))
}

func writeOverlapped(mod api.Module, ovPtr uint32, errorCode uint32, continued uint64, resultExt uint64) error {
	mem := mod.Memory()
	if mem == nil {
		return fmt.Errorf("no memory export")
	}

	buf, ok := mem.Read(ovPtr, 24)
	if !ok {
		return fmt.Errorf("ovPtr %d out of bounds", ovPtr)
	}

	binary.LittleEndian.PutUint32(buf[0:4], 1)
	binary.LittleEndian.PutUint32(buf[4:8], errorCode)
	binary.LittleEndian.PutUint64(buf[8:16], continued)
	binary.LittleEndian.PutUint64(buf[16:24], resultExt)

	return nil
}

func (h *HostEnv) Register(ctx context.Context, r wazero.Runtime) error {
	builder := r.NewHostModuleBuilder("env")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			panic("WASM Panicked!")
		}), []api.ValueType{}, []api.ValueType{}).
		Export("host_panic")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ns := time.Now().UnixNano()
			stack[0] = api.EncodeI64(int64(ns))
		}), []api.ValueType{}, []api.ValueType{api.ValueTypeI64}).
		Export("get_time")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ptr := uint32(stack[0])
			lenBytes := uint32(stack[1])

			mem := m.Memory()
			buf, ok := mem.Read(ptr, lenBytes)
			if !ok {
				panic("get_random: out of bounds")
			}
			rand.Read(buf)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("get_random")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ptr := uint32(stack[0])
			lenBytes := uint32(stack[1])

			args := os.Args
			var bytesNeeded uint32
			for _, arg := range args {
				bytesNeeded += uint32(len(arg)) + 1
			}

			count := uint64(len(args))

			if ptr != 0 && lenBytes >= bytesNeeded {
				mem := m.Memory()
				buf, ok := mem.Read(ptr, bytesNeeded)
				if !ok {
					panic("get_args: out of bounds")
				}
				offset := 0
				for _, arg := range args {
					copy(buf[offset:], arg)
					offset += len(arg)
					buf[offset] = 0
					offset++
				}
			}

			res := (count << 32) | uint64(bytesNeeded)
			stack[0] = api.EncodeI64(int64(res))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).
		Export("get_args")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ptr := uint32(stack[0])
			lenBytes := uint32(stack[1])

			vars := os.Environ()
			hasPWD := false
			for _, envVar := range vars {
				if strings.HasPrefix(envVar, "PWD=") {
					hasPWD = true
					break
				}
			}
			if !hasPWD {
				if cwd, err := resolveUsableCwd(); err == nil {
					vars = append(vars, "PWD="+cwd)
				}
			}

			var bytesNeeded uint32
			for _, envVar := range vars {
				parts := strings.SplitN(envVar, "=", 2)
				k, v := parts[0], ""
				if len(parts) > 1 {
					v = parts[1]
				}
				bytesNeeded += uint32(len(k)) + 1 + uint32(len(v)) + 1
			}

			count := uint64(len(vars))

			if ptr != 0 && lenBytes >= bytesNeeded {
				mem := m.Memory()
				buf, ok := mem.Read(ptr, bytesNeeded)
				if !ok {
					panic("get_env: out of bounds")
				}
				offset := 0
				for _, envVar := range vars {
					parts := strings.SplitN(envVar, "=", 2)
					k, v := parts[0], ""
					if len(parts) > 1 {
						v = parts[1]
					}
					copy(buf[offset:], k)
					offset += len(k)
					buf[offset] = '='
					offset++
					copy(buf[offset:], v)
					offset += len(v)
					buf[offset] = 0
					offset++
				}
			}

			res := (count << 32) | uint64(bytesNeeded)
			stack[0] = api.EncodeI64(int64(res))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).
		Export("get_env")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
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
				mem := m.Memory()
				buf, ok := mem.Read(ptr, bytesNeeded)
				if !ok {
					panic("get_cwd: out of bounds")
				}
				copy(buf, cwd)
			}

			res := (uint64(0) << 32) | uint64(bytesNeeded)
			debugLog("get_cwd ok: ptr=%d len=%d cwd=%q bytes=%d", ptr, lenBytes, cwd, bytesNeeded)
			stack[0] = api.EncodeI64(int64(res))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).
		Export("get_cwd")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
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
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("set_cwd")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ovPtr := uint32(stack[0])
			delayMs := uint32(stack[1])

			h.mu.Lock()
			defer h.mu.Unlock()
			h.timers[ovPtr] = time.Now().Add(time.Duration(delayMs) * time.Millisecond)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("timer_set")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ovPtr := uint32(stack[0])

			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.timers, ovPtr)
		}), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).
		Export("timer_cancel")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
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

			go func() {
				tmp := make([]byte, lenBytes)
				n, err := f.Read(tmp)
				retCode := uint32(0)
				if err != nil && err != io.EOF {
					retCode = mapErrno(err)
				}
				payload := tmp[:n]
				h.fileOpsQueue <- func() {
					if retCode == 0 {
						if ok := mem.Write(ptr, payload); !ok {
							writeOverlapped(m, ovPtr, 22, 0, 0)
							return
						}
					}
					writeOverlapped(m, ovPtr, retCode, 0, uint64(n))
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("read")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
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

			// Capture the slice so we don't hold the memory lock or pointer
			// However in Wazero memory is just a slice and doesn't get relocated
			// But creating a copy ensures we don't accidentally do undefined behavior if memory grows.
			dataCopy := make([]byte, lenBytes)
			copy(dataCopy, buf)

			go func() {
				n, err := f.Write(dataCopy)
				retCode := uint32(0)
				if err != nil {
					retCode = 5 // EIO
				}
				h.fileOpsQueue <- func() {
					writeOverlapped(m, ovPtr, retCode, 0, uint64(n))
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("write")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
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
		}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{}).
		Export("handle_close")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
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
			pathStr := string(buf)
			readFlag := (flags & 1) != 0
			writeFlag := (flags & 2) != 0
			createFlag := (flags & 4) != 0
			truncateFlag := (flags & 8) != 0
			appendFlag := (flags & 16) != 0
			createNewFlag := (flags & 32) != 0

			osFlags := 0
			if readFlag && writeFlag {
				osFlags |= os.O_RDWR
			} else if writeFlag {
				osFlags |= os.O_WRONLY
			} else {
				osFlags |= os.O_RDONLY
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
			if createNewFlag {
				osFlags |= os.O_EXCL | os.O_CREATE
			}

			go func() {
				f, err := os.OpenFile(pathStr, osFlags, 0666)
				// Windows directory handles often require directory-specific open semantics.
				// Treat zero-flags as read-only intent (used by wasm read_dir).
				if err != nil && !writeFlag && !createFlag && !truncateFlag && !appendFlag && !createNewFlag {
					f, err = os.Open(pathStr)
				}
				retCode := uint32(0)
				extResult := uint64(0)
				if err != nil {
					debugLog("path_open fail: path=%q flags=%d osFlags=%d err=%v", pathStr, flags, osFlags, err)
					fmt.Fprintf(os.Stderr, "file_open err: %s %v\n", pathStr, err)
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
					writeOverlapped(m, ovPtr, retCode, 0, extResult)
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("path_open")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
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
			f, isFile := fAny.(*os.File)
			var scan *DirScan
			if !isFile {
				scan, ok = fAny.(*DirScan)
				if !ok {
					writeOverlapped(m, ovPtr, 9, 0, 0)
					return
				}
			} else {
				// Convert to DirScan
				entries, _ := f.Readdirnames(-1)
				scan = &DirScan{Names: entries, File: f}
				h.mu.Lock()
				h.handles[handle] = scan // Cache it
				h.mu.Unlock()
			}

			mem := m.Memory()
			_, memOk := mem.Read(ptr, lenBytes)
			if !memOk {
				writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
				return
			}

			go func() {
				copied := 0
				var payload []byte
				for len(scan.Leftovers) < int(lenBytes) && len(scan.Names) > 0 {
					name := scan.Names[0]
					scan.Names = scan.Names[1:]
					scan.Leftovers = append(scan.Leftovers, []byte(name)...)
					scan.Leftovers = append(scan.Leftovers, 0)
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
				h.fileOpsQueue <- func() {
					if copied > 0 {
						if ok := mem.Write(ptr, payload); !ok {
							writeOverlapped(m, ovPtr, 22, 0, 0)
							return
						}
					}
					debugLog("dir_read: handle=%d copied=%d remaining=%d", handle, copied, len(scan.Names))
					writeOverlapped(m, ovPtr, 0, 0, uint64(copied))
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("dir_read")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
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
			pathStr := string(buf)

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
					retCode = mapErrno(err)
				} else if outLen < 64 {
					retCode = 34 // ERANGE
				} else {
					payload = marshalAbiStat(createAbiStat(fi))
				}
				h.fileOpsQueue <- func() {
					if retCode == 0 {
						if ok := mem.Write(outPtr, payload); !ok {
							writeOverlapped(m, ovPtr, 22, 0, 0)
							return
						}
					}
					writeOverlapped(m, ovPtr, retCode, 0, extResult)
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("path_stat")


	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ovPtr := uint32(stack[0])
			writeOverlapped(m, ovPtr, 38, 0, 0) // ENOSYS
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("net_open")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ovPtr := uint32(stack[0])
			writeOverlapped(m, ovPtr, 38, 0, 0) // ENOSYS
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64}, []api.ValueType{}).
		Export("net_accept")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ovPtr := uint32(stack[0])
			cfgPtr := uint32(stack[1])
			cfgLen := uint32(stack[2])

			mem := m.Memory()
			buf, ok := mem.Read(cfgPtr, cfgLen)
			if !ok {
				writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
				return
			}
			cfg := make([]byte, cfgLen)
			copy(cfg, buf)

			go func() {
				parts := bytes.Split(cfg, []byte{0})
				if len(parts) == 0 || len(parts[0]) == 0 {
					h.fileOpsQueue <- func() { writeOverlapped(m, ovPtr, 22, 0, 0) }
					return
				}
				program := string(parts[0])
				args := []string{}
				envVars := []string{}
				inEnv := false
				for i := 1; i < len(parts); i++ {
					if len(parts[i]) == 0 {
						if !inEnv {
							inEnv = true
							continue
						} else {
							break
						}
					}
					if inEnv {
						envVars = append(envVars, string(parts[i]))
					} else {
						args = append(args, string(parts[i]))
					}
				}

				cmd := exec.Command(program, args...)
				// Match std::process::Command semantics: inherit parent env,
				// then apply explicit key=value overrides from the guest.
				mergedEnv := append([]string{}, os.Environ()...)
				if len(envVars) > 0 {
					indexByKey := map[string]int{}
					for i, kv := range mergedEnv {
						if eq := strings.IndexByte(kv, '='); eq > 0 {
							indexByKey[kv[:eq]] = i
						}
					}
					for _, kv := range envVars {
						if eq := strings.IndexByte(kv, '='); eq > 0 {
							k := kv[:eq]
							if idx, ok := indexByKey[k]; ok {
								mergedEnv[idx] = kv
							} else {
								mergedEnv = append(mergedEnv, kv)
								indexByKey[k] = len(mergedEnv) - 1
							}
						}
					}
				}
				cmd.Env = mergedEnv
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr

				err := cmd.Start()
				retCode := uint32(0)
				extResult := uint64(0)
				if err != nil {
					retCode = mapErrno(err)
				} else {
					h.mu.Lock()
					h.nextHandle++
					handle := h.nextHandle
					h.handles[handle] = cmd
					h.mu.Unlock()
					extResult = handle
				}
				h.fileOpsQueue <- func() {
					writeOverlapped(m, ovPtr, retCode, 0, extResult)
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("process_spawn")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ovPtr := uint32(stack[0])
			handle := stack[1]

			h.mu.Lock()
			fAny, ok := h.handles[handle]
			h.mu.Unlock()

			if !ok {
				writeOverlapped(m, ovPtr, 9, 0, 0) // EBADF
				return
			}
			cmd, isCmd := fAny.(*exec.Cmd)
			if !isCmd || cmd == nil {
				writeOverlapped(m, ovPtr, 9, 0, 0) // EBADF
				return
			}

			go func() {
				err := cmd.Wait()
				exitCode := uint64(0)
				if cmd.ProcessState != nil {
					exitCode = uint64(uint32(cmd.ProcessState.ExitCode()))
				} else if err != nil {
					exitCode = 1
				}
				packed := (exitCode << 32) | (exitCode & 0xFFFF_FFFF)
				h.fileOpsQueue <- func() {
					writeOverlapped(m, ovPtr, 0, 0, packed)
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64}, []api.ValueType{}).
		Export("process_wait")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			// dummy - no op
		}), []api.ValueType{api.ValueTypeI64, api.ValueTypeI32}, []api.ValueType{}).
		Export("process_signal")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			// dummy - no op
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("signal_wait")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			mode := uint32(stack[1])

			h.mu.Lock()
			defer h.mu.Unlock()

			fd := int(os.Stdin.Fd())
			if mode == 1 {
				if h.ttyRawState == nil {
					state, err := term.MakeRaw(fd)
					if err == nil {
						h.ttyRawState = state
						h.ttyRawFd = fd
					}
				}
			} else {
				if h.ttyRawState != nil {
					_ = term.Restore(h.ttyRawFd, h.ttyRawState)
					h.ttyRawState = nil
					h.ttyRawFd = 0
				}
			}
		}), []api.ValueType{api.ValueTypeI64, api.ValueTypeI32}, []api.ValueType{}).
		Export("tty_set_mode")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			handle := stack[0]
			fd := int(os.Stdout.Fd())
			if handle == 0 {
				fd = int(os.Stdin.Fd())
			} else if handle == 2 {
				fd = int(os.Stderr.Fd())
			}

			cols, rows, err := term.GetSize(fd)
			if err != nil || cols <= 0 || rows <= 0 {
				cols, rows = 80, 24
			}
			stack[0] = uint64((uint32(cols) << 16) | uint32(rows))
		}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).
		Export("tty_get_size")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			code := int32(stack[0])
			os.Exit(int(code))
		}), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).
		Export("process_exit")

	_, err := builder.Instantiate(ctx)
	return err
}

func (h *HostEnv) Poll(ctx context.Context, mod api.Module) {
	// Process queued completions
	for {
		select {
		case op := <-h.fileOpsQueue:
			op()
		default:
			goto timers
		}
	}

timers:
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	for ovPtr, deadline := range h.timers {
		if now.After(deadline) {
			delete(h.timers, ovPtr)
			writeOverlapped(mod, ovPtr, 0, 0, 0)
		}
	}
}
