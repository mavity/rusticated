package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type HostEnv struct {
	mu           sync.Mutex
	timers       map[uint32]time.Time
	handles      map[uint64]interface{}
	stats        map[uint64]*StatInfo
	nextHandle   uint64
	nextStat     uint64
	fileOpsQueue chan func()
}

type StatInfo struct {
	Len       uint64
	IsDir     bool
	IsSymlink bool
	Readonly  bool
	Mode      uint32
	Nlink     uint64
	Uid       uint32
	Gid       uint32
	Inode     uint64
	MtimeNs   uint64
	AtimeNs   uint64
	CtimeNs   uint64
}

type DirScan struct {
	Leftovers []byte
	Names     []string
}

func NewHostEnv() *HostEnv {
	env := &HostEnv{
		timers:       make(map[uint32]time.Time),
		handles:      make(map[uint64]interface{}),
		stats:        make(map[uint64]*StatInfo),
		nextHandle:   3, // 0,1,2 reserved
		nextStat:     1,
		fileOpsQueue: make(chan func(), 100),
	}
	env.handles[0] = os.Stdin
	env.handles[1] = os.Stdout
	env.handles[2] = os.Stderr
	return env
}

func createStatInfo(fi os.FileInfo) *StatInfo {
	return &StatInfo{
		Len:       uint64(fi.Size()),
		IsDir:     fi.IsDir(),
		IsSymlink: fi.Mode()&os.ModeSymlink != 0,
		Readonly:  fi.Mode().Perm()&0222 == 0,
		Mode:      uint32(fi.Mode().Perm()),
		Nlink:     1,
		Uid:       0,
		Gid:       0,
		Inode:     0,
		MtimeNs:   uint64(fi.ModTime().UnixNano()),
		AtimeNs:   uint64(fi.ModTime().UnixNano()),
		CtimeNs:   uint64(fi.ModTime().UnixNano()),
	}
}

func (h *HostEnv) getStat(handle uint64) *StatInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stats[handle]
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
			buf, memOk := mem.Read(ptr, lenBytes)
			if !memOk {
				writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
				return
			}

			go func() {
				n, err := f.Read(buf)
				retCode := uint32(0)
				if err != nil && err != io.EOF {
					retCode = 5 // EIO
				}
				h.fileOpsQueue <- func() {
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
				retCode := uint32(0)
				extResult := uint64(0)
				if err != nil {
					fmt.Fprintf(os.Stderr, "file_open err: %s %v\n", pathStr, err)
					retCode = 5 // EIO
				} else {
					h.mu.Lock()
					handle := h.nextHandle
					h.nextHandle++
					h.handles[handle] = f
					h.mu.Unlock()
					extResult = handle
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
				scan = &DirScan{Names: entries}
				h.mu.Lock()
				h.handles[handle] = scan // Cache it
				h.mu.Unlock()
			}

			mem := m.Memory()
			buf, memOk := mem.Read(ptr, lenBytes)
			if !memOk {
				writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
				return
			}

			go func() {
				copied := 0
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
					copy(buf, scan.Leftovers[:toCopy])
					scan.Leftovers = scan.Leftovers[toCopy:]
					copied = toCopy
				}
				h.fileOpsQueue <- func() {
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

			mem := m.Memory()
			buf, ok := mem.Read(pathPtr, pathLen)
			if !ok {
				writeOverlapped(m, ovPtr, 22, 0, 0)
				return
			}
			pathStr := string(buf)

			go func() {
				fi, err := os.Stat(pathStr)
				retCode := uint32(0)
				extResult := uint64(0)
				if err != nil {
					retCode = 5 // EIO
				} else {
					h.mu.Lock()
					h.nextStat++
					statHandle := h.nextStat
					h.stats[statHandle] = createStatInfo(fi)
					h.mu.Unlock()
					extResult = statHandle
				}
				h.fileOpsQueue <- func() {
					writeOverlapped(m, ovPtr, retCode, 0, extResult)
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("path_stat")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ovPtr := uint32(stack[0])
			pathPtr := uint32(stack[1])
			pathLen := uint32(stack[2])

			mem := m.Memory()
			buf, ok := mem.Read(pathPtr, pathLen)
			if !ok {
				writeOverlapped(m, ovPtr, 22, 0, 0)
				return
			}
			pathStr := string(buf)

			go func() {
				fi, err := os.Lstat(pathStr)
				retCode := uint32(0)
				extResult := uint64(0)
				if err != nil {
					retCode = 5 // EIO
				} else {
					h.mu.Lock()
					h.nextStat++
					statHandle := h.nextStat
					h.stats[statHandle] = createStatInfo(fi)
					h.mu.Unlock()
					extResult = statHandle
				}
				h.fileOpsQueue <- func() {
					writeOverlapped(m, ovPtr, retCode, 0, extResult)
				}
			}()
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("path_lstat")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = s.Len
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI64}).Export("stat_len")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil && s.IsDir {
			stack[0] = 1
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).Export("stat_is_dir")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil && !s.IsDir && !s.IsSymlink {
			stack[0] = 1
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).Export("stat_is_file")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = s.MtimeNs
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI64}).Export("stat_mtime")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = s.AtimeNs
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI64}).Export("stat_atime")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = s.CtimeNs
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI64}).Export("stat_ctime")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil && s.IsSymlink {
			stack[0] = 1
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).Export("stat_is_symlink")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil && s.Readonly {
			stack[0] = 1
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).Export("stat_readonly")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = uint64(s.Mode)
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).Export("stat_mode")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = s.Nlink
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI64}).Export("stat_nlink")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = uint64(s.Uid)
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).Export("stat_uid")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = uint64(s.Gid)
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).Export("stat_gid")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		if s := h.getStat(stack[0]); s != nil {
			stack[0] = s.Inode
		} else {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI64}).Export("stat_inode")

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
					retCode = 38 // ENOSYS (or equivalent)
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
				status := uint64(0)
				if cmd.ProcessState != nil {
					status = uint64(cmd.ProcessState.ExitCode() & 0xFF)
				} else if err != nil {
					status = 1
				}
				h.fileOpsQueue <- func() {
					writeOverlapped(m, ovPtr, 0, 0, status)
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
			// dummy - no op
		}), []api.ValueType{api.ValueTypeI64, api.ValueTypeI32}, []api.ValueType{}).
		Export("tty_set_mode")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			stack[0] = uint64((80 << 16) | 24)
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
