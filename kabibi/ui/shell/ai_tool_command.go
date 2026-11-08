package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"ai"
)

func normalizeAIToolName(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
}

// IsAIToolName reports whether name resolves to a registered Kabibi AI tool.
func IsAIToolName(name string) bool {
	_, ok := BuildKabibiTools(nil)[normalizeAIToolName(name)]
	return ok
}

// ExecuteOneShotAITool runs a registered tool directly and writes its Markdown
// result without starting an AI conversation.
func ExecuteOneShotAITool(ctx context.Context, name string, args []string, out io.Writer, verbose bool) error {
	if out == nil {
		out = os.Stdout
	}
	ai.Verbose = verbose
	execSess, err := NewExecutionSession("")
	if err != nil {
		return fmt.Errorf("initialize shell session: %w", err)
	}
	defer execSess.Close()

	var embedder ai.PartEmbedder
	needsEmbedder := true
	if normalizeAIToolName(name) == "find_files" {
		needsEmbedder = false
		if !(len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
			input, parseErr := parseFindFilesInput(execSess, args)
			if parseErr != nil {
				return fmt.Errorf("ai find_files: %w", parseErr)
			}
			needsEmbedder = findFilesNeedsEmbedder(input)
		}
	}
	if needsEmbedder {
		setup := func() error {
			return RunAISetup(ctx, execSess, os.Stdin, os.Stdout)
		}
		var closeEmbedder func()
		var diagnostic string
		embedder, closeEmbedder, diagnostic, err = resolveOneShotAIToolEmbedder(ctx, setup)
		if err != nil {
			return err
		}
		if closeEmbedder != nil {
			defer closeEmbedder()
		}
		if diagnostic != "" {
			fmt.Fprintf(os.Stderr, "[kabibi] %s\n", diagnostic)
		}
	}
	result, err := executeAITool(ctx, execSess, name, args, embedder)
	if err != nil {
		return err
	}
	n, err := io.WriteString(out, result)
	if err != nil {
		return fmt.Errorf("write AI tool result: %w", err)
	}
	if n != len(result) {
		return io.ErrShortWrite
	}
	return nil
}

func executeAITool(ctx context.Context, execSess *ExecutionSession, name string, args []string, embedder ai.PartEmbedder) (string, error) {
	tools := BuildKabibiTools(execSess)
	canonicalName := normalizeAIToolName(name)
	tool, ok := tools[canonicalName]
	if !ok {
		return "", fmt.Errorf("unknown AI tool %q (try 'ai --help')", name)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return formatAIToolHelp(canonicalName, tool), nil
	}

	rawArguments, err := parseAIToolArguments(tool, args)
	if err != nil {
		return "", fmt.Errorf("ai %s: %w", canonicalName, err)
	}
	result, err := tool.ExecuteWithContext(ctx, &ai.ToolContext{Embedder: embedder}, ai.ToolCall{
		Name:         canonicalName,
		RawArguments: rawArguments,
	})
	if err != nil {
		return "", fmt.Errorf("ai %s: %w", canonicalName, err)
	}
	return formatAIToolResultMarkdown(canonicalName, result)
}

func parseAIToolArguments(tool ai.Tool, args []string) (json.RawMessage, error) {
	state, err := scanAIToolArguments(tool, args, false)
	if err != nil {
		return nil, err
	}
	for _, required := range tool.Required {
		if _, ok := state.values[required]; !ok {
			return nil, fmt.Errorf("--%s is required", strings.ReplaceAll(required, "_", "-"))
		}
	}
	return json.Marshal(state.values)
}

type aiToolArgumentState struct {
	values         map[string]any
	pendingValue   map[string]any
	pendingBoolean map[string]any
}

func scanAIToolArguments(tool ai.Tool, args []string, allowIncomplete bool) (aiToolArgumentState, error) {
	state := aiToolArgumentState{
		values: make(map[string]any, len(args)/2),
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			return aiToolArgumentState{}, fmt.Errorf("expected --option value, got %q", arg)
		}
		option, rawValue, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		property := ""
		for name := range tool.Parameters {
			if normalizeAIToolName(name) == normalizeAIToolName(option) {
				property = name
				break
			}
		}
		ok := property != ""
		if !ok {
			return aiToolArgumentState{}, fmt.Errorf("unknown option --%s", option)
		}
		schema, ok := tool.Parameters[property].(map[string]any)
		if !ok {
			return aiToolArgumentState{}, fmt.Errorf("unsupported schema for --%s", option)
		}
		kind, _ := schema["type"].(string)

		if kind == "boolean" {
			if !hasValue && i+1 < len(args) && isBooleanValue(args[i+1]) {
				i++
				rawValue = args[i]
				hasValue = true
			}
			if !hasValue {
				if allowIncomplete && i+1 == len(args) {
					state.pendingBoolean = schema
					continue
				}
				rawValue = "true"
			}
		} else if !hasValue {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				if allowIncomplete && i+1 == len(args) {
					state.pendingValue = schema
					continue
				}
				return aiToolArgumentState{}, fmt.Errorf("--%s requires a value", option)
			}
			i++
			rawValue = args[i]
		}

		value, err := parseAIToolOptionValue(schema, rawValue)
		if err != nil {
			return aiToolArgumentState{}, fmt.Errorf("invalid --%s: %w", option, err)
		}
		if kind == "array" {
			state.values[property] = appendAIToolArrayValue(state.values[property], value)
		} else {
			state.values[property] = value
		}
	}
	return state, nil
}

func aiToolOptionSchema(tool ai.Tool, option string) map[string]any {
	for name, rawSchema := range tool.Parameters {
		if normalizeAIToolName(name) != normalizeAIToolName(option) {
			continue
		}
		schema, _ := rawSchema.(map[string]any)
		return schema
	}
	return nil
}

func parseAIToolOptionValue(schema map[string]any, raw string) (any, error) {
	kind, _ := schema["type"].(string)
	switch kind {
	case "string":
		return raw, nil
	case "boolean":
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("want true or false, got %q", raw)
		}
		return value, nil
	case "integer":
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("want an integer, got %q", raw)
		}
		return value, nil
	case "number":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("want a number, got %q", raw)
		}
		return value, nil
	case "array":
		items, _ := schema["items"].(map[string]any)
		itemType, _ := items["type"].(string)
		if itemType == "string" && !strings.HasPrefix(strings.TrimSpace(raw), "[") {
			return []any{raw}, nil
		}
		var value []any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, fmt.Errorf("want a JSON array, got %q", raw)
		}
		return value, nil
	case "object":
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, fmt.Errorf("want a JSON object, got %q", raw)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported option type %q", kind)
	}
}

func appendAIToolArrayValue(previous, next any) []any {
	values, _ := previous.([]any)
	nextValues, isArray := next.([]any)
	if isArray {
		return append(values, nextValues...)
	}
	return append(values, next)
}

func isBooleanValue(value string) bool {
	return strings.EqualFold(value, "true") || strings.EqualFold(value, "false")
}

func formatAIToolResultMarkdown(name string, result any) (string, error) {
	var (
		body         string
		language     = "text"
		markdownBody bool
	)
	if text, ok := result.(string); ok {
		body = text
		markdownBody = normalizeAIToolName(name) == "find_files"
	} else {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", fmt.Errorf("format tool result as JSON: %w", err)
		}
		body = string(encoded)
		language = "json"
	}
	body = strings.TrimRight(sanitizeMarkdownControls(body), "\n")
	if markdownBody {
		return fmt.Sprintf("### %s\n\n%s\n", name, body), nil
	}
	fence := markdownCodeFence(body)
	return fmt.Sprintf("### %s\n\n%s%s\n%s\n%s\n", name, fence, language, body, fence), nil
}

func markdownCodeFence(source string) string {
	longest := 2
	current := 0
	for _, r := range source {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return strings.Repeat("`", longest+1)
}

func formatAIToolHelp(name string, tool ai.Tool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Usage: ai %s [--option value ...]\n\n%s\n\nOptions:\n", strings.ReplaceAll(name, "_", "-"), tool.Description)

	names := make([]string, 0, len(tool.Parameters))
	for property := range tool.Parameters {
		names = append(names, property)
	}
	sort.Strings(names)
	required := make(map[string]bool, len(tool.Required))
	for _, property := range tool.Required {
		required[property] = true
	}
	for _, property := range names {
		schema, _ := tool.Parameters[property].(map[string]any)
		kind, _ := schema["type"].(string)
		description, _ := schema["description"].(string)
		marker := ""
		if required[property] {
			marker = " (required)"
		}
		fmt.Fprintf(&out, "  --%s <%s>%s  %s\n",
			strings.ReplaceAll(property, "_", "-"), kind, marker, description)
	}
	out.WriteString("\nUse JSON values for object and non-string array options. String arrays accept repeated options.\n")
	return out.String()
}

func formatAIToolsHelp(tools map[string]ai.Tool) string {
	var out strings.Builder
	out.WriteString("Usage: ai <tool> [--option value ...]\n\n")
	out.WriteString("Tool names and option names accept either '-' or '_'. Use 'ai <tool> --help' for options.\n\nTools:\n")

	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&out, "  %-22s %s\n", strings.ReplaceAll(name, "_", "-"), tools[name].Description)
	}
	return out.String()
}
