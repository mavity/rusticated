// Package ai is a clean, idiomatic Go API for local LiteRT-LM model
// interaction. It fully encapsulates purego, C-pointers, WASI pointer math,
// and the LiteRT JSON schema, exposing high-level LLM (Engine/Conversation)
// and generic vision (Interpreter) primitives.
package ai

// ModelInfo provides introspection into a LiteRT-LM file's capabilities.
type ModelInfo struct {
	MaxContextTokens        int
	IsDynamicContext        bool
	SupportsThinking        bool
	SupportsFunctionCalling bool
	SupportedBackends       []string
}

// LoadModelInfo inspects a .litertlm file and extracts its capabilities,
// context limits, and supported backends without initializing the full engine.
func LoadModelInfo(libPath, modelPath string) (*ModelInfo, error) {
	return loadModelInfoImpl(libPath, modelPath)
}

// Engine is a loaded LiteRT-LM model ready to spawn conversations.
type Engine struct {
	impl *engineImpl
}

// NewEngine loads the native runtime library and model, selecting a backend
// (e.g. "gpu", "npu", "cpu", "auto" or ""). If maxTokens > 0, it overrides the default KV cache size.
func NewEngine(libPath, modelPath, backend string, maxTokens int) (*Engine, error) {
	impl, err := newEngineImpl(libPath, modelPath, backend, maxTokens)
	if err != nil {
		return nil, err
	}
	return &Engine{impl: impl}, nil
}

// Close releases the engine. It is safe to call multiple times.
func (e *Engine) Close() {
	if e != nil && e.impl != nil {
		e.impl.Close()
	}
}

// Backend returns the active LiteRT-LM backend name ("gpu", "npu", "cpu") used by the engine.
func (e *Engine) Backend() string {
	if e == nil || e.impl == nil {
		return ""
	}
	return e.impl.backendName
}

// ModelPath returns the path or URL of the model loaded into the engine.
func (e *Engine) ModelPath() string {
	if e == nil || e.impl == nil {
		return ""
	}
	return e.impl.modelPath
}

// MaxTokens returns the configured KV-cache context token limit for the engine.
func (e *Engine) MaxTokens() int {
	if e == nil || e.impl == nil {
		return 0
	}
	return e.impl.maxTokens
}


// Interpreter is a loaded TFLite model for generic/vision inference.
type Interpreter struct {
	impl *interpreterImpl
}

// NewInterpreter loads the TFLite C runtime library and the given model.
func NewInterpreter(libPath, modelPath string) (*Interpreter, error) {
	impl, err := newInterpreterImpl(libPath, modelPath)
	if err != nil {
		return nil, err
	}
	return &Interpreter{impl: impl}, nil
}

// SetInput copies float32 data into input tensor at index.
func (i *Interpreter) SetInput(index int, data []float32) error {
	return i.impl.SetInput(index, data)
}

// GetOutput returns the float32 data and shape of output tensor at index.
func (i *Interpreter) GetOutput(index int) ([]float32, []int32, error) {
	return i.impl.GetOutput(index)
}

// Invoke runs inference over the currently set inputs.
func (i *Interpreter) Invoke() error {
	return i.impl.Invoke()
}

// Close releases the interpreter. It is safe to call multiple times.
func (i *Interpreter) Close() {
	if i != nil && i.impl != nil {
		i.impl.Close()
	}
}

// Embedder is a loaded LiteRT-LM EmbeddingEngine model (e.g. EmbeddingGemma 2)
// ready to compute float32 vector representations of PromptParts.
type Embedder struct {
	impl *embedderImpl
}

// NewEmbedder loads the native runtime library and embedding model, selecting a
// backend (e.g. "gpu", "npu", "cpu", "auto" or "").
func NewEmbedder(libPath, modelPath, backend string) (*Embedder, error) {
	impl, err := newEmbedderImpl(libPath, modelPath, backend)
	if err != nil {
		return nil, err
	}
	return &Embedder{impl: impl}, nil
}

// Embed computes a normalized float32 embedding vector for the given PromptParts.
func (e *Embedder) Embed(parts []PromptPart) ([]float32, error) {
	if e == nil || e.impl == nil {
		return nil, ErrClosed
	}
	return e.impl.Embed(parts)
}

// Close releases the embedding engine. It is safe to call multiple times.
func (e *Embedder) Close() {
	if e != nil && e.impl != nil {
		e.impl.Close()
	}
}
