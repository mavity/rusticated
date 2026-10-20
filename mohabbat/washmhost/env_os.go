package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"time"

	"github.com/tetratelabs/wazero/api"
	"golang.org/x/term"
)

func (h *HostEnv) sys_time_now(ctx context.Context, m api.Module, stack []uint64) {
	now := time.Now().UnixNano()
	stack[0] = uint64(now)
}

func (h *HostEnv) sys_get_time(ctx context.Context, apiMod api.Module, stack []uint64) {
	ns := time.Now().UnixNano()
	stack[0] = uint64(ns)
}

func (h *HostEnv) sys_get_random(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	lenBytes := uint32(stack[1])

	buf := make([]byte, lenBytes)
	_, _ = rand.Read(buf)
	if ok := m.Memory().Write(ptr, buf); !ok {
		panic("get_random: out of bounds")
	}
}

func (h *HostEnv) sys_timer_set(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	millis := int64(stack[1])

	h.mu.Lock()
	if old, exists := h.timers[ovPtr]; exists {
		old.Stop()
		delete(h.timers, ovPtr)
		if state, ok := h.activeOps[ovPtr]; ok && !state.isCancelled {
			state.isCancelled = true
			h.mu.Unlock()
			h.DecOpsFor(state)
			h.mu.Lock()
		}
	}
	state := h.registerOpLocked(ovPtr, nil)
	timer := time.AfterFunc(time.Duration(millis)*time.Millisecond, func() {
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			delete(h.timers, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, 0, 0, 0)
		}
	})
	h.timers[ovPtr] = timer
	h.mu.Unlock()
}

func (h *HostEnv) sys_cancel(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	h.CancelOp(ovPtr)
}

func (h *HostEnv) sys_debug_log(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	lenBytes := uint32(stack[1])
	mem := m.Memory()
	buf, ok := mem.Read(ptr, lenBytes)
	if !ok {
		return
	}
	debugLog("GUEST: %s\n", string(buf))
}

func (h *HostEnv) sys_panic(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	lenBytes := uint32(stack[1])
	mem := m.Memory()
	buf, ok := mem.Read(ptr, lenBytes)
	if !ok {
		debugLog("ABI host_panic: invalid guest memory range ptr=%d len=%d", ptr, lenBytes)
		os.Exit(1)
	}
	debugLog("ABI host_panic: %s", string(buf))
	fmt.Fprintf(os.Stderr, "GUEST PANIC: %s\n", string(buf))
	os.Exit(1)
}

func (h *HostEnv) sys_tty_set_mode(ctx context.Context, apiMod api.Module, stack []uint64) {
	handle := stack[0]
	mode := uint32(stack[1])
	debugLog("ABI tty_set_mode: handle=%d mode=%d", handle, mode)

	h.mu.Lock()
	defer h.mu.Unlock()

	f, ok := h.handles[handle].(*os.File)
	if !ok {
		debugLog("ABI tty_set_mode: handle=%d is not an OS file", handle)
		return
	}
	fd := int(f.Fd())

	if mode == 1 {
		if h.ttyRawState != nil {
			debugLog("ABI tty_set_mode: raw mode already active on fd=%d", h.ttyRawFd)
			return
		}
		state, err := term.MakeRaw(fd)
		if err != nil {
			debugLog("ABI tty_set_mode: entering raw mode failed: fd=%d error=%v", fd, err)
			return
		}
		h.ttyRawState = state
		h.ttyRawFd = fd
		debugLog("ABI tty_set_mode: raw mode enabled on fd=%d", fd)
	} else {
		if h.ttyRawState != nil {
			err := term.Restore(h.ttyRawFd, h.ttyRawState)
			debugLog("ABI tty_set_mode: restored fd=%d error=%v", h.ttyRawFd, err)
			h.ttyRawState = nil
		} else {
			debugLog("ABI tty_set_mode: restore requested but raw mode was not active")
		}
	}
}

func (h *HostEnv) sys_tty_get_size(ctx context.Context, apiMod api.Module, stack []uint64) {
	handle := stack[0]
	debugLog("ABI tty_get_size: handle=%d", handle)

	h.mu.Lock()
	fAny, ok := h.handles[handle]
	h.mu.Unlock()

	if !ok {
		// Default to 80x24 if handle not found
		stack[0] = uint64(uint32(80)<<16 | uint32(24))
		debugLog("ABI tty_get_size: unknown handle; returning fallback 80x24")
		return
	}

	if f, ok := fAny.(*os.File); ok {
		width, height, err := term.GetSize(int(f.Fd()))
		if err != nil {
			debugLog("ABI tty_get_size: fd=%d size query failed: %v; returning fallback 80x24", f.Fd(), err)
			width = 80
			height = 24
		}
		stack[0] = uint64(uint32(width)<<16 | uint32(height))
		debugLog("ABI tty_get_size: fd=%d result=%dx%d", f.Fd(), width, height)
		return
	}

	stack[0] = uint64(uint32(80)<<16 | uint32(24))
	debugLog("ABI tty_get_size: handle=%d is not an OS file; returning fallback 80x24", handle)
}

func (h *HostEnv) sys_tty_set_size(ctx context.Context, apiMod api.Module, stack []uint64) {
	handle := stack[0]
	cols := int(stack[1])
	rows := int(stack[2])
	debugLog("ABI tty_set_size: handle=%d cols=%d rows=%d", handle, cols, rows)

	h.mu.Lock()
	fAny, ok := h.handles[handle]
	h.mu.Unlock()

	if !ok {
		stack[0] = uint64(wasiEBADF)
		debugLog("ABI tty_set_size failed: unknown handle=%d", handle)
		return
	}

	if pty, ok := fAny.(PtySession); ok {
		err := pty.Resize(cols, rows)
		if err != nil {
			stack[0] = uint64(mapErrno(err))
			debugLog("ABI tty_set_size PTY resize failed: %v", err)
		} else {
			stack[0] = uint64(wasiSuccess)
			debugLog("ABI tty_set_size PTY resize succeeded")
		}
		return
	}

	if f, ok := fAny.(*os.File); ok {
		if !term.IsTerminal(int(f.Fd())) {
			stack[0] = uint64(wasiENOTTY)
			debugLog("ABI tty_set_size rejected non-terminal fd=%d", f.Fd())
			return
		}
		stack[0] = uint64(wasiSuccess)
		debugLog("ABI tty_set_size accepted terminal fd=%d", f.Fd())
		return
	}

	stack[0] = uint64(wasiENOTTY)
	debugLog("ABI tty_set_size rejected non-file handle=%d", handle)
}

// sys_fd_isatty implements the "env" import fd_isatty(fd int32) int32.
// The guest fd is treated as a handle (fds 0/1/2 alias the standard-stream handles).
// Returns 1 if the underlying OS file is a terminal, 0 otherwise.
func (h *HostEnv) sys_fd_isatty(ctx context.Context, apiMod api.Module, stack []uint64) {
	fd := int64(stack[0]) // fd is passed as i32 but wazero sign-extends into uint64

	h.mu.Lock()
	fAny, ok := h.handles[uint64(fd)]
	h.mu.Unlock()

	if !ok {
		stack[0] = 0
		debugLog("ABI fd_isatty: fd=%d unknown result=false", fd)
		return
	}
	if _, ok := fAny.(PtySession); ok {
		stack[0] = 1
		debugLog("ABI fd_isatty: fd=%d is PTY result=true", fd)
		return
	}
	if f, ok := fAny.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		stack[0] = 1
		debugLog("ABI fd_isatty: fd=%d host_fd=%d result=true", fd, f.Fd())
		return
	}
	stack[0] = 0
	debugLog("ABI fd_isatty: fd=%d result=false", fd)
}
