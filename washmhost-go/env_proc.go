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
				defer h.DecOps()
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
			defer h.DecOps()
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, extResult)
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
			defer h.DecOps()
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
	if _, exists := h.signalWaiters[signum]; exists {
		h.DecOps()
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
	fmt.Printf("HOST: process_exit(%d) called.\n", code)
	fmt.Printf("  Live Ops:  %d\n", live)
	fmt.Printf("  Ghost Ops: %d\n", ghost)
	fmt.Printf("  Handles:   %d, Timers: %d, SignalWaiters: %d\n",
		handlesCount, timersCount, waitersCount)
	os.Exit(int(code))
}

func (h *HostEnv) sys_get_args(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	lenBytes := uint32(stack[1])

	args := os.Args
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
