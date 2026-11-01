package filelist

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"ai"
)

func TestEmbedSchedulerUsesStrictFourToOneRatioAndChunkWeights(t *testing.T) {
	ctx := context.Background()
	job := func(id uint64, text string, priorities ...bool) *embeddingJob {
		result := &embeddingJob{
			key:      text,
			part:     ai.PromptPart{Role: ai.RoleModel, Text: text},
			sequence: id,
			waiters:  make(map[uint64]*embeddingRequest),
		}
		for offset, low := range priorities {
			requestID := id + uint64(offset)
			result.waiters[requestID] = &embeddingRequest{
				id:          requestID,
				ctx:         ctx,
				part:        result.part,
				lowPriority: low,
			}
		}
		return result
	}
	scheduler := &concurrentEmbedder{
		ctx: context.Background(),
		pending: []*embeddingJob{
			job(1, "popular", false, false),
			job(3, "other", false),
			job(4, "third", false),
			job(5, "fourth", false),
			job(6, "low", true),
		},
		jobs:           make(map[string]*embeddingJob),
		queuedRequests: 6,
	}
	for _, pending := range scheduler.pending {
		scheduler.jobs[pending.key] = pending
	}

	firstBatch := scheduler.nextBatch()
	if len(firstBatch) != 4 {
		t.Fatalf("first batch size = %d, want four high-priority chunks", len(firstBatch))
	}
	selected := make([]string, len(firstBatch))
	for index, job := range firstBatch {
		selected[index] = job.part.Text
	}
	if want := []string{"popular", "other", "third", "fourth"}; !reflect.DeepEqual(selected, want) {
		t.Fatalf("first scheduled batch = %v, want %v", selected, want)
	}
	secondBatch := scheduler.nextBatch()
	if len(secondBatch) != 1 || secondBatch[0].part.Text != "low" {
		t.Fatalf("second batch = %#v, want one low-priority chunk after four high-priority chunks", secondBatch)
	}
}

func TestEmbedSchedulerUsesCurrentOrderForLowPriorityRequests(t *testing.T) {
	scheduler := &concurrentEmbedder{
		ctx:  context.Background(),
		jobs: make(map[string]*embeddingJob),
		wake: make(chan struct{}, 1),
	}
	makeJob := func(id uint64, text string, rank int) *embeddingJob {
		part := ai.PromptPart{Role: ai.RoleModel, Text: text}
		order := scheduler.NewOrderHandle()
		order.SetRank(rank)
		request := &embeddingRequest{
			id:          id,
			ctx:         context.Background(),
			part:        part,
			lowPriority: true,
			order:       order,
		}
		job := &embeddingJob{
			key:      text,
			part:     part,
			waiters:  map[uint64]*embeddingRequest{id: request},
			sequence: id,
		}
		request.job = job
		scheduler.jobs[text] = job
		scheduler.pending = append(scheduler.pending, job)
		scheduler.queuedRequests++
		return job
	}
	first := makeJob(1, "first", 0)
	second := makeJob(2, "second", 1)

	selected := scheduler.highestWeightedJobLocked(true)
	if selected != first {
		t.Fatalf("initial low-priority selection = %#v, want top-ranked first job", selected)
	}

	first.waiters[1].order.SetRank(1)
	second.waiters[2].order.SetRank(0)
	selected = scheduler.highestWeightedJobLocked(true)
	if selected != second {
		t.Fatalf("selection after rank update = %#v, want promoted second job", selected)
	}
}

func TestEmbedSchedulerCancellationRemovesOnlyThatRequestContribution(t *testing.T) {
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB := context.Background()
	request := func(id uint64, ctx context.Context, job *embeddingJob, low bool) *embeddingRequest {
		return &embeddingRequest{
			id:          id,
			ctx:         ctx,
			part:        job.part,
			lowPriority: low,
			job:         job,
		}
	}
	shared := &embeddingJob{key: "shared", part: ai.PromptPart{Role: ai.RoleModel, Text: "shared"}, waiters: make(map[uint64]*embeddingRequest), sequence: 1}
	other := &embeddingJob{key: "other", part: ai.PromptPart{Role: ai.RoleModel, Text: "other"}, waiters: make(map[uint64]*embeddingRequest), sequence: 4}
	shared.waiters[1] = request(1, ctxA, shared, false)
	shared.waiters[2] = request(2, ctxA, shared, false)
	shared.waiters[3] = request(3, ctxB, shared, false)
	other.waiters[4] = request(4, ctxB, other, false)
	other.waiters[5] = request(5, ctxB, other, false)
	scheduler := &concurrentEmbedder{
		ctx:            context.Background(),
		pending:        []*embeddingJob{shared, other},
		jobs:           map[string]*embeddingJob{"shared": shared, "other": other},
		queuedRequests: 5,
	}

	cancelA()
	scheduler.remove(1)
	scheduler.remove(2)
	next := scheduler.nextBatch()
	if len(next) != 2 || next[0].part.Text != "other" {
		t.Fatalf("next batch after cancellation = %#v, want the still-more-popular other chunk first", next)
	}
	if len(shared.waiters) != 1 {
		t.Fatalf("remaining shared context contributions = %d, want one", len(shared.waiters))
	}
}

type cancellationEmbedder struct {
	started chan struct{}
	calls   chan string
}

func (e *cancellationEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (e *cancellationEmbedder) EmbedBatchContext(ctx context.Context, parts []ai.PromptPart) ([][]float32, error) {
	input := parts[0].Text
	e.calls <- input
	if e.started != nil && input == "slow" {
		close(e.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return [][]float32{{1, 0}}, nil
}

func TestEmbedSchedulerCanceledWaiterDoesNotPreventQueuedHighPriorityWork(t *testing.T) {
	embedder := &cancellationEmbedder{
		started: make(chan struct{}),
		calls:   make(chan string, 4),
	}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()

	slowCtx, cancelSlow := context.WithCancel(context.Background())
	slowResult := make(chan error, 1)
	go func() {
		_, err := scheduler.Embed(slowCtx, ai.PromptPart{Role: ai.RoleModel, Text: "slow"}, true, nil)
		slowResult <- err
	}()
	<-embedder.started
	cancelSlow()
	if err := <-slowResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Embed() error = %v, want context.Canceled", err)
	}

	vector, err := scheduler.Embed(context.Background(), ai.PromptPart{Role: ai.RoleModel, Text: "high"}, false, nil)
	if err != nil {
		t.Fatalf("high-priority Embed(): %v", err)
	}
	if !reflect.DeepEqual(vector, []float32{1, 0}) {
		t.Fatalf("high-priority vector = %v", vector)
	}
	if got := <-embedder.calls; got != "slow" {
		t.Fatalf("first embed input = %q, want slow", got)
	}
	if got := <-embedder.calls; got != "high" {
		t.Fatalf("second embed input = %q, want high", got)
	}
}

type sharedRequestEmbedder struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (e *sharedRequestEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (e *sharedRequestEmbedder) EmbedBatchContext(ctx context.Context, parts []ai.PromptPart) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	select {
	case e.started <- struct{}{}:
	default:
	}
	select {
	case <-e.release:
		return [][]float32{{1, 0}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestEmbedSchedulerSharesChunkAndRetainsOtherContextDemand(t *testing.T) {
	embedder := &sharedRequestEmbedder{started: make(chan struct{}, 1), release: make(chan struct{})}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := scheduler.Embed(firstCtx, ai.PromptPart{Role: ai.RoleModel, Text: "same chunk"}, true, nil)
		firstResult <- err
	}()
	<-embedder.started

	secondResult := make(chan error, 1)
	go func() {
		vector, err := scheduler.Embed(context.Background(), ai.PromptPart{Role: ai.RoleModel, Text: "same chunk"}, true, nil)
		if err == nil && !reflect.DeepEqual(vector, []float32{1, 0}) {
			err = errors.New("shared request returned the wrong vector")
		}
		secondResult <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		scheduler.mu.Lock()
		job := scheduler.jobs[embeddingKey(ai.PromptPart{Role: ai.RoleModel, Text: "same chunk"})]
		waiters := 0
		if job != nil {
			waiters = len(job.waiters)
		}
		scheduler.mu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second waiter did not attach to the in-flight chunk")
		}
		time.Sleep(time.Millisecond)
	}
	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter error = %v, want context.Canceled", err)
	}
	close(embedder.release)
	if err := <-secondResult; err != nil {
		t.Fatalf("second waiter should retain the shared embedding: %v", err)
	}
	embedder.mu.Lock()
	calls := embedder.calls
	embedder.mu.Unlock()
	if calls != 1 {
		t.Fatalf("underlying embed calls = %d, want one shared computation", calls)
	}
}

type contextIgnoringEmbedder struct {
	started chan struct{}
	release chan struct{}
}

func (e *contextIgnoringEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	<-e.release
	return []float32{1, 0}, nil
}

func (e *contextIgnoringEmbedder) EmbedBatchContext(_ context.Context, parts []ai.PromptPart) ([][]float32, error) {
	select {
	case e.started <- struct{}{}:
	default:
	}
	<-e.release
	return [][]float32{{1, 0}}, nil
}

func TestCanceledAwaitUnwindsWhileEmbeddingContinues(t *testing.T) {
	embedder := &contextIgnoringEmbedder{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := scheduler.Embed(ctx, ai.PromptPart{Role: ai.RoleModel, Text: "quote"}, true, nil)
		result <- err
	}()
	<-embedder.started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled await error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled await remained blocked on the in-flight embedder call")
	}

	scheduler.Close()
	close(embedder.release)
	select {
	case <-scheduler.workerDone:
	case <-time.After(time.Second):
		t.Fatal("scheduler worker did not exit after the embedder call returned")
	}
}
