package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"ai"
	"ai/assets"
)

const (
	defaultGemmaModelURL      = "https://huggingface.co/litert-community/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it.litertlm"
	defaultGemmaModelSize     = 2038755678
	defaultEmbeddingModelURL  = "https://huggingface.co/litert-community/embeddinggemma-2-text-270m-litert-lm/resolve/main/embeddinggemma-2-text-270m.litertlm"
	defaultEmbeddingModelSize = 164626432
	maxKabibiToolOutput       = 32768
)

const defaultKabibiSystemPrompt = `You are Kabibi, an intelligent shell and developer assistant embedded directly inside an interactive shell session.
When the user asks a question or describes a task that is not a direct shell command, you assist them concisely and accurately.
You have access to Kabibi's live shell state, command history (including commands the user ran before talking to you), shell command execution in the active working directory, and file tools rooted at the shell's current working directory.`

// AIRunner abstracts the AI prompt execution backend so Shell can use either the
// lazy LiteRT-LM ai.Session manager or a deterministic test double in unit tests.
type AIRunner interface {
	RunTurn(ctx context.Context, prompt string, onFeedback ai.LogFeedback, onContent ai.LogFeedback) error
	Close()
}

func truncateToolOutput(s string) string {
	if len(s) > maxKabibiToolOutput {
		return s[:maxKabibiToolOutput] + "\n...[truncated]"
	}
	return s
}

func compactToolLabel(s string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(s))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

func resolveShellPath(cwd, target string) string {
	if target == "" {
		return cwd
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return filepath.Clean(filepath.Join(cwd, target))
}

// BuildKabibiTools constructs Kabibi's dedicated AI tool registry bound to the
// given ExecutionSession. None of these tools are shared with Pope; all file and
// command operations respect and update Kabibi's active shell working directory
// and command history.
func BuildKabibiTools(execSess *ExecutionSession) map[string]ai.Tool {
	return map[string]ai.Tool{
		"exec": {
			Description: "Execute a command inside Kabibi's interactive shell runner (sharing its working directory and environment) and return its output and exit code.",
			Parameters: map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command line to execute inside Kabibi's shell",
				},
			},
			Required: []string{"command"},
			Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
				var in struct {
					Command string `json:"command"`
				}
				if len(call.RawArguments) > 0 {
					if err := json.Unmarshal(call.RawArguments, &in); err != nil {
						return nil, fmt.Errorf("invalid arguments: %w", err)
					}
				}
				if strings.TrimSpace(in.Command) == "" {
					return nil, errors.New("command argument is required")
				}
				if feedback != nil {
					feedback("💻 " + compactToolLabel(in.Command, 22) + " ... ")
				}
				rec, runErr := execSess.RunCommand(ctx, in.Command, nil, SourceAITool, nil)
				if feedback != nil {
					if runErr != nil || rec.ExitCode != 0 {
						feedback("failed")
					} else {
						feedback("ok")
					}
				}
				res := map[string]any{
					"output":    truncateToolOutput(rec.Output),
					"exit_code": rec.ExitCode,
					"cwd":       rec.Cwd,
				}
				if runErr != nil {
					res["error"] = runErr.Error()
				}
				return res, nil
			},
		},

		"shell_history": {
			Description: "Retrieve recent shell commands and their outputs from Kabibi's shell session, including commands the user ran between AI prompts.",
			Parameters: map[string]any{
				"since_last_ai_turn": map[string]any{
					"type":        "boolean",
					"description": "If true, only return shell commands executed since the previous AI turn",
				},
				"count": map[string]any{
					"type":        "integer",
					"description": "Maximum number of recent command records to return (default 10)",
				},
				"include_output": map[string]any{
					"type":        "boolean",
					"description": "Whether to include captured stdout/stderr for each command (default true)",
				},
			},
			Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
				in := struct {
					SinceLastAITurn bool  `json:"since_last_ai_turn"`
					Count           int   `json:"count"`
					IncludeOutput   *bool `json:"include_output"`
				}{
					Count: 10,
				}
				if len(call.RawArguments) > 0 {
					if err := json.Unmarshal(call.RawArguments, &in); err != nil {
						return nil, fmt.Errorf("invalid arguments: %w", err)
					}
				}
				includeOut := true
				if in.IncludeOutput != nil {
					includeOut = *in.IncludeOutput
				}
				if feedback != nil {
					feedback("🐚 history ... ")
				}
				records := execSess.QueryHistory(in.SinceLastAITurn, in.Count, includeOut)
				if feedback != nil {
					feedback(fmt.Sprintf("%d command(s)", len(records)))
				}
				return map[string]any{
					"cwd":      execSess.Dir(),
					"commands": records,
				}, nil
			},
		},

		"shell_state": {
			Description: "Inspect Kabibi's current shell working directory, platform OS, last command status, and optional environment variables.",
			Parameters: map[string]any{
				"env_keys": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional list of environment variable names to inspect",
				},
			},
			Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
				var in struct {
					EnvKeys []string `json:"env_keys"`
				}
				if len(call.RawArguments) > 0 {
					if err := json.Unmarshal(call.RawArguments, &in); err != nil {
						return nil, fmt.Errorf("invalid arguments: %w", err)
					}
				}
				if feedback != nil {
					feedback("🧭 shell_state ... ")
				}
				envMap := execSess.EnvMap()
				selectedEnv := map[string]string{}
				for _, k := range in.EnvKeys {
					if v, ok := lookupEnvValue(envMap, k); ok {
						selectedEnv[k] = v
					}
				}
				records := execSess.Records()
				var lastCmd any
				if len(records) > 0 {
					last := records[len(records)-1]
					lastCmd = map[string]any{
						"id":        last.ID,
						"command":   last.Command,
						"exit_code": last.ExitCode,
						"cwd":       last.Cwd,
					}
				}
				if feedback != nil {
					feedback("ok")
				}
				return map[string]any{
					"cwd":          execSess.Dir(),
					"os":           runtime.GOOS,
					"env":          selectedEnv,
					"last_command": lastCmd,
				}, nil
			},
		},

		"read_file": {
			Description: "Read the contents of a file resolved relative to Kabibi's current shell working directory.",
			Parameters: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path to read (relative to current shell cwd or absolute)",
				},
				"start_line": map[string]any{
					"type":        "integer",
					"description": "Optional 1-based start line (inclusive)",
				},
				"end_line": map[string]any{
					"type":        "integer",
					"description": "Optional 1-based end line (inclusive)",
				},
			},
			Required: []string{"path"},
			Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
				var in struct {
					Path      string `json:"path"`
					StartLine int    `json:"start_line"`
					EndLine   int    `json:"end_line"`
				}
				if len(call.RawArguments) > 0 {
					if err := json.Unmarshal(call.RawArguments, &in); err != nil {
						return nil, fmt.Errorf("invalid arguments: %w", err)
					}
				}
				if strings.TrimSpace(in.Path) == "" {
					return nil, errors.New("path argument is required")
				}
				resolved := resolveShellPath(execSess.Dir(), in.Path)
				if feedback != nil {
					feedback("📖 " + compactToolLabel(filepath.Base(resolved), 18) + " ... ")
				}
				data, err := os.ReadFile(resolved)
				if err != nil {
					if feedback != nil {
						feedback("failed")
					}
					return nil, err
				}
				content := string(data)
				if in.StartLine > 0 || in.EndLine > 0 {
					lines := strings.Split(content, "\n")
					start := in.StartLine
					if start <= 0 {
						start = 1
					}
					end := in.EndLine
					if end <= 0 || end > len(lines) {
						end = len(lines)
					}
					if start > len(lines) {
						content = ""
					} else if start <= end {
						content = strings.Join(lines[start-1:end], "\n")
					}
				}
				if feedback != nil {
					feedback(fmt.Sprintf("%d bytes read", len(data)))
				}
				return map[string]any{
					"path":    resolved,
					"content": truncateToolOutput(content),
				}, nil
			},
		},

		"write_file": {
			Description: "Write or overwrite a file resolved relative to Kabibi's current shell working directory.",
			Parameters: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path to write (relative to current shell cwd or absolute)",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The text content to write to the file",
				},
			},
			Required: []string{"path", "content"},
			Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
				var in struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				if len(call.RawArguments) > 0 {
					if err := json.Unmarshal(call.RawArguments, &in); err != nil {
						return nil, fmt.Errorf("invalid arguments: %w", err)
					}
				}
				if strings.TrimSpace(in.Path) == "" {
					return nil, errors.New("path argument is required")
				}
				resolved := resolveShellPath(execSess.Dir(), in.Path)
				if feedback != nil {
					feedback("📝 " + compactToolLabel(filepath.Base(resolved), 18) + " ... ")
				}
				if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
					if feedback != nil {
						feedback("failed")
					}
					return nil, err
				}
				if err := os.WriteFile(resolved, []byte(in.Content), 0o644); err != nil {
					if feedback != nil {
						feedback("failed")
					}
					return nil, err
				}
				if feedback != nil {
					feedback("ok")
				}
				return map[string]any{
					"path":          resolved,
					"success":       true,
					"bytes_written": len(in.Content),
				}, nil
			},
		},

		"list_dir": {
			Description: "List files and directories at a path resolved relative to Kabibi's current shell working directory.",
			Parameters: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path to list (defaults to current shell working directory '.')",
				},
			},
			Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
				var in struct {
					Path string `json:"path"`
				}
				if len(call.RawArguments) > 0 {
					if err := json.Unmarshal(call.RawArguments, &in); err != nil {
						return nil, fmt.Errorf("invalid arguments: %w", err)
					}
				}
				target := in.Path
				if strings.TrimSpace(target) == "" {
					target = "."
				}
				resolved := resolveShellPath(execSess.Dir(), target)
				if feedback != nil {
					feedback("📂 " + compactToolLabel(filepath.Base(resolved), 18) + " ... ")
				}
				entries, err := os.ReadDir(resolved)
				if err != nil {
					if feedback != nil {
						feedback("failed")
					}
					return nil, err
				}
				type item struct {
					Name  string `json:"name"`
					IsDir bool   `json:"is_dir"`
					Size  int64  `json:"size,omitempty"`
				}
				items := make([]item, 0, len(entries))
				for _, e := range entries {
					var sz int64
					if info, err := e.Info(); err == nil && !e.IsDir() {
						sz = info.Size()
					}
					items = append(items, item{
						Name:  e.Name(),
						IsDir: e.IsDir(),
						Size:  sz,
					})
				}
				sort.Slice(items, func(i, j int) bool {
					if items[i].IsDir != items[j].IsDir {
						return items[i].IsDir
					}
					return items[i].Name < items[j].Name
				})
				if feedback != nil {
					feedback(fmt.Sprintf("%d entries", len(items)))
				}
				return map[string]any{
					"path":    resolved,
					"entries": items,
				}, nil
			},
		},
	}
}

// LazyAIRunner initializes LiteRT-LM (Engine, Embedder, and ai.Session) on demand
// when the first AI fallback turn occurs, forwarding all live feedback and content
// callbacks to the currently active turn.
type LazyAIRunner struct {
	mu       sync.Mutex
	execSess *ExecutionSession
	engine   *ai.Engine
	embedder *ai.Embedder
	session  *ai.Session
	verbose  bool

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

func defaultCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("user home directory unavailable: %w", err)
	}
	if assets.HostOS() == "windows" {
		return filepath.Join(home, "AppData", "Local", "kabibi", "litert_cache"), nil
	}
	return filepath.Join(home, ".cache", "kabibi", "litert_cache"), nil
}

func (r *LazyAIRunner) ensureInitialized(ctx context.Context) error {
	if r.session != nil {
		return nil
	}

	cacheDir, err := defaultCacheDir()
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

	modelPath, err := assets.EnsureModel(ctx, defaultGemmaModelURL, defaultGemmaModelSize, opts)
	if err != nil {
		return fmt.Errorf("model download error: %w", err)
	}

	chosenBackend := "cpu"
	maxTokens := 8192
	if info, err := ai.LoadModelInfo(libPath, modelPath); err == nil && info != nil {
		if r.verbose {
			fmt.Fprintf(os.Stderr, "[info] Max Context Tokens: %d (dynamic: %v)\n", info.MaxContextTokens, info.IsDynamicContext)
			fmt.Fprintf(os.Stderr, "[info] Supports Function Calling: %v\n", info.SupportsFunctionCalling)
			fmt.Fprintf(os.Stderr, "[info] Supports Thinking: %v\n", info.SupportsThinking)
			fmt.Fprintf(os.Stderr, "[info] Supported Backends: %v\n", info.SupportedBackends)
		}
		if info.MaxContextTokens > 0 {
			maxTokens = int(float64(info.MaxContextTokens) * 0.5)
		}
		for _, b := range info.SupportedBackends {
			if strings.EqualFold(b, "gpu") {
				chosenBackend = "gpu"
				break
			}
		}
	}

	engine, err := ai.NewEngine(libPath, modelPath, chosenBackend, maxTokens)
	if err != nil && chosenBackend != "cpu" {
		chosenBackend = "cpu"
		engine, err = ai.NewEngine(libPath, modelPath, chosenBackend, maxTokens)
	}
	if err != nil {
		return fmt.Errorf("create engine: %w", err)
	}

	var partEmb ai.PartEmbedder
	var emb *ai.Embedder
	if embedPath, err := assets.EnsureModel(ctx, defaultEmbeddingModelURL, defaultEmbeddingModelSize, opts); err == nil && embedPath != "" {
		if e, err := ai.NewEmbedder(libPath, embedPath, chosenBackend); err == nil {
			emb = e
			partEmb = e
		}
	}

	compactionLimit := int(float64(maxTokens) * 0.80)
	sess, err := ai.NewSession(ai.SessionConfig{
		Engine:          engine,
		Embedder:        partEmb,
		SystemMessage:   defaultKabibiSystemPrompt,
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

// FormatPromptWithShellContext enriches the user's prompt with a lightweight,
// factual shell context note when shell commands have been executed since the
// previous AI turn, ensuring Stage 1 triage knows when recent shell activity
// exists without bloating simple prompts.
func FormatPromptWithShellContext(execSess *ExecutionSession, userPrompt string) string {
	if execSess == nil {
		return userPrompt
	}
	recent := execSess.QueryHistory(true, 5, false)
	cwd := execSess.Dir()
	if len(recent) == 0 {
		return fmt.Sprintf("[Shell CWD: %s]\n%s", cwd, userPrompt)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Shell CWD: %s | %d shell command(s) ran since last AI turn: ", cwd, len(recent)))
	for i, rec := range recent {
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(fmt.Sprintf("#%d %q (exit %d)", rec.ID, rec.Command, rec.ExitCode))
	}
	sb.WriteString(" — use shell_history tool if you need their full output]\n")
	sb.WriteString(userPrompt)
	return sb.String()
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

	augmented := FormatPromptWithShellContext(r.execSess, prompt)
	err := r.session.RunTurn(ctx, augmented)
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
			if rec.ExitCode == 127 {
				// Stage B safety net: fall back to AI if executable was not found
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
