package filelist

import (
	"context"
	"errors"
	"sync"
)

const liveQuoteWorkers = 2

type liveQuoteJob struct {
	entry    fileEntry
	ctx      context.Context
	cancel   context.CancelFunc
	rank     int
	started  bool
	released bool
}

type liveQuoteManager struct {
	ctx        context.Context
	cancel     context.CancelFunc
	embedder   *concurrentEmbedder
	progress   *progressTracker
	limit      int
	mu         sync.Mutex
	queryVec   []float32
	jobs       map[string]*liveQuoteJob
	pending    []*liveQuoteJob
	err        error
	closed     bool
	wake       chan struct{}
	workers    sync.WaitGroup
	activeJobs sync.WaitGroup
}

func newLiveQuoteManager(ctx context.Context, embedder *concurrentEmbedder, progress *progressTracker, limit int) *liveQuoteManager {
	if ctx == nil {
		ctx = context.Background()
	}
	quoteCtx, cancel := context.WithCancel(ctx)
	manager := &liveQuoteManager{
		ctx:      quoteCtx,
		cancel:   cancel,
		embedder: embedder,
		progress: progress,
		limit:    limit,
		jobs:     make(map[string]*liveQuoteJob),
		wake:     make(chan struct{}, 1),
	}
	for range liveQuoteWorkers {
		manager.workers.Add(1)
		go manager.run()
	}
	return manager
}

func (m *liveQuoteManager) setQuery(vector []float32) {
	m.mu.Lock()
	m.queryVec = append([]float32(nil), vector...)
	m.mu.Unlock()
}

func (m *liveQuoteManager) sync(entries []fileEntry) {
	m.mu.Lock()
	if m.closed || len(m.queryVec) == 0 {
		m.mu.Unlock()
		return
	}
	displayed := entries[:min(len(entries), m.limit)]
	visible := make(map[string]int, len(displayed))
	for rank, entry := range displayed {
		if entry.hasScore && entry.quote == "" {
			visible[entry.relPath] = rank
		}
	}
	for path, job := range m.jobs {
		if rank, ok := visible[path]; ok && job.ctx.Err() == nil {
			job.rank = rank
			continue
		}
		if job.ctx.Err() != nil {
			if !job.started && !job.released {
				m.activeJobs.Done()
				job.released = true
			}
			delete(m.jobs, path)
			continue
		}
		job.cancel()
		if !job.started {
			m.activeJobs.Done()
			job.released = true
		}
		delete(m.jobs, path)
	}
	pending := m.pending[:0]
	for _, job := range m.pending {
		if _, ok := m.jobs[job.entry.relPath]; ok && job.ctx.Err() == nil {
			pending = append(pending, job)
		} else {
			job.cancel()
		}
	}
	m.pending = pending
	for _, entry := range displayed {
		rank, isVisible := visible[entry.relPath]
		if !isVisible {
			continue
		}
		if _, exists := m.jobs[entry.relPath]; exists {
			continue
		}
		jobCtx, cancel := context.WithCancel(m.ctx)
		job := &liveQuoteJob{
			entry:  entry,
			ctx:    jobCtx,
			cancel: cancel,
			rank:   rank,
		}
		m.jobs[entry.relPath] = job
		m.pending = append(m.pending, job)
		m.activeJobs.Add(1)
	}
	m.mu.Unlock()
	m.signal()
}

func (m *liveQuoteManager) Finish(entries []fileEntry) error {
	m.sync(entries)
	m.activeJobs.Wait()
	m.mu.Lock()
	err := m.err
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.ctx.Err()
}

func (m *liveQuoteManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		for _, job := range m.jobs {
			job.cancel()
			if !job.started && !job.released {
				m.activeJobs.Done()
				job.released = true
			}
		}
		m.pending = nil
	}
	m.mu.Unlock()
	m.cancel()
	m.signal()
	m.workers.Wait()
	m.activeJobs.Wait()
}

func (m *liveQuoteManager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *liveQuoteManager) next() *liveQuoteJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.pending) > 0 {
		bestIndex := -1
		for index, job := range m.pending {
			if m.jobs[job.entry.relPath] != job || job.ctx.Err() != nil {
				continue
			}
			if bestIndex < 0 || job.rank < m.pending[bestIndex].rank {
				bestIndex = index
			}
		}
		if bestIndex < 0 {
			m.pending = nil
			continue
		}
		job := m.pending[bestIndex]
		m.pending = append(m.pending[:bestIndex], m.pending[bestIndex+1:]...)
		job.started = true
		return job
	}
	return nil
}

func (m *liveQuoteManager) run() {
	defer m.workers.Done()
	for {
		job := m.next()
		if job == nil {
			select {
			case <-m.ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}
		m.extract(job)
	}
}

func (m *liveQuoteManager) extract(job *liveQuoteJob) {
	defer m.activeJobs.Done()
	m.mu.Lock()
	query := append([]float32(nil), m.queryVec...)
	m.mu.Unlock()
	quote, startLine, endLine, err := relevantQuote(job.ctx, job.entry, query, m.embedder, m.progress)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		m.fail(job, err)
		return
	}
	if err != nil {
		return
	}

	m.mu.Lock()
	if m.jobs[job.entry.relPath] != job || job.ctx.Err() != nil || m.closed {
		m.mu.Unlock()
		return
	}
	job.cancel()
	delete(m.jobs, job.entry.relPath)
	m.mu.Unlock()
	if err := m.progress.updateQuote(job.entry.relPath, quote, startLine, endLine); err != nil && m.ctx.Err() == nil {
		m.fail(job, err)
	}
}

func (m *liveQuoteManager) fail(job *liveQuoteJob, err error) {
	m.mu.Lock()
	if m.jobs[job.entry.relPath] == job && job.ctx.Err() == nil && m.err == nil {
		m.err = err
		for _, current := range m.jobs {
			current.cancel()
			if !current.started && !current.released {
				m.activeJobs.Done()
				current.released = true
			}
		}
		m.pending = nil
	}
	m.mu.Unlock()
}
