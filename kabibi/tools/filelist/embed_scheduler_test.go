package filelist

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"ai"
)

func TestBatchQuotasAcrossCapacitiesAndRemainders(t *testing.T) {
	for slots := 0; slots <= 16; slots++ {
		for remainder := 0; remainder <= highRequestsPerLow; remainder++ {
			high, low, next := batchQuotas(slots, remainder)
			if high+low != slots {
				t.Fatalf("slots=%d remainder=%d: high+low=%d, want %d", slots, remainder, high+low, slots)
			}
			total := slots*highRequestsPerLow + remainder
			if high != total/(highRequestsPerLow+1) || next != total%(highRequestsPerLow+1) {
				t.Fatalf("slots=%d remainder=%d: quotas=%d/%d next=%d, want high=%d next=%d",
					slots, remainder, high, low, next, total/(highRequestsPerLow+1), total%(highRequestsPerLow+1))
			}
			if slots == 0 && next != remainder {
				t.Fatalf("empty batch advanced remainder %d to %d", remainder, next)
			}
		}
	}
}

func TestBatchQuotaRemainderCarryover(t *testing.T) {
	tests := []struct {
		name     string
		slots    int
		want     [][2]int
		wantNext int
	}{
		{
			name:  "one slot batches",
			slots: 1,
			want:  [][2]int{{0, 1}, {1, 0}, {1, 0}, {1, 0}, {1, 0}},
		},
		{
			name:  "two slot batches",
			slots: 2,
			want:  [][2]int{{1, 1}, {2, 0}, {1, 1}, {2, 0}, {2, 0}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remainder := 0
			for index, want := range test.want {
				high, low, next := batchQuotas(test.slots, remainder)
				if got := [2]int{high, low}; got != want {
					t.Fatalf("batch %d quotas = %v, want %v", index, got, want)
				}
				remainder = next
			}
			if remainder != test.wantNext {
				t.Fatalf("final remainder = %d, want %d", remainder, test.wantNext)
			}
		})
	}
}

func TestSelectEmbeddingBatchUsesOneSetAndBothSortOrders(t *testing.T) {
	tasks := []*embeddingJob{
		{key: "A", sequence: 1, highCount: 12, lowCount: 0},
		{key: "B", sequence: 2, highCount: 12, lowCount: 1},
		{key: "C", sequence: 3, highCount: 10, lowCount: 3},
		{key: "D", sequence: 4, highCount: 9, lowCount: 1},
		{key: "E", sequence: 5, highCount: 8, lowCount: 0},
		{key: "F", sequence: 6, highCount: 7, lowCount: 4},
		{key: "G", sequence: 7, highCount: 6, lowCount: 1},
		{key: "H", sequence: 8, highCount: 5, lowCount: 0},
		{key: "I", sequence: 9, highCount: 1, lowCount: 7},
		{key: "J", sequence: 10, highCount: 2, lowCount: 7},
		{key: "K", sequence: 11, highCount: 4, lowCount: 8},
		{key: "L", sequence: 12, highCount: 3, lowCount: 8},
	}
	selected, remainder := selectEmbeddingBatch(tasks, 10, 0)
	if remainder != 0 {
		t.Fatalf("remainder = %d, want zero after ten slots", remainder)
	}
	var got []string
	seen := make(map[*embeddingJob]bool)
	for _, task := range selected {
		if seen[task] {
			t.Fatalf("task %q selected more than once", task.key)
		}
		seen[task] = true
		got = append(got, task.key)
	}
	want := []string{"B", "A", "C", "D", "E", "F", "G", "H", "K", "L"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected tasks = %v, want %v", got, want)
	}
}

func TestSelectEmbeddingBatchUsesSequenceForCompleteTies(t *testing.T) {
	earlier := &embeddingJob{key: "earlier", sequence: 1, highCount: 2, lowCount: 2}
	later := &embeddingJob{key: "later", sequence: 2, highCount: 2, lowCount: 2}
	selected, _ := selectEmbeddingBatch([]*embeddingJob{later, earlier}, 2, 0)
	if len(selected) != 2 || selected[0] != earlier || selected[1] != later {
		t.Fatalf("selected = %#v, want insertion sequence order", selected)
	}
}

func TestSelectEmbeddingBatchDoesNotCreatePriorityPools(t *testing.T) {
	tasks := []*embeddingJob{
		{key: "low-most", sequence: 1, lowCount: 5},
		{key: "low-next", sequence: 2, lowCount: 3},
		{key: "none", sequence: 3},
	}
	selected, _ := selectEmbeddingBatch(tasks, 3, 0)
	if len(selected) != len(tasks) {
		t.Fatalf("selected %d tasks, want %d", len(selected), len(tasks))
	}
	got := []string{selected[0].key, selected[1].key, selected[2].key}
	if want := []string{"low-most", "low-next", "none"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all-low task order = %v, want %v", got, want)
	}

	tasks = []*embeddingJob{
		{key: "high-most", sequence: 1, highCount: 5},
		{key: "high-next", sequence: 2, highCount: 3},
		{key: "none", sequence: 3},
	}
	selected, _ = selectEmbeddingBatch(tasks, 3, 0)
	got = []string{selected[0].key, selected[1].key, selected[2].key}
	if want := []string{"high-most", "high-next", "none"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all-high task order = %v, want %v", got, want)
	}
}

func TestNextBatchCapacityEmptyQueueAndUnboundedPendingTasks(t *testing.T) {
	scheduler := newManualScheduler(&simpleEmbedder{})
	defer scheduler.Close()
	for index := range 300 {
		part := ai.PromptPart{Role: ai.RoleModel, Text: fmt.Sprintf("task-%03d", index)}
		if _, err := scheduler.enqueue(context.Background(), part, index%2 == 0); err != nil {
			t.Fatalf("enqueue task %d: %v", index, err)
		}
	}
	if got := len(scheduler.pending); got != 300 {
		t.Fatalf("pending tasks = %d, want 300 (no queue capacity limit)", got)
	}
	first := scheduler.nextBatch()
	if len(first) != maxEmbeddingBatch {
		t.Fatalf("first batch size = %d, want %d", len(first), maxEmbeddingBatch)
	}
	if scheduler.remainder != 1 {
		t.Fatalf("remainder after four slots = %d, want 1", scheduler.remainder)
	}
	emptyScheduler := newManualScheduler(&simpleEmbedder{})
	defer emptyScheduler.Close()
	if empty := emptyScheduler.nextBatch(); len(empty) != 0 {
		t.Fatalf("empty batch = %v, want none", empty)
	}
	if emptyScheduler.remainder != 0 {
		t.Fatalf("empty batch advanced remainder to %d", emptyScheduler.remainder)
	}
}

func TestCanceledPendingRequestDecrementsDemandAndEjectsTask(t *testing.T) {
	scheduler := newManualScheduler(&simpleEmbedder{})
	defer scheduler.Close()
	highCtx, cancelHigh := context.WithCancel(context.Background())
	defer cancelHigh()
	lowCtx, cancelLow := context.WithCancel(context.Background())
	defer cancelLow()
	part := ai.PromptPart{Role: ai.RoleModel, Text: "shared"}
	high, err := scheduler.enqueue(highCtx, part, false)
	if err != nil {
		t.Fatal(err)
	}
	low, err := scheduler.enqueue(lowCtx, part, true)
	if err != nil {
		t.Fatal(err)
	}
	job := scheduler.jobs[embeddingKey(part)]
	if job.highCount != 1 || job.lowCount != 1 {
		t.Fatalf("demand counts = %d/%d, want 1/1", job.highCount, job.lowCount)
	}

	highDone := runAwait(scheduler, highCtx, high)
	cancelHigh()
	if err := <-highDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("high waiter error = %v, want context.Canceled", err)
	}
	if job.highCount != 0 || job.lowCount != 1 || scheduler.jobs[job.key] != job {
		t.Fatalf("after high cancellation counts=%d/%d taskPresent=%t; want 0/1 and present",
			job.highCount, job.lowCount, scheduler.jobs[job.key] == job)
	}

	lowDone := runAwait(scheduler, lowCtx, low)
	cancelLow()
	if err := <-lowDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("low waiter error = %v, want context.Canceled", err)
	}
	if _, exists := scheduler.jobs[job.key]; exists {
		t.Fatal("zero-waiter task remains in task pool")
	}
	if len(scheduler.pending) != 0 {
		t.Fatalf("pending tasks = %d, want zero", len(scheduler.pending))
	}
	if selected := scheduler.nextBatch(); len(selected) != 0 {
		t.Fatalf("selected canceled task: %v", selected)
	}
}

func TestCoalescedWaitersShareInflightTaskAndCancellation(t *testing.T) {
	embedder := newGatedBatchEmbedder()
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	embedder.scheduler = scheduler
	defer closeSchedulerAndWait(t, scheduler)
	part := ai.PromptPart{Role: ai.RoleModel, Text: "shared"}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first, err := scheduler.enqueue(firstCtx, part, false)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := runAwait(scheduler, firstCtx, first)

	call := receiveBatch(t, embedder.calls)
	if got := call.inputs; !reflect.DeepEqual(got, []string{"shared"}) {
		t.Fatalf("backend inputs = %v, want one coalesced task", got)
	}
	if !call.lockWasAvailable {
		t.Fatal("scheduler mutex was held while calling the embedder")
	}
	second, err := scheduler.enqueue(context.Background(), part, true)
	if err != nil {
		t.Fatal(err)
	}
	secondDone := runAwait(scheduler, context.Background(), second)
	scheduler.mu.Lock()
	job := scheduler.jobs[embeddingKey(part)]
	inFlight, highCount, lowCount := job.inFlight, job.highCount, job.lowCount
	scheduler.mu.Unlock()
	if !inFlight || highCount != 1 || lowCount != 1 {
		t.Fatalf("in-flight task state=%t demand=%d/%d; want true and 1/1", inFlight, highCount, lowCount)
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	embedder.release <- struct{}{}
	if err := <-secondDone; err != nil {
		t.Fatalf("remaining waiter failed: %v", err)
	}
	if got := embedder.callCount(); got != 1 {
		t.Fatalf("backend calls = %d, want 1", got)
	}
}

func TestCoalescedTaskTracksThreeHighAndTwoLowWaiters(t *testing.T) {
	embedder := newGatedBatchEmbedder()
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	embedder.scheduler = scheduler
	defer closeSchedulerAndWait(t, scheduler)

	part := ai.PromptPart{Role: ai.RoleModel, Text: "coalesced-demand"}
	requests := make([]*embeddingRequest, 0, 5)
	contexts := make([]context.Context, 0, 5)
	for index := range 5 {
		ctx := context.Background()
		lowPriority := index >= 3
		request, err := scheduler.enqueue(ctx, part, lowPriority)
		if err != nil {
			t.Fatalf("enqueue waiter %d: %v", index, err)
		}
		requests = append(requests, request)
		contexts = append(contexts, ctx)
	}
	scheduler.mu.Lock()
	job := scheduler.jobs[embeddingKey(part)]
	taskCount, highCount, lowCount := len(scheduler.jobs), job.highCount, job.lowCount
	scheduler.mu.Unlock()
	if taskCount != 1 || highCount != 3 || lowCount != 2 {
		t.Fatalf("task pool=%d demand=%d/%d; want one task with 3/2 waiters",
			taskCount, highCount, lowCount)
	}
	done := make([]<-chan error, len(requests))
	for index, request := range requests {
		done[index] = runAwait(scheduler, contexts[index], request)
	}
	call := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(call.inputs, []string{"coalesced-demand"}) {
		t.Fatalf("backend inputs = %v, want one unique embedding task", call.inputs)
	}
	embedder.release <- struct{}{}
	for index, result := range done {
		if err := <-result; err != nil {
			t.Errorf("waiter %d failed: %v", index, err)
		}
	}
	if got := embedder.callCount(); got != 1 {
		t.Fatalf("backend calls = %d, want one", got)
	}
}

func TestWorkerBatchesDistinctTasksByLiveDemand(t *testing.T) {
	embedder := newGatedBatchEmbedder()
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	embedder.scheduler = scheduler
	defer closeSchedulerAndWait(t, scheduler)

	var waiterResults []<-chan error
	enqueueDemand := func(text string, high, low int) {
		t.Helper()
		part := ai.PromptPart{Role: ai.RoleModel, Text: text}
		for range high {
			request, err := scheduler.enqueue(context.Background(), part, false)
			if err != nil {
				t.Fatalf("enqueue high-priority %q: %v", text, err)
			}
			waiterResults = append(waiterResults, runAwait(scheduler, context.Background(), request))
		}
		for range low {
			request, err := scheduler.enqueue(context.Background(), part, true)
			if err != nil {
				t.Fatalf("enqueue low-priority %q: %v", text, err)
			}
			waiterResults = append(waiterResults, runAwait(scheduler, context.Background(), request))
		}
	}

	enqueueDemand("blocker", 1, 0)
	firstBatch := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(firstBatch.inputs, []string{"blocker"}) {
		t.Fatalf("initial batch = %v, want the blocker task", firstBatch.inputs)
	}
	scheduler.mu.Lock()
	scheduler.remainder = 3
	scheduler.mu.Unlock()

	enqueueDemand("task-a", 5, 0)
	enqueueDemand("task-b", 4, 0)
	enqueueDemand("task-c", 3, 0)
	enqueueDemand("task-d", 2, 6)
	enqueueDemand("task-e", 1, 1)

	embedder.release <- struct{}{}
	secondBatch := receiveBatch(t, embedder.calls)
	wantSecondBatch := []string{"task-a", "task-b", "task-c", "task-d"}
	if !reflect.DeepEqual(secondBatch.inputs, wantSecondBatch) {
		t.Fatalf("second batch = %v, want %v", secondBatch.inputs, wantSecondBatch)
	}
	embedder.release <- struct{}{}
	thirdBatch := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(thirdBatch.inputs, []string{"task-e"}) {
		t.Fatalf("third batch = %v, want the remaining task-e", thirdBatch.inputs)
	}
	embedder.release <- struct{}{}

	for index, done := range waiterResults {
		if err := awaitError(t, fmt.Sprintf("waiter %d", index+1), done); err != nil {
			t.Errorf("waiter %d failed: %v", index+1, err)
		}
	}
}

func TestCanceledQueuedTaskIsNotSubmitted(t *testing.T) {
	embedder := newGatedBatchEmbedder()
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	embedder.scheduler = scheduler
	defer closeSchedulerAndWait(t, scheduler)

	blockerPart := ai.PromptPart{Role: ai.RoleModel, Text: "blocker"}
	blocker, err := scheduler.enqueue(context.Background(), blockerPart, false)
	if err != nil {
		t.Fatal(err)
	}
	blockerDone := runAwait(scheduler, context.Background(), blocker)
	firstCall := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(firstCall.inputs, []string{"blocker"}) {
		t.Fatalf("first backend inputs = %v, want blocker", firstCall.inputs)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	canceledPart := ai.PromptPart{Role: ai.RoleModel, Text: "canceled-before-selection"}
	canceled, err := scheduler.enqueue(cancelCtx, canceledPart, true)
	if err != nil {
		t.Fatal(err)
	}
	canceledDone := runAwait(scheduler, cancelCtx, canceled)
	cancel()
	if err := awaitError(t, "canceled queued request", canceledDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled queued request error = %v, want context.Canceled", err)
	}
	if _, exists := scheduler.jobs[embeddingKey(canceledPart)]; exists {
		t.Fatal("canceled pending task was not ejected")
	}

	survivorPart := ai.PromptPart{Role: ai.RoleModel, Text: "survivor"}
	survivor, err := scheduler.enqueue(context.Background(), survivorPart, false)
	if err != nil {
		t.Fatal(err)
	}
	survivorDone := runAwait(scheduler, context.Background(), survivor)
	embedder.release <- struct{}{}
	if err := awaitError(t, "blocker request", blockerDone); err != nil {
		t.Fatalf("blocker failed: %v", err)
	}
	secondCall := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(secondCall.inputs, []string{"survivor"}) {
		t.Fatalf("second backend inputs = %v, canceled queued work reached inference", secondCall.inputs)
	}
	embedder.release <- struct{}{}
	if err := awaitError(t, "survivor request", survivorDone); err != nil {
		t.Fatalf("survivor failed: %v", err)
	}
}

func TestCloseCancelsPendingAndInflightWaiters(t *testing.T) {
	embedder := newGatedBatchEmbedder()
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	embedder.scheduler = scheduler
	first, err := scheduler.enqueue(context.Background(), ai.PromptPart{Role: ai.RoleModel, Text: "first"}, false)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := runAwait(scheduler, context.Background(), first)
	_ = receiveBatch(t, embedder.calls)
	second, err := scheduler.enqueue(context.Background(), ai.PromptPart{Role: ai.RoleModel, Text: "second"}, false)
	if err != nil {
		t.Fatal(err)
	}
	secondDone := runAwait(scheduler, context.Background(), second)

	scheduler.Close()
	for name, done := range map[string]<-chan error{"inflight": firstDone, "pending": secondDone} {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s waiter error = %v, want context.Canceled", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s waiter remained blocked after Close", name)
		}
	}
	select {
	case <-scheduler.workerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler worker did not exit after backend cancellation")
	}
}

type simpleEmbedder struct{}

func (*simpleEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func newManualScheduler(embedder ai.PartEmbedder) *concurrentEmbedder {
	ctx, cancel := context.WithCancel(context.Background())
	return &concurrentEmbedder{
		ctx:        ctx,
		cancel:     cancel,
		embedder:   embedder,
		jobs:       make(map[string]*embeddingJob),
		wake:       make(chan struct{}, 1),
		workerDone: make(chan struct{}),
	}
}

func runAwait(scheduler *concurrentEmbedder, ctx context.Context, request *embeddingRequest) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := scheduler.await(ctx, request)
		done <- err
	}()
	return done
}

type gatedBatchCall struct {
	inputs           []string
	lockWasAvailable bool
}

type gatedBatchEmbedder struct {
	mu        sync.Mutex
	calls     chan gatedBatchCall
	release   chan struct{}
	canceled  chan struct{}
	scheduler *concurrentEmbedder
	count     int
}

func newGatedBatchEmbedder() *gatedBatchEmbedder {
	return &gatedBatchEmbedder{
		calls:    make(chan gatedBatchCall, 16),
		release:  make(chan struct{}, 16),
		canceled: make(chan struct{}, 16),
	}
}

func (e *gatedBatchEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (e *gatedBatchEmbedder) EmbedBatchContext(ctx context.Context, parts []ai.PromptPart) ([][]float32, error) {
	inputs := make([]string, len(parts))
	for i, part := range parts {
		inputs[i] = part.Text
	}
	lockAvailable := true
	if e.scheduler != nil {
		lockAvailable = e.scheduler.mu.TryLock()
		if lockAvailable {
			e.scheduler.mu.Unlock()
		}
	}
	e.mu.Lock()
	e.count++
	e.mu.Unlock()
	e.calls <- gatedBatchCall{inputs: inputs, lockWasAvailable: lockAvailable}
	select {
	case <-e.release:
		vectors := make([][]float32, len(parts))
		for i := range vectors {
			vectors[i] = []float32{1, 0}
		}
		return vectors, nil
	case <-ctx.Done():
		select {
		case e.canceled <- struct{}{}:
		default:
		}
		return nil, ctx.Err()
	}
}

func (e *gatedBatchEmbedder) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

func receiveBatch(t *testing.T, calls <-chan gatedBatchCall) gatedBatchCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for embedder batch")
		return gatedBatchCall{}
	}
}

func awaitError(t *testing.T, name string, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}

func closeSchedulerAndWait(t *testing.T, scheduler *concurrentEmbedder) {
	t.Helper()
	scheduler.Close()
	select {
	case <-scheduler.workerDone:
	case <-time.After(5 * time.Second):
		t.Error("scheduler worker did not exit after Close")
	}
}

func TestSelectedTaskCancellationDoesNotDropLiveCoalescedWaiter(t *testing.T) {
	embedder := newGatedBatchEmbedder()
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer closeSchedulerAndWait(t, scheduler)
	part := ai.PromptPart{Role: ai.RoleModel, Text: "selected"}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first, err := scheduler.enqueue(firstCtx, part, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.enqueue(context.Background(), part, true)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := runAwait(scheduler, firstCtx, first)
	secondDone := runAwait(scheduler, context.Background(), second)
	call := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(call.inputs, []string{"selected"}) {
		t.Fatalf("backend inputs = %v, want one selected task", call.inputs)
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	embedder.release <- struct{}{}
	if err := <-secondDone; err != nil {
		t.Fatalf("live coalesced waiter failed after sibling cancellation: %v", err)
	}
}

func TestSelectedTaskCancellationBeforeBackendCallPreservesLiveWaiter(t *testing.T) {
	embedder := newGatedBatchEmbedder()
	scheduler := newManualScheduler(embedder)
	embedder.scheduler = scheduler
	defer scheduler.Close()

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	part := ai.PromptPart{Role: ai.RoleModel, Text: "selected-before-call"}
	first, err := scheduler.enqueue(firstCtx, part, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.enqueue(context.Background(), part, true)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := runAwait(scheduler, firstCtx, first)
	secondDone := runAwait(scheduler, context.Background(), second)

	selected := scheduler.nextBatch()
	if len(selected) != 1 {
		t.Fatalf("selected batch size = %d, want one coalesced task", len(selected))
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	if selected[0].highCount != 0 || selected[0].lowCount != 1 {
		t.Fatalf("selected task demand = %d/%d, want 0/1", selected[0].highCount, selected[0].lowCount)
	}

	type batchResult struct {
		vectors [][]float32
		errs    []error
	}
	batchDone := make(chan batchResult, 1)
	go func() {
		vectors, errs := scheduler.embedBatch(selected)
		batchDone <- batchResult{vectors: vectors, errs: errs}
	}()
	call := receiveBatch(t, embedder.calls)
	if !reflect.DeepEqual(call.inputs, []string{"selected-before-call"}) {
		t.Fatalf("backend inputs = %v, want selected task despite post-selection cancellation", call.inputs)
	}
	embedder.release <- struct{}{}
	result := <-batchDone
	scheduler.complete(selected[0], result.vectors[0], result.errs[0])
	if err := <-secondDone; err != nil {
		t.Fatalf("remaining selected-task waiter failed: %v", err)
	}
}

func TestCancelingLastInflightWaiterCancelsBackendContext(t *testing.T) {
	embedder := newGatedBatchEmbedder()
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	embedder.scheduler = scheduler
	defer closeSchedulerAndWait(t, scheduler)

	ctx, cancel := context.WithCancel(context.Background())
	request, err := scheduler.enqueue(ctx, ai.PromptPart{Role: ai.RoleModel, Text: "cancel-inflight"}, true)
	if err != nil {
		t.Fatal(err)
	}
	done := runAwait(scheduler, ctx, request)
	_ = receiveBatch(t, embedder.calls)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	select {
	case <-embedder.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("last waiter cancellation did not cancel the in-flight backend context")
	}
	scheduler.Close()
	select {
	case <-scheduler.workerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not finish after backend cancellation")
	}
}

func TestSelectionDoesNotDuplicateDualDemandTask(t *testing.T) {
	dual := &embeddingJob{key: "dual", sequence: 1, highCount: 10, lowCount: 10}
	tasks := []*embeddingJob{dual}
	selected, _ := selectEmbeddingBatch(tasks, 1, 0)
	if len(selected) != 1 || selected[0] != dual {
		t.Fatalf("selected = %v, want dual-demand task exactly once", selected)
	}
}

func TestSortIsStableForAllPriorityCounts(t *testing.T) {
	tasks := []*embeddingJob{
		{key: "a", sequence: 3, highCount: 0, lowCount: 0},
		{key: "b", sequence: 1, highCount: 0, lowCount: 0},
		{key: "c", sequence: 2, highCount: 0, lowCount: 0},
	}
	selected, _ := selectEmbeddingBatch(tasks, len(tasks), 0)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].sequence < tasks[j].sequence })
	if !reflect.DeepEqual(selected, tasks) {
		t.Fatalf("selection order = %v, want insertion sequence", selected)
	}
}
