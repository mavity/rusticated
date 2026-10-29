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

// PartEmbedder can embed prompt parts for background indexing and RAG compaction.
type PartEmbedder interface {
	Embed(parts []PromptPart) ([]float32, error)
}

// LogFeedback receives streamed text fragments.
type LogFeedback func(text string)

// ToolContext provides explicit runtime and session capabilities to tool
// handlers, allowing tools to inspect, clone, modify, or replace the Session
// and active Conversation in place.
type ToolContext struct {
	Session        *Session
	Feedback       LogFeedback
	baseHistory    *[]PromptPart
	sessionSwapped bool
}

// ReplaceSession adopts next and makes its updated history available to the
// active turn and subsequent turns.
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
		_, raw, err := decodeToolArguments(call.RawArguments, call.Arguments)
		if err != nil {
			return in, fmt.Errorf("invalid arguments: %w", err)
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return in, fmt.Errorf("invalid arguments: %w", err)
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
	// OnToolFeedback receives tool-provided status values separately from model-stage feedback.
	OnToolFeedback LogFeedback
	Verbose        bool
}

// Session tracks conversation state, history, and context compaction.
type Session struct {
	engine           *Engine
	embedder         PartEmbedder
	compressor       textCompressorFunc
	conv             *Conversation
	systemMessage    string
	tools            []map[string]any
	toolRegistry     map[string]Tool
	compactionLimit  int
	verbose          bool
	onContent        LogFeedback
	onFeedback       LogFeedback
	onToolFeedback   LogFeedback
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
		onToolFeedback:  cfg.OnToolFeedback,
		toolRegistry:    make(map[string]Tool, len(cfg.Tools)),
	}
	if s.compactionLimit <= 0 {
		s.compactionLimit = 6000
	}
	// The removed deliberation pipeline classified prompts and generated steps,
	// critiques, and a plan through staged model calls; see around v0.8.9.

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
	s.totalTokensEst = s.estimateTokens(s.CompactedHistory)
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

func (s *Session) emitToolFeedback(text string) {
	if s.onToolFeedback != nil {
		s.onToolFeedback(text)
		return
	}
	s.emitFeedback(text)
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
		return toolErrorResult(fmt.Errorf("unknown tool: %s", call.Name))
	}
	arguments, _, err := decodeToolArguments(call.RawArguments, call.Arguments)
	if err != nil {
		return toolErrorResult(fmt.Errorf("invalid arguments for tool %q: %w", call.Name, err))
	}
	call.Arguments = arguments
	if tc == nil {
		tc = &ToolContext{
			Session:  s,
			Feedback: s.emitFeedback,
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
			return toolErrorResult(err)
		}
		return result
	}
	if tool.Execute == nil {
		return toolErrorResult(fmt.Errorf("tool has no executor: %s", call.Name))
	}
	fb := s.emitFeedback
	if tc.Feedback != nil {
		fb = tc.Feedback
	}
	result, err := tool.Execute(ctx, call, fb)
	syncSessionAfterTool()
	if err != nil {
		return toolErrorResult(err)
	}
	return result
}

func toolErrorResult(err error) map[string]any {
	return map[string]any{"error": err.Error()}
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
		s.historyMu.Lock()
		s.applyEmbeddingLocked(idx, part, vec, err)
		s.historyMu.Unlock()
		s.turnGate.Unlock()
	}
}

func (s *Session) applyEmbeddingLocked(idx int, part PromptPart, vec []float32, err error) {
	if err == nil && len(vec) > 0 && idx < len(s.CanonicalHistory) {
		s.CanonicalHistory[idx].Embedding = vec
		syncCompactedEmbedding(s.CompactedHistory, part, vec)
		if s.verbose {
			fmt.Fprintf(os.Stderr, "[embed] #%d %s %s\n", idx, part.Role, formatEmbeddingSummary(vec))
		}
	} else if err != nil && s.verbose {
		fmt.Fprintf(os.Stderr, "[embed] #%d %s error: %v\n", idx, part.Role, err)
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
		s.historyMu.Lock()
		s.applyEmbeddingLocked(idx, part, vec, err)
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
	return s.initWithHistory(s.snapshotCompactedHistory())
}

func (s *Session) createConversation(history []PromptPart) (*Conversation, error) {
	if s.engine == nil {
		return nil, errors.New("ai: engine is not initialized")
	}
	cfg := ConversationConfig{
		SystemMessage: s.systemMessage,
		Tools:         s.tools,
		History:       history,
	}
	return s.engine.NewConversationWithConfig(cfg)
}

func (s *Session) replaceConversation(history []PromptPart) error {
	s.closeConv()
	conv, err := s.createConversation(history)
	if err != nil {
		return err
	}
	s.conv = conv
	return nil
}

func (s *Session) estimateTokens(history []PromptPart) int {
	total := len(s.systemMessage)/4 + historySize(history)/4
	for _, tool := range s.tools {
		if b, err := json.Marshal(tool); err == nil {
			total += len(b) / 4
		}
	}
	return total
}

func (s *Session) initWithHistory(history []PromptPart) error {
	if s.engine == nil {
		return nil
	}
	if err := s.replaceConversation(history); err != nil {
		return err
	}
	s.totalTokensEst = s.estimateTokens(history)
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

func (s *Session) snapshotCanonicalHistory() []PromptPart {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return append([]PromptPart(nil), s.CanonicalHistory...)
}

// runAgenticTurn runs the user's request with registered tools until the model
// returns a final answer.
func (s *Session) runAgenticTurn(
	ctx context.Context,
	baseHistory *[]PromptPart,
	userPart PromptPart,
	streamer *liveStreamer,
) (executedToolParts []PromptPart, draftAnswer string, compactedDuringTurn bool, err error) {
	buildHistory := func(parts []PromptPart) []PromptPart {
		history := append([]PromptPart(nil), (*baseHistory)...)
		history = append(history, userPart)
		return append(history, parts...)
	}

	if s.engine == nil {
		streamer.Wipe()
		return nil, "", false, errors.New("ai: engine is not initialized")
	}
	if s.conv == nil {
		if err := s.initWithHistory(*baseHistory); err != nil {
			streamer.Wipe()
			return nil, "", false, err
		}
	}

	turnParts := []PromptPart{userPart}
	var overflowRetries int
	for {
		var toolCalls []ToolCall
		var generatedText strings.Builder
		var genStart, genEnd time.Time
		for event, streamErr := range s.conv.SendStream(ctx, turnParts) {
			if streamErr != nil {
				err = streamErr
				break
			}
			now := time.Now()
			if genStart.IsZero() {
				genStart = now
			}
			genEnd = now
			switch e := event.(type) {
			case TextEvent:
				generatedText.WriteString(e.Text)
			case ToolCallEvent:
				toolCalls = append(toolCalls, e.ToolCalls...)
			}
		}
		_ = streamer.EndStage()
		if err != nil {
			if isContextOverflowError(err) && overflowRetries == 0 {
				streamer.Wipe()
				s.tryCompressTurnPartsOnOverflow(ctx, turnParts, executedToolParts)
				history := s.snapshotCanonicalHistory()
				if len(history) == 0 {
					history = append([]PromptPart(nil), (*baseHistory)...)
				}
				completedParts := executedToolParts
				if len(turnParts) > 0 && turnParts[0].Role == RoleTool &&
					len(completedParts) >= len(turnParts) {
					completedParts = completedParts[:len(completedParts)-len(turnParts)]
				}
				if len(turnParts) > 0 && turnParts[0].Role == RoleTool {
					history = append(history, userPart)
					history = append(history, completedParts...)
				}
				before := historySize(*baseHistory)
				s.emitFeedback("🗜️ ")
				projected := s.projectHistory(ctx, history)
				if replaceErr := s.replaceConversation(projected); replaceErr != nil {
					return nil, "", compactedDuringTurn, fmt.Errorf("rebuild conversation after context overflow: %w", replaceErr)
				}
				*baseHistory = s.snapshotCompactedHistory()
				s.emitFeedback(fmt.Sprintf("%d:%d", before, historySize(projected)))
				overflowRetries++
				compactedDuringTurn = true
				err = nil
				continue
			}
			streamer.Wipe()
			return nil, "", compactedDuringTurn, err
		}

		if len(toolCalls) == 0 {
			return executedToolParts, strings.TrimSpace(generatedText.String()), compactedDuringTurn, nil
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

		toolResponses, sessionMutated := s.executeToolCalls(ctx, baseHistory, toolCalls)
		executedToolParts = append(executedToolParts, toolResponses...)
		if sessionMutated {
			history := buildHistory(executedToolParts[:len(executedToolParts)-len(toolResponses)])
			if err := s.replaceConversation(history); err != nil {
				return nil, "", compactedDuringTurn, fmt.Errorf("rebuild conversation after tool-updated session: %w", err)
			}
		}
		turnParts = toolResponses
	}
}

func (s *Session) executeToolCalls(ctx context.Context, baseHistory *[]PromptPart, calls []ToolCall) ([]PromptPart, bool) {
	toolResponses := make([]PromptPart, 0, len(calls))
	sessionMutated := false
	for _, call := range calls {
		explanation := extractToolExplanation(call)
		tc := &ToolContext{
			Session:     s,
			Feedback:    s.emitToolFeedback,
			baseHistory: baseHistory,
		}
		toolStart := time.Now()
		result := s.executeTool(ctx, tc, call)
		toolEnd := time.Now()
		sessionMutated = sessionMutated || tc.sessionSwapped
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
	return toolResponses, sessionMutated
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

	baseHistory := s.snapshotCompactedHistory()
	streamer := newLiveStreamer(s.emitFeedback, defaultStreamerMaxRunes)

	// The removed planner calibrated response length, drafted steps, ran three critiques, and synthesized a plan.
	// See its historical implementation around tag v0.8.9.
	responseStartedAt := time.Now()
	executedToolParts, finalAnswer, compactedDuringTurn, err := s.runAgenticTurn(ctx, &baseHistory, userPart, streamer)
	if err != nil {
		s.closeConv()
		if restoreErr := s.initWithHistory(s.snapshotCompactedHistory()); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore session conversation after failed turn: %w", restoreErr))
		}
		return err
	}
	streamer.Wipe()
	if finalAnswer != "" {
		s.emitContent(finalAnswer)
		if s.verbose {
			s.emitContent("\n")
		}
	}
	responseCompletedAt := time.Now()

	spokenParts := make([]PromptPart, 0, 2+len(executedToolParts))
	spokenParts = append(spokenParts, userPart)
	spokenParts = append(spokenParts, executedToolParts...)
	spokenParts = append(spokenParts, PromptPart{
		Role:        RoleModel,
		Text:        finalAnswer,
		StartedAt:   responseStartedAt,
		CompletedAt: responseCompletedAt,
		Duration:    responseCompletedAt.Sub(responseStartedAt),
	})
	s.appendHistory(spokenParts...)

	if compactedDuringTurn || (s.totalTokensEst > s.compactionLimit && len(s.CompactedHistory) > 4) {
		if err := s.compactContext(ctx, s.emitFeedback); err != nil {
			return fmt.Errorf("compact session history after turn: %w", err)
		}
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
	for _, key := range []string{"explanation", "purpose", "reason", "note"} {
		if call.Arguments != nil {
			if exp, ok := call.Arguments[key].(string); ok && strings.TrimSpace(exp) != "" {
				return strings.TrimSpace(exp)
			}
		}
		if len(call.RawArguments) > 0 {
			var m map[string]any
			if json.Unmarshal(call.RawArguments, &m) == nil {
				if exp, ok := m[key].(string); ok && strings.TrimSpace(exp) != "" {
					return strings.TrimSpace(exp)
				}
			}
		}
	}
	return ""
}

// compressImmediateToolOverflow compresses an oversized immediate tool output
// that exceeded the context window during tool execution. It:
//  1. Retains a short verbatim head and tail of the raw tool output,
//  2. Breaks the middle into overlapped consumable chunks,
//  3. Summarizes each chunk via a dedicated LLM call adorned with an aggressive
//     summarization prompt that includes the tool call's purpose/explanation note,
//  4. Tells the agent explicitly that compression was necessary because the tool
//     output size was too large for the context window.
func (s *Session) compressImmediateToolOverflow(ctx context.Context, part PromptPart, rawJSON string) map[string]any {
	purpose := strings.TrimSpace(part.Explanation)
	if purpose == "" {
		if part.ToolName != "" {
			purpose = "inspecting output of tool " + part.ToolName
		} else {
			purpose = "answering the user's request"
		}
	}

	runes := []rune(rawJSON)
	totalRunes := len(runes)
	const (
		headRunes = 400
		tailRunes = 400
	)
	if totalRunes <= headRunes+tailRunes+200 {
		return map[string]any{
			"compressed_due_to_size": true,
			"notice":                 fmt.Sprintf("Tool output (%d bytes) exceeded the context window and was compressed.", len(rawJSON)),
			"purpose":                purpose,
			"content":                rawJSON,
		}
	}

	headText := strings.TrimSpace(string(runes[:headRunes]))
	tailText := strings.TrimSpace(string(runes[totalRunes-tailRunes:]))
	middle := runes[headRunes : totalRunes-tailRunes]

	// Determine consumable chunk size in runes (~1600 runes per chunk, or scaled to gapTokenBudget)
	chunkSize := 1600
	if budget := s.gapTokenBudget(); budget > 0 {
		if candidate := budget * 2; candidate >= 600 && candidate <= 3200 {
			chunkSize = candidate
		}
	}
	overlap := chunkSize / 5
	if overlap < 100 {
		overlap = 100
	}
	step := chunkSize - overlap
	if step < 1 {
		step = 1
	}

	var chunks []string
	for start := 0; start < len(middle); start += step {
		end := start + chunkSize
		if end > len(middle) {
			end = len(middle)
		}
		chunkStr := strings.TrimSpace(string(middle[start:end]))
		if chunkStr != "" {
			chunks = append(chunks, chunkStr)
		}
		if end == len(middle) {
			break
		}
	}

	// Cap total chunks so an enormous file doesn't trigger dozens of LLM calls;
	// if there are more than 6 chunks, evenly sample/merge into at most 6 overlapped windows.
	const maxChunks = 3
	if len(chunks) > maxChunks {
		merged := make([]string, 0, maxChunks)
		groupSize := (len(chunks) + maxChunks - 1) / maxChunks
		for i := 0; i < len(chunks); i += groupSize {
			end := i + groupSize
			if end > len(chunks) {
				end = len(chunks)
			}
			combined := strings.Join(chunks[i:end], "\n")
			cRunes := []rune(combined)
			if len(cRunes) > chunkSize*2 {
				cRunes = cRunes[:chunkSize*2]
			}
			merged = append(merged, string(cRunes))
		}
		chunks = merged
	}

	if s.onFeedback != nil {
		s.onFeedback(fmt.Sprintf("🗜️ tool(%d chunks)\n", len(chunks)))
	}

	var chunkSummaries []string
	for idx, ch := range chunks {
		sysPrompt := fmt.Sprintf(
			"AGGRESSIVE TOOL OUTPUT COMPRESSOR: This tool output was too large for the context window and is being compressed in overlapped parts (part %d of %d). "+
				"The agent invoked tool %q for this specific purpose: %q. "+
				"Aggressively extract ONLY the concrete facts, identifiers, structure, and details relevant to that purpose. "+
				"Discard decorative ASCII/emoji art, repetitive boilerplate, and filler. Maximum 80 words. No preamble.",
			idx+1, len(chunks), part.ToolName, purpose,
		)
		sum, err := s.runCompressorPrompt(ctx, sysPrompt, ch)
		sum = strings.TrimSpace(sum)
		if err != nil || sum == "" {
			sum = truncateToWords(ch, 60)
		} else if wordCount(sum) > 100 {
			sum = truncateToWords(sum, 90)
		}
		if sum != "" {
			chunkSummaries = append(chunkSummaries, fmt.Sprintf("[Part %d/%d]: %s", idx+1, len(chunks), sum))
		}
	}

	return map[string]any{
		"compressed_due_to_size": true,
		"notice": fmt.Sprintf(
			"NOTE TO AGENT: Compression was necessary because the raw %s output (%d bytes) exceeded the context window. "+
				"Below are the verbatim head, the purpose-guided summary of %d overlapped middle segments (for purpose: %q), and the verbatim tail.",
			part.ToolName, len(rawJSON), len(chunks), purpose,
		),
		"head_excerpt":              headText,
		"overlapped_middle_summary": strings.Join(chunkSummaries, "\n"),
		"tail_excerpt":              tailText,
	}
}

func (s *Session) tryCompressTurnPartsOnOverflow(ctx context.Context, turnParts []PromptPart, executedToolParts []PromptPart) bool {
	compressedAny := false
	for i := range turnParts {
		if turnParts[i].Role != RoleTool || turnParts[i].ToolResponse == nil {
			continue
		}
		if m, ok := turnParts[i].ToolResponse.(map[string]any); ok {
			if already, _ := m["compressed_due_to_size"].(bool); already {
				continue
			}
		}
		b, err := json.Marshal(turnParts[i].ToolResponse)
		if err != nil || len(b) <= 1200 {
			continue
		}
		compacted := s.compressImmediateToolOverflow(ctx, turnParts[i], string(b))
		turnParts[i].ToolResponse = compacted
		compressedAny = true
	}
	if compressedAny && len(executedToolParts) >= len(turnParts) {
		offset := len(executedToolParts) - len(turnParts)
		for i := range turnParts {
			executedToolParts[offset+i].ToolResponse = turnParts[i].ToolResponse
		}
	}
	return compressedAny
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
