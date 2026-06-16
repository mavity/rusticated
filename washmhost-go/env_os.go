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
			h.DecOps()
			h.mu.Lock()
		}
	}
	state := h.registerOpLocked(ovPtr, nil)
	timer := time.AfterFunc(time.Duration(millis)*time.Millisecond, func() {
		h.fileOpsQueue <- func() {
			defer h.DecOps()
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
	fmt.Printf("GUEST: %s\n", string(buf))
}

func (h *HostEnv) sys_panic(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	lenBytes := uint32(stack[1])
	mem := m.Memory()
	buf, ok := mem.Read(ptr, lenBytes)
	if !ok {
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "GUEST PANIC: %s\n", string(buf))
	os.Exit(1)
}

func (h *HostEnv) sys_tty_set_mode(ctx context.Context, apiMod api.Module, stack []uint64) {
	handle := stack[0]
	mode := uint32(stack[1])

	h.mu.Lock()
	defer h.mu.Unlock()

	f, ok := h.handles[handle].(*os.File)
	if !ok {
		return
	}
	fd := int(f.Fd())

	if mode == 1 {
		if h.ttyRawState != nil {
			return
		}
		state, err := term.MakeRaw(fd)
		if err != nil {
			return
		}
		h.ttyRawState = state
		h.ttyRawFd = fd
	} else {
		if h.ttyRawState != nil {
			_ = term.Restore(h.ttyRawFd, h.ttyRawState)
			h.ttyRawState = nil
		}
	}
}

func (h *HostEnv) sys_tty_get_size(ctx context.Context, apiMod api.Module, stack []uint64) {
	handle := stack[0]

	h.mu.Lock()
	f, ok := h.handles[handle].(*os.File)
	h.mu.Unlock()

	if !ok {
		// Default to 80x24 if handle not found
		stack[0] = uint64(uint32(80)<<16 | uint32(24))
		return
	}

	width, height, err := term.GetSize(int(f.Fd()))
	if err != nil {
		width = 80
		height = 24
	}

	stack[0] = uint64(uint32(width)<<16 | uint32(height))
}
