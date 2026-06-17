package main

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSysMisc(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x1000)

	t.Run("1. sys_debug_log valid", func(t *testing.T) {
		msg := "Hello Debug"
		ptr := uint32(0x100)
		mod.Memory().Write(ptr, []byte(msg))

		// This will print to stdout, hard to capture without redirecting,
		// but we can check it doesn't panic.
		env.sys_debug_log(context.Background(), mod, []uint64{uint64(ptr), uint64(len(msg))})
	})

	t.Run("2. sys_debug_log invalid ptr", func(t *testing.T) {
		env.sys_debug_log(context.Background(), mod, []uint64{999999, 10})
	})

	t.Run("3. sys_signal_wait registration", func(t *testing.T) {
		ovPtr := uint32(0x200)
		env.sys_signal_wait(context.Background(), mod, []uint64{uint64(ovPtr), 2}) // SIGINT (2)

		env.mu.Lock()
		_, ok := env.signalWaiters[2]
		env.mu.Unlock()
		if !ok {
			t.Error("signal waiter not registered")
		}

		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})
	})

	t.Run("4. sys_signal_wait fire", func(t *testing.T) {
		ovPtr := uint32(0x300)
		env.sys_signal_wait(context.Background(), mod, []uint64{uint64(ovPtr), 2}) // SIGINT

		// Manually push to the internal channel to simulate signal arrival
		env.signals <- syscall.SIGINT

		// Give time for the multiplexer goroutine to work
		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		signum := binary.LittleEndian.Uint64(val[16:24])
		if signum != 2 {
			t.Errorf("expected signum 2 in resultExt, got %d", signum)
		}
	})

	t.Run("5. sys_signal_wait overwrite", func(t *testing.T) {
		ovPtr := uint32(0x400)
		env.sys_signal_wait(context.Background(), mod, []uint64{uint64(ovPtr), 15}) // SIGTERM
		env.sys_signal_wait(context.Background(), mod, []uint64{uint64(0x500), 15}) // override same signum

		env.mu.Lock()
		state := env.signalWaiters[15]
		env.mu.Unlock()
		if state.ovPtr != 0x500 {
			t.Error("failed to overwrite signal waiter")
		}

		env.sys_cancel(context.Background(), mod, []uint64{0x500})
	})

	t.Run("6. mapErrno translation", func(t *testing.T) {
		if mapErrno(os.ErrNotExist) != 2 {
			t.Errorf("ENOENT mismatch: got %d", mapErrno(os.ErrNotExist))
		}
		if mapErrno(os.ErrPermission) != 13 {
			t.Errorf("EACCES mismatch: got %d", mapErrno(os.ErrPermission))
		}
		ebadf := mapErrno(syscall.EBADF)
		if ebadf == 0 {
			t.Error("EBADF should not be 0")
		}
		if mapErrno(errors.New("random")) != 5 { // EIO
			t.Error("EIO mismatch")
		}
		if mapErrno(nil) != 0 {
			t.Error("nil error should be 0")
		}
	})

	t.Run("7. writeOverlapped OOB", func(t *testing.T) {
		err := writeOverlapped(mod, 999999, 0, 0, 0)
		if err == nil {
			t.Error("expected error for OOB write")
		}
	})

	t.Run("8. writeOverlapped success", func(t *testing.T) {
		ptr := uint32(0x600)
		err := writeOverlapped(mod, ptr, 123, 456, 789)
		if err != nil {
			t.Fatal(err)
		}

		val, _ := mod.Memory().Read(ptr, 24)
		if binary.LittleEndian.Uint32(val[0:4]) != 1 {
			t.Error("version mismatch")
		}
		if binary.LittleEndian.Uint32(val[4:8]) != 123 {
			t.Error("error code mismatch")
		}
		if binary.LittleEndian.Uint64(val[8:16]) != 456 {
			t.Error("continued mismatch")
		}
		if binary.LittleEndian.Uint64(val[16:24]) != 789 {
			t.Error("resultExt mismatch")
		}
	})

	t.Run("9. sys_signal_wait cancellation", func(t *testing.T) {
		ovPtr := uint32(0x700)
		env.sys_signal_wait(context.Background(), mod, []uint64{uint64(ovPtr), 2})
		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})

		env.mu.Lock()
		_, ok := env.signalWaiters[2]
		env.mu.Unlock()
		if ok {
			t.Error("waiter should be removed on cancel")
		}
	})

	t.Run("10. sys_get_args with nil module", func(t *testing.T) {
		// Should not panic if ptr is 0
		env.sys_get_args(context.Background(), mod, []uint64{0, 0})
	})

	t.Run("11. sys_get_env with nil module", func(t *testing.T) {
		env.sys_get_env(context.Background(), mod, []uint64{0, 0})
	})

	t.Run("12. Poll with no work", func(t *testing.T) {
		if env.Poll(context.Background(), mod) {
			t.Error("Poll should return false when no work is pending")
		}
	})

	t.Run("13. resolveUsableCwd", func(t *testing.T) {
		cwd, err := resolveUsableCwd()
		if err != nil {
			t.Fatal(err)
		}
		if cwd == "" {
			t.Error("empty cwd")
		}
	})

	t.Run("14. sys_debug_log extreme length", func(t *testing.T) {
		ptr := uint32(0x800)
		env.sys_debug_log(context.Background(), mod, []uint64{uint64(ptr), 0xFFFFFFFF})
	})
}
