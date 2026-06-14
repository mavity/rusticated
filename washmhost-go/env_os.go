package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"time"

	"github.com/tetratelabs/wazero/api"
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
	nanos := int64(stack[1])

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
	timer := time.AfterFunc(time.Duration(nanos), func() {
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
