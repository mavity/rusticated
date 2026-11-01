package filelist

import (
	"context"
	"sync"
	"time"
)

type ProgressMatch struct {
	Path  string
	Score float64
}

type Progress struct {
	Phase       string
	Viewed      int
	Total       int
	CurrentPath string
	Elapsed     time.Duration
	AvgPerFile  time.Duration
	Matches     []ProgressMatch
}

type SearchIterator struct {
	ctx     context.Context
	cancel  context.CancelFunc
	updates chan struct{}
	done    chan struct{}

	tracker *progressTracker
	mu      sync.RWMutex
	result  string
	err     error
}

type progressTracker struct {
	mu      sync.RWMutex
	state   Progress
	entries []fileEntry
	started time.Time
	iter    *SearchIterator
}

func NewSearchIterator(ctx context.Context, cwd string, opts Options) *SearchIterator {
	if ctx == nil {
		ctx = context.Background()
	}
	searchCtx, cancel := context.WithCancel(ctx)
	iter := &SearchIterator{
		ctx:     searchCtx,
		cancel:  cancel,
		updates: make(chan struct{}),
		done:    make(chan struct{}),
	}
	iter.tracker = &progressTracker{iter: iter}
	go func() {
		defer close(iter.done)
		defer close(iter.updates)
		result, err := listFiles(searchCtx, cwd, opts, iter.tracker)
		iter.mu.Lock()
		iter.result = result
		iter.err = err
		iter.mu.Unlock()
	}()
	return iter
}

func (i *SearchIterator) Updates() <-chan struct{} {
	if i == nil {
		return nil
	}
	return i.updates
}

func (i *SearchIterator) Current() Progress {
	if i == nil {
		return Progress{}
	}
	return i.tracker.current()
}

func (i *SearchIterator) Result() (string, error) {
	if i == nil {
		return "", context.Canceled
	}
	<-i.done
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.result, i.err
}

func (i *SearchIterator) Close() {
	if i == nil {
		return
	}
	i.cancel()
	<-i.done
}

func (i *SearchIterator) notify() error {
	select {
	case i.updates <- struct{}{}:
		return nil
	case <-i.ctx.Done():
		return i.ctx.Err()
	}
}

func (p *progressTracker) update(phase, current string, viewed, total int) error {
	p.mu.Lock()
	if phase == "candidate" && p.started.IsZero() {
		p.started = time.Now()
	}
	p.state.Phase = phase
	p.state.Viewed = viewed
	p.state.Total = total
	p.state.CurrentPath = current
	if !p.started.IsZero() {
		p.state.Elapsed = time.Since(p.started)
		if viewed > 0 {
			p.state.AvgPerFile = p.state.Elapsed / time.Duration(viewed)
		}
	}
	p.mu.Unlock()
	return p.iter.notify()
}

func (p *progressTracker) addMatch(entry fileEntry, limit int) {
	p.mu.Lock()
	p.entries = insertTopMatch(p.entries, entry, limit)
	p.mu.Unlock()
}

func (p *progressTracker) matches() []fileEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]fileEntry(nil), p.entries...)
}

func (p *progressTracker) current() Progress {
	p.mu.RLock()
	defer p.mu.RUnlock()
	state := p.state
	state.Matches = make([]ProgressMatch, len(p.entries))
	for i, entry := range p.entries {
		state.Matches[i] = ProgressMatch{Path: entry.relPath, Score: entry.score}
	}
	return state
}
