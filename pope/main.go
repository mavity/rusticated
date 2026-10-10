package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"ai"
	"ai/assets"
)

const (
	gemmaModelURL  = "https://huggingface.co/litert-community/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it.litertlm"
	gemmaModelSize = 2038755678
)

func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("user home directory unavailable: %w", err)
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Local", "kabibi", "litert_cache"), nil
	}
	return filepath.Join(home, ".cache", "kabibi", "litert_cache"), nil
}

func main() {
	var (
		promptFlag  string
		modelFlag   string
		backendFlag string
		systemFlag  string
		verboseFlag bool
		infoFlag    bool
	)

	flag.StringVar(&promptFlag, "p", "", "One-shot prompt execution")
	flag.StringVar(&promptFlag, "prompt", "", "One-shot prompt execution")
	flag.StringVar(&modelFlag, "m", "", "Path to .litertlm model file (optional)")
	flag.StringVar(&modelFlag, "model", "", "Path to .litertlm model file (optional)")
	flag.StringVar(&backendFlag, "b", "gpu", "LiteRT-LM backend (gpu, cpu, npu, auto)")
	flag.StringVar(&backendFlag, "backend", "gpu", "LiteRT-LM backend (gpu, cpu, npu, auto)")
	flag.StringVar(&systemFlag, "s", "You are Pope, a helpful and precise assistant equipped with system tools.", "System message")
	flag.StringVar(&systemFlag, "system", "You are Pope, a helpful and precise assistant equipped with system tools.", "System message")
	flag.BoolVar(&verboseFlag, "v", false, "Verbose output")
	flag.BoolVar(&verboseFlag, "verbose", false, "Verbose output")
	flag.BoolVar(&infoFlag, "i", false, "Print model info and exit")
	flag.BoolVar(&infoFlag, "info", false, "Print model info and exit")
	flag.Parse()

	ctx := context.Background()
	dir, err := cacheDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pope: cache dir error: %v\n", err)
		os.Exit(1)
	}

	opts := assets.FetchOptions{
		CacheDir: dir,
		OnProgress: func(stage, details string, percent int) {
			if verboseFlag {
				if percent >= 0 {
					fmt.Printf("[%s] %s (%d%%)\n", stage, details, percent)
				} else {
					fmt.Printf("[%s] %s\n", stage, details)
				}
			}
		},
	}

	libPath, err := assets.EnsureRuntime(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pope: runtime library error: %v\n", err)
		os.Exit(1)
	}

	modelPath := modelFlag
	if modelPath == "" {
		m, err := assets.EnsureModel(ctx, gemmaModelURL, gemmaModelSize, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pope: model download error: %v\n", err)
			os.Exit(1)
		}
		modelPath = m
	}

	// 1. Model Introspection
	info, err := ai.LoadModelInfo(libPath, modelPath)

	if infoFlag {
		fmt.Printf("Model Path: %s\n", modelPath)
		if err != nil {
			fmt.Printf("Model introspection unavailable: %v\n", err)
			os.Exit(1)
		}
		if info != nil {
			fmt.Printf("Max Context Tokens: %d\n", info.MaxContextTokens)
			fmt.Printf("Dynamic Context: %v\n", info.IsDynamicContext)
			fmt.Printf("Supports Thinking: %v\n", info.SupportsThinking)
			fmt.Printf("Supports Function Calling: %v\n", info.SupportsFunctionCalling)
			fmt.Printf("Supported Backends: %s\n", strings.Join(info.SupportedBackends, ", "))
		}
		return
	}

	if err != nil && verboseFlag {
		fmt.Fprintf(os.Stderr, "[info] warning: model introspection unavailable: %v\n", err)
	}

	if info != nil && verboseFlag {
		fmt.Printf("[info] Max Context Tokens: %d (dynamic: %v)\n", info.MaxContextTokens, info.IsDynamicContext)
		fmt.Printf("[info] Supports Function Calling: %v\n", info.SupportsFunctionCalling)
		fmt.Printf("[info] Supports Thinking: %v\n", info.SupportsThinking)
		fmt.Printf("[info] Supported Backends: %v\n", info.SupportedBackends)
	}

	// 2. Validate backend
	chosenBackend := backendFlag
	if info != nil && len(info.SupportedBackends) > 0 {
		supported := false
		for _, b := range info.SupportedBackends {
			if strings.EqualFold(b, chosenBackend) {
				supported = true
				break
			}
		}
		if !supported && chosenBackend != "auto" {
			if verboseFlag {
				fmt.Printf("[info] backend %s not in model supported backends %v; falling back to %s\n",
					chosenBackend, info.SupportedBackends, info.SupportedBackends[0])
			}
			chosenBackend = info.SupportedBackends[0]
		}
	}

	// 3. Initialize Engine
	// We allocate 75% of the model's reported max capacity (or a reasonable 8k default)
	// to avoid OOMs on limited devices while giving enough room for chat history.
	maxTokens := 8192
	if info != nil && info.MaxContextTokens > 0 {
		maxTokens = int(float64(info.MaxContextTokens) * 0.5)
	}

	engine, err := ai.NewEngine(libPath, modelPath, chosenBackend, maxTokens)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pope: failed to create engine: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	// 4. Setup Tools
	// The Gemma 4 E2B model metadata incorrectly reports SupportsFunctionCalling: false.
	// We force it to true and register tools anyway, as the model is instruction-tuned for it.
	var tools []map[string]any
	if true /* info == nil || info.SupportsFunctionCalling */ {
		tools = getToolDeclarations()
	}

	// 5. Setup Compaction Threshold
	// We proactively trigger compaction well before we hit the allocated limit.
	compactionLimit := int(float64(maxTokens) * 0.80)

	session := &HarnessSession{
		engine:          engine,
		systemMessage:   systemFlag,
		tools:           tools,
		compactionLimit: compactionLimit,
		verbose:         verboseFlag,
	}

	if err := session.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "pope: conversation init failed: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	// 6. Execution: One-Shot or REPL
	if promptFlag != "" {
		if err := session.RunTurn(ctx, promptFlag); err != nil {
			fmt.Fprintf(os.Stderr, "pope: turn error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Interactive REPL
	fmt.Println("Pope CLI AI Harness (Ctrl+C or 'exit' to quit)")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		if err := session.RunTurn(ctx, line); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
}

// HarnessSession tracks conversation state, history, and context compaction.
type HarnessSession struct {
	engine          *ai.Engine
	conv            *ai.Conversation
	systemMessage   string
	tools           []map[string]any
	compactionLimit int
	verbose         bool
	history         []ai.PromptPart
	totalTokensEst  int
}

func (s *HarnessSession) Init() error {
	cfg := ai.ConversationConfig{
		SystemMessage: s.systemMessage,
		Tools:         s.tools,
		History:       s.history,
	}
	conv, err := s.engine.NewConversationWithConfig(cfg)
	if err != nil {
		return err
	}
	s.conv = conv

	// Accurate starting estimate: count System message and Tools JSON size.
	s.totalTokensEst = len(s.systemMessage) / 4
	for _, t := range s.tools {
		if b, err := json.Marshal(t); err == nil {
			s.totalTokensEst += len(b) / 4
		}
	}
	return nil
}

func (s *HarnessSession) Close() {
	if s.conv != nil {
		s.conv.Close()
	}
}

func (s *HarnessSession) RunTurn(ctx context.Context, userText string) error {
	turnParts := []ai.PromptPart{{Role: ai.RoleUser, Text: userText}}
	s.history = append(s.history, turnParts...)
	s.totalTokensEst += len(userText) / 4

	for {
		stream, err := s.conv.SendStream(ctx, turnParts)
		if err != nil {
			return err
		}

		var toolCalls []ai.ToolCall
		var generatedText strings.Builder

		var streamErr error
		for event := range stream {
			switch e := event.(type) {
			case ai.TextEvent:
				fmt.Print(e.Text)
				generatedText.WriteString(e.Text)
			case ai.ToolCallEvent:
				toolCalls = append(toolCalls, e.ToolCalls...)
			case ai.ErrorEvent:
				streamErr = e.Err
			}
		}

		if streamErr != nil {
			// Catch out-of-context errors and trigger silent compaction
			if strings.Contains(streamErr.Error(), "exceeds available state entries") {
				if s.verbose {
					fmt.Printf("\n[engine hit KV limit (%v); forcing immediate compaction and retry]\n", streamErr)
				}
				s.compactContext(ctx)
				continue // retry the turn seamlessly
			}
			return streamErr
		}

		if len(toolCalls) == 0 {
			if s.verbose && generatedText.Len() > 0 {
				fmt.Println()
			}
			s.history = append(s.history, ai.PromptPart{Role: ai.RoleModel, Text: generatedText.String()})
			s.totalTokensEst += generatedText.Len() / 4
			break
		}

		// Model emitted tool calls
		var toolResponses []ai.PromptPart
		for _, call := range toolCalls {
			if s.verbose {
				fmt.Printf("\n[tool call: %s(%v)]\n", call.Name, call.Arguments)
			}
			result := executeTool(call.Name, call.Arguments)
			if s.verbose {
				fmt.Printf("[tool result: %v]\n", result)
			}

			// Track token consumption for the tool response
			if b, err := json.Marshal(result); err == nil {
				s.totalTokensEst += len(b) / 4
			}

			toolResponses = append(toolResponses, ai.PromptPart{
				Role:         ai.RoleTool,
				ToolName:     call.Name,
				ToolResponse: result,
			})
		}

		// Feed tool responses back to model in next iteration
		turnParts = toolResponses
		s.history = append(s.history, toolResponses...)
	}

	// Check if context compaction is needed
	if s.totalTokensEst > s.compactionLimit && len(s.history) > 4 {
		s.compactContext(ctx)
	}

	return nil
}

func (s *HarnessSession) compactContext(ctx context.Context) {
	if s.verbose {
		fmt.Println("\n[context compaction: summarizing earlier history...]")
	}
	cutoff := len(s.history) - 4
	older := s.history[:cutoff]
	recent := s.history[cutoff:]

	var historyText strings.Builder
	for _, p := range older {
		historyText.WriteString(fmt.Sprintf("%s: %s\n", p.Role, p.Text))
	}

	summaryConv, err := s.engine.NewConversationWithConfig(ai.ConversationConfig{
		SystemMessage: "You are a concise conversation summarizer. Summarize the key facts from the conversation below in 2-3 sentences.",
	})
	if err == nil {
		defer summaryConv.Close()
		var summary strings.Builder
		_ = summaryConv.SendText(ctx, historyText.String(), func(token string) {
			summary.WriteString(token)
		})

		sumText := strings.TrimSpace(summary.String())
		if sumText != "" {
			if s.verbose {
				fmt.Printf("[context compacted summary: %s]\n", sumText)
			}
			s.history = append([]ai.PromptPart{{Role: ai.RoleUser, Text: "[Previous conversation summary]: " + sumText}}, recent...)
			s.totalTokensEst = len(sumText)/4 + 200
			s.Close()
			_ = s.Init()
		}
	}
}

func getToolDeclarations() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read the contents of a local file at the specified path.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "The file path to read"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write_file",
				"description": "Write or overwrite content to a local file at the specified path.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "The file path to write to"},
						"content": map[string]any{"type": "string", "description": "The text content to write"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "exec",
				"description": "Execute a shell command on the host system and return its output.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string", "description": "The shell command to run"},
					},
					"required": []string{"command"},
				},
			},
		},
	}
}

func executeTool(name string, args map[string]any) map[string]any {
	switch name {
	case "read_file":
		path, _ := args["path"].(string)
		if path == "" {
			return map[string]any{"error": "path argument is required"}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		content := string(data)
		if len(content) > 32768 {
			content = content[:32768] + "\n...[truncated]"
		}
		return map[string]any{"content": content}

	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if path == "" {
			return map[string]any{"error": "path argument is required"}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return map[string]any{"error": err.Error()}
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"success": true, "bytes_written": len(content)}

	case "exec":
		cmdStr, _ := args["command"].(string)
		if cmdStr == "" {
			return map[string]any{"error": "command argument is required"}
		}
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd.exe", "/c", cmdStr)
		} else {
			cmd = exec.Command("sh", "-c", cmdStr)
		}
		out, err := cmd.CombinedOutput()
		outStr := string(out)
		if len(outStr) > 32768 {
			outStr = outStr[:32768] + "\n...[truncated]"
		}
		if err != nil {
			return map[string]any{"output": outStr, "error": err.Error()}
		}
		return map[string]any{"output": outStr}

	default:
		return map[string]any{"error": fmt.Sprintf("unknown tool: %s", name)}
	}
}
