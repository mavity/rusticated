package main

import (
	"context"
	"encoding/binary"
	"testing"
	"time"
)

func timerDelayMillis(delay time.Duration) uint64 {
	return uint64(delay / time.Millisecond)
}

func TestSysTimerSetExtended(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x100000)

	waitForCompletion := func(t *testing.T, ovPtr uint32) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			header, ok := mod.Memory().Read(ovPtr, 4)
			if !ok {
				t.Fatalf("overlapped pointer %d is out of bounds", ovPtr)
			}
			if binary.LittleEndian.Uint32(header) == 1 {
				result, ok := mod.Memory().Read(ovPtr, 24)
				if !ok {
					t.Fatalf("overlapped result at %d is out of bounds", ovPtr)
				}
				return result
			}
			env.Poll(ctx, mod)
			if ctx.Err() != nil {
				t.Fatalf("timeout waiting for timer completion at %d", ovPtr)
			}
		}
	}

	setTimer := func(t *testing.T, ovPtr uint32, delay time.Duration) {
		t.Helper()
		if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
			t.Fatalf("failed to initialize overlapped request at %d", ovPtr)
		}
		env.sys_timer_set(context.Background(), mod, []uint64{uint64(ovPtr), timerDelayMillis(delay)})
	}

	assertSuccessfulCompletion := func(t *testing.T, ovPtr uint32) {
		t.Helper()
		result := waitForCompletion(t, ovPtr)
		if got := binary.LittleEndian.Uint32(result[4:8]); got != wasiSuccess {
			t.Errorf("timer completion errno = %d, want %d", got, wasiSuccess)
		}
		if got := binary.LittleEndian.Uint64(result[16:24]); got != 0 {
			t.Errorf("timer completion result = %d, want 0", got)
		}
	}

	assertNoTimer := func(t *testing.T, ovPtr uint32) {
		t.Helper()
		env.mu.Lock()
		_, exists := env.timers[ovPtr]
		env.mu.Unlock()
		if exists {
			t.Errorf("timer at %d should be removed", ovPtr)
		}
	}

	t.Run("1. basic timer set and fire", func(t *testing.T) {
		ovPtr := uint32(0x100)
		setTimer(t, ovPtr, 10*time.Millisecond)
		if got := env.PendingOps(); got != 1 {
			t.Fatalf("pending ops after timer set = %d, want 1", got)
		}

		assertSuccessfulCompletion(t, ovPtr)
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after timer fire = %d, want 0", got)
		}
		assertNoTimer(t, ovPtr)
	})

	t.Run("2. timer overwrite stops old timer", func(t *testing.T) {
		ovPtr := uint32(0x200)
		setTimer(t, ovPtr, time.Hour)
		if got := env.PendingOps(); got != 1 {
			t.Fatalf("pending ops after first timer = %d, want 1", got)
		}

		env.sys_timer_set(context.Background(), mod, []uint64{uint64(ovPtr), timerDelayMillis(time.Millisecond)})
		if got := env.PendingOps(); got != 1 {
			t.Fatalf("pending ops after overwrite = %d, want 1", got)
		}

		assertSuccessfulCompletion(t, ovPtr)
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after replacement timer fired = %d, want 0", got)
		}
		assertNoTimer(t, ovPtr)
	})

	t.Run("3. zero timeout fires quickly", func(t *testing.T) {
		ovPtr := uint32(0x300)
		setTimer(t, ovPtr, 0)
		assertSuccessfulCompletion(t, ovPtr)
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after zero timer fired = %d, want 0", got)
		}
		assertNoTimer(t, ovPtr)
	})

	t.Run("4. cancelled timer does not write back", func(t *testing.T) {
		ovPtr := uint32(0x400)
		setTimer(t, ovPtr, time.Hour)
		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})

		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after cancellation = %d, want 0", got)
		}
		val, ok := mod.Memory().Read(ovPtr, 24)
		if !ok {
			t.Fatal("failed to read cancelled timer request")
		}
		if binary.LittleEndian.Uint32(val[:4]) != 0 {
			t.Error("cancelled timer should not have written to memory")
		}
		assertNoTimer(t, ovPtr)
	})

	t.Run("5. sys_cancel also works for timers", func(t *testing.T) {
		ovPtr := uint32(0x500)
		setTimer(t, ovPtr, time.Hour)
		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})

		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after cancellation = %d, want 0", got)
		}
		assertNoTimer(t, ovPtr)
	})

	t.Run("6. timer firing removes from timers map", func(t *testing.T) {
		ovPtr := uint32(0x600)
		setTimer(t, ovPtr, time.Millisecond)
		assertSuccessfulCompletion(t, ovPtr)
		assertNoTimer(t, ovPtr)
	})

	t.Run("7. high frequency timer set", func(t *testing.T) {
		ovPtr := uint32(0x700)
		setTimer(t, ovPtr, time.Hour)
		for i := 0; i < 50; i++ {
			env.sys_timer_set(context.Background(), mod, []uint64{uint64(ovPtr), timerDelayMillis(time.Millisecond)})
		}
		if got := env.PendingOps(); got != 1 {
			t.Fatalf("pending ops after repeated overwrite = %d, want 1", got)
		}

		assertSuccessfulCompletion(t, ovPtr)
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after final timer fired = %d, want 0", got)
		}
		assertNoTimer(t, ovPtr)
	})

	t.Run("8. multiple concurrent timers", func(t *testing.T) {
		const count = 20
		ovPtrs := make([]uint32, count)
		for i := 0; i < count; i++ {
			ovPtrs[i] = uint32(0x800 + i*32)
			setTimer(t, ovPtrs[i], time.Millisecond)
		}
		if got := env.PendingOps(); got != count {
			t.Fatalf("pending ops after setting timers = %d, want %d", got, count)
		}

		for _, ovPtr := range ovPtrs {
			assertSuccessfulCompletion(t, ovPtr)
			assertNoTimer(t, ovPtr)
		}
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after all timers fired = %d, want 0", got)
		}
	})

	t.Run("9. large timeout behavior", func(t *testing.T) {
		ovPtr := uint32(0xD80)
		setTimer(t, ovPtr, 24*time.Hour)
		if got := env.PendingOps(); got != 1 {
			t.Fatalf("pending ops for long timer = %d, want 1", got)
		}
		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after cancelling long timer = %d, want 0", got)
		}
		assertNoTimer(t, ovPtr)
	})

	t.Run("10. timer fire after host_cancel", func(t *testing.T) {
		for len(env.fileOpsQueue) > 0 {
			env.Poll(context.Background(), mod)
		}

		ovPtr := uint32(0xA00)
		setTimer(t, ovPtr, 2*time.Millisecond)

		deadline := time.Now().Add(5 * time.Second)
		for len(env.fileOpsQueue) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("timer callback was not queued")
			}
			time.Sleep(time.Millisecond)
		}

		env.sys_timer_set(context.Background(), mod, []uint64{uint64(ovPtr), timerDelayMillis(time.Hour)})
		if got := env.PendingOps(); got != 1 {
			t.Fatalf("pending ops after replacing queued timer = %d, want 1", got)
		}
		env.Poll(context.Background(), mod)

		val, ok := mod.Memory().Read(ovPtr, 24)
		if !ok {
			t.Fatal("failed to read timer request")
		}
		if binary.LittleEndian.Uint32(val[:4]) != 0 {
			t.Error("superseded timer should not have written back")
		}

		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after cancelling replacement = %d, want 0", got)
		}
		assertNoTimer(t, ovPtr)
	})

	t.Run("11. timer set near end of memory", func(t *testing.T) {
		ovPtr := uint32(0x100000 - 32)
		setTimer(t, ovPtr, time.Millisecond)
		assertSuccessfulCompletion(t, ovPtr)
	})

	t.Run("12. Poll processes multiple timers", func(t *testing.T) {
		const count = 5
		ovPtrs := make([]uint32, count)
		for i := 0; i < count; i++ {
			ovPtrs[i] = uint32(0xB00 + i*32)
			setTimer(t, ovPtrs[i], time.Millisecond)
		}
		if got := env.PendingOps(); got != count {
			t.Fatalf("pending ops after setting timers = %d, want %d", got, count)
		}

		for _, ovPtr := range ovPtrs {
			assertSuccessfulCompletion(t, ovPtr)
		}
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after Poll processed all timers = %d, want 0", got)
		}
	})

	t.Run("13. timer precision check (approximate)", func(t *testing.T) {
		ovPtr := uint32(0xC00)
		delay := 20 * time.Millisecond
		start := time.Now()
		setTimer(t, ovPtr, delay)

		assertSuccessfulCompletion(t, ovPtr)
		elapsed := time.Since(start)
		if elapsed < delay {
			t.Errorf("timer fired too early: %v", elapsed)
		}
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after precision timer fired = %d, want 0", got)
		}
	})

	t.Run("14. proactor queue pressure from timers", func(t *testing.T) {
		const count = 100
		ovPtrs := make([]uint32, count)
		for i := 0; i < count; i++ {
			ovPtrs[i] = uint32(0xD00 + i*32)
			setTimer(t, ovPtrs[i], time.Millisecond)
		}
		if got := env.PendingOps(); got != count {
			t.Fatalf("pending ops after setting timers = %d, want %d", got, count)
		}

		for _, ovPtr := range ovPtrs {
			assertSuccessfulCompletion(t, ovPtr)
		}
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending ops after queue pressure test = %d, want 0", got)
		}
	})
}
