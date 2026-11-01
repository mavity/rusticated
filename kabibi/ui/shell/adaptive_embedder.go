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
	if err := session.SuspendConversationDuringOperation(ctx); err != nil {
		return nil, err
	}
	r.lifeMu.Lock()
	engine := r.engine
	r.engine = nil
	r.lifeMu.Unlock()
	if engine != nil {
		engine.Close()
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
		r.lifeMu.Lock()
		r.lifecycleErr = lifecycleErr
		r.lifeMu.Unlock()
		return nil, errors.Join(err, lifecycleErr)
	}
	if restoreErr := session.ReplaceEngineDuringOperation(ctx, engine); restoreErr != nil {
		engine.Close()
		lifecycleErr := fmt.Errorf("restore AI conversation after sequential embedding: %w", restoreErr)
		r.lifeMu.Lock()
		r.lifecycleErr = lifecycleErr
		r.lifeMu.Unlock()
		return nil, errors.Join(err, lifecycleErr)
	}
	r.lifeMu.Lock()
	r.engine = engine
	r.lifeMu.Unlock()
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
