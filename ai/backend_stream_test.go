package ai

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeChunk struct {
	text   string
	final  bool
	errMsg string
}

// fakeDrv implements only the part of LMDriver that SendStream uses. Embedding
// the nil interface makes any other call panic.
type fakeDrv struct {
	LMDriver

	mu     sync.Mutex
	chunks map[uint64]fakeChunk
	nextID uint64

	// onSend runs inside convSendStream with the request's userData.
	onSend func(d *fakeDrv, userData uint64)

	sendCalls       atomic.Int32
	cancelCalls     atomic.Int32
	deleteCalls     atomic.Int32
	cancelSeen      chan struct{}
	terminalStarted atomic.Bool
	deletedEarly    atomic.Bool // convDelete ran before the terminal chunk
}

func newFakeDrv() *fakeDrv {
	return &fakeDrv{chunks: map[uint64]fakeChunk{}, cancelSeen: make(chan struct{})}
}

func (d *fakeDrv) stringToCPtr(s string) (uint64, func()) { return 1, func() {} }
func (d *fakeDrv) createTrampoline() uint64               { return 1 }
func (d *fakeDrv) convClone(conv uint64) uint64           { return conv + 1 }

func (d *fakeDrv) convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) int32 {
	d.sendCalls.Add(1)
	d.onSend(d, userData)
	return 0
}

func (d *fakeDrv) convCancelProcess(conv uint64) {
	if d.cancelCalls.Add(1) == 1 {
		close(d.cancelSeen)
	}
}

func (d *fakeDrv) convDelete(conv uint64) {
	d.deleteCalls.Add(1)
	if !d.terminalStarted.Load() {
		d.deletedEarly.Store(true)
	}
}

func (d *fakeDrv) chunk(id uint64) fakeChunk {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.chunks[id]
}

func (d *fakeDrv) streamChunkGetText(c uint64) uint64 {
	if d.chunk(c).text == "" {
		return 0
	}
	return c
}
func (d *fakeDrv) streamChunkIsFinal(c uint64) bool { return d.chunk(c).final }
func (d *fakeDrv) streamChunkGetError(c uint64) uint64 {
	if d.chunk(c).errMsg == "" {
		return 0
	}
	return c | 1<<32
}
func (d *fakeDrv) ptrToString(p uint64) string {
	if p>>32 == 1 {
		return d.chunk(p & 0xffffffff).errMsg
	}
	return d.chunk(p).text
}

// emit delivers one chunk the way the native runtime does: by calling the
// shared callback with the request's userData.
func (d *fakeDrv) emit(userData uint64, text string, final bool, errMsg string) {
	if final {
		d.terminalStarted.Store(true)
	}
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	d.chunks[id] = fakeChunk{text: text, final: final, errMsg: errMsg}
	d.mu.Unlock()
	sharedTokenCallback(d, userData, id)
}

type chunkLog struct {
	mu   sync.Mutex
	list []string
}

func (l *chunkLog) add(s string) {
	l.mu.Lock()
	l.list = append(l.list, s)
	l.mu.Unlock()
}

func (l *chunkLog) get() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.list, ",")
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func newTestBackend(d *fakeDrv) *drvConversationBackend {
	return &drvConversationBackend{drv: d, conv: 7}
}

func TestBackendDeliversChunksAndCompletes(t *testing.T) {
	d := newFakeDrv()
	d.onSend = func(d *fakeDrv, ud uint64) {
		go func() {
			d.emit(ud, "a", false, "")
			d.emit(ud, "b", false, "")
			d.emit(ud, "", true, "")
		}()
	}
	c := newTestBackend(d)

	var log chunkLog
	if err := c.SendStream(context.Background(), "{}", log.add); err != nil {
		t.Fatal(err)
	}
	if got := log.get(); got != "a,b" {
		t.Fatalf("chunks = %q, want a,b", got)
	}
	if d.deleteCalls.Load() != 0 || d.cancelCalls.Load() != 0 {
		t.Fatal("a completed stream must leave the conversation alive and uncancelled")
	}

	// Reusable after completion, and deleted exactly once on Close.
	if err := c.SendStream(context.Background(), "{}", log.add); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c.Close()
	if d.deleteCalls.Load() != 1 {
		t.Fatalf("convDelete calls = %d, want 1", d.deleteCalls.Load())
	}
	if err := c.SendStream(context.Background(), "{}", log.add); !errors.Is(err, ErrClosed) {
		t.Fatalf("SendStream after Close = %v, want ErrClosed", err)
	}
	if d.sendCalls.Load() != 2 {
		t.Fatalf("native send calls = %d, want 2 (none after Close)", d.sendCalls.Load())
	}
}

// The runtime may deliver the whole stream before convSendStream returns.
func TestBackendCallbacksBeforeSendReturns(t *testing.T) {
	d := newFakeDrv()
	d.onSend = func(d *fakeDrv, ud uint64) {
		d.emit(ud, "a", false, "")
		d.emit(ud, "", true, "")
	}
	var log chunkLog
	if err := newTestBackend(d).SendStream(context.Background(), "{}", log.add); err != nil {
		t.Fatal(err)
	}
	if got := log.get(); got != "a" {
		t.Fatalf("chunks = %q, want a", got)
	}
}

func TestBackendReturnsTerminalChunkError(t *testing.T) {
	d := newFakeDrv()
	d.onSend = func(d *fakeDrv, ud uint64) {
		go func() {
			d.emit(ud, "a", false, "")
			d.emit(ud, "", true, "boom")
		}()
	}
	err := newTestBackend(d).SendStream(context.Background(), "{}", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want one mentioning boom", err)
	}
}

func TestBackendCancelledContextDoesNotStartStream(t *testing.T) {
	d := newFakeDrv()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newTestBackend(d).SendStream(ctx, "{}", func(string) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d.sendCalls.Load() != 0 {
		t.Fatal("native stream was started with a cancelled context")
	}
}

// Cancelling ctx returns at once, asks the runtime to cancel, drops chunks
// that arrive later, and deletes the conversation only after the terminal
// chunk.
func TestBackendContextCancelReturnsEarlyAndTearsDownAfterTerminalChunk(t *testing.T) {
	d := newFakeDrv()
	firstChunk := make(chan struct{})
	finish := make(chan struct{})
	d.onSend = func(d *fakeDrv, ud uint64) {
		go func() {
			d.emit(ud, "a", false, "")
			<-d.cancelSeen
			d.emit(ud, "late", false, "")
			<-finish
			d.emit(ud, "", true, "CANCELLED")
		}()
	}
	c := newTestBackend(d)

	ctx, cancel := context.WithCancel(context.Background())
	var log chunkLog
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.SendStream(ctx, "{}", func(s string) {
			log.add(s)
			if s == "a" {
				close(firstChunk)
			}
		})
	}()
	<-firstChunk
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendStream did not return after ctx cancellation")
	}
	if d.cancelCalls.Load() != 1 {
		t.Fatalf("native cancel calls = %d, want 1", d.cancelCalls.Load())
	}
	if err := c.SendStream(context.Background(), "{}", log.add); !errors.Is(err, ErrClosed) {
		t.Fatalf("SendStream on a cancelled conversation = %v, want ErrClosed", err)
	}

	if d.deleteCalls.Load() != 0 {
		t.Fatal("conversation was deleted before the runtime finished with it")
	}
	close(finish)
	waitUntil(t, "teardown after the terminal chunk", func() bool { return d.deleteCalls.Load() == 1 })
	if d.deletedEarly.Load() {
		t.Fatal("convDelete ran before the terminal chunk")
	}
	if got := log.get(); got != "a" {
		t.Fatalf("chunks = %q, want only a: later chunks must not reach onChunk", got)
	}
	if c.active.Load() != nil {
		waitUntil(t, "active stream cleared", func() bool { return c.active.Load() == nil })
	}
}

// Cancellation must not return while onChunk is still running on the callback
// thread, or the caller could carry on while a loop body is mid-flight.
func TestBackendCancelWaitsForRunningOnChunk(t *testing.T) {
	d := newFakeDrv()
	d.onSend = func(d *fakeDrv, ud uint64) {
		go func() {
			d.emit(ud, "a", false, "")
			<-d.cancelSeen
			d.emit(ud, "", true, "CANCELLED")
		}()
	}
	c := newTestBackend(d)

	entered := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.SendStream(ctx, "{}", func(string) {
			close(entered)
			<-release
		})
	}()
	<-entered
	cancel()

	select {
	case <-errCh:
		t.Fatal("SendStream returned while onChunk was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendStream did not return once onChunk finished")
	}
	waitUntil(t, "teardown", func() bool { return d.deleteCalls.Load() == 1 })
}

func TestBackendCloseMidStreamReturnsErrClosedAndDeletesAfterTerminalChunk(t *testing.T) {
	d := newFakeDrv()
	firstChunk := make(chan struct{})
	d.onSend = func(d *fakeDrv, ud uint64) {
		go func() {
			d.emit(ud, "a", false, "")
			<-d.cancelSeen
			d.emit(ud, "", true, "CANCELLED")
		}()
	}
	c := newTestBackend(d)

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.SendStream(context.Background(), "{}", func(string) { close(firstChunk) })
	}()
	<-firstChunk
	c.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendStream did not return after Close")
	}
	waitUntil(t, "teardown", func() bool { return d.deleteCalls.Load() == 1 })
	if d.deletedEarly.Load() {
		t.Fatal("convDelete ran before the terminal chunk")
	}
}

// Close from inside the loop body only signals; it must not wait for the body.
func TestBackendCloseFromOnChunkDoesNotDeadlock(t *testing.T) {
	d := newFakeDrv()
	d.onSend = func(d *fakeDrv, ud uint64) {
		go func() {
			d.emit(ud, "a", false, "")
			d.emit(ud, "b", false, "")
			<-d.cancelSeen
			d.emit(ud, "", true, "CANCELLED")
		}()
	}
	c := newTestBackend(d)

	var log chunkLog
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.SendStream(context.Background(), "{}", func(s string) {
			log.add(s)
			c.Close()
		})
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: Close from onChunk")
	}
	waitUntil(t, "teardown", func() bool { return d.deleteCalls.Load() == 1 })
}

func TestBackendRejectsSecondConcurrentStream(t *testing.T) {
	d := newFakeDrv()
	started := make(chan uint64, 1)
	d.onSend = func(d *fakeDrv, ud uint64) { started <- ud }
	c := newTestBackend(d)

	errCh := make(chan error, 1)
	go func() { errCh <- c.SendStream(context.Background(), "{}", func(string) {}) }()
	ud := <-started

	if err := c.SendStream(context.Background(), "{}", func(string) {}); !errors.Is(err, errStreamActive) {
		t.Fatalf("second stream err = %v, want errStreamActive", err)
	}
	d.emit(ud, "", true, "")
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}
