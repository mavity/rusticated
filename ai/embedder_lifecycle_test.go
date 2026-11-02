package ai

import (
	"sync"
	"testing"
	"time"
)

type blockingEmbedderBackend struct {
	started             chan struct{}
	release             chan struct{}
	closed              chan struct{}
	mu                  sync.Mutex
	active              bool
	closedDuringCompute bool
}

func (b *blockingEmbedderBackend) ComputeEmbedding([]embedInput) ([]float32, error) {
	b.mu.Lock()
	b.active = true
	b.mu.Unlock()
	close(b.started)
	<-b.release
	b.mu.Lock()
	b.active = false
	b.mu.Unlock()
	return []float32{1, 0}, nil
}

func (b *blockingEmbedderBackend) Close() error {
	b.mu.Lock()
	b.closedDuringCompute = b.active
	b.mu.Unlock()
	close(b.closed)
	return nil
}

func TestEmbedderCloseDefersNativeTeardownUntilEmbedReturns(t *testing.T) {
	backend := &blockingEmbedderBackend{
		started: make(chan struct{}),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(backend.release) }) }
	defer release()

	embedder := &Embedder{impl: &embedderImpl{backend: backend}}
	embedResult := make(chan error, 1)
	go func() {
		_, err := embedder.Embed([]PromptPart{{Role: RoleUser, Text: "query"}})
		embedResult <- err
	}()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("embedding did not reach the backend")
	}

	closeReturned := make(chan struct{})
	go func() {
		embedder.Close()
		close(closeReturned)
	}()
	select {
	case <-closeReturned:
	case <-time.After(time.Second):
		t.Fatal("Close() waited for the in-flight embedding call")
	}
	select {
	case <-backend.closed:
		t.Fatal("native backend was closed while embedding was still running")
	default:
	}

	release()
	select {
	case err := <-embedResult:
		if err != nil {
			t.Fatalf("Embed() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Embed() did not return after release")
	}
	select {
	case <-backend.closed:
	case <-time.After(time.Second):
		t.Fatal("native backend was not closed after embedding returned")
	}
	backend.mu.Lock()
	closedDuringCompute := backend.closedDuringCompute
	backend.mu.Unlock()
	if closedDuringCompute {
		t.Fatal("native backend Close() overlapped ComputeEmbedding()")
	}
	if _, err := embedder.Embed([]PromptPart{{Role: RoleUser, Text: "later"}}); err != ErrClosed {
		t.Fatalf("Embed() after Close() error = %v, want %v", err, ErrClosed)
	}
}
