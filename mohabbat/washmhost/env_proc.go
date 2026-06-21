package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/tetratelabs/wazero/api"
)

func (h *HostEnv) sys_process_spawn(ctx context.Context, m api.Module, stack []uint64) {
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

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		parts := bytes.Split(cfg, []byte{0})
		if len(parts) == 0 || len(parts[0]) == 0 {
			h.fileOpsQueue <- func() {
				defer h.DecOpsFor(state)
				if !h.IsOpActive(ovPtr, state.opID) {
					return
				}
				h.mu.Lock()
				delete(h.activeOps, ovPtr)
				h.mu.Unlock()
				writeOverlapped(m, ovPtr, 22, 0, 0)
			}
			return
		}
		program := string(parts[0])
		args := []string{}
		envVars := []string{}
		cwd := ""
		stdio := []string{}
		section := 0 // 0=args, 1=env, 2=cwd, 3=stdio
		for i := 1; i < len(parts); i++ {
			if len(parts[i]) == 0 {
				section++
				if section > 3 {
					break
				}
				continue
			}
			switch section {
			case 0:
				args = append(args, string(parts[i]))
			case 1:
				envVars = append(envVars, string(parts[i]))
			case 2:
				if cwd == "" {
					cwd = h.translatePath(string(parts[i]))
				}
			case 3:
				stdio = append(stdio, string(parts[i]))
			}
		}

		program = h.translatePath(program)
		cmd := exec.Command(program, args...)
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
		if cwd != "" {
			cmd.Dir = cwd
		}

		if len(stdio) > 0 {
			var pipeHandles []uint64
			for i, spec := range stdio {
				var f *os.File
				if spec == "inherit" {
					switch i {
					case 0:
						f = os.Stdin
					case 1:
						f = os.Stdout
					case 2:
						f = os.Stderr
					}
				} else if spec == "pipe" {
					pr, pw, _ := os.Pipe()
					h.mu.Lock()
					ph := h.nextHandle
					h.nextHandle++
					if i == 0 {
						h.handles[ph] = pw // Parent writes to pw, child reads from pr
						f = pr
					} else {
						h.handles[ph] = pr // Parent reads from pr, child writes to pw
						f = pw
					}
					h.mu.Unlock()
					pipeHandles = append(pipeHandles, ph)
				} else if spec == "null" {
					f, _ = os.OpenFile(os.DevNull, os.O_RDWR, 0)
				} else if strings.HasPrefix(spec, "fd:") {
					handleStr := spec[3:]
					var handle uint64
					fmt.Sscanf(handleStr, "%d", &handle)
					h.mu.Lock()
					fAny, ok := h.handles[handle]
					h.mu.Unlock()
					if ok {
						if ff, ok := fAny.(*os.File); ok {
							f = ff
						}
					}
				}
				if f != nil {
					switch i {
					case 0:
						cmd.Stdin = f
					case 1:
						cmd.Stdout = f
					case 2:
						cmd.Stderr = f
					default:
						cmd.ExtraFiles = append(cmd.ExtraFiles, f)
					}
				}
			}
			// Pack pipe handles into resultExt (up to 3 handles, 16-bits each)
			// resultExt = (h3 << 32) | (h2 << 16) | h1
			var resExt uint64
			for j, ph := range pipeHandles {
				if j < 4 {
					resExt |= (ph & 0xFFFF) << (j * 16)
				}
			}
			state.reserved = resExt
		} else {
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		}

		err := cmd.Start()
		if err != nil {
			h.fileOpsQueue <- func() {
				defer h.DecOps()
				if !h.IsOpActive(ovPtr, state.opID) {
					return
				}
				h.mu.Lock()
				delete(h.activeOps, ovPtr)
				h.mu.Unlock()
				writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
			}
			return
		}

		h.mu.Lock()
		ph := h.nextHandle
		h.nextHandle++
		h.handles[ph] = cmd
		h.mu.Unlock()

		// resultExt = (pipeHandles << 32) | childHandle
		extResult := ph | (state.reserved << 32)

		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, 0, 0, extResult)
		}
	}()
}

func (h *HostEnv) sys_process_wait(ctx context.Context, m api.Module, stack []uint64) {
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

	state := h.RegisterOp(ovPtr, nil)
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
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, 0, 0, packed)
		}
	}()
}

func (h *HostEnv) sys_process_signal(ctx context.Context, m api.Module, stack []uint64) {
	processHandle := stack[0]
	signum := uint32(stack[1])

	h.mu.Lock()
	fAny, ok := h.handles[processHandle]
	h.mu.Unlock()

	if !ok {
		return
	}
	cmd, isCmd := fAny.(*exec.Cmd)
	if !isCmd || cmd == nil || cmd.Process == nil {
		return
	}

	var sig os.Signal
	switch signum {
	case 2:
		sig = syscall.SIGINT
	case 9:
		sig = syscall.SIGKILL
	case 15:
		sig = syscall.SIGTERM
	default:
		return
	}
	_ = cmd.Process.Signal(sig)
}

func (h *HostEnv) sys_signal_wait(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	signum := uint32(stack[1])

	h.mu.Lock()
	if old, exists := h.signalWaiters[signum]; exists {
		if !old.isCancelled {
			old.isCancelled = true
			delete(h.activeOps, old.ovPtr)
			h.mu.Unlock()
			h.DecOpsFor(old)
			h.mu.Lock()
		}
	}
	state := h.registerOpLocked(ovPtr, nil)
	state.signum = signum
	h.signalWaiters[signum] = state
	h.mu.Unlock()
}

func (h *HostEnv) sys_process_exit(ctx context.Context, m api.Module, stack []uint64) {
	code := int32(stack[0])
	h.mu.Lock()
	handlesCount := len(h.handles)
	timersCount := len(h.timers)
	waitersCount := len(h.signalWaiters)

	live := 0
	ghost := 0
	for _, op := range h.activeOps {
		if op.isCancelled {
			ghost++
		} else {
			live++
		}
	}
	h.mu.Unlock()
	debugLog("HOST: process_exit(%d) called.\n", code)
	debugLog("  Live Ops:  %d\n", live)
	debugLog("  Ghost Ops: %d\n", ghost)
	debugLog("  Handles:   %d, Timers: %d, SignalWaiters: %d\n",
		handlesCount, timersCount, waitersCount)

	h.Close()
	os.Exit(int(code))
}

func (h *HostEnv) sys_get_args(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	lenBytes := uint32(stack[1])

	args := h.args
	var bytesNeeded uint32
	for _, arg := range args {
		bytesNeeded += uint32(len(arg)) + 1
	}

	count := uint64(len(args))

	if ptr != 0 && lenBytes >= bytesNeeded {
		buf := make([]byte, bytesNeeded)
		offset := 0
		for _, arg := range args {
			copy(buf[offset:], arg)
			offset += len(arg)
			buf[offset] = 0
			offset++
		}
		if ok := m.Memory().Write(ptr, buf); !ok {
			panic("get_args: out of bounds")
		}
	}

	res := (count << 32) | uint64(bytesNeeded)
	stack[0] = api.EncodeI64(int64(res))
}

func (h *HostEnv) sys_get_env(ctx context.Context, m api.Module, stack []uint64) {
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
		buf := make([]byte, bytesNeeded)
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
		if ok := m.Memory().Write(ptr, buf); !ok {
			panic("get_env: out of bounds")
		}
	}

	res := (count << 32) | uint64(bytesNeeded)
	stack[0] = api.EncodeI64(int64(res))
}

func (h *HostEnv) sys_process_pipe(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])

	r, w, err := os.Pipe()
	if err != nil {
		writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
		return
	}

	h.mu.Lock()
	rh := h.nextHandle
	h.nextHandle++
	h.handles[rh] = r

	wh := h.nextHandle
	h.nextHandle++
	h.handles[wh] = w
	h.mu.Unlock()

	res := (uint64(wh) << 32) | uint64(rh)
	writeOverlapped(m, ovPtr, 0, 0, res)
}
