package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// TurnTiming records user-side timestamps for a single conversational turn:
// when the user typed the first character of the prompt (TypedAt) and when the
// user submitted the prompt (SubmittedAt).
type TurnTiming struct {
	TypedAt     time.Time
	SubmittedAt time.Time
}

type turnTimingCtxKey struct{}

// WithTurnTiming attaches user turn timing metadata to ctx for Session.RunTurn.
func WithTurnTiming(ctx context.Context, timing TurnTiming) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnTimingCtxKey{}, timing)
}

// TurnTimingFromContext extracts any TurnTiming attached to ctx.
func TurnTimingFromContext(ctx context.Context) TurnTiming {
	if ctx == nil {
		return TurnTiming{}
	}
	if t, ok := ctx.Value(turnTimingCtxKey{}).(TurnTiming); ok {
		return t
	}
	return TurnTiming{}
}

type textCompressorFunc func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

type ephemeralStreamFunc func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error

// PartEmbedder can embed prompt parts for background indexing and RAG compaction.
type PartEmbedder interface {
	Embed(parts []PromptPart) ([]float32, error)
}

// LogFeedback receives streamed text fragments.
type LogFeedback func(text string)

// DeliberateToolName is the canonical name of the built-in deliberation tool.
const DeliberateToolName = "deliberate"

// ToolContext provides explicit runtime and session capabilities to tool
// handlers, allowing tools to inspect, clone, modify, or replace the Session
// and active Conversation in place.
type ToolContext struct {
	Session        *Session
	Engine         *Engine
	Conversation   *Conversation
	Feedback       LogFeedback
	baseHistory    *[]PromptPart
	userText       string
	userPart       PromptPart
	streamer       *liveStreamer
	trace          *CognitiveTrace
	sessionSwapped bool
}

// ReplaceSession replaces the current Session with next, transferring modified
// history, compaction state, or configuration so subsequent turns and stages
// continue seamlessly with the updated session state.
func (tc *ToolContext) ReplaceSession(next *Session) {
	if tc == nil || next == nil {
		return
	}
	if tc.Session != nil && tc.Session != next {
		tc.Session.AdoptSessionState(next)
	} else {
		tc.Session = next
	}
	if tc.baseHistory != nil && tc.Session != nil {
		*tc.baseHistory = tc.Session.snapshotCompactedHistory()
	}
	tc.sessionSwapped = true
}

// CloneConversation creates a lightweight, zero-cost branched conversation handle
// using LiteRT-LM's native litert_lm_conversation_clone C API.
func (tc *ToolContext) CloneConversation(ctx context.Context) (*Conversation, error) {
	if tc == nil || tc.Conversation == nil {
		return nil, errors.New("ai: no active conversation handle in tool context")
	}
	return tc.Conversation.Clone(ctx)
}

// Tool defines a capability registered with a Session.
type Tool struct {
	Description        string
	Parameters         map[string]any
	Required           []string
	Execute            func(ctx context.Context, call ToolCall, feedback LogFeedback) (any, error)
	ExecuteWithContext func(ctx context.Context, tc *ToolContext, call ToolCall) (any, error)
}

// NewTool constructs a declarative Tool by reflecting over the typed input
// struct T: a blank `_ struct{}` field's `doc` tag provides the tool
// Description, and each exported field's `json` and `doc` tags define its
// JSON Schema property and whether it is required (unless `omitempty` or a
// pointer type is used).
func NewTool[T any](fn func(ctx context.Context, in T, feedback LogFeedback) (any, error)) Tool {
	return NewSessionTool(func(ctx context.Context, tc *ToolContext, in T) (any, error) {
		var fb LogFeedback
		if tc != nil {
			fb = tc.Feedback
		}
		return fn(ctx, in, fb)
	})
}

// NewSessionTool constructs a declarative Tool whose handler receives *ToolContext
// directly, giving the tool full access to inspect, clone, modify, or replace
// tc.Session (including CanonicalHistory and CompactedHistory).
func NewSessionTool[T any](fn func(ctx context.Context, tc *ToolContext, in T) (any, error)) Tool {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("ai.NewSessionTool: input type must be a struct, got %v", t))
	}
	description, properties, required := reflectStructSchema(t)
	decode := func(call ToolCall) (T, error) {
		var in T
		raw := call.RawArguments
		if len(raw) == 0 && call.Arguments != nil {
			var err error
			raw, err = json.Marshal(call.Arguments)
			if err != nil {
				return in, err
			}
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return in, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		return in, nil
	}
	return Tool{
		Description: description,
		Parameters:  properties,
		Required:    required,
		Execute: func(ctx context.Context, call ToolCall, feedback LogFeedback) (any, error) {
			in, err := decode(call)
			if err != nil {
				return nil, err
			}
			return fn(ctx, &ToolContext{Feedback: feedback}, in)
		},
		ExecuteWithContext: func(ctx context.Context, tc *ToolContext, call ToolCall) (any, error) {
			in, err := decode(call)
			if err != nil {
				return nil, err
			}
			return fn(ctx, tc, in)
		},
	}
}

// ToolLead formats the standard tool progress prefix ("<emoji> <arg> ... ")
// from the `_ struct{}` field's `emoji` tag on a tool input struct.
func ToolLead(input any, primaryArg string, maxArgLen int) string {
	in := reflect.TypeOf(input)
	for in.Kind() == reflect.Pointer {
		in = in.Elem()
	}
	for i := range in.NumField() {
		field := in.Field(i)
		if field.Name == "_" {
			runes := []rune(strings.TrimSpace(primaryArg))
			compact := string(runes)
			if maxArgLen > 0 && len(runes) > maxArgLen {
				if maxArgLen == 1 {
					compact = "…"
				} else {
					compact = string(runes[:maxArgLen-1]) + "…"
				}
			}
			return field.Tag.Get("emoji") + " " + compact + " ... "
		}
	}
	panic(fmt.Sprintf("tool input %v has no metadata field", in))
}

func reflectStructSchema(in reflect.Type) (description string, properties map[string]any, required []string) {
	properties = make(map[string]any)
	for i := range in.NumField() {
		f := in.Field(i)
		if f.Name == "_" {
			description = f.Tag.Get("doc")
			continue
		}
		if !f.IsExported() {
			continue
		}
		param, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if param == "-" {
			continue
		}
		if param == "" {
			param = strings.ToLower(f.Name)
		}
		schema := reflectFieldTypeSchema(f.Type)
		if doc := f.Tag.Get("doc"); doc != "" {
			schema["description"] = doc
		}
		properties[param] = schema
		if !strings.Contains(opts, "omitempty") && f.Type.Kind() != reflect.Pointer {
			required = append(required, param)
		}
	}
	return description, properties, required
}

func reflectFieldTypeSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": reflectFieldTypeSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.Struct:
		desc, props, req := reflectStructSchema(t)
		schema := map[string]any{
			"type":       "object",
			"properties": props,
		}
		if desc != "" {
			schema["description"] = desc
		}
		if len(req) > 0 {
			schema["required"] = req
		}
		return schema
	}
	panic(fmt.Sprintf("ai: unsupported tool parameter type %v", t))
}

// Embedder returns the Session's configured PartEmbedder, if any.
func (s *Session) Embedder() PartEmbedder {
	if s == nil {
		return nil
	}
	return s.embedder
}

// SessionConfig configures a high-level reflective chat session.
type SessionConfig struct {
	Engine          *Engine
	Embedder        PartEmbedder
	SystemMessage   string
	Tools           map[string]Tool
	CompactionLimit int
	OnContent       LogFeedback
	OnFeedback      LogFeedback
	Verbose         bool
}

// Session tracks conversation state, history, and context compaction.
type Session struct {
	engine           *Engine
	embedder         PartEmbedder
	compressor       textCompressorFunc
	ephemeralStream  ephemeralStreamFunc
	conv             *Conversation
	systemMessage    string
	tools            []map[string]any
	toolRegistry     map[string]Tool
	compactionLimit  int
	verbose          bool
	onContent        LogFeedback
	onFeedback       LogFeedback
	CanonicalHistory []PromptPart
	CompactedHistory []PromptPart
	totalTokensEst   int
	artifactCache    map[string]string

	historyMu      sync.Mutex
	turnGate       sync.Mutex
	turnWaiting    atomic.Bool
	embeddedUpTo   int
	embedTrigger   chan struct{}
	embedStop      chan struct{}
	embedDone      chan struct{}
	embedOnce      sync.Once
	embedCloseOnce sync.Once
}

// NewSession constructs a Session from declarative configuration.
func NewSession(cfg SessionConfig) (*Session, error) {
	s := &Session{
		engine:          cfg.Engine,
		embedder:        cfg.Embedder,
		systemMessage:   cfg.SystemMessage,
		compactionLimit: cfg.CompactionLimit,
		verbose:         cfg.Verbose,
		onContent:       cfg.OnContent,
		onFeedback:      cfg.OnFeedback,
		toolRegistry:    make(map[string]Tool, len(cfg.Tools)),
	}
	if s.compactionLimit <= 0 {
		s.compactionLimit = 6000
	}
	// Register the built-in "deliberate" tool so the model can invoke structured
	// contemplation on demand via ToolContext (or via the Stage 1 GOAL: trigger).
	s.toolRegistry[DeliberateToolName] = NewDeliberateTool()

	names := make([]string, 0, len(cfg.Tools))
	for name := range cfg.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tool := cfg.Tools[name]
		s.toolRegistry[name] = tool
		parameters := compileToolParameters(tool)
		s.tools = append(s.tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": tool.Description,
				"parameters":  parameters,
			},
		})
	}
	return s, nil
}

func compileToolParameters(tool Tool) map[string]any {
	parameters := make(map[string]any)
	for key, value := range tool.Parameters {
		parameters[key] = value
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		properties = make(map[string]any, len(tool.Parameters)+1)
		for key, value := range tool.Parameters {
			properties[key] = value
		}
		parameters = map[string]any{"type": "object"}
		parameters["properties"] = properties
	} else {
		propertiesCopy := make(map[string]any, len(properties)+1)
		for key, value := range properties {
			propertiesCopy[key] = value
		}
		properties = propertiesCopy
		parameters["properties"] = properties
	}
	if _, ok := properties["explanation"]; !ok {
		properties["explanation"] = map[string]any{
			"type":        "string",
			"description": "Brief explanation of why this tool is being called and what specific information is sought.",
		}
	}
	required := append([]string(nil), tool.Required...)
	switch raw := parameters["required"].(type) {
	case []string:
		required = append(required, raw...)
	case []any:
		for _, value := range raw {
			if name, ok := value.(string); ok {
				required = append(required, name)
			}
		}
	}
	seen := make(map[string]bool, len(required)+len(tool.Required)+1)
	for _, name := range required {
		seen[name] = true
	}
	for _, name := range tool.Required {
		if !seen[name] {
			required = append(required, name)
			seen[name] = true
		}
	}
	if !seen["explanation"] {
		required = append(required, "explanation")
	}
	parameters["required"] = required
	return parameters
}

func (s *Session) appendHistory(parts ...PromptPart) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.CanonicalHistory = append(s.CanonicalHistory, parts...)
	s.CompactedHistory = append(s.CompactedHistory, parts...)
}

// AdoptSessionState replaces s's mutable history, compaction state, and
// configuration with next's state in a thread-safe manner. This allows a tool
// that constructs or modifies a replacement *Session (and assigns tc.Session = next
// or calls tc.ReplaceSession(next)) to update the live session in place.
func (s *Session) AdoptSessionState(next *Session) {
	if s == nil || next == nil || s == next {
		return
	}
	s.historyMu.Lock()
	defer s.historyMu.Unlock()

	s.CanonicalHistory = append([]PromptPart(nil), next.CanonicalHistory...)
	s.CompactedHistory = append([]PromptPart(nil), next.CompactedHistory...)
	if next.systemMessage != "" {
		s.systemMessage = next.systemMessage
	}
	if next.compactionLimit > 0 {
		s.compactionLimit = next.compactionLimit
	}
	if len(next.tools) > 0 {
		s.tools = next.tools
		s.toolRegistry = next.toolRegistry
	}
	if next.artifactCache != nil {
		s.artifactCache = next.artifactCache
	}
	s.embeddedUpTo = 0
}

func (s *Session) hasEmbedder() bool {
	if s.embedder == nil {
		return false
	}
	value := reflect.ValueOf(s.embedder)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func (s *Session) emitFeedback(text string) {
	if s.onFeedback != nil {
		s.onFeedback(text)
	}
}

func (s *Session) emitContent(text string) {
	if s.onContent != nil {
		s.onContent(text)
	}
}

// executeTool dispatches a model tool call and converts execution failures to
// a result the model can inspect and recover from. If the tool modifies or
// replaces tc.Session, the active session state and baseHistory are synchronized.
func (s *Session) executeTool(ctx context.Context, tc *ToolContext, call ToolCall) any {
	tool, ok := s.toolRegistry[call.Name]
	if !ok {
		return map[string]any{"error": "unknown tool: " + call.Name}
	}
	if tc == nil {
		tc = &ToolContext{
			Session:      s,
			Engine:       s.engine,
			Conversation: s.conv,
			Feedback:     s.emitFeedback,
		}
	} else if tc.Session == nil {
		tc.Session = s
	}

	prevCompactedLen := len(s.CompactedHistory)
	var prevFirstText string
	if prevCompactedLen > 0 {
		prevFirstText = s.CompactedHistory[0].Text
	}

	syncSessionAfterTool := func() {
		if tc.Session != nil && tc.Session != s {
			s.AdoptSessionState(tc.Session)
			tc.Session = s
			tc.sessionSwapped = true
		}
		if len(s.CompactedHistory) != prevCompactedLen || (len(s.CompactedHistory) > 0 && s.CompactedHistory[0].Text != prevFirstText) {
			tc.sessionSwapped = true
		}
		if tc.sessionSwapped && tc.baseHistory != nil {
			*tc.baseHistory = s.snapshotCompactedHistory()
		}
	}

	if tool.ExecuteWithContext != nil {
		result, err := tool.ExecuteWithContext(ctx, tc, call)
		syncSessionAfterTool()
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return result
	}
	if tool.Execute == nil {
		return map[string]any{"error": "tool has no executor: " + call.Name}
	}
	fb := s.emitFeedback
	if tc.Feedback != nil {
		fb = tc.Feedback
	}
	result, err := tool.Execute(ctx, call, fb)
	syncSessionAfterTool()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return result
}

func (s *Session) startEmbedWorker() {
	if !s.hasEmbedder() {
		return
	}
	s.embedOnce.Do(func() {
		s.embedTrigger = make(chan struct{}, 1)
		s.embedStop = make(chan struct{})
		s.embedDone = make(chan struct{})
		go s.embedWorkerLoop()
	})
}

func (s *Session) triggerBackgroundEmbedding() {
	if !s.hasEmbedder() {
		return
	}
	s.startEmbedWorker()
	select {
	case s.embedTrigger <- struct{}{}:
	default:
	}
}

func (s *Session) embedWorkerLoop() {
	defer close(s.embedDone)
	for {
		select {
		case <-s.embedStop:
			return
		case <-s.embedTrigger:
			s.processPendingEmbeddings(false)
		}
	}
}

// processPendingEmbeddings embeds unembedded PromptPart items in CanonicalHistory
// one by one while holding turnGate so it never contends with active LLM generation.
// If flushAll is false, it yields immediately as soon as a user turn is waiting.
func (s *Session) processPendingEmbeddings(flushAll bool) {
	for {
		if !flushAll {
			select {
			case <-s.embedStop:
				return
			default:
			}
			if s.turnWaiting.Load() {
				return
			}
		}

		s.turnGate.Lock()
		if !flushAll && s.turnWaiting.Load() {
			s.turnGate.Unlock()
			return
		}

		s.historyMu.Lock()
		for s.embeddedUpTo < len(s.CanonicalHistory) && len(s.CanonicalHistory[s.embeddedUpTo].Embedding) > 0 {
			s.embeddedUpTo++
		}
		if s.embeddedUpTo >= len(s.CanonicalHistory) {
			s.historyMu.Unlock()
			s.turnGate.Unlock()
			return
		}
		idx := s.embeddedUpTo
		part := s.CanonicalHistory[idx]
		s.historyMu.Unlock()

		vec, err := s.embedder.Embed([]PromptPart{part})
		var traceVec []float32
		if part.CognitiveTrace != nil && !part.CognitiveTrace.IsDirect && len(part.CognitiveTrace.TraceEmbeddings) == 0 {
			traceText := strings.TrimSpace(part.CognitiveTrace.Goal + "\n" + part.CognitiveTrace.SynthesizedPlan)
			if traceText != "" {
				if tv, terr := s.embedder.Embed([]PromptPart{{Role: RoleModel, Text: traceText}}); terr == nil && len(tv) > 0 {
					traceVec = tv
				}
			}
		}
		s.historyMu.Lock()
		s.applyEmbeddingLocked(idx, part, vec, traceVec, err)
		s.historyMu.Unlock()
		s.turnGate.Unlock()
	}
}

func (s *Session) applyEmbeddingLocked(idx int, part PromptPart, vec, traceVec []float32, err error) {
	if err == nil && len(vec) > 0 && idx < len(s.CanonicalHistory) {
		s.CanonicalHistory[idx].Embedding = vec
		syncCompactedEmbedding(s.CompactedHistory, part, vec)
		if s.verbose {
			fmt.Fprintf(os.Stderr, "[embed] #%d %s %s\n", idx, part.Role, formatEmbeddingSummary(vec))
		}
	} else if err != nil && s.verbose {
		fmt.Fprintf(os.Stderr, "[embed] #%d %s error: %v\n", idx, part.Role, err)
	}
	if len(traceVec) > 0 && idx < len(s.CanonicalHistory) && s.CanonicalHistory[idx].CognitiveTrace != nil {
		s.CanonicalHistory[idx].CognitiveTrace.TraceEmbeddings = [][]float32{traceVec}
		if s.verbose {
			fmt.Fprintf(os.Stderr, "[embed] #%d trace %s\n", idx, formatEmbeddingSummary(traceVec))
		}
	}
	if idx == s.embeddedUpTo {
		s.embeddedUpTo++
	}
}

func (s *Session) flushEmbeddingsInline() {
	if !s.hasEmbedder() {
		return
	}
	for {
		s.historyMu.Lock()
		for s.embeddedUpTo < len(s.CanonicalHistory) && len(s.CanonicalHistory[s.embeddedUpTo].Embedding) > 0 {
			s.embeddedUpTo++
		}
		if s.embeddedUpTo >= len(s.CanonicalHistory) {
			s.historyMu.Unlock()
			return
		}
		idx := s.embeddedUpTo
		part := s.CanonicalHistory[idx]
		s.historyMu.Unlock()

		vec, err := s.embedder.Embed([]PromptPart{part})
		var traceVec []float32
		if part.CognitiveTrace != nil && !part.CognitiveTrace.IsDirect && len(part.CognitiveTrace.TraceEmbeddings) == 0 {
			traceText := strings.TrimSpace(part.CognitiveTrace.Goal + "\n" + part.CognitiveTrace.SynthesizedPlan)
			if traceText != "" {
				if tv, terr := s.embedder.Embed([]PromptPart{{Role: RoleModel, Text: traceText}}); terr == nil && len(tv) > 0 {
					traceVec = tv
				}
			}
		}
		s.historyMu.Lock()
		s.applyEmbeddingLocked(idx, part, vec, traceVec, err)
		s.historyMu.Unlock()
	}
}

// formatEmbeddingSummary returns an industry-standard compact diagnostic representation
// of an embedding vector: dimension, preview of leading/trailing components, L2 norm, and 64-bit SimHash.
func formatEmbeddingSummary(vec []float32) string {
	n := len(vec)
	if n == 0 {
		return "dim=0 []"
	}
	var sumSq float64
	var simhash uint64
	for i, v := range vec {
		sumSq += float64(v) * float64(v)
		if i < 64 && v >= 0 {
			simhash |= 1 << uint(63-i)
		}
	}
	norm := math.Sqrt(sumSq)
	if n <= 4 {
		var parts []string
		for _, v := range vec {
			parts = append(parts, fmt.Sprintf("%+.4f", v))
		}
		return fmt.Sprintf("dim=%d [%s] l2=%.4f simhash=%016x", n, strings.Join(parts, ", "), norm, simhash)
	}
	return fmt.Sprintf("dim=%d [%+.4f, %+.4f, %+.4f, ..., %+.4f] l2=%.4f simhash=%016x",
		n, vec[0], vec[1], vec[2], vec[n-1], norm, simhash)
}

func syncCompactedEmbedding(compacted []PromptPart, target PromptPart, vec []float32) {
	for i := len(compacted) - 1; i >= 0; i-- {
		p := &compacted[i]
		if len(p.Embedding) == 0 && p.Role == target.Role && p.Text == target.Text && p.ToolName == target.ToolName && len(p.ToolCalls) == len(target.ToolCalls) {
			p.Embedding = vec
			return
		}
	}
}

// FlushEmbeddings synchronously finishes any pending embeddings in CanonicalHistory.
func (s *Session) FlushEmbeddings() {
	if !s.hasEmbedder() {
		return
	}
	s.processPendingEmbeddings(true)
}

func (s *Session) Init() error {
	return s.initWithHistory(s.CompactedHistory)
}

func (s *Session) initWithHistory(history []PromptPart) error {
	if s.engine == nil {
		return nil
	}
	cfg := ConversationConfig{
		SystemMessage: s.systemMessage,
		Tools:         s.tools,
		History:       history,
	}
	conv, err := s.engine.NewConversationWithConfig(cfg)
	if err != nil {
		return err
	}
	s.conv = conv

	// Accurate starting estimate: count System message, Tools, and seeded History size.
	s.totalTokensEst = len(s.systemMessage)/4 + historySize(history)/4
	for _, t := range s.tools {
		if b, err := json.Marshal(t); err == nil {
			s.totalTokensEst += len(b) / 4
		}
	}
	s.startEmbedWorker()
	return nil
}

// ReplaceEngine rebuilds the active conversation on engine using the current
// compacted history. Callers must hold the turn gate and ensure no generation
// or other embedding operation is active while replacing the engine.
func (s *Session) ReplaceEngine(engine *Engine) error {
	if s == nil || engine == nil {
		return errors.New("replace session engine requires a non-nil session and engine")
	}
	s.closeConv()
	s.engine = engine
	s.historyMu.Lock()
	history := append([]PromptPart(nil), s.CompactedHistory...)
	s.historyMu.Unlock()
	if err := s.initWithHistory(history); err != nil {
		s.engine = nil
		return err
	}
	return nil
}

// SuspendConversation closes the current model conversation before an engine
// is released. The caller must hold the Session turn gate.
func (s *Session) SuspendConversation() {
	if s != nil {
		s.closeConv()
	}
}

func (s *Session) Close() {
	s.closeConv()
}

// CompactContext projects canonical history into the compacted model context.
func (s *Session) CompactContext(ctx context.Context) error {
	s.turnWaiting.Store(true)
	s.turnGate.Lock()
	s.turnWaiting.Store(false)
	defer func() {
		s.turnGate.Unlock()
		s.triggerBackgroundEmbedding()
	}()
	err := s.compactContext(ctx, s.emitFeedback)
	s.emitFeedback("\n")
	return err
}

func (s *Session) closeConv() {
	if s.conv != nil {
		s.conv.Close()
		s.conv = nil
	}
}

// Shutdown stops the background embedding worker and closes the active conversation.
func (s *Session) Shutdown() {
	if s.embedStop != nil {
		s.embedCloseOnce.Do(func() {
			s.turnWaiting.Store(true)
			close(s.embedStop)
			<-s.embedDone
		})
	}
	s.closeConv()
}

func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "exceeds available state entries") ||
		strings.Contains(msg, "Input token ids are too long") ||
		strings.Contains(msg, "Exceeding the maximum number of tokens") ||
		strings.Contains(msg, "Max number of tokens reached")
}

const defaultStreamerMaxRunes = 76

// liveStreamer implements the Zero-ANSI Live Streamer specification: single-line
// stage ticker overwritten via carriage-return + space-padding, without ANSI codes.
type liveStreamer struct {
	feedback      LogFeedback
	maxRunes      int
	prevLineRunes int
	currLineRunes int
	isCapped      bool
	lastWasSpace  bool
	buf           strings.Builder
}

func newLiveStreamer(feedback LogFeedback, maxRunes int) *liveStreamer {
	if maxRunes <= 0 {
		maxRunes = defaultStreamerMaxRunes
	}
	return &liveStreamer{
		feedback: feedback,
		maxRunes: maxRunes,
	}
}

func (l *liveStreamer) write(text string) {
	if l.feedback != nil {
		l.feedback(text)
	}
}

func (l *liveStreamer) StartStage(label string) {
	if l.prevLineRunes > 0 {
		l.write(fmt.Sprintf("\r%s\r", strings.Repeat(" ", l.prevLineRunes)))
	}
	l.currLineRunes = 0
	l.isCapped = false
	l.lastWasSpace = true
	l.buf.Reset()

	tag := "[" + label + "] "
	l.write(tag)
	l.currLineRunes = utf8.RuneCountInString(tag)
}

func (l *liveStreamer) Feed(tokenChunk string) {
	l.buf.WriteString(tokenChunk)
	if l.isCapped {
		return
	}
	for _, r := range tokenChunk {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			if l.lastWasSpace {
				continue
			}
			r = ' '
			l.lastWasSpace = true
		} else {
			l.lastWasSpace = false
		}
		if l.currLineRunes+1 == l.maxRunes {
			l.write("…")
			l.currLineRunes++
			l.isCapped = true
			break
		}
		l.write(string(r))
		l.currLineRunes++
	}
}

func (l *liveStreamer) EndStage() string {
	l.prevLineRunes = l.currLineRunes
	return strings.TrimSpace(l.buf.String())
}

func (l *liveStreamer) Wipe() {
	if l.prevLineRunes > 0 {
		l.write(fmt.Sprintf("\r%s\r", strings.Repeat(" ", l.prevLineRunes)))
		l.prevLineRunes = 0
	} else if l.currLineRunes > 0 {
		l.write(fmt.Sprintf("\r%s\r", strings.Repeat(" ", l.currLineRunes)))
	}
	l.currLineRunes = 0
	l.isCapped = false
	l.lastWasSpace = false
}

func (s *Session) snapshotCompactedHistory() []PromptPart {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return append([]PromptPart(nil), s.CompactedHistory...)
}

func (s *Session) systemMessageWithToolNames() string {
	var names []string
	for _, t := range s.tools {
		if fn, ok := t["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" && name != DeliberateToolName {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return s.systemMessage
	}
	sort.Strings(names)
	base := strings.TrimSpace(s.systemMessage)
	if base != "" {
		base += "\n"
	}
	return base + "Available tools (for planning reference only): " + strings.Join(names, ", ")
}

// DeliberateOutput is the structured synthesis returned by the built-in
// "deliberate" tool after running expectation calibration, step ideation,
// 3-way orthogonal critique, and tribunal plan synthesis.
type DeliberateOutput struct {
	Goal           string `json:"goal"`
	ExpectedDetail string `json:"expected_detail"`
	Steps          string `json:"steps"`
	Plan           string `json:"plan"`
}

// DeliberateInput defines the typed argument struct for the built-in "deliberate" tool.
type DeliberateInput struct {
	_           struct{} `doc:"Deliberate on a task before acting: calibrates expected response detail, breaks down initial steps, critiques relevancy/safety/groundedness, and synthesizes a concrete execution plan." emoji:"🧠"`
	Goal        string   `json:"goal" doc:"One-sentence description of the underlying goal and task required to address the user's request."`
	Explanation string   `json:"explanation" doc:"Brief explanation of why deliberation is needed"`
}

// NewDeliberateTool returns the built-in "deliberate" tool implementation that
// uses ToolContext (and native Conversation.Clone when available) to execute
// isolated, tool-free contemplation forks.
func NewDeliberateTool() Tool {
	return NewSessionTool(func(ctx context.Context, tc *ToolContext, in DeliberateInput) (any, error) {
		if tc == nil || tc.Session == nil {
			return nil, errors.New("ai: deliberate requires a valid ToolContext with Session")
		}
		goal := strings.TrimSpace(in.Goal)
		if goal == "" && tc.trace != nil {
			goal = strings.TrimSpace(tc.trace.Goal)
		}
		if goal == "" {
			goal = tc.userText
		}
		return tc.Session.executeDeliberationWithContext(ctx, tc, goal)
	})
}

// streamClonedStage attempts to fork parentConv via native litert_lm_conversation_clone
// and stream trigger through the cloned handle. If parentConv is nil or Clone is
// unsupported by the active backend, it returns (false, "", nil) so the caller
// falls back seamlessly to runEphemeralStage.
func (s *Session) streamClonedStage(
	ctx context.Context,
	parentConv *Conversation,
	trigger []PromptPart,
	stageLabel string,
	streamer *liveStreamer,
) (usedClone bool, out string, err error) {
	if s.ephemeralStream != nil || parentConv == nil {
		return false, "", nil
	}
	tc := &ToolContext{
		Session:      s,
		Engine:       s.engine,
		Conversation: parentConv,
		Feedback:     s.emitFeedback,
	}
	clonedConv, cloneErr := tc.CloneConversation(ctx)
	if cloneErr != nil || clonedConv == nil {
		return false, "", nil
	}
	defer clonedConv.Close()

	streamer.StartStage(stageLabel)
	for event, streamErr := range clonedConv.SendStream(ctx, trigger) {
		if streamErr != nil {
			_ = streamer.EndStage()
			streamer.Wipe()
			return true, "", streamErr
		}
		if e, ok := event.(TextEvent); ok {
			streamer.Feed(e.Text)
		}
	}
	return true, streamer.EndStage(), nil
}

func (s *Session) executeDeliberationWithContext(
	ctx context.Context,
	tc *ToolContext,
	goal string,
) (*DeliberateOutput, error) {
	baseHistory := tc.baseHistory
	if baseHistory == nil {
		snap := s.snapshotCompactedHistory()
		baseHistory = &snap
	}
	userText := tc.userText
	userPart := tc.userPart
	if userPart.Role == "" {
		userPart = PromptPart{Role: RoleUser, Text: userText}
	}
	streamer := tc.streamer
	if streamer == nil {
		streamer = newLiveStreamer(tc.Feedback, defaultStreamerMaxRunes)
	}
	trace := tc.trace
	if trace == nil {
		trace = &CognitiveTrace{}
		tc.trace = trace
	}
	trace.IsDirect = false
	trace.Goal = goal

	// Stage 2: Expectation Calibration (<detail_level>)
	seedExpectation := func(base []PromptPart) []PromptPart {
		seed := make([]PromptPart, 0, len(base)+2)
		seed = append(seed, base...)
		seed = append(seed,
			userPart,
			PromptPart{Role: RoleModel, Text: stageExpectSyntheticWait},
		)
		return seed
	}
	detailLevel, err := s.runEphemeralStage(ctx, baseHistory, seedExpectation, stageExpectTriggerPrompt, s.systemMessage, "expectation", streamer)
	if err != nil {
		return nil, err
	}
	trace.ExpectedDetail = detailLevel

	effectiveGoal := goal
	if detailLevel != "" {
		effectiveGoal = goal + "\nExpected response detail: " + detailLevel
	}

	seedWithGoal := func(base []PromptPart) []PromptPart {
		seed := make([]PromptPart, 0, len(base)+2)
		seed = append(seed, base...)
		seed = append(seed,
			userPart,
			PromptPart{Role: RoleModel, Text: "I need to think first, the goal here is probably: " + effectiveGoal},
		)
		return seed
	}

	// Create a shared Tools: nil contemplation conversation seeded with seedWithGoal
	// so Steps, Relevancy, Safety, and Groundedness can fork from it via
	// litert_lm_conversation_clone (ToolContext.CloneConversation) without re-prefilling
	// the entire conversation history four times.
	var goalPrefixConv *Conversation
	if s.ephemeralStream == nil && s.engine != nil {
		if conv, cerr := s.engine.NewConversationWithConfig(ConversationConfig{
			SystemMessage: s.systemMessageWithToolNames(),
			Tools:         nil,
			History:       seedWithGoal(*baseHistory),
		}); cerr == nil {
			goalPrefixConv = conv
			defer goalPrefixConv.Close()
		}
	}

	// Stage 3: Step Ideation (<steps>)
	var steps string
	if used, clonedOut, cerr := s.streamClonedStage(ctx, goalPrefixConv, []PromptPart{{Role: RoleUser, Text: stage2TriggerPrompt}}, "steps", streamer); used && cerr == nil {
		steps = clonedOut
	} else {
		steps, err = s.runEphemeralStage(ctx, baseHistory, seedWithGoal, stage2TriggerPrompt, s.systemMessage, "steps", streamer)
		if err != nil {
			return nil, err
		}
	}
	trace.InitialSteps = steps

	// Stage 4: The Orthogonal Triple-Fork Critique (<relevancy>, <safety>, <grounded>)
	critiqueSysMsg := s.systemMessageWithToolNames()

	var relevancy string
	if used, clonedOut, cerr := s.streamClonedStage(ctx, goalPrefixConv, []PromptPart{{Role: RoleUser, Text: stage3APrefix + steps}}, "relevancy", streamer); used && cerr == nil {
		relevancy = clonedOut
	} else {
		relevancy, err = s.runEphemeralStage(ctx, baseHistory, seedWithGoal, stage3APrefix+steps, critiqueSysMsg, "relevancy", streamer)
		if err != nil {
			return nil, err
		}
	}
	trace.CritiqueRelevancy = relevancy

	var safety string
	if used, clonedOut, cerr := s.streamClonedStage(ctx, goalPrefixConv, []PromptPart{{Role: RoleUser, Text: stage3BPrefix + steps}}, "safety", streamer); used && cerr == nil {
		safety = clonedOut
	} else {
		safety, err = s.runEphemeralStage(ctx, baseHistory, seedWithGoal, stage3BPrefix+steps, critiqueSysMsg, "safety", streamer)
		if err != nil {
			return nil, err
		}
	}
	trace.CritiqueSafety = safety

	var grounded string
	if used, clonedOut, cerr := s.streamClonedStage(ctx, goalPrefixConv, []PromptPart{{Role: RoleUser, Text: stage3CPrefix + steps}}, "grounded", streamer); used && cerr == nil {
		grounded = clonedOut
	} else {
		grounded, err = s.runEphemeralStage(ctx, baseHistory, seedWithGoal, stage3CPrefix+steps, critiqueSysMsg, "grounded", streamer)
		if err != nil {
			return nil, err
		}
	}
	trace.CritiqueGrounded = grounded

	// Stage 5: The Tribunal Join & Plan Formulation (<plan>)
	seedStage5Plan := func(base []PromptPart) []PromptPart {
		seed := make([]PromptPart, 0, len(base)+4)
		seed = append(seed, base...)
		seed = append(seed,
			userPart,
			PromptPart{Role: RoleModel, Text: "I need to think first, the goal here is probably: " + effectiveGoal},
			PromptPart{Role: RoleUser, Text: "Maybe these steps would get us there?\n" + steps},
			PromptPart{
				Role: RoleModel,
				Text: "I have analysed those proposed steps from three angles:\n- Relevancy: " + relevancy + "\n- Safety & Risks: " + safety + "\n- Information Grounding: " + grounded,
			},
		)
		return seed
	}
	plan, err := s.runEphemeralStage(ctx, baseHistory, seedStage5Plan, stage4TriggerPrompt, s.systemMessage, "plan", streamer)
	if err != nil {
		return nil, err
	}
	trace.SynthesizedPlan = plan

	return &DeliberateOutput{
		Goal:           effectiveGoal,
		ExpectedDetail: detailLevel,
		Steps:          steps,
		Plan:           plan,
	}, nil
}

func (s *Session) streamOnce(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
	if s.ephemeralStream != nil {
		return s.ephemeralStream(ctx, cfg, trigger, onEvent)
	}
	if s.engine == nil {
		return errors.New("ai: engine is not initialized")
	}
	ephemeralConv, err := s.engine.NewConversationWithConfig(cfg)
	if err != nil {
		return err
	}
	defer ephemeralConv.Close()

	for event, err := range ephemeralConv.SendStream(ctx, trigger) {
		if err != nil {
			return err
		}
		if onEvent != nil && !onEvent(event) {
			break
		}
	}
	return nil
}

// runEphemeralStage executes a single pure-text contemplation stage (Tools: nil)
// with overflow/loop compaction retries.
func (s *Session) runEphemeralStage(
	ctx context.Context,
	baseHistory *[]PromptPart,
	buildSeed func(base []PromptPart) []PromptPart,
	triggerText string,
	sysMessage string,
	stageLabel string,
	streamer *liveStreamer,
) (string, error) {
	trigger := []PromptPart{{Role: RoleUser, Text: triggerText}}
	var retries int
	for {
		seed := buildSeed(*baseHistory)
		cfg := ConversationConfig{
			SystemMessage: sysMessage,
			Tools:         nil,
			History:       seed,
		}
		streamer.StartStage(stageLabel)
		err := s.streamOnce(ctx, cfg, trigger, func(event ModelEvent) bool {
			if e, ok := event.(TextEvent); ok {
				streamer.Feed(e.Text)
			}
			return true
		})
		out := streamer.EndStage()
		if err == nil {
			return out, nil
		}
		if errors.Is(err, context.Canceled) {
			streamer.Wipe()
			return "", err
		}
		if (isContextOverflowError(err) || errors.Is(err, ErrRepetitionLoop)) && retries < 2 {
			retries++
			streamer.Wipe()
			if cerr := s.compactForEphemeralRetry(ctx); cerr != nil {
				return "", cerr
			}
			*baseHistory = s.snapshotCompactedHistory()
			continue
		}
		streamer.Wipe()
		return "", err
	}
}

func (s *Session) compactForEphemeralRetry(ctx context.Context) error {
	err := s.compactContextForRetry(ctx, 0, s.emitFeedback)
	s.emitFeedback("\n")
	s.closeConv()
	return err
}

const (
	stage1SyntheticWait      = "Let me consider how to respond..."
	stage1TriggerPrompt      = "If you can answer my request above directly without running tools or multi-step planning, simply write your final answer now, with or without a 'DIRECT:' prefix. ONLY if this request requires tool execution, codebase inspection, or multi-step problem solving, do NOT answer yet—instead, you MUST begin your response with the exact prefix 'GOAL:' followed by a one-sentence description of the goal and task required."
	stageExpectSyntheticWait = "Before I begin working on this, let me check how brief and direct the user expects the reply to be..."
	stageExpectTriggerPrompt = "Look strictly at my prompt above. Default to extreme brevity—the shortest, most direct answer that fully satisfies the request. Unless I explicitly asked for an essay or exhaustive breakdown, prescribe a strict, tiny length limit (e.g., 'Just the name/value in 1-5 words', 'One short sentence under 15 words', or 'At most 2-3 brief sentences'). State ONLY the required format and strict maximum word/sentence cap in one short sentence."
	stage2TriggerPrompt      = "What are the concrete steps you would take to achieve that goal?"
	stage3APrefix            = "I think you might take these steps below, but are these actually relevant to the goal, or do they miss the point or include unnecessary detours?\n"
	stage3BPrefix            = "I think you might take these steps below, but are these safe, or do they carry risks, edge cases, or destructive side effects?\n"
	stage3CPrefix            = "I think you might take these steps below, but are these well-informed, or are they making unverified assumptions where we need to collect more information first?\n"
	stage4TriggerPrompt      = "I see. Given that analysis, what do you propose as the final, concrete execution plan?"
)

// runStage1Triage sends unprefixed and "DIRECT:" replies to the fast path; only
// an explicit "GOAL:" prefix enters the reflective path.
func (s *Session) runStage1Triage(
	ctx context.Context,
	baseHistory *[]PromptPart,
	userPart PromptPart,
	streamer *liveStreamer,
) (isDirect bool, text string, firstOutAt, lastOutAt time.Time, err error) {
	trigger := []PromptPart{{Role: RoleUser, Text: stage1TriggerPrompt}}
	var retries int
	for {
		seed := make([]PromptPart, 0, len(*baseHistory)+2)
		seed = append(seed, *baseHistory...)
		seed = append(seed,
			userPart,
			PromptPart{Role: RoleModel, Text: stage1SyntheticWait},
		)
		cfg := ConversationConfig{
			SystemMessage: s.systemMessage,
			Tools:         nil,
			History:       seed,
		}

		var probeBuf strings.Builder
		var fullDirect strings.Builder
		decided := false
		directMode := false
		var firstProbeAt time.Time
		firstOutAt = time.Time{}
		lastOutAt = time.Time{}

		recordDirectChunk := func(chunk string, startHint time.Time) {
			if chunk == "" {
				return
			}
			now := time.Now()
			if firstOutAt.IsZero() {
				if !startHint.IsZero() {
					firstOutAt = startHint
				} else {
					firstOutAt = now
				}
			}
			lastOutAt = now
			s.emitContent(chunk)
			fullDirect.WriteString(chunk)
		}

		decidePrefix := func(finalFlush bool) {
			if decided {
				return
			}
			raw := probeBuf.String()
			trimmed := strings.TrimLeft(raw, " \t\r\n")
			upper := strings.ToUpper(trimmed)
			if strings.HasPrefix(upper, "GOAL:") {
				decided = true
				rest := strings.TrimLeft(trimmed[len("GOAL:"):], " ")
				streamer.StartStage("goal")
				if rest != "" {
					streamer.Feed(rest)
				}
				return
			}
			if strings.HasPrefix(upper, "DIRECT:") {
				decided = true
				directMode = true
				rest := strings.TrimLeft(trimmed[len("DIRECT:"):], " ")
				streamer.Wipe()
				if rest != "" {
					recordDirectChunk(rest, firstProbeAt)
				}
				return
			}

			stillPossiblePrefix := strings.HasPrefix("GOAL:", upper) || strings.HasPrefix("DIRECT:", upper)
			if !finalFlush && stillPossiblePrefix {
				return
			}

			decided = true
			directMode = true
			streamer.Wipe()
			recordDirectChunk(raw, firstProbeAt)
		}

		err := s.streamOnce(ctx, cfg, trigger, func(event ModelEvent) bool {
			e, ok := event.(TextEvent)
			if !ok {
				return true
			}
			if !decided {
				if e.Text != "" && firstProbeAt.IsZero() {
					firstProbeAt = time.Now()
				}
				probeBuf.WriteString(e.Text)
				decidePrefix(false)
				return true
			}
			if directMode {
				recordDirectChunk(e.Text, time.Time{})
			} else {
				streamer.Feed(e.Text)
			}
			return true
		})
		if err == nil {
			if !decided {
				decidePrefix(true)
			}
			if directMode {
				if firstOutAt.IsZero() {
					now := time.Now()
					firstOutAt = now
					lastOutAt = now
				} else if lastOutAt.IsZero() {
					lastOutAt = time.Now()
				}
				if s.verbose && fullDirect.Len() > 0 {
					s.emitContent("\n")
				}
				return true, strings.TrimSpace(fullDirect.String()), firstOutAt, lastOutAt, nil
			}
			goalText := streamer.EndStage()
			return false, goalText, time.Time{}, time.Time{}, nil
		}

		if errors.Is(err, context.Canceled) {
			streamer.Wipe()
			return false, "", time.Time{}, time.Time{}, err
		}
		if (isContextOverflowError(err) || errors.Is(err, ErrRepetitionLoop)) && retries < 2 {
			retries++
			streamer.Wipe()
			if directMode && fullDirect.Len() > 0 {
				s.emitContent("\n")
			}
			if cerr := s.compactForEphemeralRetry(ctx); cerr != nil {
				return false, "", time.Time{}, time.Time{}, cerr
			}
			*baseHistory = s.snapshotCompactedHistory()
			continue
		}
		streamer.Wipe()
		return false, "", time.Time{}, time.Time{}, err
	}
}

// runStage5Execution runs Stage 5 ("Let It Rip") with tools enabled, executing
// any requested tools in a loop and returning the executed tool parts and draft answer.
func (s *Session) runStage5Execution(
	ctx context.Context,
	baseHistory *[]PromptPart,
	userPart PromptPart,
	goal, plan string,
	streamer *liveStreamer,
) (executedToolParts []PromptPart, draftAnswer string, err error) {
	userText := userPart.Text
	var retries int

	for {
		seed := make([]PromptPart, 0, len(*baseHistory)+2)
		seed = append(seed, *baseHistory...)
		seed = append(seed,
			userPart,
			PromptPart{
				Role: RoleModel,
				Text: "I need to think first. The goal here is: " + goal + "\nMy execution plan is:\n" + plan,
			},
		)

		turnParts := []PromptPart{{
			Role: RoleUser,
			Text: "I see what you mean. Please proceed with that plan now, using any necessary tools, and keep in mind the original request—answer it completely after your work is done:\n" + userText,
		}}

		executedToolParts = nil

		if s.ephemeralStream != nil {
			historyCursor := append([]PromptPart(nil), seed...)
			var stageErr error
			for {
				var toolCalls []ToolCall
				var generatedText strings.Builder
				var genStart, genEnd time.Time
				cfg := ConversationConfig{
					SystemMessage: s.systemMessage,
					Tools:         s.tools,
					History:       historyCursor,
				}
				streamer.StartStage("work")
				stageErr = s.ephemeralStream(ctx, cfg, turnParts, func(event ModelEvent) bool {
					now := time.Now()
					if genStart.IsZero() {
						genStart = now
					}
					genEnd = now
					switch e := event.(type) {
					case TextEvent:
						streamer.Feed(e.Text)
						generatedText.WriteString(e.Text)
					case ToolCallEvent:
						toolCalls = append(toolCalls, e.ToolCalls...)
					}
					return true
				})
				_ = streamer.EndStage()
				if stageErr != nil {
					break
				}
				historyCursor = append(historyCursor, turnParts...)
				if len(toolCalls) == 0 {
					return executedToolParts, strings.TrimSpace(generatedText.String()), nil
				}
				streamer.Wipe()
				if genStart.IsZero() {
					genStart = time.Now()
					genEnd = genStart
				}
				modelTurn := PromptPart{
					Role:        RoleModel,
					Text:        generatedText.String(),
					ToolCalls:   toolCalls,
					StartedAt:   genStart,
					CompletedAt: genEnd,
					Duration:    genEnd.Sub(genStart),
				}
				executedToolParts = append(executedToolParts, modelTurn)
				historyCursor = append(historyCursor, modelTurn)

				var toolResponses []PromptPart
				sessionMutated := false
				for _, call := range toolCalls {
					explanation := extractToolExplanation(call)
					tc := &ToolContext{
						Session:     s,
						Engine:      s.engine,
						Feedback:    s.emitFeedback,
						baseHistory: baseHistory,
						userText:    userText,
						userPart:    userPart,
						streamer:    streamer,
					}
					toolStart := time.Now()
					result := s.executeTool(ctx, tc, call)
					toolEnd := time.Now()
					if tc.sessionSwapped {
						sessionMutated = true
					}
					s.emitFeedback("\n")
					toolResponses = append(toolResponses, PromptPart{
						Role:         RoleTool,
						ToolName:     call.Name,
						ToolCallID:   call.ID,
						Explanation:  explanation,
						ToolResponse: result,
						StartedAt:    toolStart,
						CompletedAt:  toolEnd,
						Duration:     toolEnd.Sub(toolStart),
					})
				}
				executedToolParts = append(executedToolParts, toolResponses...)
				if sessionMutated {
					rebuilt := make([]PromptPart, 0, len(*baseHistory)+2+1+len(executedToolParts)-len(toolResponses))
					rebuilt = append(rebuilt, *baseHistory...)
					rebuilt = append(rebuilt,
						userPart,
						PromptPart{
							Role: RoleModel,
							Text: "I need to think first. The goal here is: " + goal + "\nMy execution plan is:\n" + plan,
						},
						PromptPart{
							Role: RoleUser,
							Text: "I see what you mean. Please proceed with that plan now, using any necessary tools, and keep in mind the original request—answer it completely after your work is done:\n" + userText,
						},
					)
					rebuilt = append(rebuilt, executedToolParts[:len(executedToolParts)-len(toolResponses)]...)
					historyCursor = rebuilt
				}
				turnParts = toolResponses
			}
			if errors.Is(stageErr, context.Canceled) {
				streamer.Wipe()
				return nil, "", stageErr
			}
			if (isContextOverflowError(stageErr) || errors.Is(stageErr, ErrRepetitionLoop)) && retries < 2 {
				retries++
				streamer.Wipe()
				if cerr := s.compactForEphemeralRetry(ctx); cerr != nil {
					return nil, "", cerr
				}
				*baseHistory = s.snapshotCompactedHistory()
				continue
			}
			streamer.Wipe()
			return nil, "", stageErr
		}

		if s.engine == nil {
			streamer.Wipe()
			return nil, "", errors.New("ai: engine is not initialized")
		}
		cfg := ConversationConfig{
			SystemMessage: s.systemMessage,
			Tools:         s.tools,
			History:       seed,
		}
		ephemeralConv, createErr := s.engine.NewConversationWithConfig(cfg)
		if createErr != nil {
			streamer.Wipe()
			return nil, "", createErr
		}

		var stageErr error
		for {
			var toolCalls []ToolCall
			var generatedText strings.Builder
			var genStart, genEnd time.Time
			streamer.StartStage("work")
			for event, err := range ephemeralConv.SendStream(ctx, turnParts) {
				if err != nil {
					stageErr = err
					break
				}
				now := time.Now()
				if genStart.IsZero() {
					genStart = now
				}
				genEnd = now
				switch e := event.(type) {
				case TextEvent:
					streamer.Feed(e.Text)
					generatedText.WriteString(e.Text)
				case ToolCallEvent:
					toolCalls = append(toolCalls, e.ToolCalls...)
				}
			}
			_ = streamer.EndStage()
			if stageErr != nil {
				break
			}
			if len(toolCalls) == 0 {
				ephemeralConv.Close()
				return executedToolParts, strings.TrimSpace(generatedText.String()), nil
			}

			streamer.Wipe()
			if genStart.IsZero() {
				genStart = time.Now()
				genEnd = genStart
			}
			modelTurn := PromptPart{
				Role:        RoleModel,
				Text:        generatedText.String(),
				ToolCalls:   toolCalls,
				StartedAt:   genStart,
				CompletedAt: genEnd,
				Duration:    genEnd.Sub(genStart),
			}
			executedToolParts = append(executedToolParts, modelTurn)

			var toolResponses []PromptPart
			sessionMutated := false
			for _, call := range toolCalls {
				explanation := extractToolExplanation(call)
				tc := &ToolContext{
					Session:      s,
					Engine:       s.engine,
					Conversation: ephemeralConv,
					Feedback:     s.emitFeedback,
					baseHistory:  baseHistory,
					userText:     userText,
					userPart:     userPart,
					streamer:     streamer,
				}
				toolStart := time.Now()
				result := s.executeTool(ctx, tc, call)
				toolEnd := time.Now()
				if tc.sessionSwapped {
					sessionMutated = true
				}
				s.emitFeedback("\n")
				toolResponses = append(toolResponses, PromptPart{
					Role:         RoleTool,
					ToolName:     call.Name,
					ToolCallID:   call.ID,
					Explanation:  explanation,
					ToolResponse: result,
					StartedAt:    toolStart,
					CompletedAt:  toolEnd,
					Duration:     toolEnd.Sub(toolStart),
				})
			}
			executedToolParts = append(executedToolParts, toolResponses...)
			if sessionMutated {
				rebuilt := make([]PromptPart, 0, len(*baseHistory)+3+len(executedToolParts)-len(toolResponses))
				rebuilt = append(rebuilt, *baseHistory...)
				rebuilt = append(rebuilt,
					userPart,
					PromptPart{
						Role: RoleModel,
						Text: "I need to think first. The goal here is: " + goal + "\nMy execution plan is:\n" + plan,
					},
					PromptPart{
						Role: RoleUser,
						Text: "I see what you mean. Please proceed with that plan now, using any necessary tools, and keep in mind the original request—answer it completely after your work is done:\n" + userText,
					},
				)
				rebuilt = append(rebuilt, executedToolParts[:len(executedToolParts)-len(toolResponses)]...)
				newConv, cerr := s.engine.NewConversationWithConfig(ConversationConfig{
					SystemMessage: s.systemMessage,
					Tools:         s.tools,
					History:       rebuilt,
				})
				if cerr == nil {
					ephemeralConv.Close()
					ephemeralConv = newConv
				}
			}
			turnParts = toolResponses
		}
		ephemeralConv.Close()

		if errors.Is(stageErr, context.Canceled) {
			streamer.Wipe()
			return nil, "", stageErr
		}
		if (isContextOverflowError(stageErr) || errors.Is(stageErr, ErrRepetitionLoop)) && retries < 2 {
			retries++
			streamer.Wipe()
			if cerr := s.compactForEphemeralRetry(ctx); cerr != nil {
				return nil, "", cerr
			}
			*baseHistory = s.snapshotCompactedHistory()
			continue
		}
		streamer.Wipe()
		return nil, "", stageErr
	}
}

// runStageConvergence replants workAndAnswer as a synthetic RoleModel turn
// (omitting intermediate planning and raw tool turns) and asks the model to
// reformulate the final response to strictly fit detailLevel. Right before the
// first token arrives, streamer.Wipe() clears the [work] ticker line and tokens
// stream through the permanent-content callback.
func (s *Session) runStageConvergence(
	ctx context.Context,
	baseHistory *[]PromptPart,
	userPart PromptPart,
	detailLevel, workAndAnswer string,
	streamer *liveStreamer,
) (finalText string, firstOutAt, lastOutAt time.Time, err error) {
	userText := userPart.Text
	trigger := []PromptPart{{
		Role: RoleUser,
		Text: "That draft is far too wordy. Look at the strict target length and format: " + detailLevel + "\nRewrite your answer now to strictly obey that limit using the minimum words possible. Cut all prefaces, roleplay, meta-commentary, internal phases, failed tool mentions, bullet lists, and extra examples unless explicitly requested. Output ONLY the final distilled answer to: " + userText,
	}}

	var retries int
	for {
		seed := make([]PromptPart, 0, len(*baseHistory)+2)
		seed = append(seed, *baseHistory...)
		seed = append(seed,
			userPart,
			PromptPart{Role: RoleModel, Text: workAndAnswer},
		)

		cfg := ConversationConfig{
			SystemMessage: s.systemMessage,
			Tools:         nil,
			History:       seed,
		}

		var finalBuf strings.Builder
		wiped := false
		firstOutAt = time.Time{}
		lastOutAt = time.Time{}

		err := s.streamOnce(ctx, cfg, trigger, func(event ModelEvent) bool {
			e, ok := event.(TextEvent)
			if !ok || e.Text == "" {
				return true
			}
			if !wiped {
				streamer.Wipe()
				wiped = true
			}
			now := time.Now()
			if firstOutAt.IsZero() {
				firstOutAt = now
			}
			lastOutAt = now
			s.emitContent(e.Text)
			finalBuf.WriteString(e.Text)
			return true
		})

		if err == nil {
			if !wiped {
				streamer.Wipe()
			}
			finalText := strings.TrimSpace(finalBuf.String())
			if finalText == "" && workAndAnswer != "" {
				now := time.Now()
				firstOutAt = now
				s.emitContent(workAndAnswer)
				lastOutAt = time.Now()
				finalText = workAndAnswer
			}
			if firstOutAt.IsZero() {
				now := time.Now()
				firstOutAt = now
				lastOutAt = now
			} else if lastOutAt.IsZero() {
				lastOutAt = time.Now()
			}
			if s.verbose && len(finalText) > 0 {
				s.emitContent("\n")
			}
			return finalText, firstOutAt, lastOutAt, nil
		}

		if errors.Is(err, context.Canceled) {
			streamer.Wipe()
			return "", time.Time{}, time.Time{}, err
		}
		if (isContextOverflowError(err) || errors.Is(err, ErrRepetitionLoop)) && retries < 2 {
			retries++
			streamer.Wipe()
			if wiped && finalBuf.Len() > 0 {
				s.emitContent("\n")
			}
			if cerr := s.compactForEphemeralRetry(ctx); cerr != nil {
				return "", time.Time{}, time.Time{}, cerr
			}
			*baseHistory = s.snapshotCompactedHistory()
			continue
		}
		streamer.Wipe()
		return "", time.Time{}, time.Time{}, err
	}
}

func (s *Session) RunTurn(ctx context.Context, userText string) error {
	return s.RunTurnWithTiming(ctx, userText, TurnTimingFromContext(ctx))
}

func (s *Session) RunTurnWithTiming(ctx context.Context, userText string, timing TurnTiming) error {
	s.turnWaiting.Store(true)
	s.turnGate.Lock()
	s.turnWaiting.Store(false)
	defer func() {
		s.turnGate.Unlock()
		s.triggerBackgroundEmbedding()
	}()

	submittedAt := timing.SubmittedAt
	if submittedAt.IsZero() {
		submittedAt = time.Now()
	}
	typedAt := timing.TypedAt
	if typedAt.IsZero() || typedAt.After(submittedAt) {
		typedAt = submittedAt
	}
	userPart := PromptPart{
		Role:        RoleUser,
		Text:        userText,
		StartedAt:   typedAt,
		CompletedAt: submittedAt,
		Duration:    submittedAt.Sub(typedAt),
	}

	// Release any idle persistent conversation before opening ephemeral stage forks.
	s.closeConv()

	baseHistory := s.snapshotCompactedHistory()
	streamer := newLiveStreamer(s.emitFeedback, defaultStreamerMaxRunes)

	restorePersistentConv := func() {
		s.closeConv()
		_ = s.Init()
	}

	// Stage 1: Triage & Goal Extraction ("DIRECT:" vs. "GOAL:")
	isDirect, stage1Text, firstOutAt, lastOutAt, err := s.runStage1Triage(ctx, &baseHistory, userPart, streamer)
	if err != nil {
		restorePersistentConv()
		return err
	}

	var executedToolParts []PromptPart
	var finalAnswer string
	trace := &CognitiveTrace{
		IsDirect: isDirect,
	}

	if isDirect {
		finalAnswer = stage1Text
	} else {
		goal := stage1Text
		goalJSON, _ := json.Marshal(struct {
			Goal        string `json:"goal"`
			Explanation string `json:"explanation"`
		}{
			Goal:        goal,
			Explanation: "Deliberate on the goal, expected response detail, steps, critiques, and execution plan.",
		})

		tc := &ToolContext{
			Session:      s,
			Engine:       s.engine,
			Conversation: s.conv,
			Feedback:     s.emitFeedback,
			baseHistory:  &baseHistory,
			userText:     userText,
			userPart:     userPart,
			streamer:     streamer,
			trace:        trace,
		}

		delibTool := s.toolRegistry[DeliberateToolName]
		delibRes, err := delibTool.ExecuteWithContext(ctx, tc, ToolCall{
			Name:         DeliberateToolName,
			RawArguments: goalJSON,
		})
		if err != nil {
			restorePersistentConv()
			return err
		}
		delibOut, _ := delibRes.(*DeliberateOutput)
		var effectiveGoal, detailLevel, plan string
		if delibOut != nil {
			effectiveGoal = delibOut.Goal
			detailLevel = delibOut.ExpectedDetail
			plan = delibOut.Plan
		}

		// Stage 6: Execution ("Let It Rip" — <work_and_answer>)
		toolParts, workAndAnswer, err := s.runStage5Execution(ctx, &baseHistory, userPart, effectiveGoal, plan, streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		executedToolParts = toolParts
		trace.RawWorkAndAnswer = workAndAnswer
		trace.DraftIterations = append(trace.DraftIterations, workAndAnswer)

		// Stage 7: Convergence & Re-Formulation Flip
		var convFirstAt, convLastAt time.Time
		convergedAnswer, convFirstAt, convLastAt, err := s.runStageConvergence(ctx, &baseHistory, userPart, detailLevel, workAndAnswer, streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		finalAnswer = convergedAnswer
		firstOutAt = convFirstAt
		lastOutAt = convLastAt
	}

	if firstOutAt.IsZero() {
		now := time.Now()
		firstOutAt = now
		lastOutAt = now
	} else if lastOutAt.Before(firstOutAt) {
		lastOutAt = firstOutAt
	}

	// Stage 7: Replanting the Iceberg & Syncing Session State
	spokenParts := make([]PromptPart, 0, 2+len(executedToolParts))
	spokenParts = append(spokenParts, userPart)
	spokenParts = append(spokenParts, executedToolParts...)
	spokenParts = append(spokenParts, PromptPart{
		Role:           RoleModel,
		Text:           finalAnswer,
		CognitiveTrace: trace,
		StartedAt:      firstOutAt,
		CompletedAt:    lastOutAt,
		Duration:       lastOutAt.Sub(firstOutAt),
	})
	s.appendHistory(spokenParts...)

	if err := s.initWithHistory(s.snapshotCompactedHistory()); err != nil {
		return err
	}

	// Check if context compaction is needed
	if s.totalTokensEst > s.compactionLimit && len(s.CompactedHistory) > 4 {
		_ = s.compactContext(ctx, s.emitFeedback)
		s.emitFeedback("\n")
	}

	return nil
}

func historySize(history []PromptPart) int {
	var total int
	for _, p := range history {
		total += len(p.Text)
		if len(p.ToolCalls) > 0 {
			if b, err := json.Marshal(p.ToolCalls); err == nil {
				total += len(b)
			}
		}
		if p.ToolResponse != nil {
			if b, err := json.Marshal(p.ToolResponse); err == nil {
				total += len(b)
			}
		}
	}
	return total
}

func (s *Session) compactContext(ctx context.Context, feedback LogFeedback) error {
	return s.compactContextForRetry(ctx, 0, feedback)
}

const (
	defaultActiveTailUnits = 8
	topicDecayFactor       = 0.85
)

// atomicTurnUnit represents an indivisible unit of conversation history:
// 1. A standalone RoleUser turn
// 2. A standalone RoleModel turn without ToolCalls
// 3. A RoleModel turn with ToolCalls plus all immediately following RoleTool responses
type atomicTurnUnit struct {
	parts     []PromptPart
	embedding []float32
}

// groupIntoAtomicUnits partitions a slice of PromptParts into indivisible atomicTurnUnits.
func groupIntoAtomicUnits(history []PromptPart) []atomicTurnUnit {
	var units []atomicTurnUnit
	for i := 0; i < len(history); i++ {
		p := history[i]
		if p.Role == RoleModel && len(p.ToolCalls) > 0 {
			end := i + 1
			for end < len(history) && history[end].Role == RoleTool {
				end++
			}
			slice := append([]PromptPart(nil), history[i:end]...)
			units = append(units, atomicTurnUnit{
				parts:     slice,
				embedding: computeUnitEmbedding(slice),
			})
			i = end - 1
			continue
		}
		slice := []PromptPart{p}
		units = append(units, atomicTurnUnit{
			parts:     slice,
			embedding: computeUnitEmbedding(slice),
		})
	}
	return units
}

// computeUnitEmbedding returns the L2-normalized sum of constituent part embeddings.
func computeUnitEmbedding(parts []PromptPart) []float32 {
	var dim int
	for _, p := range parts {
		if len(p.Embedding) > 0 {
			dim = len(p.Embedding)
			break
		}
	}
	if dim == 0 {
		return nil
	}
	sum := make([]float32, dim)
	for _, p := range parts {
		if len(p.Embedding) != dim {
			continue
		}
		for i, v := range p.Embedding {
			sum[i] += v
		}
	}
	return l2Normalize(sum)
}

func l2Normalize(vec []float32) []float32 {
	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	if sumSq == 0 {
		return nil
	}
	invNorm := float32(1.0 / math.Sqrt(sumSq))
	out := make([]float32, len(vec))
	for i, v := range vec {
		out[i] = v * invNorm
	}
	return out
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return -1
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

// computeTopicAndBoundary computes the exponentially-weighted topic vector V_topic
// over the last X units and calibrates T_boundary as the minimum similarity over the last X/2 units.
func computeTopicAndBoundary(units []atomicTurnUnit, x int) (topic []float32, boundary float32, ok bool) {
	n := len(units)
	if n == 0 {
		return nil, 0, false
	}
	if x > n {
		x = n
	}
	var dim int
	for i := n - x; i < n; i++ {
		if len(units[i].embedding) > 0 {
			dim = len(units[i].embedding)
			break
		}
	}
	if dim == 0 {
		return nil, 0, false
	}

	weighted := make([]float32, dim)
	weight := float32(1.0)
	hasAny := false
	for i := 0; i < x; i++ {
		u := units[n-1-i]
		if len(u.embedding) == dim {
			hasAny = true
			for d := 0; d < dim; d++ {
				weighted[d] += weight * u.embedding[d]
			}
		}
		weight *= float32(topicDecayFactor)
	}
	if !hasAny {
		return nil, 0, false
	}
	topic = l2Normalize(weighted)
	if len(topic) == 0 {
		return nil, 0, false
	}

	calibCount := x / 2
	if calibCount < 1 {
		calibCount = 1
	}
	if calibCount > n {
		calibCount = n
	}
	boundary = 2.0
	for i := n - calibCount; i < n; i++ {
		if len(units[i].embedding) == dim {
			sim := cosineSimilarity(units[i].embedding, topic)
			if sim < boundary {
				boundary = sim
			}
		}
	}
	if boundary > 1.0 {
		return nil, 0, false
	}
	return topic, boundary, true
}

// selectPinnedAnchors scans the discard/search zone (units[:searchEnd]), filters
// for units with similarity >= boundary, and returns a boolean set of the top K = max(1, x/4) unit indices.
func selectPinnedAnchors(units []atomicTurnUnit, searchEnd int, topic []float32, boundary float32, x int) map[int]bool {
	pinned := make(map[int]bool)
	if searchEnd <= 0 || len(topic) == 0 {
		return pinned
	}
	k := x / 4
	if k < 1 {
		k = 1
	}
	type candidate struct {
		idx int
		sim float32
	}
	var candidates []candidate
	for i := 0; i < searchEnd; i++ {
		if len(units[i].embedding) != len(topic) {
			continue
		}
		sim := cosineSimilarity(units[i].embedding, topic)
		if sim >= boundary {
			candidates = append(candidates, candidate{idx: i, sim: sim})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].sim > candidates[j].sim
	})
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	for _, c := range candidates {
		pinned[c.idx] = true
	}
	return pinned
}

// serializeGapUnits formats all parts in a gap into a plain text dialogue block
// without algorithmic string truncation, replacing ImageBase64 with a textual descriptor.
func serializeGapUnits(units []atomicTurnUnit) string {
	var sb strings.Builder
	for _, u := range units {
		for _, p := range u.parts {
			sb.WriteString(string(p.Role))
			sb.WriteString(": ")
			if p.ImageBase64 != "" {
				sb.WriteString("[image: user attached image] ")
			}
			if p.Text != "" {
				sb.WriteString(p.Text)
			}
			if len(p.ToolCalls) > 0 {
				for _, tc := range p.ToolCalls {
					args := string(tc.RawArguments)
					if args == "" && len(tc.Arguments) > 0 {
						if b, err := json.Marshal(tc.Arguments); err == nil {
							args = string(b)
						}
					}
					sb.WriteString(fmt.Sprintf(" [call %s(%s)]", tc.Name, args))
				}
			}
			if p.ToolResponse != nil {
				if b, err := json.Marshal(p.ToolResponse); err == nil {
					sb.WriteString(fmt.Sprintf(" [tool %s -> %s]", p.ToolName, string(b)))
				}
			}
			sb.WriteByte('\n')
		}
	}
	return strings.TrimSpace(sb.String())
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}

func truncateToWords(s string, maxWords int) string {
	fields := strings.Fields(s)
	if len(fields) <= maxWords || maxWords <= 0 {
		return strings.TrimSpace(s)
	}
	return strings.Join(fields[:maxWords], " ")
}

func (s *Session) runCompressorPrompt(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if s.compressor != nil {
		return s.compressor(ctx, systemPrompt, userPrompt)
	}
	if s.engine == nil {
		return "", errors.New("no engine configured for summarization")
	}
	summaryConv, err := s.engine.NewConversationWithConfig(ConversationConfig{
		SystemMessage: systemPrompt,
	})
	if err != nil {
		return "", err
	}
	defer summaryConv.Close()

	var summary strings.Builder
	for event, err := range summaryConv.SendStream(ctx, []PromptPart{{Role: RoleUser, Text: userPrompt}}) {
		if err != nil {
			return strings.TrimSpace(summary.String()), err
		}
		if e, ok := event.(TextEvent); ok {
			summary.WriteString(e.Text)
		}
	}
	return strings.TrimSpace(summary.String()), nil
}

// compressTextBlock enforces the word ceiling (40% of input words, min 15) with up to 2 retries
// stepping down by 0.75x on budget overrun, and a deterministic word-truncation escape hatch.
func (s *Session) compressTextBlock(ctx context.Context, gapText string) string {
	gapText = strings.TrimSpace(gapText)
	if gapText == "" {
		return ""
	}
	wIn := wordCount(gapText)
	if wIn == 0 {
		return ""
	}
	wTarget := int(float64(wIn) * 0.40)
	if wTarget < 15 {
		wTarget = 15
	}

	var shortest string
	for attempt := 0; attempt <= 2; attempt++ {
		sysPrompt := fmt.Sprintf(
			"You are a conversation compressor. Output ONLY a terse factual record of actions, decisions, and tool results from the dialogue below. Maximum %d words. NO commentary, NO intros.",
			wTarget,
		)
		out, err := s.runCompressorPrompt(ctx, sysPrompt, gapText)
		out = strings.TrimSpace(out)
		if err == nil && out != "" {
			wOut := wordCount(out)
			if shortest == "" || wOut < wordCount(shortest) {
				shortest = out
			}
			if wOut < wIn && wOut <= wTarget {
				return out
			}
		}
		wTarget = int(float64(wTarget) * 0.75)
		if wTarget < 10 {
			wTarget = 10
		}
	}

	if shortest != "" {
		return truncateToWords(shortest, wTarget)
	}
	return truncateToWords(gapText, wTarget)
}

func (s *Session) gapTokenBudget() int {
	if s.compactionLimit > 0 {
		return s.compactionLimit / 2
	}
	return 2048
}

func extractToolExplanation(call ToolCall) string {
	if call.Arguments != nil {
		if exp, ok := call.Arguments["explanation"].(string); ok && strings.TrimSpace(exp) != "" {
			return strings.TrimSpace(exp)
		}
	}
	if len(call.RawArguments) > 0 {
		var m map[string]any
		if json.Unmarshal(call.RawArguments, &m) == nil {
			if exp, ok := m["explanation"].(string); ok && strings.TrimSpace(exp) != "" {
				return strings.TrimSpace(exp)
			}
		}
	}
	return ""
}

func (s *Session) largeBlockByteThreshold() int {
	// 1/5 of gapTokenBudget() in tokens (~4 bytes per token).
	thresh := (s.gapTokenBudget() * 4) / 5
	if thresh < 512 {
		thresh = 512
	}
	return thresh
}

func artifactCacheKey(role Role, toolName, explanation, rawContent string) string {
	sum := sha256.Sum256([]byte(string(role) + "\x00" + toolName + "\x00" + explanation + "\x00" + rawContent))
	return hex.EncodeToString(sum[:])
}

// compressLargeBlock collapses a single large unpinned block into a <=20-word gist
// guided by explanation, using artifactCache to avoid redundant LLM calls across turns.
func (s *Session) compressLargeBlock(ctx context.Context, role Role, toolName, explanation, rawContent string) string {
	if s.artifactCache == nil {
		s.artifactCache = make(map[string]string)
	}
	key := artifactCacheKey(role, toolName, explanation, rawContent)
	if cached, ok := s.artifactCache[key]; ok {
		return cached
	}

	intent := strings.TrimSpace(explanation)
	if intent == "" {
		if toolName != "" {
			intent = "tool execution of " + toolName
		} else {
			intent = "key facts and decisions"
		}
	}

	inputText := strings.TrimSpace(rawContent)
	budgetBytes := s.gapTokenBudget() * 4
	if budgetBytes <= 0 {
		budgetBytes = 8192
	}
	if len(inputText) > budgetBytes {
		inputText = s.summarizeOversizedText(ctx, inputText, s.gapTokenBudget())
	}

	wTarget := 20
	var shortest string
	for attempt := 0; attempt <= 2; attempt++ {
		sysPrompt := fmt.Sprintf(
			"Extract only the factual findings relevant to: %s. Maximum %d words. No intro.",
			intent, wTarget,
		)
		out, err := s.runCompressorPrompt(ctx, sysPrompt, inputText)
		out = strings.TrimSpace(out)
		if err == nil && out != "" {
			wOut := wordCount(out)
			if shortest == "" || wOut < wordCount(shortest) {
				shortest = out
			}
			if wOut <= wTarget {
				s.artifactCache[key] = "COMPACTED: " + out
				return s.artifactCache[key]
			}
		}
		wTarget = int(float64(wTarget) * 0.75)
		if wTarget < 8 {
			wTarget = 8
		}
	}

	var gist string
	if shortest != "" {
		gist = truncateToWords(shortest, 20)
	} else {
		gist = truncateToWords(inputText, 20)
	}
	res := "COMPACTED: " + gist
	s.artifactCache[key] = res
	return res
}

// collapseUnpinnedLargeBlocks is the pre-main-compaction pass: for all unpinned units
// prior to tailStart, any large tool response or large text block is collapsed into its
// cached `COMPACTED: ...` gist before main gap summarization runs.
func (s *Session) collapseUnpinnedLargeBlocks(ctx context.Context, units []atomicTurnUnit, tailStart int, pinned map[int]bool) {
	thresh := s.largeBlockByteThreshold()
	for i := 0; i < tailStart; i++ {
		if pinned[i] {
			continue
		}
		var callExplanations []string
		for _, p := range units[i].parts {
			for _, tc := range p.ToolCalls {
				callExplanations = append(callExplanations, extractToolExplanation(tc))
			}
		}
		expIdx := 0
		parts := make([]PromptPart, len(units[i].parts))
		copy(parts, units[i].parts)
		for j := range parts {
			p := &parts[j]
			if p.Role == RoleTool {
				exp := p.Explanation
				if exp == "" && expIdx < len(callExplanations) {
					exp = callExplanations[expIdx]
				}
				expIdx++
				if p.ToolResponse != nil {
					if b, err := json.Marshal(p.ToolResponse); err == nil && len(b) > thresh {
						compacted := s.compressLargeBlock(ctx, p.Role, p.ToolName, exp, string(b))
						p.ToolResponse = map[string]any{"output": compacted}
					}
				} else if len(p.Text) > thresh {
					p.Text = s.compressLargeBlock(ctx, p.Role, p.ToolName, exp, p.Text)
				}
			} else if len(p.Text) > thresh {
				p.Text = s.compressLargeBlock(ctx, p.Role, "", p.Explanation, p.Text)
			}
		}
		units[i].parts = parts
	}
}

// summarizeGap recursively halves gaps that exceed the context token budget,
// respecting atomic unit boundaries when len(units) > 1 and splitting text in half when len(units) == 1.
func (s *Session) summarizeGap(ctx context.Context, units []atomicTurnUnit) string {
	if len(units) == 0 {
		return ""
	}
	budget := s.gapTokenBudget()
	gapText := serializeGapUnits(units)
	if gapText == "" {
		return ""
	}
	estTokens := len(gapText) / 4
	if estTokens > budget {
		if len(units) > 1 {
			mid := len(units) / 2
			left := s.summarizeGap(ctx, units[:mid])
			right := s.summarizeGap(ctx, units[mid:])
			return strings.TrimSpace(left + " " + right)
		}
		return s.summarizeOversizedText(ctx, gapText, budget)
	}
	return s.compressTextBlock(ctx, gapText)
}

func (s *Session) summarizeOversizedText(ctx context.Context, text string, budget int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text)/4 <= budget || len(text) <= 64 {
		return s.compressTextBlock(ctx, text)
	}
	mid := len(text) / 2
	for mid < len(text) && text[mid] != ' ' && text[mid] != '\n' {
		mid++
	}
	if mid >= len(text) {
		mid = len(text) / 2
	}
	left := s.summarizeOversizedText(ctx, text[:mid], budget)
	right := s.summarizeOversizedText(ctx, text[mid:], budget)
	return strings.TrimSpace(left + " " + right)
}

// projectHistory builds the RAG-projected CompactedHistory from CanonicalHistory units.
func (s *Session) projectHistory(ctx context.Context, prior []PromptPart) []PromptPart {
	units := groupIntoAtomicUnits(prior)
	n := len(units)
	if n == 0 {
		return nil
	}

	x := defaultActiveTailUnits
	if x >= n {
		// If history has fewer than defaultActiveTailUnits units, still allow compaction
		// when there are > 2 units or oversized payloads.
		if n <= 2 {
			if historySize(prior) <= 4096 {
				return prior
			}
			s.collapseUnpinnedLargeBlocks(ctx, units, n, nil)
			sumText := s.summarizeGap(ctx, units)
			if sumText == "" {
				return prior
			}
			return []PromptPart{{Role: RoleUser, Text: "[Context summary]: " + sumText}}
		}
		x = n - 2
		if x < 2 {
			x = 2
		}
	}

	// Ensure the active tail fits within half the compaction byte budget (~2 bytes/token)
	// and starts on a RoleUser boundary so seeded history is always grammatically valid.
	tailStart := n - x
	maxTailBytes := s.compactionLimit * 2
	if maxTailBytes <= 0 {
		maxTailBytes = 8192
	}
	for tailStart < n && units[tailStart].parts[0].Role != RoleUser {
		tailStart++
	}
	for tailStart < n {
		var tailParts []PromptPart
		for i := tailStart; i < n; i++ {
			tailParts = append(tailParts, units[i].parts...)
		}
		if historySize(tailParts) <= maxTailBytes {
			break
		}
		tailStart++
		for tailStart < n && units[tailStart].parts[0].Role != RoleUser {
			tailStart++
		}
	}

	effectiveX := n - tailStart
	if effectiveX < 2 {
		effectiveX = 2
	}
	topic, boundary, hasEmbeddings := computeTopicAndBoundary(units, effectiveX)
	var pinned map[int]bool
	if hasEmbeddings {
		pinned = selectPinnedAnchors(units, tailStart, topic, boundary, effectiveX)
	}

	// Pre-main-compaction step: collapse all large blocks outside of pinned units
	// into their cached `COMPACTED: ...` gist guided by tool explanation.
	s.collapseUnpinnedLargeBlocks(ctx, units, tailStart, pinned)

	var projected []PromptPart
	var currentGap []atomicTurnUnit

	flushGap := func() {
		if len(currentGap) == 0 {
			return
		}
		if sumText := s.summarizeGap(ctx, currentGap); sumText != "" {
			projected = append(projected, PromptPart{
				Role: RoleUser,
				Text: "[Context summary]: " + sumText,
			})
		}
		currentGap = nil
	}

	for i := 0; i < tailStart; i++ {
		if pinned[i] {
			flushGap()
			projected = append(projected, units[i].parts...)
		} else {
			currentGap = append(currentGap, units[i])
		}
	}
	flushGap()

	for i := tailStart; i < n; i++ {
		projected = append(projected, units[i].parts...)
	}
	return projected
}

func (s *Session) compactContextForRetry(ctx context.Context, pendingParts int, feedback LogFeedback) error {
	// Step 1: Synchronously flush any missing embeddings in CanonicalHistory.
	s.flushEmbeddingsInline()

	before := historySize(s.CompactedHistory)
	if feedback != nil {
		feedback("🗜️ ")
	}

	// Project directly from CanonicalHistory so previously compacted turns can resurrect
	// when the conversation topic returns to them.
	s.historyMu.Lock()
	source := append([]PromptPart(nil), s.CanonicalHistory...)
	if len(source) == 0 && len(s.CompactedHistory) > 0 {
		source = append([]PromptPart(nil), s.CompactedHistory...)
	}
	s.historyMu.Unlock()

	var pending []PromptPart
	prior := source
	if pendingParts > 0 && len(prior) >= pendingParts {
		split := len(prior) - pendingParts
		pending = append([]PromptPart(nil), prior[split:]...)
		prior = prior[:split]
	}

	if len(prior) > 0 {
		prior = s.projectHistory(ctx, prior)
	}

	// If a pending tool output itself exceeds budget on retry, collapse it into a COMPACTED: gist.
	for i := range pending {
		if pending[i].Role == RoleTool && pending[i].ToolResponse != nil {
			if b, err := json.Marshal(pending[i].ToolResponse); err == nil && len(b) > 8192 {
				compacted := s.compressLargeBlock(ctx, pending[i].Role, pending[i].ToolName, pending[i].Explanation, string(b))
				pending[i].ToolResponse = map[string]any{"output": compacted}
			}
		}
	}

	s.historyMu.Lock()
	s.CompactedHistory = append(append([]PromptPart(nil), prior...), pending...)
	after := historySize(s.CompactedHistory)
	s.historyMu.Unlock()
	if feedback != nil {
		feedback(fmt.Sprintf("%d:%d", before, after))
	}

	s.Close()
	// Seed the new conversation with prior history only; RunTurn will send pending turnParts next.
	return s.initWithHistory(prior)
}
