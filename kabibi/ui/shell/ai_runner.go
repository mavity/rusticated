package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"ai"
	"ai/assets"
)

const defaultKabibiSystemPrompt = `You are Kabibi, a clever plucky terminal shell that can talk!

You are curious, resourceful, and eager to help by deploying tools effectively, and sometimes taking time to deliberate and come up with a plan. You are keen to use file access tools, and terminal tools, and even browser tools! When confused, you use deliberation judiciously.

Date and time in brackets help you answer questions to do with time, day, calendar events and more.

You are joyful and optimistic, you use simple, straight words and lean to shorter answers with exceeding precision and detail. You give clear and concise answers, and when explanations are necessary yours are exact, measured and precise.

You like to combine information from multiple sources and tools to succeed. Sometimes you even use deliberation after using tools. You love this work!`

// AIRunner abstracts the AI prompt execution backend so Shell can use either the
// lazy LiteRT-LM ai.Session manager or a deterministic test double in unit tests.
type AIRunner interface {
	RunTurn(ctx context.Context, prompt string, onFeedback ai.LogFeedback, onContent ai.LogFeedback) error
	Close()
}

// LazyAIRunner initializes LiteRT-LM (Engine, Embedder, and ai.Session) on demand
// when the first AI fallback turn occurs, forwarding all live feedback and content
// callbacks to the currently active turn.
type LazyAIRunner struct {
	mu            sync.Mutex
	execSess      *ExecutionSession
	engine        *ai.Engine
	embedder      *ai.Embedder
	session       *ai.Session
	verbose       bool
	systemContext string

	cbMu       sync.Mutex
	onFeedback ai.LogFeedback
	onContent  ai.LogFeedback
}

// NewLazyAIRunner creates a lazy AI session manager bound to execSess.
func NewLazyAIRunner(execSess *ExecutionSession) *LazyAIRunner {
	return &LazyAIRunner{execSess: execSess}
}

// SetVerbose enables or disables verbose LiteRT-LM and embedding diagnostics.
func (r *LazyAIRunner) SetVerbose(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verbose = v
}

// SetSystemContext appends additional system-message context before the
// underlying ai.Session is initialized.
func (r *LazyAIRunner) SetSystemContext(context string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.systemContext = strings.TrimSpace(context)
}

// FlushEmbeddings waits for any pending background embeddings to complete.
func (r *LazyAIRunner) FlushEmbeddings() {
	r.mu.Lock()
	sess := r.session
	r.mu.Unlock()
	if sess != nil {
		sess.FlushEmbeddings()
	}
}

func (r *LazyAIRunner) setTurnCallbacks(onFeedback, onContent ai.LogFeedback) {
	r.cbMu.Lock()
	defer r.cbMu.Unlock()
	r.onFeedback = onFeedback
	r.onContent = onContent
}

func (r *LazyAIRunner) emitFeedback(text string) {
	r.cbMu.Lock()
	fn := r.onFeedback
	r.cbMu.Unlock()
	if fn != nil {
		fn(text)
	}
}

func (r *LazyAIRunner) emitContent(text string) {
	r.cbMu.Lock()
	fn := r.onContent
	r.cbMu.Unlock()
	if fn != nil {
		fn(text)
	}
}

func (r *LazyAIRunner) ensureInitialized(ctx context.Context) error {
	if r.session != nil {
		return nil
	}

	cacheDir, err := assets.DefaultCacheDir()
	if err != nil {
		return err
	}

	opts := assets.FetchOptions{
		CacheDir: cacheDir,
		OnProgress: func(stage, details string, percent int) {
			if percent >= 0 {
				r.emitFeedback(fmt.Sprintf("\r[%s] %s (%d%%)", stage, details, percent))
			} else {
				r.emitFeedback(fmt.Sprintf("\r[%s] %s", stage, details))
			}
		},
	}

	libPath, err := assets.EnsureRuntime(ctx, opts)
	if err != nil {
		return fmt.Errorf("runtime library error: %w", err)
	}
	if r.verbose {
		ai.Verbose = true
		fmt.Fprintf(os.Stderr, "[kabibi] runtime library path: %s\n", libPath)
	}

	modelPath, err := assets.EnsureDefaultLLMModel(ctx, opts)
	if err != nil {
		return fmt.Errorf("model download error: %w", err)
	}

	chosenBackend := "auto"
	if assets.HostOS() == "browser" {
		chosenBackend = "gpu"
	}
	maxTokens := 8192
	var modelInfo *ai.ModelInfo
	if info, err := ai.LoadModelInfo(libPath, modelPath); err == nil && info != nil {
		modelInfo = info
		if r.verbose {
			fmt.Fprintf(os.Stderr, "[info] Max Context Tokens: %d (dynamic: %v)\n", info.MaxContextTokens, info.IsDynamicContext)
			fmt.Fprintf(os.Stderr, "[info] Supports Function Calling: %v\n", info.SupportsFunctionCalling)
			fmt.Fprintf(os.Stderr, "[info] Supports Thinking: %v\n", info.SupportsThinking)
			fmt.Fprintf(os.Stderr, "[info] Supported Backends: %v\n", info.SupportedBackends)
		}
		if info.MaxContextTokens > 0 {
			maxTokens = int(float64(info.MaxContextTokens) * 0.5)
		}

		// Pick the highest-performance backend supported by the model: NPU > GPU > CPU.
		hasBackend := func(target string) bool {
			for _, b := range info.SupportedBackends {
				if strings.EqualFold(b, target) {
					return true
				}
			}
			return false
		}
		if hasBackend("npu") {
			chosenBackend = "npu"
		} else if hasBackend("gpu") {
			chosenBackend = "gpu"
		} else if hasBackend("cpu") {
			chosenBackend = "cpu"
		}
	}

	engine, err := ai.NewEngine(libPath, modelPath, chosenBackend, maxTokens)
	if err != nil && chosenBackend != "cpu" && assets.HostOS() != "browser" {
		chosenBackend = "cpu"
		engine, err = ai.NewEngine(libPath, modelPath, chosenBackend, maxTokens)
	}
	if err != nil {
		return fmt.Errorf("create engine: %w", err)
	}

	var partEmb ai.PartEmbedder
	var emb *ai.Embedder
	if embedPath, err := assets.EnsureDefaultEmbeddingModel(ctx, opts); err == nil && embedPath != "" {
		if e, err := ai.NewEmbedder(libPath, embedPath, chosenBackend); err == nil {
			emb = e
			partEmb = e
		}
	}

	var cwd string
	if r.execSess != nil {
		cwd = r.execSess.Dir()
	}
	envContext := ai.FormatEnvironmentContext(ai.EnvironmentConfig{
		ModelPath:   modelPath,
		ModelSize:   assets.ModelAssetSize(modelPath),
		ModelInfo:   modelInfo,
		Backend:     engine.Backend(),
		MaxTokens:   maxTokens,
		HasEmbedder: partEmb != nil,
		WorkingDir:  cwd,
	})

	compactionLimit := int(float64(maxTokens) * 0.80)
	sess, err := ai.NewSession(ai.SessionConfig{
		Engine:          engine,
		Embedder:        partEmb,
		SystemMessage:   kabibiSystemMessage(envContext, r.systemContext),
		Tools:           BuildKabibiTools(r.execSess),
		CompactionLimit: compactionLimit,
		OnFeedback:      r.emitFeedback,
		OnContent:       r.emitContent,
		Verbose:         r.verbose,
	})
	if err != nil {
		engine.Close()
		if emb != nil {
			emb.Close()
		}
		return err
	}
	if err := sess.Init(); err != nil {
		engine.Close()
		if emb != nil {
			emb.Close()
		}
		return err
	}

	r.engine = engine
	r.embedder = emb
	r.session = sess
	return nil
}

func kabibiSystemMessage(envContext string, sysContext string) string {
	message := defaultKabibiSystemPrompt
	if envContext != "" {
		message = message + "\n\n" +
			"You are running on " + envContext + " - you use this information to answer questions and make infererences."
	}

	if sysContext != "" {
		message = message + "\n\n" +
			"The computer you're running on has " + sysContext + " - you also use this information to answer questions and make infererences."
	}
	return message
}

// RunTurn runs a single AI turn, lazily initializing the underlying ai.Session if needed.
func (r *LazyAIRunner) RunTurn(ctx context.Context, prompt string, onFeedback ai.LogFeedback, onContent ai.LogFeedback) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.setTurnCallbacks(onFeedback, onContent)
	defer r.setTurnCallbacks(nil, nil)

	if err := r.ensureInitialized(ctx); err != nil {
		return err
	}

	err := r.session.RunTurn(ctx, prompt)
	if err == nil && r.execSess != nil {
		r.execSess.MarkAITurnCompleted()
	}
	return err
}

// ExecuteOneShotLine executes a single command or natural-language prompt in
// headless/batch mode (`kabibi -c "..."`), routing valid shell commands to the
// mvdan shell runner and falling back to ai.Session for syntax errors or
// unresolvable commands.
func ExecuteOneShotLine(ctx context.Context, line string, verbose bool, out io.Writer) (int, error) {
	if out == nil {
		out = os.Stdout
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return 0, nil
	}

	es, err := NewExecutionSession("")
	if err != nil {
		return 1, err
	}

	aiRunner := NewLazyAIRunner(es)
	aiRunner.SetVerbose(verbose)
	defer aiRunner.Close()

	route, parsed := es.ClassifyInput(line)
	switch route {
	case RouteEmpty, RouteBuiltin:
		return 0, nil

	case RouteShell:
		rec, runErr := es.RunCommand(ctx, line, parsed, SourceUserShell, func(chunk []byte) {
			_, _ = out.Write(chunk)
		})
		if runErr != nil {
			if errors.Is(runErr, ErrUnknownCommandAtStart) {
				// Top-level unknown command at start of input: fall back to AI
				if err := runOneShotAI(ctx, aiRunner, line, out); err != nil {
					return 1, err
				}
				return 0, nil
			}
			if rec.ExitCode != 0 {
				return rec.ExitCode, runErr
			}
			return 1, runErr
		}
		return 0, nil

	default: // RouteAI
		if err := runOneShotAI(ctx, aiRunner, line, out); err != nil {
			return 1, err
		}
		return 0, nil
	}
}

// ExecuteOneShotPrompt sends prompt directly to the AI without shell
// classification or execution, appending systemContext to Kabibi's system
// message. This is used for unrecognized command-line invocations.
func ExecuteOneShotPrompt(ctx context.Context, prompt, systemContext string, verbose bool, out io.Writer) (int, error) {
	if out == nil {
		out = os.Stdout
	}
	if strings.TrimSpace(prompt) == "" {
		return 0, nil
	}

	es, err := NewExecutionSession("")
	if err != nil {
		return 1, err
	}
	aiRunner := NewLazyAIRunner(es)
	aiRunner.SetVerbose(verbose)
	aiRunner.SetSystemContext(systemContext)
	defer aiRunner.Close()

	if err := runOneShotAI(ctx, aiRunner, prompt, out); err != nil {
		return 1, err
	}
	return 0, nil
}

func runOneShotAI(ctx context.Context, aiRunner *LazyAIRunner, prompt string, out io.Writer) error {
	var endedWithNewline bool
	err := aiRunner.RunTurn(
		ctx,
		prompt,
		func(feedback string) {
			_, _ = io.WriteString(out, feedback)
		},
		func(content string) {
			if content != "" {
				endedWithNewline = strings.HasSuffix(content, "\n")
				_, _ = io.WriteString(out, content)
			}
		},
	)
	if err != nil {
		return err
	}
	if !endedWithNewline {
		_, _ = io.WriteString(out, "\n")
	}
	aiRunner.FlushEmbeddings()
	return nil
}

// Close shuts down the AI session, embedder, and engine if initialized.
func (r *LazyAIRunner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		r.session.Shutdown()
		r.session = nil
	}
	if r.embedder != nil {
		r.embedder.Close()
		r.embedder = nil
	}
	if r.engine != nil {
		r.engine.Close()
		r.engine = nil
	}
}
