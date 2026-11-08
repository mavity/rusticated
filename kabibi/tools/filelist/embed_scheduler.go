package filelist

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"ai"
)

const (
	highRequestsPerLow = 4
	maxEmbeddingBatch  = 4
)

type embeddingResult struct {
	vector []float32
	err    error
}

type embeddingRequest struct {
	id          uint64
	ctx         context.Context
	cancel      context.CancelFunc
	lowPriority bool
	result      chan embeddingResult
	job         *embeddingJob
}

type embeddingJob struct {
	key       string
	part      ai.PromptPart
	waiters   map[uint64]*embeddingRequest
	sequence  uint64
	highCount int
	lowCount  int
	ctx       context.Context
	cancel    context.CancelFunc
	inFlight  bool
}

func (job *embeddingJob) stop() {
	if job.cancel != nil {
		job.cancel()
	}
}

type concurrentEmbedder struct {
	ctx        context.Context
	cancel     context.CancelFunc
	embedder   ai.PartEmbedder
	diagnostic func(string, ...any)

	mu         sync.Mutex
	pending    []*embeddingJob
	jobs       map[string]*embeddingJob
	nextID     uint64
	remainder  int
	closed     bool
	wake       chan struct{}
	workerDone chan struct{}
}

func newConcurrentEmbedder(ctx context.Context, embedder ai.PartEmbedder) *concurrentEmbedder {
	if ctx == nil {
		ctx = context.Background()
	}
	schedulerCtx, cancel := context.WithCancel(ctx)
	scheduler := &concurrentEmbedder{
		ctx:        schedulerCtx,
		cancel:     cancel,
		embedder:   embedder,
		jobs:       make(map[string]*embeddingJob),
		wake:       make(chan struct{}, 1),
		workerDone: make(chan struct{}),
	}
	go scheduler.run()
	return scheduler
}

func (e *concurrentEmbedder) Close() {
	if e == nil {
		return
	}
	e.cancel()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	jobs := make([]*embeddingJob, 0, len(e.jobs))
	var waiters []*embeddingRequest
	for _, job := range e.jobs {
		jobs = append(jobs, job)
		for _, waiter := range job.waiters {
			waiters = append(waiters, waiter)
		}
		job.waiters = nil
		job.highCount = 0
		job.lowCount = 0
	}
	e.pending = nil
	e.jobs = make(map[string]*embeddingJob)
	e.mu.Unlock()
	for _, job := range jobs {
		job.stop()
	}
	for _, waiter := range waiters {
		waiter.cancel()
	}
	e.signal()
	select {
	case <-e.workerDone:
	default:
	}
}

func (e *concurrentEmbedder) Embed(ctx context.Context, part ai.PromptPart, lowPriority bool) ([]float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := e.enqueue(ctx, part, lowPriority)
	if err != nil {
		return nil, err
	}
	return e.await(ctx, request)
}

func (e *concurrentEmbedder) enqueue(ctx context.Context, part ai.PromptPart, lowPriority bool) (*embeddingRequest, error) {
	if e == nil || e.embedder == nil {
		return nil, fmt.Errorf("no embedder configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithCancel(ctx)
	stopParent := context.AfterFunc(e.ctx, cancel)
	request := &embeddingRequest{
		ctx:         requestCtx,
		cancel:      func() { stopParent(); cancel() },
		lowPriority: lowPriority,
		result:      make(chan embeddingResult, 1),
	}

	e.mu.Lock()
	if e.closed || e.ctx.Err() != nil {
		e.mu.Unlock()
		request.cancel()
		return nil, context.Canceled
	}
	key := embeddingKey(part)
	job := e.jobs[key]
	if job == nil {
		job = &embeddingJob{
			key:     key,
			part:    part,
			waiters: make(map[uint64]*embeddingRequest),
		}
		e.jobs[key] = job
		e.pending = append(e.pending, job)
	}
	e.nextID++
	request.id = e.nextID
	request.job = job
	job.waiters[request.id] = request
	if job.sequence == 0 {
		job.sequence = request.id
	}
	if lowPriority {
		job.lowCount++
	} else {
		job.highCount++
	}
	e.mu.Unlock()
	e.signal()
	return request, nil
}

func (e *concurrentEmbedder) await(ctx context.Context, request *embeddingRequest) ([]float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result := <-request.result:
		request.cancel()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := e.ctx.Err(); err != nil {
			return nil, err
		}
		return result.vector, result.err
	case <-ctx.Done():
		e.remove(request)
		request.cancel()
		return nil, ctx.Err()
	case <-e.ctx.Done():
		e.remove(request)
		request.cancel()
		return nil, e.ctx.Err()
	}
}

func (e *concurrentEmbedder) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *concurrentEmbedder) remove(request *embeddingRequest) {
	e.mu.Lock()
	var cancel context.CancelFunc
	if request != nil && request.job != nil {
		cancel = e.removeWaiterLocked(request.job, request)
	}
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.signal()
}

func (e *concurrentEmbedder) removeWaiterLocked(job *embeddingJob, request *embeddingRequest) context.CancelFunc {
	if job.waiters[request.id] != request {
		return nil
	}
	delete(job.waiters, request.id)
	if request.lowPriority {
		job.lowCount--
	} else {
		job.highCount--
	}
	if len(job.waiters) != 0 {
		return nil
	}
	cancel := job.cancel
	if e.jobs[job.key] == job {
		delete(e.jobs, job.key)
	}
	if !job.inFlight {
		e.removePendingLocked(job)
	}
	return cancel
}

func (e *concurrentEmbedder) removePendingLocked(job *embeddingJob) {
	for index, pending := range e.pending {
		if pending == job {
			e.pending = append(e.pending[:index], e.pending[index+1:]...)
			return
		}
	}
}

func (e *concurrentEmbedder) run() {
	defer close(e.workerDone)
	for {
		if !e.hasPending() {
			select {
			case <-e.ctx.Done():
				return
			case <-e.wake:
			}
		}
		jobs := e.nextBatch()
		if len(jobs) == 0 {
			continue
		}
		vectors, errs := e.embedBatch(jobs)
		for index, job := range jobs {
			e.complete(job, vectors[index], errs[index])
		}
	}
}

func (e *concurrentEmbedder) hasPending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending) > 0
}

func (e *concurrentEmbedder) embedBatch(jobs []*embeddingJob) (vectors [][]float32, errs []error) {
	started := time.Now()
	_, batchAPI := e.embedder.(ai.BatchPartEmbedder)
	e.logVerbose("embedding batch start items=%d batch_api=%t", len(jobs), batchAPI)
	defer func() {
		failures := 0
		firstError := ""
		for _, err := range errs {
			if err != nil {
				failures++
				if firstError == "" {
					firstError = err.Error()
				}
			}
		}
		e.logVerbose("embedding batch finish items=%d batch_api=%t duration=%s failures=%d error=%q",
			len(jobs), batchAPI, time.Since(started), failures, firstError)
	}()
	parts := make([]ai.PromptPart, len(jobs))
	for index, job := range jobs {
		parts[index] = job.part
	}
	if batch, ok := e.embedder.(ai.BatchPartEmbedder); ok {
		batchCtx, cancel := context.WithCancel(e.ctx)
		defer cancel()
		var activeJobs atomic.Int32
		activeJobs.Store(int32(len(jobs)))
		stops := make([]func() bool, 0, len(jobs))
		for _, job := range jobs {
			stops = append(stops, context.AfterFunc(job.ctx, func() {
				if activeJobs.Add(-1) == 0 {
					cancel()
				}
			}))
		}
		defer func() {
			for _, stop := range stops {
				stop()
			}
		}()
		var err error
		vectors, err = batch.EmbedBatchContext(batchCtx, parts)
		if err != nil {
			errs = make([]error, len(jobs))
			for index := range errs {
				errs[index] = err
			}
			return make([][]float32, len(jobs)), errs
		}
		if len(vectors) != len(jobs) {
			errs = make([]error, len(jobs))
			for index := range errs {
				errs[index] = fmt.Errorf("embedder returned %d vectors for %d embedding requests", len(vectors), len(jobs))
			}
			return make([][]float32, len(jobs)), errs
		}
		errs = make([]error, len(jobs))
		return vectors, errs
	}
	vectors = make([][]float32, len(jobs))
	errs = make([]error, len(jobs))
	for index, job := range jobs {
		if err := job.ctx.Err(); err != nil {
			errs[index] = err
			continue
		}
		vectors[index], errs[index] = ai.EmbedWithContext(job.ctx, e.embedder, []ai.PromptPart{job.part})
	}
	return vectors, errs
}

func (e *concurrentEmbedder) logVerbose(format string, args ...any) {
	if e != nil && e.diagnostic != nil {
		e.diagnostic(format, args...)
	}
}

func (e *concurrentEmbedder) nextBatch() []*embeddingJob {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.ctx.Err() != nil {
		return nil
	}

	active := make([]*embeddingJob, 0, len(e.pending))
	for _, job := range e.pending {
		for id, request := range job.waiters {
			if request.ctx.Err() != nil {
				delete(job.waiters, id)
				if request.lowPriority {
					job.lowCount--
				} else {
					job.highCount--
				}
				request.cancel()
			}
		}
		if len(job.waiters) == 0 {
			job.stop()
			if e.jobs[job.key] == job {
				delete(e.jobs, job.key)
			}
			continue
		}
		active = append(active, job)
	}
	e.pending = active
	if len(active) == 0 {
		return nil
	}

	slots := min(maxEmbeddingBatch, len(active))
	selected, nextRemainder := selectEmbeddingBatch(active, slots, e.remainder)
	e.remainder = nextRemainder

	for _, job := range selected {
		e.removePendingLocked(job)
		job.inFlight = true
		job.ctx, job.cancel = context.WithCancel(e.ctx)
	}
	return selected
}

func selectEmbeddingBatch(tasks []*embeddingJob, slots, remainder int) ([]*embeddingJob, int) {
	slots = min(slots, len(tasks))
	if slots <= 0 {
		return nil, remainder
	}
	highQuota, lowQuota, nextRemainder := batchQuotas(slots, remainder)
	highOrdered := append([]*embeddingJob(nil), tasks...)
	sort.Slice(highOrdered, func(i, j int) bool {
		left, right := highOrdered[i], highOrdered[j]
		if left.highCount != right.highCount {
			return left.highCount > right.highCount
		}
		if left.lowCount != right.lowCount {
			return left.lowCount > right.lowCount
		}
		return left.sequence < right.sequence
	})
	selected := make([]*embeddingJob, 0, slots)
	selectedSet := make(map[*embeddingJob]struct{}, slots)
	for _, task := range highOrdered[:highQuota] {
		selected = append(selected, task)
		selectedSet[task] = struct{}{}
	}

	lowOrdered := make([]*embeddingJob, 0, len(tasks)-len(selected))
	for _, task := range tasks {
		if _, exists := selectedSet[task]; !exists {
			lowOrdered = append(lowOrdered, task)
		}
	}
	sort.Slice(lowOrdered, func(i, j int) bool {
		left, right := lowOrdered[i], lowOrdered[j]
		if left.lowCount != right.lowCount {
			return left.lowCount > right.lowCount
		}
		if left.highCount != right.highCount {
			return left.highCount > right.highCount
		}
		return left.sequence < right.sequence
	})
	selected = append(selected, lowOrdered[:lowQuota]...)
	return selected, nextRemainder
}

func batchQuotas(slots, remainder int) (high, low, nextRemainder int) {
	if slots <= 0 {
		return 0, 0, remainder
	}
	total := slots*highRequestsPerLow + remainder
	high = total / (highRequestsPerLow + 1)
	low = slots - high
	nextRemainder = total % (highRequestsPerLow + 1)
	return high, low, nextRemainder
}

func (e *concurrentEmbedder) complete(job *embeddingJob, vector []float32, err error) {
	e.mu.Lock()
	if e.jobs[job.key] == job {
		delete(e.jobs, job.key)
	}
	cancel := job.cancel
	waiters := make([]*embeddingRequest, 0, len(job.waiters))
	for _, request := range job.waiters {
		waiters = append(waiters, request)
	}
	job.waiters = nil
	job.highCount = 0
	job.lowCount = 0
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, request := range waiters {
		if request.ctx.Err() == nil {
			request.result <- embeddingResult{vector: vector, err: err}
		}
		request.cancel()
	}
}

func embedScheduledBatch(ctx context.Context, embedder *concurrentEmbedder, parts []ai.PromptPart, lowPriority bool) ([][]float32, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		index  int
		vector []float32
		err    error
	}
	results := make(chan result, len(parts))
	for index, part := range parts {
		go func(index int, part ai.PromptPart) {
			vector, err := embedder.Embed(batchCtx, part, lowPriority)
			results <- result{index: index, vector: vector, err: err}
		}(index, part)
	}
	vectors := make([][]float32, len(parts))
	var firstErr error
	for range parts {
		result := <-results
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancel()
			}
			continue
		}
		if len(result.vector) == 0 && firstErr == nil {
			firstErr = fmt.Errorf("embedder returned an empty vector for request %d", result.index+1)
			cancel()
			continue
		}
		vectors[result.index] = result.vector
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return vectors, nil
}

func embeddingKey(part ai.PromptPart) string {
	return string(part.Role) + "\x00" + part.Text
}
