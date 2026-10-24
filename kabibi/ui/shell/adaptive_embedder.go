package shell

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"ai"
)

type adaptiveEmbedder struct {
	mu             sync.Mutex
	runner         *LazyAIRunner
	session        *ai.Session
	resident       *ai.Embedder
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

func (e *adaptiveEmbedder) Embed(parts []ai.PromptPart) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.disabledErr != nil {
		return nil, e.disabledErr
	}
	if !e.sequentialMode && e.resident != nil {
		vector, err := e.resident.Embed(parts)
		if err == nil && len(vector) > 0 {
			return vector, nil
		}
		if err == nil {
			err = errors.New("resident embedder returned an empty vector")
		}
		e.resident.Close()
		e.resident = nil
		e.sequentialMode = true
	}
	return e.embedSequential(parts)
}

func (e *adaptiveEmbedder) embedSequential(parts []ai.PromptPart) ([]float32, error) {
	if e.session == nil {
		return nil, errors.New("sequential embedding has no bound AI session")
	}
	r := e.runner
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()

	e.session.SuspendConversation()
	if r.engine != nil {
		r.engine.Close()
		r.engine = nil
	}

	embedder, err := e.openEmbedder()
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

	engine, reopenErr := e.reopenEngine()
	if reopenErr != nil {
		lifecycleErr := fmt.Errorf("reload local AI engine after sequential embedding: %w", reopenErr)
		r.lifecycleErr = lifecycleErr
		return nil, errors.Join(err, lifecycleErr)
	}
	r.engine = engine
	if restoreErr := e.session.ReplaceEngine(engine); restoreErr != nil {
		engine.Close()
		r.engine = nil
		lifecycleErr := fmt.Errorf("restore AI conversation after sequential embedding: %w", restoreErr)
		r.lifecycleErr = lifecycleErr
		return nil, errors.Join(err, lifecycleErr)
	}
	if err != nil {
		e.disabledErr = fmt.Errorf("sequential embedding failed: %w", err)
		fmt.Fprintf(os.Stderr, "[kabibi] %v\n", e.disabledErr)
		return nil, e.disabledErr
	}
	return vector, nil
}

func (e *adaptiveEmbedder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.resident != nil {
		e.resident.Close()
		e.resident = nil
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
