package main

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewHostEnvExhaustive(t *testing.T) {
	env := NewHostEnv()

	t.Run("ReturnsNonNil", func(t *testing.T) {
		if env == nil {
			t.Fatal("NewHostEnv() returned nil")
		}
	})

	t.Run("HandlesMapInit", func(t *testing.T) {
		if env.handles == nil {
			t.Error("env.handles map is nil")
		}
	})

	t.Run("StandardHandlesCount", func(t *testing.T) {
		if len(env.handles) != 3 {
			t.Errorf("Expected 3 handles, got %d", len(env.handles))
		}
	})

	t.Run("Handle0Stdin", func(t *testing.T) {
		if env.handles[0] != os.Stdin {
			t.Error("Handle 0 is not os.Stdin")
		}
	})

	t.Run("Handle1Stdout", func(t *testing.T) {
		if env.handles[1] != os.Stdout {
			t.Error("Handle 1 is not os.Stdout")
		}
	})

	t.Run("Handle2Stderr", func(t *testing.T) {
		if env.handles[2] != os.Stderr {
			t.Error("Handle 2 is not os.Stderr")
		}
	})

	t.Run("NextHandlePointer", func(t *testing.T) {
		if env.nextHandle != 3 {
			t.Errorf("Expected nextHandle 3, got %d", env.nextHandle)
		}
	})

	t.Run("ActiveOpsMapInit", func(t *testing.T) {
		if env.activeOps == nil {
			t.Error("env.activeOps is nil")
		}
	})

	t.Run("TimersMapInit", func(t *testing.T) {
		if env.timers == nil {
			t.Error("env.timers is nil")
		}
	})

	t.Run("SignalWaitersMapInit", func(t *testing.T) {
		if env.signalWaiters == nil {
			t.Error("env.signalWaiters is nil")
		}
	})

	t.Run("FileOpsQueueCap", func(t *testing.T) {
		if cap(env.fileOpsQueue) != 1000 {
			t.Errorf("Expected 1000 cap, got %d", cap(env.fileOpsQueue))
		}
	})

	t.Run("PendingSignalsCap", func(t *testing.T) {
		if cap(env.pendingSignals) != 100 {
			t.Errorf("Expected 100 cap, got %d", cap(env.pendingSignals))
		}
	})

	t.Run("OutstandingOpsInit", func(t *testing.T) {
		if atomic.LoadInt32(&env.outstandingOps) != 0 {
			t.Errorf("Expected 0 ops, got %d", env.outstandingOps)
		}
	})

	t.Run("SignalsChannelInit", func(t *testing.T) {
		if env.signals == nil {
			t.Error("env.signals is nil")
		}
		if cap(env.signals) != 10 {
			t.Errorf("Expected 10 signals cap, got %d", cap(env.signals))
		}
	})
}

func TestNewHostEnvExtendedState(t *testing.T) {
	env := NewHostEnv()

	t.Run("MapWritable", func(t *testing.T) {
		env.mu.Lock()
		env.handles[100] = "test"
		env.mu.Unlock()
		if env.handles[100] != "test" {
			t.Error("Handles map not correctly writable")
		}
	})

	t.Run("NextHandleIncrements", func(t *testing.T) {
		env.mu.Lock()
		h := env.nextHandle
		env.nextHandle++
		env.mu.Unlock()
		if h != 3 {
			t.Errorf("Expected first custom handle to be 3, got %d", h)
		}
		if env.nextHandle != 4 {
			t.Errorf("Expected nextHandle to be 4 after increment, got %d", env.nextHandle)
		}
	})

	t.Run("QueueFunctionality", func(t *testing.T) {
		done := make(chan bool, 1) // buffered to avoid block
		env.fileOpsQueue <- func() {
			done <- true
		}

		var op func()
		select {
		case op = <-env.fileOpsQueue:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Timeout waiting for queue item")
		}

		if op != nil {
			op()
		}

		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			t.Error("Done signal not received")
		}
	})

	t.Run("RegisterOpLifecycle", func(t *testing.T) {
		ovPtr := uint32(0x100)
		state := env.RegisterOp(ovPtr, "handle")
		if state == nil {
			t.Fatal("RegisterOp returned nil")
		}
		if env.PendingOps() == 0 {
			t.Error("PendingOps should be > 0 after registration")
		}
		if !env.IsOpActive(ovPtr, state.opID) {
			t.Error("Op should be active")
		}

		env.DecOps()
		// it's still in activeOps until removed by the op completion logic,
		// but PendingOps should decrease
	})

	t.Run("CancelOpTimerBehavior", func(t *testing.T) {
		ovPtr := uint32(0x200)
		env.mu.Lock()
		env.timers[ovPtr] = time.NewTimer(time.Hour)
		env.activeOps[ovPtr] = &OpState{ovPtr: ovPtr, opID: 1}
		env.mu.Unlock()
		env.IncOps()

		env.CancelOp(ovPtr)

		env.mu.Lock()
		_, exists := env.timers[ovPtr]
		_, active := env.activeOps[ovPtr]
		env.mu.Unlock()

		if exists {
			t.Error("Timer should have been deleted")
		}
		if active {
			t.Error("Active op should have been deleted on timer cancel")
		}
	})

	t.Run("CancelOpHandleBehavior", func(t *testing.T) {
		ovPtr := uint32(0x300)
		state := env.RegisterOp(ovPtr, nil)
		env.CancelOp(ovPtr)
		if !state.isCancelled {
			t.Error("isCancelled should be true")
		}
	})

	t.Run("HasOutstandingOpsBehavior", func(t *testing.T) {
		env2 := NewHostEnv()
		if env2.HasOutstandingOps() {
			t.Error("Should not have outstanding web ops initially")
		}
		env2.IncOps()
		if !env2.HasOutstandingOps() {
			t.Error("Should have outstanding ops")
		}
		env2.DecOps()
		if env2.HasOutstandingOps() {
			t.Error("Should not have outstanding ops after DecOps")
		}
	})

	t.Run("NextOpIDMonotonicity", func(t *testing.T) {
		env3 := NewHostEnv()
		s1 := env3.RegisterOp(1, nil)
		s2 := env3.RegisterOp(2, nil)
		if s2.opID <= s1.opID {
			t.Error("opID must be monotonic")
		}
	})
}
