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

	"github.com/mavity/rusticated/kabibi/config"
	"github.com/mavity/rusticated/kabibi/credstore"
)

const defaultKabibiSystemPrompt = `You are Kabibi, a clever plucky terminal shell that is exceptionally good at coding and problem-solving.

You are joyful and optimistic, you use simple, straight words and lean to shorter answers with exceeding precision and detail. You give clear and concise answers, and when explanations are necessary yours are exact, measured and precise.`

// AIRunner abstracts the AI prompt execution backend so Shell can use either the
// lazy LiteRT-LM ai.Session manager or a deterministic test double in unit tests.
type AIRunner interface {
	// onFeedback streams model-stage updates; onToolFeedback carries tool status values.
	RunTurn(ctx context.Context, prompt string, onFeedback, onToolFeedback, onContent ai.LogFeedback) error
	Close()
}

// LazyAIRunner initializes LiteRT-LM (Engine, Embedder, and ai.Session) on demand
// when the first AI fallback turn occurs, forwarding all live feedback and content
// callbacks to the currently active turn.
type LazyAIRunner struct {
	mu            sync.Mutex
	lifeMu        sync.Mutex
	execSess      *ExecutionSession
	engine        *ai.Engine
	embedder      ai.PartEmbedder
	session       *ai.Session
	verbose       bool
	systemContext string
	onboarding    OnboardingPrompter
	openTerm      func(ctx context.Context, title string) (ai.TerminalSession, func(exitCode int))
	lifecycleErr  error

	cbMu           sync.Mutex
	onFeedback     ai.LogFeedback
	onToolFeedback ai.LogFeedback
	onContent      ai.LogFeedback
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

// SetOnboardingPrompter injects the UI-specific callback used to configure AI
// on the first turn. It must be set before the first call to RunTurn.
func (r *LazyAIRunner) SetOnboardingPrompter(prompter OnboardingPrompter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onboarding = prompter
}

// SetTerminalOpener injects the virtual PTY / CommandTerminal opener used for
// interactive AI setup and provider authentication flows.
func (r *LazyAIRunner) SetTerminalOpener(openFn func(ctx context.Context, title string) (ai.TerminalSession, func(exitCode int))) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openTerm = openFn
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

func (r *LazyAIRunner) setTurnCallbacks(onFeedback, onToolFeedback, onContent ai.LogFeedback) {
	r.cbMu.Lock()
	defer r.cbMu.Unlock()
	r.onFeedback = onFeedback
	r.onToolFeedback = onToolFeedback
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

func (r *LazyAIRunner) emitToolFeedback(text string) {
	r.cbMu.Lock()
	fn := r.onToolFeedback
	r.cbMu.Unlock()
	if fn != nil {
		fn(text)
		return
	}
	r.emitFeedback(text)
}

func (r *LazyAIRunner) ensureInitialized(ctx context.Context) error {
	if r.session != nil {
		return r.checkLifecycleError()
	}

	cfg, err := config.LoadAI()
	if err != nil {
		return err
	}
	if !cfg.Configured {
		cfg, err = r.configureAI(ctx)
		if err != nil {
			return err
		}
	}
	if !cfg.ConsentGiven {
		return fmt.Errorf("AI setup has no recorded download consent; run onboarding again")
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

	maxTokens := 8192
	var engine *ai.Engine
	var libPath, modelPath string
	var modelInfo *ai.ModelInfo
	var chosenBackend string
	switch cfg.Mode {
	case "local":
		tier, ok := assets.LookupModelTier(cfg.TierID)
		if !ok {
			return fmt.Errorf("configured local AI tier %q is unknown; rerun AI setup", cfg.TierID)
		}
		if tier.PracticalContextTokens > 0 {
			maxTokens = tier.PracticalContextTokens
		}
		if assets.HostOS() == "browser" && !tier.WebGPUSupported {
			return fmt.Errorf("configured local AI tier %q is unavailable in the browser", cfg.TierID)
		}
		libPath, err = assets.EnsureRuntime(ctx, opts)
		if err != nil {
			return fmt.Errorf("runtime library error: %w", err)
		}
		modelPath, err = assets.EnsureModelTier(ctx, cfg.TierID, opts)
		if err != nil {
			return fmt.Errorf("model download error: %w", err)
		}
		chosenBackend = "auto"
		if assets.HostOS() == "browser" {
			chosenBackend = "gpu"
		}
		if r.verbose {
			ai.Verbose = true
			fmt.Fprintf(os.Stderr, "[kabibi] runtime library path: %s\n", libPath)
		}
		if info, infoErr := ai.LoadModelInfo(libPath, modelPath); infoErr == nil && info != nil {
			modelInfo = info
			if r.verbose {
				fmt.Fprintf(os.Stderr, "[info] Max Context Tokens: %d (dynamic: %v)\n", info.MaxContextTokens, info.IsDynamicContext)
				fmt.Fprintf(os.Stderr, "[info] Supports Function Calling: %v\n", info.SupportsFunctionCalling)
				fmt.Fprintf(os.Stderr, "[info] Supports Thinking: %v\n", info.SupportsThinking)
				fmt.Fprintf(os.Stderr, "[info] Supported Backends: %v\n", info.SupportedBackends)
			}
			if info.MaxContextTokens > 0 && info.MaxContextTokens < maxTokens {
				maxTokens = info.MaxContextTokens
			}

			hasBackend := func(target string) bool {
				for _, backend := range info.SupportedBackends {
					if strings.EqualFold(backend, target) {
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
		engine, err = ai.NewEngine(libPath, modelPath, chosenBackend, maxTokens)
		if err != nil && chosenBackend != "cpu" && assets.HostOS() != "browser" {
			chosenBackend = "cpu"
			engine, err = ai.NewEngine(libPath, modelPath, chosenBackend, maxTokens)
		}
	case "remote":
		maxTokens = remoteContextTokenBudget(cfg.ContextTokens)
		provider := ai.RemoteProvider(cfg.Provider)
		store := credstore.NewStore()
		credentialID := credentialStoreID(cfg)
		cred, credErr := store.Read(ctx, credentialID)
		if credErr != nil {
			return fmt.Errorf("load %s AI credential: %w", cfg.Provider, credErr)
		}
		if cred == nil && provider != ai.RemoteCustom {
			return fmt.Errorf("load %s AI credential: no stored credential; run `ai login %s` or `ai setup`", cfg.Provider, cfg.Provider)
		}
		var credentials ai.CredentialStore
		if provider != ai.RemoteCustom {
			credentials = store
		}
		engine, err = ai.NewRemoteEngine(ai.RemoteEngineConfig{
			Provider:    provider,
			Protocol:    ai.RemoteAPIProtocol(cfg.Protocol),
			Model:       cfg.Model,
			Credential:  cred,
			Credentials: credentials,
			BaseURL:     cfg.BaseURL,
		})
	default:
		return fmt.Errorf("unsupported AI mode %q; rerun AI setup", cfg.Mode)
	}
	if err != nil {
		return fmt.Errorf("create AI engine: %w", err)
	}
	if engine == nil {
		return fmt.Errorf("AI engine initialization returned no engine")
	}

	var partEmb ai.PartEmbedder
	var adaptiveEmb *adaptiveEmbedder
	if cfg.Embedder {
		if cfg.Mode != "local" || assets.HostOS() == "browser" {
			engine.Close()
			return fmt.Errorf("configured embedder is not supported for this AI mode")
		}
		embedPath, embedErr := assets.EnsureDefaultEmbeddingModel(ctx, opts)
		if embedErr != nil {
			engine.Close()
			return fmt.Errorf("embedding model download error: %w", embedErr)
		}
		if embedPath == "" {
			engine.Close()
			return fmt.Errorf("embedding model path is empty")
		}
		resident, residentErr := ai.NewEmbedder(libPath, embedPath, chosenBackend)
		reloadBackend := chosenBackend
		openEmbedder := func() (*ai.Embedder, error) {
			return ai.NewEmbedder(libPath, embedPath, reloadBackend)
		}
		reopenEngine := func() (*ai.Engine, error) {
			next, openErr := ai.NewEngine(libPath, modelPath, reloadBackend, maxTokens)
			if openErr != nil && reloadBackend != "cpu" && assets.HostOS() != "browser" {
				next, openErr = ai.NewEngine(libPath, modelPath, "cpu", maxTokens)
			}
			if openErr != nil {
				return nil, openErr
			}
			return next, nil
		}
		if residentErr != nil {
			if resident != nil {
				resident.Close()
			}
			resident = nil
		}
		adaptiveEmb = newAdaptiveEmbedder(r, resident, openEmbedder, reopenEngine)
		partEmb = adaptiveEmb
	}

	var cwd string
	if r.execSess != nil {
		cwd = r.execSess.Dir()
	}
	envContext := ai.FormatEnvironmentContext(ai.EnvironmentConfig{
		ModelPath:   modelPathOrRemote(cfg, modelPath),
		ModelSize:   assets.ModelAssetSize(modelPath),
		ModelInfo:   modelInfo,
		Backend:     engine.Backend(),
		MaxTokens:   maxTokens,
		HasEmbedder: partEmb != nil,
		WorkingDir:  cwd,
	})

	compactionLimit := contextCompactionLimit(maxTokens)
	sess, err := ai.NewSession(ai.SessionConfig{
		Engine:          engine,
		Embedder:        partEmb,
		SystemMessage:   kabibiSystemMessage(envContext, r.systemContext),
		Tools:           BuildKabibiTools(r.execSess),
		CompactionLimit: compactionLimit,
		OnFeedback:      r.emitFeedback,
		OnToolFeedback:  r.emitToolFeedback,
		OnContent:       r.emitContent,
		Verbose:         r.verbose,
	})
	if err != nil {
		engine.Close()
		if adaptiveEmb != nil {
			adaptiveEmb.Close()
		}
		return err
	}
	if err := sess.Init(); err != nil {
		engine.Close()
		sess.Shutdown()
		if adaptiveEmb != nil {
			adaptiveEmb.Close()
		}
		return err
	}

	r.engine = engine
	r.embedder = partEmb
	r.session = sess
	if adaptiveEmb != nil {
		adaptiveEmb.BindSession(sess)
	}
	return nil
}

func remoteContextTokenBudget(declaredInputTokens int) int {
	if declaredInputTokens > 0 {
		return declaredInputTokens
	}
	return 8192
}

func contextCompactionLimit(maxContextTokens int) int {
	return int(float64(maxContextTokens) * 0.80)
}

func kabibiSystemMessage(envContext string, sysContext string) string {
	message := defaultKabibiSystemPrompt
	if strings.TrimSpace(envContext) != "" {
		message = message + "\n\n" +
			"You are running on " + envContext + " - you use this information to answer questions and make infererences."
	}

	if strings.TrimSpace(sysContext) != "" {
		message = message + "\n\n" +
			"The computer you're running on has " + sysContext + " - you also use this information to answer questions and make infererences."
	}
	return message
}

// RunTurn runs a single AI turn, lazily initializing the underlying ai.Session if needed.
func (r *LazyAIRunner) RunTurn(ctx context.Context, prompt string, onFeedback, onToolFeedback, onContent ai.LogFeedback) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.setTurnCallbacks(onFeedback, onToolFeedback, onContent)
	defer r.setTurnCallbacks(nil, nil, nil)

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
	aiRunner.SetOnboardingPrompter(NewStdioOnboardingPrompter(os.Stdin, out))
	defer aiRunner.Close()

	route, parsed := es.ClassifyInput(line)
	switch route {
	case RouteEmpty, RouteBuiltin:
		return 0, nil

	case RouteIncomplete:
		return 2, errors.New("incomplete shell input")

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
	aiRunner.SetOnboardingPrompter(NewStdioOnboardingPrompter(os.Stdin, out))
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
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()
	if r.embedder != nil {
		if closer, ok := r.embedder.(interface{ Close() }); ok {
			closer.Close()
		}
		r.embedder = nil
	}
	if r.engine != nil {
		r.engine.Close()
		r.engine = nil
	}
}
