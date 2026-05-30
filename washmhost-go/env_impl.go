package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type HostEnv struct {
	mu           sync.Mutex
	timers       map[uint32]time.Time
	pendingReads map[uint32]*PendingOp
}

type PendingOp struct {
	ovPtr    uint32
	guestPtr uint32
	guestLen uint32
}

func NewHostEnv() *HostEnv {
	return &HostEnv{
		timers:       make(map[uint32]time.Time),
		pendingReads: make(map[uint32]*PendingOp),
	}
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

	// 1 = FLAG_COMPLETED
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
			fmt.Println("*** WASM INVOKED HOST PANIC ***")
			// Wazero doesn't have an easy host-panic bubble up besides panicking the goroutine
			// We'll panic here and Wazero will translate it to an error return
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
		// In Go, since CGO=0 we can't invoke BCryptGenRandom correctly without x/sys/windows.
		// Luckily `crypto/rand` works beautifully across platforms in Go out of the box!
		rand.Read(buf)
	}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
	Export("get_random")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			ptr := uint32(stack[0])
			lenBytes := uint32(stack[1])

			args := os.Args[1:] // skip our own host process name
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

			// Get env vars
			vars := os.Environ()
			// Need to split on `=` just like rust implementation does to mimic it.
			// Actually os.Environ returns "K=V", the rust implementation returned them mapped, and then recreated K=V
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

	_, err := builder.Instantiate(ctx)
	return err
}

func (h *HostEnv) Poll(ctx context.Context, mod api.Module) {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	for ovPtr, deadline := range h.timers {
		if now.After(deadline) {
			delete(h.timers, ovPtr)
			writeOverlapped(mod, ovPtr, 0, 0, 0)
		}
	}

	// In a real port we'll check I/O completion channels here
	// This mirrors the original select!() that Rust had.
}
