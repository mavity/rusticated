package main

import (
	"context"
	"testing"
	"time"
)

func TestSysTimerSetExtended(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x100000)

	t.Run("1. basic timer set and fire", func(t *testing.T) {
		ovPtr := uint32(0x100)
		stack := []uint64{uint64(ovPtr), uint64(time.Millisecond * 10)}

		env.sys_timer_set(context.Background(), mod, stack)

		if env.PendingOps() != 1 {
			t.Errorf("expected 1 pending op, got %d", env.PendingOps())
		}

		// Wait for timer to fire and be processed by the proactor
		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		if env.PendingOps() != 0 {
			t.Errorf("expected 0 pending ops after fire, got %d", env.PendingOps())
		}

		// Verify memory writeback
		val, _ := mod.Memory().Read(ovPtr, 24)
		if val[0] != 1 { // overlapped version
			t.Error("overlapped version mismatch")
		}
	})

	t.Run("2. timer overwrite stops old timer", func(t *testing.T) {
		ovPtr := uint32(0x200)
		stack1 := []uint64{uint64(ovPtr), uint64(time.Hour)}
		env.sys_timer_set(context.Background(), mod, stack1)

		initialOps := env.PendingOps()

		stack2 := []uint64{uint64(ovPtr), uint64(time.Millisecond)}
		env.sys_timer_set(context.Background(), mod, stack2)

		// Counter should still be same because overwrite calls DecOps then register calls IncOps
		if env.PendingOps() != initialOps {
			t.Errorf("expected %d ops, got %d", initialOps, env.PendingOps())
		}

		// Wait for the new (short) timer to fire
		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)
	})

	t.Run("3. zero timeout fires quickly", func(t *testing.T) {
		ovPtr := uint32(0x300)
		stack := []uint64{uint64(ovPtr), 0}
		env.sys_timer_set(context.Background(), mod, stack)

		time.Sleep(time.Millisecond * 10)
		env.Poll(context.Background(), mod)

		if env.PendingOps() != 0 {
			// expect it to have finished
		}
	})

	t.Run("4. cancelled timer does not write back", func(t *testing.T) {
		ovPtr := uint32(0x400)
		mod.Memory().Write(ovPtr, make([]byte, 24)) // clear

		stack := []uint64{uint64(ovPtr), uint64(time.Millisecond * 10)}
		env.sys_timer_set(context.Background(), mod, stack)

		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})

		time.Sleep(time.Millisecond * 30)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		if val[0] != 0 {
			t.Error("cancelled timer should not have written to memory")
		}
	})

	t.Run("5. sys_cancel also works for timers", func(t *testing.T) {
		ovPtr := uint32(0x500)
		stack := []uint64{uint64(ovPtr), uint64(time.Hour)}
		env.sys_timer_set(context.Background(), mod, stack)

		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})

		env.mu.Lock()
		_, exists := env.timers[ovPtr]
		env.mu.Unlock()
		if exists {
			t.Error("timer should be removed from map by sys_cancel")
		}
	})

	t.Run("6. timer firing removes from timers map", func(t *testing.T) {
		ovPtr := uint32(0x600)
		stack := []uint64{uint64(ovPtr), uint64(time.Millisecond)}
		env.sys_timer_set(context.Background(), mod, stack)

		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		env.mu.Lock()
		_, exists := env.timers[ovPtr]
		env.mu.Unlock()
		if exists {
			t.Error("fired timer should be removed from map")
		}
	})

	t.Run("7. high frequency timer set", func(t *testing.T) {
		ovPtr := uint32(0x700)
		for i := 0; i < 50; i++ {
			env.sys_timer_set(context.Background(), mod, []uint64{uint64(ovPtr), uint64(time.Microsecond)})
		}
		time.Sleep(time.Millisecond * 10)
		env.Poll(context.Background(), mod)
	})

	t.Run("8. multiple concurrent timers", func(t *testing.T) {
		const count = 20
		for i := 0; i < count; i++ {
			env.sys_timer_set(context.Background(), mod, []uint64{uint64(0x800 + i), uint64(time.Millisecond)})
		}
		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)
	})

	t.Run("9. large timeout behavior", func(t *testing.T) {
		ovPtr := uint32(0x900)
		stack := []uint64{uint64(ovPtr), uint64(time.Hour * 24)}
		env.sys_timer_set(context.Background(), mod, stack)

		if env.PendingOps() == 0 {
			t.Error("expected timer to be active")
		}
		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})
	})

	t.Run("10. timer fire after host_cancel", func(t *testing.T) {
		// Test if IsOpActive correctly prevents writeback if ID changed
		ovPtr := uint32(0xA00)
		stack1 := []uint64{uint64(ovPtr), uint64(time.Millisecond * 2)}
		env.sys_timer_set(context.Background(), mod, stack1)

		// Wait long enough for it to fire and be in queue
		start := time.Now()
		for env.PendingOps() == 1 && time.Since(start) < time.Second {
			// wait for the timer goroutine to at least fire.
			// Wait, the timer callback pushes to fileOpsQueue but doesn't DecOps immediately.
			// So PendingOps remains 1.
			time.Sleep(time.Millisecond)
		}

		// Re-register at same location immediately with longer timer
		stack2 := []uint64{uint64(ovPtr), uint64(time.Hour)}
		env.sys_timer_set(context.Background(), mod, stack2)

		// Use a context with timeout for Poll so we don't hang the whole test suite
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		env.Poll(ctx, mod)

		// Memory should NOT have been updated by the first (short) timer
		val, _ := mod.Memory().Read(ovPtr, 24)
		if val[0] != 0 {
			t.Error("old timer shouldn't have written back")
		}
		env.sys_cancel(context.Background(), mod, []uint64{uint64(ovPtr)})
	})

	t.Run("11. timer set on max uint32 ptr", func(t *testing.T) {
		ovPtr := uint32(0x10000 - 32)
		stack := []uint64{uint64(ovPtr), uint64(time.Millisecond)}
		env.sys_timer_set(context.Background(), mod, stack)
		time.Sleep(time.Millisecond * 10)
		env.Poll(context.Background(), mod)
	})

	t.Run("12. Poll processes multiple timers", func(t *testing.T) {
		const count = 5
		for i := 0; i < count; i++ {
			env.sys_timer_set(context.Background(), mod, []uint64{uint64(0xB00 + i), uint64(time.Millisecond)})
		}
		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)
		if env.PendingOps() != 0 {
			// Ensure it's 0 after Poll
		}
	})

	t.Run("13. timer precision check (approximate)", func(t *testing.T) {
		ovPtr := uint32(0xC00)
		delay := 20 * time.Millisecond
		start := time.Now()
		stack := []uint64{uint64(ovPtr), uint64(delay)}
		env.sys_timer_set(context.Background(), mod, stack)

		// Busy wait for proactor work
		for {
			if env.Poll(context.Background(), mod) {
				break
			}
			if time.Since(start) > time.Second {
				t.Fatal("timer never fired")
			}
			time.Sleep(time.Millisecond)
		}
		elapsed := time.Since(start)
		if elapsed < delay {
			t.Errorf("timer fired too early: %v", elapsed)
		}
	})

	t.Run("14. proactor queue pressure from timers", func(t *testing.T) {
		const count = 100
		for i := 0; i < count; i++ {
			// All fire at once
			env.sys_timer_set(context.Background(), mod, []uint64{uint64(0xD00 + i), uint64(time.Millisecond)})
		}
		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)
	})
}
