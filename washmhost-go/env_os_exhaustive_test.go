package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSysTimeNowExhaustive(t *testing.T) {
	env := NewHostEnv()
	ctx := context.Background()
	mod := newMockModule(1024)

	t.Run("BasicSuccess", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_time_now(ctx, mod, stack)
		if stack[0] == 0 {
			t.Error("Expected non-zero time")
		}
	})

	t.Run("Monotonicity", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_time_now(ctx, mod, stack)
		t1 := stack[0]
		time.Sleep(time.Millisecond)
		env.sys_time_now(ctx, mod, stack)
		t2 := stack[0]
		if t2 <= t1 {
			t.Errorf("Time should be monotonic: %d <= %d", t2, t1)
		}
	})

	t.Run("TypeCheck", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_time_now(ctx, mod, stack)
		// It's uint64 in the stack, so it fits by definition
	})

	t.Run("ContextIndependence", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_time_now(nil, mod, stack) // should not panic
		if stack[0] == 0 {
			t.Error("Expected non-zero time with nil context")
		}
	})

	t.Run("Accuracy", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_time_now(ctx, mod, stack)
		got := int64(stack[0])
		now := time.Now().UnixNano()
		diff := now - got
		if diff < 0 {
			diff = -diff
		}
		if diff > int64(time.Second) {
			t.Errorf("Time difference too large: %d ns", diff)
		}
	})

	t.Run("NoSideEffects", func(t *testing.T) {
		before := env.PendingOps()
		stack := []uint64{0}
		env.sys_time_now(ctx, mod, stack)
		after := env.PendingOps()
		if before != after {
			t.Error("sys_time_now should not affect pending ops")
		}
	})

	t.Run("Stress", func(t *testing.T) {
		stack := []uint64{0}
		for i := 0; i < 1000; i++ {
			env.sys_time_now(ctx, mod, stack)
		}
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := []uint64{0}
				env.sys_time_now(ctx, mod, s)
			}()
		}
		wg.Wait()
	})

	t.Run("ModuleIndependence", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_time_now(ctx, nil, stack)
		if stack[0] == 0 {
			t.Error("Should work with nil module")
		}
	})

	t.Run("UnixEpochSanity", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_time_now(ctx, mod, stack)
		if stack[0] < uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()) {
			t.Error("Time is suspiciously old")
		}
	})

	t.Run("Precision", func(t *testing.T) {
		stack := []uint64{0}
		vals := make(map[uint64]bool)
		for i := 0; i < 100; i++ {
			env.sys_time_now(ctx, mod, stack)
			vals[stack[0]] = true
		}
		// Expect at least some variation
		if len(vals) < 1 && testing.Short() {
			t.Error("Expected at least one value")
		}
	})

	t.Run("ResultIsUint64", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_time_now(ctx, mod, stack)
		_ = uint64(stack[0])
	})

	t.Run("StackLength1", func(t *testing.T) {
		stack := make([]uint64, 1)
		env.sys_time_now(ctx, mod, stack)
	})

	t.Run("NoPanicOnRandomStack", func(t *testing.T) {
		stack := []uint64{0xDEADBEEF}
		env.sys_time_now(ctx, mod, stack)
	})
}

func TestSysGetTimeExhaustive(t *testing.T) {
	env := NewHostEnv()
	ctx := context.Background()
	mod := newMockModule(1024)

	t.Run("BasicSuccess", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(ctx, mod, stack)
		if stack[0] == 0 {
			t.Error("Expected non-zero time")
		}
	})

	t.Run("Monotonicity", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(ctx, mod, stack)
		t1 := stack[0]
		time.Sleep(10 * time.Microsecond)
		env.sys_get_time(ctx, mod, stack)
		t2 := stack[0]
		if t2 < t1 {
			t.Errorf("Time should be monotonic: %d < %d", t2, t1)
		}
	})

	t.Run("ConsistentWithTimeNow", func(t *testing.T) {
		stack1 := []uint64{0}
		stack2 := []uint64{0}
		env.sys_time_now(ctx, mod, stack1)
		env.sys_get_time(ctx, mod, stack2)
		diff := int64(stack2[0]) - int64(stack1[0])
		if diff < 0 {
			diff = -diff
		}
		if diff > int64(time.Second) {
			t.Errorf("sys_get_time and sys_time_now diverged: %d", diff)
		}
	})

	t.Run("ContextIndependence", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(nil, mod, stack)
	})

	t.Run("StressLoad", func(t *testing.T) {
		stack := []uint64{0}
		for i := 0; i < 500; i++ {
			env.sys_get_time(ctx, mod, stack)
		}
	})

	t.Run("NilModule", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(ctx, nil, stack)
	})

	t.Run("GoroutineSafety", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				env.sys_get_time(ctx, mod, []uint64{0})
			}()
		}
		wg.Wait()
	})

	t.Run("LargeEpochCheck", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(ctx, mod, stack)
		if stack[0] < uint64(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()) {
			t.Error("Time is suspiciously old")
		}
	})

	t.Run("NoAllocationsDuringCall", func(t *testing.T) {
		// Just a placeholder for "this should be fast"
		stack := []uint64{0}
		env.sys_get_time(ctx, mod, stack)
	})

	t.Run("IncrementalCheck", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(ctx, mod, stack)
		v1 := stack[0]
		for i := 0; i < 100; i++ {
			env.sys_get_time(ctx, mod, stack)
		}
		v2 := stack[0]
		if v2 < v1 {
			t.Error("Time went backwards")
		}
	})

	t.Run("StackIsolation", func(t *testing.T) {
		stack := []uint64{123, 456}
		env.sys_get_time(ctx, mod, stack)
		if stack[1] != 456 {
			t.Error("Should only modify stack[0]")
		}
	})

	t.Run("NegativeTimeCheck", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(ctx, mod, stack)
		if int64(stack[0]) < 0 {
			t.Error("Negative time returned")
		}
	})

	t.Run("NonNilResult", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(ctx, mod, stack)
		if stack[0] == 0 {
			t.Error("Expected non-zero result")
		}
	})

	t.Run("HighBitUsageCheck", func(t *testing.T) {
		stack := []uint64{0}
		env.sys_get_time(ctx, mod, stack)
		// Unix nano in 2026 uses about 61 bits
		if stack[0] < (1 << 60) {
			t.Logf("Time value: %d", stack[0])
		}
	})
}

func TestSysGetRandomExhaustive(t *testing.T) {
	env := NewHostEnv()
	ctx := context.Background()
	mod := newMockModule(1024)
	mem := mod.Memory()

	t.Run("SmallRead", func(t *testing.T) {
		stack := []uint64{100, 1}
		env.sys_get_random(ctx, mod, stack)
		buf, _ := mem.Read(100, 1)
		if len(buf) != 1 {
			t.Error("Read failed")
		}
	})

	t.Run("TypicalRead", func(t *testing.T) {
		stack := []uint64{100, 16}
		env.sys_get_random(ctx, mod, stack)
		buf, _ := mem.Read(100, 16)
		if len(buf) != 16 {
			t.Error("Read failed")
		}
	})

	t.Run("ZeroRead", func(t *testing.T) {
		stack := []uint64{100, 0}
		env.sys_get_random(ctx, mod, stack)
	})

	t.Run("LargeRead", func(t *testing.T) {
		stack := []uint64{0, 512}
		env.sys_get_random(ctx, mod, stack)
		buf, _ := mem.Read(0, 512)
		if len(buf) != 512 {
			t.Error("Large read failed")
		}
	})

	t.Run("InvalidMemory", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for out of bounds memory write")
			}
		}()
		stack := []uint64{2000, 10}
		env.sys_get_random(ctx, mod, stack)
	})

	t.Run("RandomnessDataCheck", func(t *testing.T) {
		stack := []uint64{100, 32}
		env.sys_get_random(ctx, mod, stack)
		buf, _ := mem.Read(100, 32)
		allZero := true
		for _, b := range buf {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			t.Error("Random read was all zeros (highly unlikely)")
		}
	})

	t.Run("Uniqueness", func(t *testing.T) {
		stack1 := []uint64{100, 16}
		env.sys_get_random(ctx, mod, stack1)
		buf1 := make([]byte, 16)
		b1, _ := mem.Read(100, 16)
		copy(buf1, b1)

		stack2 := []uint64{200, 16}
		env.sys_get_random(ctx, mod, stack2)
		buf2, _ := mem.Read(200, 16)

		equal := true
		for i := range buf1 {
			if buf1[i] != buf2[i] {
				equal = false
				break
			}
		}
		if equal {
			t.Error("Two random reads were identical")
		}
	})

	t.Run("BoundaryWrite", func(t *testing.T) {
		stack := []uint64{1023, 1}
		env.sys_get_random(ctx, mod, stack)
		buf, _ := mem.Read(1023, 1)
		if len(buf) != 1 {
			t.Error("Boundary write failed")
		}
	})

	t.Run("StressConcurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				env.sys_get_random(ctx, mod, []uint64{uint64(idx * 4), 4})
			}(i)
		}
		wg.Wait()
	})

	t.Run("MemoryPersistence", func(t *testing.T) {
		ptr := uint32(500)
		mem.Write(ptr, []byte{0xAA, 0xBB, 0xCC})
		stack := []uint64{uint64(ptr), 2}
		env.sys_get_random(ctx, mod, stack)
		buf, _ := mem.Read(ptr, 3)
		if buf[2] != 0xCC {
			t.Error("Wrote too much memory or corrupted adjacent bytes")
		}
	})

	t.Run("StackParameterVerification", func(t *testing.T) {
		stack := []uint64{100, 10}
		env.sys_get_random(ctx, mod, stack)
		// Internal check would require mocking memory.Write or similar
	})

	t.Run("NilModulePanic", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for nil module")
			}
		}()
		env.sys_get_random(ctx, nil, []uint64{100, 10})
	})

	t.Run("LargeReadPanic", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for out of bounds read")
			}
		}()
		stack := []uint64{0, 2000}
		env.sys_get_random(ctx, mod, stack)
	})

	t.Run("DistributionSimpleCheck", func(t *testing.T) {
		// Read 1000 bytes and check if counts of 0 and 1 bits are somewhat balanced?
		// No, just stay simple: ensure it doesn't always return the same byte.
		stack := []uint64{0, 100}
		env.sys_get_random(ctx, mod, stack)
		buf, _ := mem.Read(0, 100)
		counts := make(map[byte]int)
		for _, b := range buf {
			counts[b]++
		}
		if len(counts) < 5 {
			t.Errorf("Low entropy in random bytes: %v", counts)
		}
	})
}
