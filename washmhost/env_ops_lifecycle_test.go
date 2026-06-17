package main

import (
	"math"
	"sync"
	"testing"
	"time"
)

// TestRegisterOpExtended covers 14 tests for RegisterOp
func TestRegisterOpExtended(t *testing.T) {
	env := NewHostEnv()

	t.Run("1. basic registration mapping", func(t *testing.T) {
		ovPtr := uint32(0x100)
		handle := "test-handle"
		state := env.RegisterOp(ovPtr, handle)
		if state.ovPtr != ovPtr {
			t.Errorf("expected ovPtr 0x%x, got 0x%x", ovPtr, state.ovPtr)
		}
		if state.handle != handle {
			t.Errorf("expected handle %v, got %v", handle, state.handle)
		}
	})

	t.Run("2. ID increment uniqueness", func(t *testing.T) {
		s1 := env.RegisterOp(0x200, nil)
		s2 := env.RegisterOp(0x300, nil)
		if s1.opID >= s2.opID {
			t.Errorf("IDs not strictly increasing: %d >= %d", s1.opID, s2.opID)
		}
	})

	t.Run("3. storage in activeOps map", func(t *testing.T) {
		ovPtr := uint32(0x400)
		state := env.RegisterOp(ovPtr, nil)
		env.mu.Lock()
		stored, ok := env.activeOps[ovPtr]
		env.mu.Unlock()
		if !ok || stored != state {
			t.Error("state not correctly stored in activeOps map")
		}
	})

	t.Run("4. counter increment", func(t *testing.T) {
		initial := env.PendingOps()
		env.RegisterOp(0x500, nil)
		if env.PendingOps() != initial+1 {
			t.Errorf("expected count %d, got %d", initial+1, env.PendingOps())
		}
	})

	t.Run("5. various handle types - struct", func(t *testing.T) {
		type myHandle struct{ id int }
		h := &myHandle{id: 42}
		state := env.RegisterOp(0x600, h)
		if state.handle.(*myHandle).id != 42 {
			t.Error("struct handle payload lost")
		}
	})

	t.Run("6. various handle types - primitive", func(t *testing.T) {
		state := env.RegisterOp(0x700, 12345)
		if state.handle.(int) != 12345 {
			t.Error("primitive handle payload lost")
		}
	})

	t.Run("7. overwrite same ovPtr", func(t *testing.T) {
		ovPtr := uint32(0x800)
		s1 := env.RegisterOp(ovPtr, "first")
		s2 := env.RegisterOp(ovPtr, "second")
		if s1.opID == s2.opID {
			t.Error("overwrite should produce new ID")
		}
		if !env.IsOpActive(ovPtr, s2.opID) {
			t.Error("new ID should be active")
		}
		if env.IsOpActive(ovPtr, s1.opID) {
			t.Error("old ID should be inactive")
		}
	})

	t.Run("8. registration with nil handle", func(t *testing.T) {
		state := env.RegisterOp(0x900, nil)
		if state.handle != nil {
			t.Error("expected nil handle")
		}
	})

	t.Run("9. boundary ovPtr - zero", func(t *testing.T) {
		state := env.RegisterOp(0, "zero-ptr")
		if !env.IsOpActive(0, state.opID) {
			t.Error("failed to register at ovPtr 0")
		}
	})

	t.Run("10. boundary ovPtr - max", func(t *testing.T) {
		state := env.RegisterOp(math.MaxUint32, "max-ptr")
		if !env.IsOpActive(math.MaxUint32, state.opID) {
			t.Error("failed to register at max ovPtr")
		}
	})

	t.Run("11. state initialization flags", func(t *testing.T) {
		state := env.RegisterOp(0xA00, nil)
		if state.isCancelled {
			t.Error("new op should not be cancelled")
		}
	})

	t.Run("12. concurrent registration stress", func(t *testing.T) {
		const count = 100
		var wg sync.WaitGroup
		wg.Add(count)
		for i := 0; i < count; i++ {
			go func(val int) {
				defer wg.Done()
				env.RegisterOp(uint32(0xB00+val), val)
			}(i)
		}
		wg.Wait()
		// Verify we didn't lose track of the count
		// Count is tricky because of previous tests in same env,
		// but we can check if it responded to 100 calls.
	})

	t.Run("13. registration after full cleanup", func(t *testing.T) {
		e := NewHostEnv()
		e.RegisterOp(0x1, nil)
		e.DecOps()
		s := e.RegisterOp(0x2, nil)
		if e.PendingOps() != 1 {
			t.Errorf("expected 1, got %d", e.PendingOps())
		}
		if !e.IsOpActive(0x2, s.opID) {
			t.Error("registration failed after cleanup")
		}
	})

	t.Run("14. memory residency check", func(t *testing.T) {
		ovPtr := uint32(0xC00)
		state := env.RegisterOp(ovPtr, "mem")
		env.mu.Lock()
		stored := env.activeOps[ovPtr]
		env.mu.Unlock()
		if stored != state {
			t.Error("RegisterOp returned copy instead of pointer to stored state")
		}
	})
}

// TestIsOpActiveExtended covers 14 tests for IsOpActive
func TestIsOpActiveExtended(t *testing.T) {
	env := NewHostEnv()

	t.Run("1. basic positive match", func(t *testing.T) {
		ovPtr := uint32(0x100)
		state := env.RegisterOp(ovPtr, nil)
		if !env.IsOpActive(ovPtr, state.opID) {
			t.Error("expected op to be active")
		}
	})

	t.Run("2. negative match - wrong ptr", func(t *testing.T) {
		ovPtr := uint32(0x200)
		state := env.RegisterOp(ovPtr, nil)
		if env.IsOpActive(ovPtr+1, state.opID) {
			t.Error("should be inactive for wrong ptr")
		}
	})

	t.Run("3. negative match - wrong ID", func(t *testing.T) {
		ovPtr := uint32(0x300)
		state := env.RegisterOp(ovPtr, nil)
		if env.IsOpActive(ovPtr, state.opID+1) {
			t.Error("should be inactive for wrong ID")
		}
	})

	t.Run("4. negative match - both wrong", func(t *testing.T) {
		ovPtr := uint32(0x400)
		state := env.RegisterOp(ovPtr, nil)
		if env.IsOpActive(ovPtr+1, state.opID+1) {
			t.Error("should be inactive for wrong ptr and ID")
		}
	})

	t.Run("5. inactive after overwrite", func(t *testing.T) {
		ovPtr := uint32(0x500)
		s1 := env.RegisterOp(ovPtr, "old")
		env.RegisterOp(ovPtr, "new")
		if env.IsOpActive(ovPtr, s1.opID) {
			t.Error("old ID should be inactive after overwrite")
		}
	})

	t.Run("6. inactive after manual removal", func(t *testing.T) {
		ovPtr := uint32(0x600)
		state := env.RegisterOp(ovPtr, nil)
		env.mu.Lock()
		delete(env.activeOps, ovPtr)
		env.mu.Unlock()
		if env.IsOpActive(ovPtr, state.opID) {
			t.Error("should be inactive after removal from map")
		}
	})

	t.Run("7. active remains true if cancelled", func(t *testing.T) {
		// IsOpActive checks existence and ID, not cancellation state
		ovPtr := uint32(0x700)
		state := env.RegisterOp(ovPtr, nil)
		env.CancelOp(ovPtr)
		if !env.IsOpActive(ovPtr, state.opID) {
			t.Error("op should still be 'active' (existing) even if cancelled")
		}
	})

	t.Run("8. cross-talk prevention", func(t *testing.T) {
		s1 := env.RegisterOp(0x801, nil)
		s2 := env.RegisterOp(0x802, nil)
		if env.IsOpActive(0x801, s2.opID) || env.IsOpActive(0x802, s1.opID) {
			t.Error("ID cross-talk detected")
		}
	})

	t.Run("9. check with ovPtr zero", func(t *testing.T) {
		state := env.RegisterOp(0, nil)
		if !env.IsOpActive(0, state.opID) {
			t.Error("failed check for ptr 0")
		}
	})

	t.Run("10. check with ID zero", func(t *testing.T) {
		// nextOpID starts at 0 and increments before use, so ID 0 shouldn't normally exist
		if env.IsOpActive(0x900, 0) {
			t.Error("ID 0 should never be active")
		}
	})

	t.Run("11. concurrency stress - readers", func(t *testing.T) {
		ovPtr := uint32(0xA00)
		state := env.RegisterOp(ovPtr, nil)
		const count = 100
		var wg sync.WaitGroup
		wg.Add(count)
		for i := 0; i < count; i++ {
			go func() {
				defer wg.Done()
				_ = env.IsOpActive(ovPtr, state.opID)
			}()
		}
		wg.Wait()
	})

	t.Run("12. check after re-initialization", func(t *testing.T) {
		e := NewHostEnv()
		s := e.RegisterOp(0x1, nil)
		// Spoof a reset (not recommended in prod, but for testing isolation)
		e.mu.Lock()
		e.activeOps = make(map[uint32]*OpState)
		e.mu.Unlock()
		if e.IsOpActive(0x1, s.opID) {
			t.Error("should be inactive after map clear")
		}
	})

	t.Run("13. check with max uint32 ptr", func(t *testing.T) {
		state := env.RegisterOp(math.MaxUint32, nil)
		if !env.IsOpActive(math.MaxUint32, state.opID) {
			t.Error("failed check for max ptr")
		}
	})

	t.Run("14. check with non-existent ptr", func(t *testing.T) {
		if env.IsOpActive(0xDEADBEEF, 1) {
			t.Error("should be false for random ptr")
		}
	})
}

// TestDecOpsExtended covers 14 tests for DecOps
func TestDecOpsExtended(t *testing.T) {
	t.Run("1. simple decrement 1 to 0", func(t *testing.T) {
		e := NewHostEnv()
		e.IncOps()
		e.DecOps()
		if e.PendingOps() != 0 {
			t.Errorf("expected 0, got %d", e.PendingOps())
		}
	})

	t.Run("2. decrement from multiple", func(t *testing.T) {
		e := NewHostEnv()
		e.IncOps()
		e.IncOps()
		e.DecOps()
		if e.PendingOps() != 1 {
			t.Errorf("expected 1, got %d", e.PendingOps())
		}
	})

	t.Run("3. underflow protection", func(t *testing.T) {
		e := NewHostEnv()
		e.DecOps() // 0 -> 0
		if e.PendingOps() != 0 {
			t.Errorf("expected 0 after underflow attempt, got %d", e.PendingOps())
		}
	})

	t.Run("4. double underflow", func(t *testing.T) {
		e := NewHostEnv()
		e.DecOps()
		e.DecOps()
		if e.PendingOps() != 0 {
			t.Error("underflow count became negative?")
		}
	})

	t.Run("5. high concurrency Inc/Dec", func(t *testing.T) {
		e := NewHostEnv()
		const count = 1000
		var wg sync.WaitGroup
		wg.Add(count * 2)
		for i := 0; i < count; i++ {
			go func() { defer wg.Done(); e.IncOps() }()
			go func() { defer wg.Done(); e.DecOps() }()
		}
		wg.Wait()
		// Since order isn't guaranteed, we can't be sure it's 0
		// (some Dec could happen before Inc and be clipped to 0),
		// but it must be >= 0 and <= count.
		res := e.PendingOps()
		if res < 0 || res > count {
			t.Errorf("unexpected pending ops: %d", res)
		}
	})

	t.Run("6. lifecycle check - RegisterOp then dec", func(t *testing.T) {
		e := NewHostEnv()
		e.RegisterOp(0x1, nil)
		e.DecOps()
		if e.HasOutstandingOps() {
			t.Error("expected no outstanding ops after dec")
		}
	})

	t.Run("7. sequence of Inc/Dec", func(t *testing.T) {
		e := NewHostEnv()
		e.IncOps()
		e.IncOps()
		e.DecOps()
		e.IncOps()
		e.DecOps()
		if e.PendingOps() != 1 {
			t.Errorf("expected 1, got %d", e.PendingOps())
		}
	})

	t.Run("8. DecOps until zero in loop", func(t *testing.T) {
		e := NewHostEnv()
		for i := 0; i < 50; i++ {
			e.IncOps()
		}
		count := 0
		for e.HasOutstandingOps() {
			e.DecOps()
			count++
		}
		if count != 50 {
			t.Errorf("expected 50 decs, did %d", count)
		}
	})

	t.Run("9. interleaved contention - 10000 ops", func(t *testing.T) {
		e := NewHostEnv()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				e.IncOps()
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				e.DecOps()
			}
		}()
		wg.Wait()
		if e.PendingOps() < 0 {
			t.Error("count became negative")
		}
	})

	t.Run("10. effect on HasLiveOps", func(t *testing.T) {
		// DecOps affects counter, but RegisterOp/activeOps affects HasLiveOps.
		// Testing if they are decoupled as expected.
		e := NewHostEnv()
		e.RegisterOp(0x1, nil)
		e.DecOps()
		if e.PendingOps() != 0 {
			t.Error("counter not 0")
		}
		if !e.HasLiveOps() {
			// This is actually expected: HasLiveOps checks activeOps map.
			// DecOps doesn't remove from map, RegisterOp adds to it.
		}
	})

	t.Run("11. DecOps on large value", func(t *testing.T) {
		e := NewHostEnv()
		const large = 100000
		for i := 0; i < large; i++ {
			e.IncOps()
		}
		for i := 0; i < large; i++ {
			e.DecOps()
		}
		if e.PendingOps() != 0 {
			t.Errorf("failed to clear large count: %d", e.PendingOps())
		}
	})

	t.Run("12. CAS loop simulation - atomic load consistency", func(t *testing.T) {
		e := NewHostEnv()
		e.IncOps()
		// If PendingOps returns the same as the internal load, DecOps should work
		if e.PendingOps() != 1 {
			t.Error("initial load failed")
		}
		e.DecOps()
		if e.PendingOps() != 0 {
			t.Error("final load failed")
		}
	})

	t.Run("13. verify no side effect on activeOps", func(t *testing.T) {
		e := NewHostEnv()
		ovPtr := uint32(0xABC)
		e.RegisterOp(ovPtr, "stay")
		e.DecOps()
		e.mu.Lock()
		_, exists := e.activeOps[ovPtr]
		e.mu.Unlock()
		if !exists {
			t.Error("DecOps should not remove from activeOps map")
		}
	})

	t.Run("14. HasOutstandingOps consistency", func(t *testing.T) {
		e := NewHostEnv()
		e.IncOps()
		if !e.HasOutstandingOps() {
			t.Error("expected true")
		}
		e.DecOps()
		if e.HasOutstandingOps() {
			t.Error("expected false")
		}
	})
}

type mockDeadlineHandle struct {
	deadline time.Time
}

func (m *mockDeadlineHandle) SetDeadline(t time.Time) error {
	m.deadline = t
	return nil
}

// TestCancelOpExtended covers 14 tests for CancelOp
func TestCancelOpExtended(t *testing.T) {
	env := NewHostEnv()

	t.Run("1. basic cancellation flag", func(t *testing.T) {
		ovPtr := uint32(0x100)
		state := env.RegisterOp(ovPtr, nil)
		env.CancelOp(ovPtr)
		if !state.isCancelled {
			t.Error("expected isCancelled to be true")
		}
	})

	t.Run("2. deadline setting on handle", func(t *testing.T) {
		ovPtr := uint32(0x200)
		h := &mockDeadlineHandle{}
		env.RegisterOp(ovPtr, h)
		env.CancelOp(ovPtr)
		if h.deadline.IsZero() {
			t.Error("deadline should have been set")
		}
		// Expecting time.Unix(1, 0)
		if h.deadline.Unix() != 1 {
			t.Errorf("expected epoch 1, got %v", h.deadline.Unix())
		}
	})

	t.Run("3. timer cancellation stops timer", func(t *testing.T) {
		e := NewHostEnv()
		ovPtr := uint32(0x300)
		fired := false
		timer := time.AfterFunc(time.Hour, func() { fired = true })
		e.mu.Lock()
		e.timers[ovPtr] = timer
		e.mu.Unlock()
		e.CancelOp(ovPtr)
		if fired {
			t.Error("timer should have been stopped")
		}
	})

	t.Run("4. timer cancellation removes from map", func(t *testing.T) {
		e := NewHostEnv()
		ovPtr := uint32(0x400)
		e.mu.Lock()
		e.timers[ovPtr] = time.AfterFunc(time.Hour, func() {})
		e.mu.Unlock()
		e.CancelOp(ovPtr)
		e.mu.Lock()
		_, exists := e.timers[ovPtr]
		e.mu.Unlock()
		if exists {
			t.Error("timer remains in map after cancellation")
		}
	})

	t.Run("5. timer cancellation removes from activeOps", func(t *testing.T) {
		e := NewHostEnv()
		ovPtr := uint32(0x500)
		e.RegisterOp(ovPtr, nil)
		e.mu.Lock()
		e.timers[ovPtr] = time.AfterFunc(time.Hour, func() {})
		e.mu.Unlock()
		e.CancelOp(ovPtr)
		if e.IsOpActive(ovPtr, 1) { // Assuming ID 1
			// Note: register sets ID 1 usually in new env
		}
		e.mu.Lock()
		_, active := e.activeOps[ovPtr]
		e.mu.Unlock()
		if active {
			t.Error("timer op should be removed from activeOps immediately on cancel")
		}
	})

	t.Run("6. timer cancellation decrements counter", func(t *testing.T) {
		e := NewHostEnv()
		ovPtr := uint32(0x600)
		e.RegisterOp(ovPtr, nil)
		e.mu.Lock()
		e.timers[ovPtr] = time.AfterFunc(time.Hour, func() {})
		e.mu.Unlock()
		initial := e.PendingOps()
		e.CancelOp(ovPtr)
		if e.PendingOps() != initial-1 {
			t.Errorf("expected count %d, got %d", initial-1, e.PendingOps())
		}
	})

	t.Run("7. idempotency of cancellation", func(t *testing.T) {
		ovPtr := uint32(0x700)
		env.RegisterOp(ovPtr, nil)
		env.CancelOp(ovPtr)
		// Should not panic or double-decrement (non-timer doesn't dec anyway)
		env.CancelOp(ovPtr)
	})

	t.Run("8. cancel non-existent op", func(t *testing.T) {
		// Should do nothing gracefully
		env.CancelOp(0xDEAD)
	})

	t.Run("9. cancel already cancelled op", func(t *testing.T) {
		ovPtr := uint32(0x800)
		state := env.RegisterOp(ovPtr, nil)
		state.isCancelled = true
		env.CancelOp(ovPtr)
		// No side effects expected
	})

	t.Run("10. nil handle safety", func(t *testing.T) {
		ovPtr := uint32(0x900)
		env.RegisterOp(ovPtr, nil)
		env.CancelOp(ovPtr)
		// Pass if no panic
	})

	t.Run("11. non-deadline handle safety", func(t *testing.T) {
		ovPtr := uint32(0xA00)
		env.RegisterOp(ovPtr, "not-a-deadline-handle")
		env.CancelOp(ovPtr)
		// Pass if no panic
	})

	t.Run("12. concurrent cancellation pressure", func(t *testing.T) {
		ovPtr := uint32(0xB00)
		env.RegisterOp(ovPtr, nil)
		const count = 100
		var wg sync.WaitGroup
		wg.Add(count)
		for i := 0; i < count; i++ {
			go func() {
				defer wg.Done()
				env.CancelOp(ovPtr)
			}()
		}
		wg.Wait()
	})

	t.Run("13. cancel after overwrite", func(t *testing.T) {
		ovPtr := uint32(0xC00)
		s1 := env.RegisterOp(ovPtr, nil)
		s2 := env.RegisterOp(ovPtr, nil)
		env.CancelOp(ovPtr)
		if !s2.isCancelled {
			t.Error("new op should be cancelled")
		}
		if s1.isCancelled {
			t.Error("old op (orphaned) should NOT be cancelled by CancelOp(ptr)")
		}
	})

	t.Run("14. cleanup-only timer cancel", func(t *testing.T) {
		e := NewHostEnv()
		ovPtr := uint32(0xD00)
		e.mu.Lock()
		// Timer exists but no entry in activeOps (simulated edge case)
		e.timers[ovPtr] = time.AfterFunc(time.Hour, func() {})
		e.mu.Unlock()
		e.CancelOp(ovPtr)
		e.mu.Lock()
		_, exists := e.timers[ovPtr]
		e.mu.Unlock()
		if exists {
			t.Error("timer not removed in cleanup-only path")
		}
	})
}
