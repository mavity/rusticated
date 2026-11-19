package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"ai"
)

type adaptiveEmbedder struct {
	mu             sync.Mutex
	busy           atomic.Bool
	runner         *LazyAIRunner
	session        *ai.Session
	resident       *ai.Embedder
	cacheIdentity  string
	openEmbedder   func() (*ai.Embedder, error)
	reopenEngine   func() (*ai.Engine, error)
	sequentialMode bool
	disabledErr    error
}

func newAdaptiveEmbedder(runner *LazyAIRunner, resident *ai.Embedder, openEmbedder func() (*ai.Embedder, error), reopenEngine func() (*ai.Engine, error)) *adaptiveEmbedder {
	return &adaptiveEmbedder{
		runner:         runner,
		resident:       resident,
		openEmbedder:   openEmbedder,
		reopenEngine:   reopenEngine,
		sequentialMode: resident == nil,
	}
}

func (e *adaptiveEmbedder) BindSession(session *ai.Session) {
	e.mu.Lock()
	e.session = session
	e.mu.Unlock()
}

func (e *adaptiveEmbedder) CacheIdentity() string {
	if e == nil {
		return ""
	}
	return e.cacheIdentity
}

func (e *adaptiveEmbedder) Embed(parts []ai.PromptPart) ([]float32, error) {
	return e.EmbedContext(context.Background(), parts)
}

func (e *adaptiveEmbedder) BeginBackgroundBatch(ctx context.Context) (ai.EmbedBatchSession, error) {
	if !e.busy.CompareAndSwap(false, true) {
		return nil, errors.New("adaptive embedder is busy")
	}

	e.mu.Lock()
	disabledErr := e.disabledErr
	sequentialMode := e.sequentialMode
	resident := e.resident
	session := e.session
	openEmbedder := e.openEmbedder
	e.mu.Unlock()

	if disabledErr != nil {
		e.busy.Store(false)
		return nil, disabledErr
	}

	b := &adaptiveBatchSession{
		owner:        e,
		session:      session,
		resident:     resident,
		openEmbedder: openEmbedder,
	}
	if sequentialMode || resident == nil {
		if err := b.enterSequential(ctx); err != nil {
			b.Close()
			return nil, err
		}
	}
	return b, nil
}

type adaptiveBatchSession struct {
	owner        *adaptiveEmbedder
	session      *ai.Session
	resident     *ai.Embedder
	openEmbedder func() (*ai.Embedder, error)
	seqEmbedder  *ai.Embedder
	sequential   bool
	closed       bool
}

func (b *adaptiveBatchSession) enterSequential(ctx context.Context) error {
	b.sequential = true
	if b.session == nil {
		return errors.New("sequential embedding has no bound AI session")
	}
	if err := b.session.SuspendEngineForEmbedding(ctx); err != nil {
		return err
	}
	if r := b.owner.runner; r != nil {
		r.lifeMu.Lock()
		engine := r.engine
		r.engine = nil
		r.lifeMu.Unlock()
		if engine != nil {
			engine.Close()
		}
	}
	if b.openEmbedder == nil {
		return b.owner.recordDisabledError(errors.New("embedding model loader is nil"))
	}
	embedder, err := b.openEmbedder()
	if err != nil {
		return b.owner.recordDisabledError(err)
	}
	if embedder == nil {
		return b.owner.recordDisabledError(errors.New("embedding model loader returned no embedder"))
	}
	b.seqEmbedder = embedder
	return nil
}

func (b *adaptiveBatchSession) EmbedContext(ctx context.Context, parts []ai.PromptPart) ([]float32, error) {
	if b.closed {
		return nil, ai.ErrClosed
	}
	if !b.sequential && b.resident != nil {
		vector, err := b.resident.Embed(parts)
		if err == nil && len(vector) > 0 {
			return vector, nil
		}
		b.owner.mu.Lock()
		if b.owner.resident == b.resident {
			b.owner.resident = nil
			b.owner.sequentialMode = true
		}
		b.owner.mu.Unlock()
		b.resident.Close()
		b.resident = nil
		if err := b.enterSequential(ctx); err != nil {
			return nil, err
		}
	}
	if b.seqEmbedder == nil {
		return nil, errors.New("sequential embedder is not initialized")
	}
	vector, err := b.seqEmbedder.Embed(parts)
	if err == nil && len(vector) == 0 {
		err = errors.New("sequential embedder returned an empty vector")
	}
	if err != nil {
		return nil, b.owner.recordDisabledError(err)
	}
	return vector, nil
}

func (b *adaptiveBatchSession) Close() {
	if b.closed {
		return
	}
	b.closed = true
	if b.seqEmbedder != nil {
		b.seqEmbedder.Close()
		b.seqEmbedder = nil
	}
	b.owner.busy.Store(false)
}

func (e *adaptiveEmbedder) recordDisabledError(err error) error {
	e.mu.Lock()
	e.disabledErr = fmt.Errorf("sequential embedding failed: %w", err)
	disabledErr := e.disabledErr
	e.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[kabibi] %v\n", disabledErr)
	return disabledErr
}

func (e *adaptiveEmbedder) EmbedContext(ctx context.Context, parts []ai.PromptPart) ([]float32, error) {
	if !e.busy.CompareAndSwap(false, true) {
		return nil, errors.New("adaptive embedder is busy")
	}
	defer e.busy.Store(false)

	e.mu.Lock()
	disabledErr := e.disabledErr
	sequentialMode := e.sequentialMode
	resident := e.resident
	e.mu.Unlock()
	if disabledErr != nil {
		return nil, disabledErr
	}
	if !sequentialMode && resident != nil {
		vector, err := resident.Embed(parts)
		if err == nil && len(vector) > 0 {
			return vector, nil
		}
		if err == nil {
			err = errors.New("resident embedder returned an empty vector")
		}
		e.mu.Lock()
		if e.resident == resident {
			e.resident = nil
			e.sequentialMode = true
		}
		e.mu.Unlock()
		resident.Close()
	}
	return e.embedSequential(ctx, parts)
}

func (e *adaptiveEmbedder) embedSequential(ctx context.Context, parts []ai.PromptPart) ([]float32, error) {
	e.mu.Lock()
	session := e.session
	openEmbedder := e.openEmbedder
	reopenEngine := e.reopenEngine
	e.mu.Unlock()
	if session == nil {
		return nil, errors.New("sequential embedding has no bound AI session")
	}
	r := e.runner
	if err := session.SuspendEngineForEmbedding(ctx); err != nil {
		return nil, err
	}
	if r != nil {
		r.lifeMu.Lock()
		engine := r.engine
		r.engine = nil
		r.lifeMu.Unlock()
		if engine != nil {
			engine.Close()
		}
	}

	embedder, err := openEmbedder()
	var vector []float32
	if err == nil {
		if embedder == nil {
			err = errors.New("embedding model loader returned no embedder")
		} else {
			vector, err = embedder.Embed(parts)
			embedder.Close()
			if err == nil && len(vector) == 0 {
				err = errors.New("sequential embedder returned an empty vector")
			}
		}
	}

	engine, reopenErr := reopenEngine()
	if reopenErr != nil {
		lifecycleErr := fmt.Errorf("reload local AI engine after sequential embedding: %w", reopenErr)
		if r != nil {
			r.lifeMu.Lock()
			r.lifecycleErr = lifecycleErr
			r.lifeMu.Unlock()
		}
		return nil, errors.Join(err, lifecycleErr)
	}
	if restoreErr := session.ReplaceEngineDuringOperation(ctx, engine); restoreErr != nil {
		engine.Close()
		lifecycleErr := fmt.Errorf("restore AI conversation after sequential embedding: %w", restoreErr)
		if r != nil {
			r.lifeMu.Lock()
			r.lifecycleErr = lifecycleErr
			r.lifeMu.Unlock()
		}
		return nil, errors.Join(err, lifecycleErr)
	}
	if r != nil {
		r.lifeMu.Lock()
		r.engine = engine
		r.lifeMu.Unlock()
	}
	if err != nil {
		e.mu.Lock()
		e.disabledErr = fmt.Errorf("sequential embedding failed: %w", err)
		disabledErr := e.disabledErr
		e.mu.Unlock()
		fmt.Fprintf(os.Stderr, "[kabibi] %v\n", disabledErr)
		return nil, disabledErr
	}
	return vector, nil
}

func (e *adaptiveEmbedder) Close() {
	e.mu.Lock()
	resident := e.resident
	e.resident = nil
	e.mu.Unlock()
	if resident != nil {
		resident.Close()
	}
}

func (r *LazyAIRunner) checkLifecycleError() error {
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()
	if r.lifecycleErr != nil {
		return fmt.Errorf("AI session could not be restored after embedding fallback: %w", r.lifecycleErr)
	}
	return nil
}
