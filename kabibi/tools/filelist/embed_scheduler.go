package filelist

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ai"
)

const (
	maxQueuedEmbeddings = 256
	highRequestsPerLow  = 4
)

var errEmbeddingQueueFull = errors.New("embedding request queue is full")

type embeddingResult struct {
	vector []float32
	err    error
}

type embeddingRequest struct {
	id          uint64
	ctx         context.Context
	cancel      context.CancelFunc
	part        ai.PromptPart
	lowPriority bool
	order       *embeddingOrderHandle
	result      chan embeddingResult
	job         *embeddingJob
}

type embeddingOrderHandle struct {
	scheduler *concurrentEmbedder
	rank      atomic.Int64
}

func (h *embeddingOrderHandle) SetRank(rank int) {
	if h == nil {
		return
	}
	if rank < 0 {
		rank = 0
	}
	h.rank.Store(int64(rank))
	h.scheduler.signal()
}

type embeddingJob struct {
	key      string
	part     ai.PromptPart
	waiters  map[uint64]*embeddingRequest
	sequence uint64
	ctx      context.Context
	cancel   context.CancelFunc
	inFlight bool
}

func (job *embeddingJob) stop() {
	if job.cancel != nil {
		job.cancel()
	}
}

type concurrentEmbedder struct {
	ctx      context.Context
	cancel   context.CancelFunc
	embedder ai.PartEmbedder

	mu             sync.Mutex
	pending        []*embeddingJob
	jobs           map[string]*embeddingJob
	queuedRequests int
	nextID         uint64
	highSinceLow   int
	closed         bool
	wake           chan struct{}
	workerDone     chan struct{}
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

func (e *concurrentEmbedder) NewOrderHandle() *embeddingOrderHandle {
	handle := &embeddingOrderHandle{scheduler: e}
	handle.rank.Store(0)
	return handle
}

func (e *concurrentEmbedder) Close() {
	if e == nil {
		return
	}
	e.cancel()
	e.mu.Lock()
	e.closed = true
	for _, job := range e.jobs {
		job.stop()
		for _, waiter := range job.waiters {
			waiter.cancel()
		}
	}
	e.pending = nil
	e.jobs = make(map[string]*embeddingJob)
	e.queuedRequests = 0
	e.mu.Unlock()
	e.signal()
	select {
	case <-e.workerDone:
	default:
	}
}

func (e *concurrentEmbedder) Embed(ctx context.Context, part ai.PromptPart, lowPriority bool, order *embeddingOrderHandle) ([]float32, error) {
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
		part:        part,
		lowPriority: lowPriority,
		order:       order,
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
		if e.queuedRequests >= maxQueuedEmbeddings {
			e.mu.Unlock()
			request.cancel()
			return nil, errEmbeddingQueueFull
		}
		job = &embeddingJob{
			key:     key,
			part:    part,
			waiters: make(map[uint64]*embeddingRequest),
		}
		e.jobs[key] = job
		e.pending = append(e.pending, job)
	}
	if len(job.waiters) >= maxQueuedEmbeddings {
		e.mu.Unlock()
		request.cancel()
		return nil, errEmbeddingQueueFull
	}
	if !job.inFlight {
		if e.queuedRequests >= maxQueuedEmbeddings {
			e.mu.Unlock()
			request.cancel()
			if len(job.waiters) == 0 {
				e.removeEmptyJobLocked(job)
			}
			return nil, errEmbeddingQueueFull
		}
		e.queuedRequests++
	}
	e.nextID++
	request.id = e.nextID
	request.job = job
	job.waiters[request.id] = request
	if job.sequence == 0 {
		job.sequence = request.id
	}
	e.mu.Unlock()
	e.signal()

	select {
	case result := <-request.result:
		request.cancel()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return result.vector, result.err
	case <-ctx.Done():
		e.remove(request.id)
		request.cancel()
		return nil, ctx.Err()
	case <-e.ctx.Done():
		e.remove(request.id)
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

func (e *concurrentEmbedder) remove(id uint64) {
	e.mu.Lock()
	for _, job := range e.jobs {
		_, exists := job.waiters[id]
		if !exists {
			continue
		}
		delete(job.waiters, id)
		if !job.inFlight {
			e.queuedRequests--
		}
		if len(job.waiters) == 0 {
			job.stop()
			delete(e.jobs, job.key)
			if !job.inFlight {
				e.removePendingLocked(job)
			}
		}
		break
	}
	e.mu.Unlock()
	e.signal()
}

func (e *concurrentEmbedder) removeEmptyJobLocked(job *embeddingJob) {
	if e.jobs[job.key] != job || job.inFlight || len(job.waiters) != 0 {
		return
	}
	delete(e.jobs, job.key)
	e.removePendingLocked(job)
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
		if _, ok := e.embedder.(ai.BatchPartEmbedder); ok {
			if !e.waitForBatchWindow() {
				return
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

func (e *concurrentEmbedder) waitForBatchWindow() bool {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-e.wake:
		}
	}
}

func (e *concurrentEmbedder) embedBatch(jobs []*embeddingJob) ([][]float32, []error) {
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
		vectors, err := batch.EmbedBatchContext(batchCtx, parts)
		if err != nil {
			errs := make([]error, len(jobs))
			for index := range errs {
				errs[index] = err
			}
			return make([][]float32, len(jobs)), errs
		}
		if len(vectors) != len(jobs) {
			errs := make([]error, len(jobs))
			for index := range errs {
				errs[index] = fmt.Errorf("embedder returned %d vectors for %d embedding requests", len(vectors), len(jobs))
			}
			return make([][]float32, len(jobs)), errs
		}
		errs := make([]error, len(jobs))
		return vectors, errs
	}
	vectors := make([][]float32, len(jobs))
	errs := make([]error, len(jobs))
	for index, job := range jobs {
		if err := job.ctx.Err(); err != nil {
			errs[index] = err
			continue
		}
		vectors[index], errs[index] = ai.EmbedWithContext(job.ctx, e.embedder, []ai.PromptPart{job.part})
	}
	return vectors, errs
}

func (e *concurrentEmbedder) nextBatch() []*embeddingJob {
	const maxHighBatch = highRequestsPerLow
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.ctx.Err() != nil {
		return nil
	}

	active := e.pending[:0]
	highWaiters, lowWaiters := 0, 0
	for _, job := range e.pending {
		for id, request := range job.waiters {
			if request.ctx.Err() != nil {
				delete(job.waiters, id)
				e.queuedRequests--
				request.cancel()
			}
		}
		if len(job.waiters) == 0 {
			job.stop()
			delete(e.jobs, job.key)
			continue
		}
		active = append(active, job)
		for _, request := range job.waiters {
			if request.lowPriority {
				lowWaiters++
			} else {
				highWaiters++
			}
		}
	}
	e.pending = active
	if len(active) == 0 {
		return nil
	}

	hasHigh := highWaiters > 0
	hasLow := lowWaiters > 0
	if !hasHigh {
		e.highSinceLow = 0
	}
	lowPriority := !hasHigh || (hasLow && e.highSinceLow >= highRequestsPerLow)
	batchLimit := 1
	if !lowPriority {
		if hasLow {
			batchLimit = highRequestsPerLow - e.highSinceLow
		} else {
			batchLimit = maxHighBatch
		}
	}
	if lowPriority {
		e.highSinceLow = 0
	}

	var selected []*embeddingJob
	for len(selected) < batchLimit {
		job := e.highestWeightedJobLocked(lowPriority)
		if job == nil {
			break
		}
		e.removePendingLocked(job)
		job.inFlight = true
		e.queuedRequests -= len(job.waiters)
		job.ctx, job.cancel = context.WithCancel(e.ctx)
		selected = append(selected, job)
		if !lowPriority && e.highSinceLow < highRequestsPerLow {
			e.highSinceLow++
		}
	}
	return selected
}

func (e *concurrentEmbedder) highestWeightedJobLocked(lowPriority bool) *embeddingJob {
	var selected *embeddingJob
	selectedWeight := 0.0
	for _, job := range e.pending {
		weight := 0.0
		for _, request := range job.waiters {
			if request.lowPriority == lowPriority {
				if !lowPriority || request.order == nil {
					weight++
				} else {
					rank := request.order.rank.Load()
					weight += 1 / float64(rank+1)
				}
			}
		}
		if weight > selectedWeight || weight == selectedWeight && weight > 0 && (selected == nil || job.sequence < selected.sequence) {
			selected = job
			selectedWeight = weight
		}
	}
	return selected
}

func (e *concurrentEmbedder) complete(job *embeddingJob, vector []float32, err error) {
	e.mu.Lock()
	if e.jobs[job.key] == job {
		delete(e.jobs, job.key)
	}
	job.stop()
	for _, request := range job.waiters {
		if request.ctx.Err() != nil {
			request.cancel()
			continue
		}
		request.result <- embeddingResult{vector: vector, err: err}
	}
	e.mu.Unlock()
}

func embedScheduledBatch(ctx context.Context, embedder *concurrentEmbedder, parts []ai.PromptPart, lowPriority bool, order *embeddingOrderHandle) ([][]float32, error) {
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
			vector, err := embedder.Embed(batchCtx, part, lowPriority, order)
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
