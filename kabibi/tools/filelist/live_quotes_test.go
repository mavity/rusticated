package filelist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ai"
)

type concurrentLiveQuoteEmbedder struct {
	mu              sync.Mutex
	candidatesSeen  int
	totalCandidates int
	quoteStarted    chan int
	releaseQuote    chan struct{}
}

func (e *concurrentLiveQuoteEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (e *concurrentLiveQuoteEmbedder) EmbedBatchContext(ctx context.Context, parts []ai.PromptPart) ([][]float32, error) {
	vectors := make([][]float32, len(parts))
	for index, part := range parts {
		switch {
		case part.Role == ai.RoleUser:
			vectors[index] = []float32{1, 0}
		case strings.HasSuffix(part.Text, ".txt"):
			e.mu.Lock()
			e.candidatesSeen++
			e.mu.Unlock()
			time.Sleep(4 * time.Millisecond)
			vectors[index] = []float32{1, 0}
		case part.Text == "relevant semantic line":
			e.mu.Lock()
			seen := e.candidatesSeen
			e.mu.Unlock()
			select {
			case e.quoteStarted <- seen:
			default:
			}
			select {
			case <-e.releaseQuote:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			vectors[index] = []float32{1, 0}
		default:
			vectors[index] = []float32{1, 0}
		}
	}
	return vectors, nil
}

func TestLiveQuoteCanCompleteBeforeCandidateScoringFinishes(t *testing.T) {
	dir := t.TempDir()
	const candidateCount = 12
	for index := range candidateCount {
		name := filepath.Join(dir, fmt.Sprintf("file%02d.txt", index))
		if err := os.WriteFile(name, []byte("relevant semantic line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	embedder := &concurrentLiveQuoteEmbedder{
		totalCandidates: candidateCount,
		quoteStarted:    make(chan int, 1),
		releaseQuote:    make(chan struct{}),
	}
	iter := NewSearchIterator(context.Background(), dir, Options{
		Query:      "unrelated semantic query",
		MaxResults: candidateCount,
		QuoteLimit: 1,
		LiveQuotes: true,
		Embedder:   embedder,
	})
	defer iter.Close()
	go func() {
		for range iter.Updates() {
		}
	}()

	select {
	case candidatesSeen := <-embedder.quoteStarted:
		if candidatesSeen >= candidateCount {
			t.Fatalf("quote embedding started after all %d candidates were scored", candidateCount)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live quote embedding did not start")
	}
	close(embedder.releaseQuote)
	result, err := iter.Result()
	if err != nil {
		t.Fatalf("SearchIterator.Result(): %v", err)
	}
	if !strings.Contains(result, "```") || !strings.Contains(result, "relevant semantic line") {
		t.Fatalf("final result does not contain completed live quote: %q", result)
	}
}

type blockingQuoteEmbedder struct {
	started chan struct{}
	mu      sync.Mutex
	calls   int
}

func (e *blockingQuoteEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (e *blockingQuoteEmbedder) EmbedBatchContext(ctx context.Context, parts []ai.PromptPart) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	select {
	case e.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDemotingMatchCancelsOnlyItsQuoteContinuation(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.txt")
	secondPath := filepath.Join(dir, "second.txt")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("one line\nsecond line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	embedder := &blockingQuoteEmbedder{started: make(chan struct{}, 1)}
	scheduler := newConcurrentEmbedder(ctx, embedder)
	defer scheduler.Close()
	iter := &SearchIterator{ctx: ctx, updates: make(chan struct{}, 16)}
	progress := &progressTracker{
		iter: iter,
		entries: []fileEntry{{
			relPath:  "first.txt",
			absPath:  firstPath,
			size:     20,
			score:    0.9,
			hasScore: true,
		}},
	}
	manager := newLiveQuoteManager(ctx, scheduler, progress, 1)
	defer manager.Close()
	progress.setQuoteManager(manager)
	progress.setQueryEmbedding([]float32{1, 0})

	select {
	case <-embedder.started:
	case <-time.After(5 * time.Second):
		t.Fatal("quote embedding did not start")
	}
	progress.mu.Lock()
	progress.entries = nil
	progress.mu.Unlock()
	manager.sync(nil)
	if err := manager.Finish(nil); err != nil {
		t.Fatalf("Finish() after demotion: %v", err)
	}

	embedder.mu.Lock()
	calls := embedder.calls
	embedder.mu.Unlock()
	if calls != 1 {
		t.Fatalf("quote embed calls after demotion = %d, want the in-flight call only", calls)
	}
}

func TestDemotingQuoteWithPendingEmbeddingEjectsTaskBeforeSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.txt")
	if err := os.WriteFile(path, []byte("one line\nsecond line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	embedder := newGatedBatchEmbedder()
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	embedder.scheduler = scheduler
	defer scheduler.Close()

	blockerPart := ai.PromptPart{Role: ai.RoleModel, Text: "inference-blocker"}
	blocker, err := scheduler.enqueue(context.Background(), blockerPart, false)
	if err != nil {
		t.Fatal(err)
	}
	blockerDone := runAwait(scheduler, context.Background(), blocker)
	firstCall := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(firstCall.inputs, []string{"inference-blocker"}) {
		t.Fatalf("initial backend inputs = %v, want inference blocker", firstCall.inputs)
	}
	select {
	case <-scheduler.wake:
	default:
	}

	entry := fileEntry{relPath: "pending.txt", absPath: path, size: 20, score: 0.9, hasScore: true}
	iter := &SearchIterator{ctx: context.Background(), updates: make(chan struct{}, 16)}
	progress := &progressTracker{iter: iter, entries: []fileEntry{entry}}
	manager := newLiveQuoteManager(context.Background(), scheduler, progress, 1)
	defer manager.Close()
	progress.setQuoteManager(manager)
	progress.setQueryEmbedding([]float32{1, 0})

	manager.sync([]fileEntry{entry})
	select {
	case <-scheduler.wake:
	case <-time.After(2 * time.Second):
		t.Fatal("quote extraction did not enqueue an embedding task")
	}
	scheduler.mu.Lock()
	pendingCount := len(scheduler.pending)
	scheduler.mu.Unlock()
	if pendingCount == 0 {
		t.Fatal("expected a pending quote task while inference worker is gated")
	}

	manager.sync(nil)
	if err := manager.Finish(nil); err != nil {
		t.Fatalf("Finish() after quote demotion: %v", err)
	}
	scheduler.mu.Lock()
	pendingCount = len(scheduler.pending)
	scheduler.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending tasks after demotion = %d, want zero", pendingCount)
	}

	sentinelPart := ai.PromptPart{Role: ai.RoleModel, Text: "sentinel"}
	sentinel, err := scheduler.enqueue(context.Background(), sentinelPart, false)
	if err != nil {
		t.Fatal(err)
	}
	sentinelDone := runAwait(scheduler, context.Background(), sentinel)
	embedder.release <- struct{}{}
	if err := <-blockerDone; err != nil {
		t.Fatalf("blocker result: %v", err)
	}
	nextCall := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(nextCall.inputs, []string{"sentinel"}) {
		t.Fatalf("next backend inputs = %v; demoted quote task reached inference", nextCall.inputs)
	}
	embedder.release <- struct{}{}
	if err := <-sentinelDone; err != nil {
		t.Fatalf("sentinel result: %v", err)
	}
}

func TestLiveQuoteQueueTracksPromotedMatchOrder(t *testing.T) {
	scheduler := &concurrentEmbedder{ctx: context.Background(), wake: make(chan struct{}, 1)}
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	first := &liveQuoteJob{
		entry:  fileEntry{relPath: "first.txt", hasScore: true},
		ctx:    firstCtx,
		cancel: firstCancel,
		rank:   0,
	}
	second := &liveQuoteJob{
		entry:  fileEntry{relPath: "second.txt", hasScore: true},
		ctx:    secondCtx,
		cancel: secondCancel,
		rank:   1,
	}
	manager := &liveQuoteManager{
		embedder: scheduler,
		queryVec: []float32{1, 0},
		limit:    2,
		jobs: map[string]*liveQuoteJob{
			"first.txt":  first,
			"second.txt": second,
		},
		pending: []*liveQuoteJob{first, second},
		wake:    make(chan struct{}, 1),
	}

	manager.sync([]fileEntry{second.entry, first.entry})
	if got := manager.next(); got != second {
		t.Fatalf("next pending quote job = %#v, want newly promoted second match", got)
	}
	if got := first.rank; got != 1 {
		t.Fatalf("first quote rank = %d, want updated rank 1", got)
	}
}
