package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"ai"
)

type partEmbedder interface {
	Embed(parts []ai.PromptPart) ([]float32, error)
}

type textCompressorFunc func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

type ephemeralStreamFunc func(ctx context.Context, cfg ai.ConversationConfig, trigger []ai.PromptPart, onEvent func(ai.ModelEvent) bool) error

// HarnessSession tracks conversation state, history, and context compaction.
type HarnessSession struct {
	engine           *ai.Engine
	embedder         partEmbedder
	compressor       textCompressorFunc
	ephemeralStream  ephemeralStreamFunc
	out              io.Writer
	conv             *ai.Conversation
	systemMessage    string
	tools            []map[string]any
	compactionLimit  int
	verbose          bool
	CanonicalHistory []ai.PromptPart
	CompactedHistory []ai.PromptPart
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

func (s *HarnessSession) appendHistory(parts ...ai.PromptPart) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.CanonicalHistory = append(s.CanonicalHistory, parts...)
	s.CompactedHistory = append(s.CompactedHistory, parts...)
}

func (s *HarnessSession) startEmbedWorker() {
	if s.embedder == nil || reflect.ValueOf(s.embedder).IsNil() {
		return
	}
	s.embedOnce.Do(func() {
		s.embedTrigger = make(chan struct{}, 1)
		s.embedStop = make(chan struct{})
		s.embedDone = make(chan struct{})
		go s.embedWorkerLoop()
	})
}

func (s *HarnessSession) triggerBackgroundEmbedding() {
	if s.embedder == nil || reflect.ValueOf(s.embedder).IsNil() {
		return
	}
	s.startEmbedWorker()
	select {
	case s.embedTrigger <- struct{}{}:
	default:
	}
}

func (s *HarnessSession) embedWorkerLoop() {
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
func (s *HarnessSession) processPendingEmbeddings(flushAll bool) {
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

		vec, err := s.embedder.Embed([]ai.PromptPart{part})
		var traceVec []float32
		if part.CognitiveTrace != nil && !part.CognitiveTrace.IsDirect && len(part.CognitiveTrace.TraceEmbeddings) == 0 {
			traceText := strings.TrimSpace(part.CognitiveTrace.Goal + "\n" + part.CognitiveTrace.SynthesizedPlan)
			if traceText != "" {
				if tv, terr := s.embedder.Embed([]ai.PromptPart{{Role: ai.RoleModel, Text: traceText}}); terr == nil && len(tv) > 0 {
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

func (s *HarnessSession) applyEmbeddingLocked(idx int, part ai.PromptPart, vec, traceVec []float32, err error) {
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

func (s *HarnessSession) flushEmbeddingsInline() {
	if s.embedder == nil || reflect.ValueOf(s.embedder).IsNil() {
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

		vec, err := s.embedder.Embed([]ai.PromptPart{part})
		var traceVec []float32
		if part.CognitiveTrace != nil && !part.CognitiveTrace.IsDirect && len(part.CognitiveTrace.TraceEmbeddings) == 0 {
			traceText := strings.TrimSpace(part.CognitiveTrace.Goal + "\n" + part.CognitiveTrace.SynthesizedPlan)
			if traceText != "" {
				if tv, terr := s.embedder.Embed([]ai.PromptPart{{Role: ai.RoleModel, Text: traceText}}); terr == nil && len(tv) > 0 {
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

func syncCompactedEmbedding(compacted []ai.PromptPart, target ai.PromptPart, vec []float32) {
	for i := len(compacted) - 1; i >= 0; i-- {
		p := &compacted[i]
		if len(p.Embedding) == 0 && p.Role == target.Role && p.Text == target.Text && p.ToolName == target.ToolName && len(p.ToolCalls) == len(target.ToolCalls) {
			p.Embedding = vec
			return
		}
	}
}

// FlushEmbeddings synchronously finishes any pending embeddings in CanonicalHistory.
func (s *HarnessSession) FlushEmbeddings() {
	if s.embedder == nil || reflect.ValueOf(s.embedder).IsNil() {
		return
	}
	s.processPendingEmbeddings(true)
}

func (s *HarnessSession) Init() error {
	// Ensure CompactedHistory never starts or ends on an orphaned tool turn.
	if len(s.CompactedHistory) > 0 && s.CompactedHistory[len(s.CompactedHistory)-1].Role == ai.RoleUser {
		// In RunTurn retry, the pending turnParts sit at the tail of CompactedHistory;
		// NewConversationWithConfig seeds the history prior to the pending turnParts.
	}
	return s.initWithHistory(s.CompactedHistory)
}

func (s *HarnessSession) initWithHistory(history []ai.PromptPart) error {
	if s.engine == nil {
		return nil
	}
	cfg := ai.ConversationConfig{
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

func (s *HarnessSession) Close() {
	s.closeConv()
}

func (s *HarnessSession) closeConv() {
	if s.conv != nil {
		s.conv.Close()
		s.conv = nil
	}
}

// Shutdown stops the background embedding worker and closes the active conversation.
func (s *HarnessSession) Shutdown() {
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
	out           io.Writer
	maxRunes      int
	prevLineRunes int
	currLineRunes int
	isCapped      bool
	lastWasSpace  bool
	buf           strings.Builder
}

func newLiveStreamer(out io.Writer, maxRunes int) *liveStreamer {
	if out == nil {
		out = os.Stdout
	}
	if maxRunes <= 0 {
		maxRunes = defaultStreamerMaxRunes
	}
	return &liveStreamer{
		out:      out,
		maxRunes: maxRunes,
	}
}

func (l *liveStreamer) StartStage(label string) {
	if l.prevLineRunes > 0 {
		_, _ = fmt.Fprintf(l.out, "\r%s\r", strings.Repeat(" ", l.prevLineRunes))
	}
	l.currLineRunes = 0
	l.isCapped = false
	l.lastWasSpace = true
	l.buf.Reset()

	tag := "[" + label + "] "
	_, _ = fmt.Fprint(l.out, tag)
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
			_, _ = fmt.Fprint(l.out, "…")
			l.currLineRunes++
			l.isCapped = true
			break
		}
		_, _ = fmt.Fprint(l.out, string(r))
		l.currLineRunes++
	}
}

func (l *liveStreamer) EndStage() string {
	l.prevLineRunes = l.currLineRunes
	return strings.TrimSpace(l.buf.String())
}

func (l *liveStreamer) Wipe() {
	if l.prevLineRunes > 0 {
		_, _ = fmt.Fprintf(l.out, "\r%s\r", strings.Repeat(" ", l.prevLineRunes))
		l.prevLineRunes = 0
	} else if l.currLineRunes > 0 {
		_, _ = fmt.Fprintf(l.out, "\r%s\r", strings.Repeat(" ", l.currLineRunes))
	}
	l.currLineRunes = 0
	l.isCapped = false
	l.lastWasSpace = false
}

func (s *HarnessSession) outputWriter() io.Writer {
	if s.out != nil {
		return s.out
	}
	return os.Stdout
}

func (s *HarnessSession) snapshotCompactedHistory() []ai.PromptPart {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return append([]ai.PromptPart(nil), s.CompactedHistory...)
}

func (s *HarnessSession) systemMessageWithToolNames() string {
	var names []string
	for _, t := range s.tools {
		if fn, ok := t["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
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

func (s *HarnessSession) streamOnce(ctx context.Context, cfg ai.ConversationConfig, trigger []ai.PromptPart, onEvent func(ai.ModelEvent) bool) error {
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
func (s *HarnessSession) runEphemeralStage(
	ctx context.Context,
	baseHistory *[]ai.PromptPart,
	buildSeed func(base []ai.PromptPart) []ai.PromptPart,
	triggerText string,
	sysMessage string,
	stageLabel string,
	streamer *liveStreamer,
) (string, error) {
	trigger := []ai.PromptPart{{Role: ai.RoleUser, Text: triggerText}}
	var retries int
	for {
		seed := buildSeed(*baseHistory)
		cfg := ai.ConversationConfig{
			SystemMessage: sysMessage,
			Tools:         nil,
			History:       seed,
		}
		streamer.StartStage(stageLabel)
		err := s.streamOnce(ctx, cfg, trigger, func(event ai.ModelEvent) bool {
			if e, ok := event.(ai.TextEvent); ok {
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
		if (isContextOverflowError(err) || errors.Is(err, ai.ErrRepetitionLoop)) && retries < 2 {
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

func (s *HarnessSession) compactForEphemeralRetry(ctx context.Context) error {
	out := s.outputWriter()
	err := s.compactContextForRetry(ctx, 0, func(text string) {
		_, _ = fmt.Fprint(out, text)
	})
	_, _ = fmt.Fprintln(out)
	s.closeConv()
	return err
}

const (
	stage1SyntheticWait      = "Wait, let me think for a moment..."
	stage1TriggerPrompt      = "Take your time. If this is trivial and you are completely confident you can answer immediately without planning or tools, prefix your response with 'DIRECT:' followed by your answer. Otherwise, do not answer yet—first analyze the situation and state the underlying goal and task required to address my prompt above, prefixing your analysis with 'GOAL:'."
	stageExpectSyntheticWait = "Before I begin working on this, let me check how brief and direct the user expects the reply to be..."
	stageExpectTriggerPrompt = "Look strictly at my prompt above. Default to extreme brevity—the shortest, most direct answer that fully satisfies the request. Unless I explicitly asked for an essay or exhaustive breakdown, prescribe a strict, tiny length limit (e.g., 'Just the name/value in 1-5 words', 'One short sentence under 15 words', or 'At most 2-3 brief sentences'). State ONLY the required format and strict maximum word/sentence cap in one short sentence."
	stage2TriggerPrompt      = "What are the concrete steps you would take to achieve that goal?"
	stage3APrefix            = "I think you might take these steps below, but are these actually relevant to the goal, or do they miss the point or include unnecessary detours?\n"
	stage3BPrefix            = "I think you might take these steps below, but are these safe, or do they carry risks, edge cases, or destructive side effects?\n"
	stage3CPrefix            = "I think you might take these steps below, but are these well-informed, or are they making unverified assumptions where we need to collect more information first?\n"
	stage4TriggerPrompt      = "I see. Given that analysis, what do you propose as the final, concrete execution plan?"
)

// runStage1Triage runs Stage 1 and detects whether the model chose the "DIRECT:"
// fast-path bypass or the "GOAL:" reflective path.
func (s *HarnessSession) runStage1Triage(
	ctx context.Context,
	baseHistory *[]ai.PromptPart,
	userText string,
	streamer *liveStreamer,
) (isDirect bool, text string, err error) {
	outWriter := s.outputWriter()
	trigger := []ai.PromptPart{{Role: ai.RoleUser, Text: stage1TriggerPrompt}}
	var retries int
	for {
		seed := make([]ai.PromptPart, 0, len(*baseHistory)+2)
		seed = append(seed, *baseHistory...)
		seed = append(seed,
			ai.PromptPart{Role: ai.RoleUser, Text: userText},
			ai.PromptPart{Role: ai.RoleModel, Text: stage1SyntheticWait},
		)
		cfg := ai.ConversationConfig{
			SystemMessage: s.systemMessage,
			Tools:         nil,
			History:       seed,
		}

		const prefixProbeRunes = 12
		var probeBuf strings.Builder
		var fullDirect strings.Builder
		decided := false
		directMode := false

		decidePrefix := func(finalFlush bool) {
			if decided {
				return
			}
			raw := probeBuf.String()
			trimmed := strings.TrimLeft(raw, " \t\r\n")
			if !finalFlush {
				if utf8.RuneCountInString(trimmed) < prefixProbeRunes && !strings.Contains(trimmed, ":") {
					return
				}
			}
			decided = true
			upper := strings.ToUpper(trimmed)
			if strings.HasPrefix(upper, "DIRECT:") {
				directMode = true
				rest := strings.TrimLeft(trimmed[len("DIRECT:"):], " ")
				streamer.Wipe()
				if rest != "" {
					_, _ = fmt.Fprint(outWriter, rest)
					fullDirect.WriteString(rest)
				}
				return
			}
			rest := trimmed
			if strings.HasPrefix(upper, "GOAL:") {
				rest = strings.TrimLeft(trimmed[len("GOAL:"):], " ")
			}
			streamer.StartStage("goal")
			if rest != "" {
				streamer.Feed(rest)
			}
		}

		err := s.streamOnce(ctx, cfg, trigger, func(event ai.ModelEvent) bool {
			e, ok := event.(ai.TextEvent)
			if !ok {
				return true
			}
			if !decided {
				probeBuf.WriteString(e.Text)
				decidePrefix(false)
				return true
			}
			if directMode {
				_, _ = fmt.Fprint(outWriter, e.Text)
				fullDirect.WriteString(e.Text)
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
				if s.verbose && fullDirect.Len() > 0 {
					_, _ = fmt.Fprintln(outWriter)
				}
				return true, strings.TrimSpace(fullDirect.String()), nil
			}
			goalText := streamer.EndStage()
			return false, goalText, nil
		}

		if errors.Is(err, context.Canceled) {
			streamer.Wipe()
			return false, "", err
		}
		if (isContextOverflowError(err) || errors.Is(err, ai.ErrRepetitionLoop)) && retries < 2 {
			retries++
			streamer.Wipe()
			if directMode && fullDirect.Len() > 0 {
				_, _ = fmt.Fprintln(outWriter)
			}
			if cerr := s.compactForEphemeralRetry(ctx); cerr != nil {
				return false, "", cerr
			}
			*baseHistory = s.snapshotCompactedHistory()
			continue
		}
		streamer.Wipe()
		return false, "", err
	}
}

// runStage5Execution runs Stage 5 ("Let It Rip") with tools enabled, executing
// any requested tools in a loop and returning the executed tool parts and draft answer.
func (s *HarnessSession) runStage5Execution(
	ctx context.Context,
	baseHistory *[]ai.PromptPart,
	userText, goal, plan string,
	streamer *liveStreamer,
) (executedToolParts []ai.PromptPart, draftAnswer string, err error) {
	outWriter := s.outputWriter()
	var retries int

	for {
		seed := make([]ai.PromptPart, 0, len(*baseHistory)+2)
		seed = append(seed, *baseHistory...)
		seed = append(seed,
			ai.PromptPart{Role: ai.RoleUser, Text: userText},
			ai.PromptPart{
				Role: ai.RoleModel,
				Text: "I need to think first. The goal here is: " + goal + "\nMy execution plan is:\n" + plan,
			},
		)

		turnParts := []ai.PromptPart{{
			Role: ai.RoleUser,
			Text: "I see what you mean. Please proceed with that plan now, using any necessary tools, and keep in mind the original request—answer it completely after your work is done:\n" + userText,
		}}

		executedToolParts = nil

		if s.ephemeralStream != nil {
			historyCursor := append([]ai.PromptPart(nil), seed...)
			var stageErr error
			for {
				var toolCalls []ai.ToolCall
				var generatedText strings.Builder
				cfg := ai.ConversationConfig{
					SystemMessage: s.systemMessage,
					Tools:         s.tools,
					History:       historyCursor,
				}
				streamer.StartStage("work")
				stageErr = s.ephemeralStream(ctx, cfg, turnParts, func(event ai.ModelEvent) bool {
					switch e := event.(type) {
					case ai.TextEvent:
						streamer.Feed(e.Text)
						generatedText.WriteString(e.Text)
					case ai.ToolCallEvent:
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
				modelTurn := ai.PromptPart{
					Role:      ai.RoleModel,
					Text:      generatedText.String(),
					ToolCalls: toolCalls,
				}
				executedToolParts = append(executedToolParts, modelTurn)
				historyCursor = append(historyCursor, modelTurn)

				var toolResponses []ai.PromptPart
				for _, call := range toolCalls {
					explanation := extractToolExplanation(call)
					result := ExecuteTool(ctx, call, func(text string) {
						_, _ = fmt.Fprint(outWriter, text)
					})
					_, _ = fmt.Fprintln(outWriter)
					toolResponses = append(toolResponses, ai.PromptPart{
						Role:         ai.RoleTool,
						ToolName:     call.Name,
						Explanation:  explanation,
						ToolResponse: result,
					})
				}
				executedToolParts = append(executedToolParts, toolResponses...)
				turnParts = toolResponses
			}
			if errors.Is(stageErr, context.Canceled) {
				streamer.Wipe()
				return nil, "", stageErr
			}
			if (isContextOverflowError(stageErr) || errors.Is(stageErr, ai.ErrRepetitionLoop)) && retries < 2 {
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
		cfg := ai.ConversationConfig{
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
			var toolCalls []ai.ToolCall
			var generatedText strings.Builder
			streamer.StartStage("work")
			for event, err := range ephemeralConv.SendStream(ctx, turnParts) {
				if err != nil {
					stageErr = err
					break
				}
				switch e := event.(type) {
				case ai.TextEvent:
					streamer.Feed(e.Text)
					generatedText.WriteString(e.Text)
				case ai.ToolCallEvent:
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
			modelTurn := ai.PromptPart{
				Role:      ai.RoleModel,
				Text:      generatedText.String(),
				ToolCalls: toolCalls,
			}
			executedToolParts = append(executedToolParts, modelTurn)

			var toolResponses []ai.PromptPart
			for _, call := range toolCalls {
				explanation := extractToolExplanation(call)
				result := ExecuteTool(ctx, call, func(text string) {
					_, _ = fmt.Fprint(outWriter, text)
				})
				_, _ = fmt.Fprintln(outWriter)
				toolResponses = append(toolResponses, ai.PromptPart{
					Role:         ai.RoleTool,
					ToolName:     call.Name,
					Explanation:  explanation,
					ToolResponse: result,
				})
			}
			executedToolParts = append(executedToolParts, toolResponses...)
			turnParts = toolResponses
		}
		ephemeralConv.Close()

		if errors.Is(stageErr, context.Canceled) {
			streamer.Wipe()
			return nil, "", stageErr
		}
		if (isContextOverflowError(stageErr) || errors.Is(stageErr, ai.ErrRepetitionLoop)) && retries < 2 {
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
// first token arrives, streamer.Wipe() clears the [work] ticker line and the
// tokens stream directly to stdout.
func (s *HarnessSession) runStageConvergence(
	ctx context.Context,
	baseHistory *[]ai.PromptPart,
	userText, detailLevel, workAndAnswer string,
	streamer *liveStreamer,
) (string, error) {
	outWriter := s.outputWriter()
	trigger := []ai.PromptPart{{
		Role: ai.RoleUser,
		Text: "That draft is far too wordy. Look at the strict target length and format: " + detailLevel + "\nRewrite your answer now to strictly obey that limit using the minimum words possible. Cut all prefaces, roleplay, meta-commentary, internal phases, failed tool mentions, bullet lists, and extra examples unless explicitly requested. Output ONLY the final distilled answer to: " + userText,
	}}

	var retries int
	for {
		seed := make([]ai.PromptPart, 0, len(*baseHistory)+2)
		seed = append(seed, *baseHistory...)
		seed = append(seed,
			ai.PromptPart{Role: ai.RoleUser, Text: userText},
			ai.PromptPart{Role: ai.RoleModel, Text: workAndAnswer},
		)

		cfg := ai.ConversationConfig{
			SystemMessage: s.systemMessage,
			Tools:         nil,
			History:       seed,
		}

		var finalBuf strings.Builder
		wiped := false

		err := s.streamOnce(ctx, cfg, trigger, func(event ai.ModelEvent) bool {
			e, ok := event.(ai.TextEvent)
			if !ok || e.Text == "" {
				return true
			}
			if !wiped {
				streamer.Wipe()
				wiped = true
			}
			_, _ = fmt.Fprint(outWriter, e.Text)
			finalBuf.WriteString(e.Text)
			return true
		})

		if err == nil {
			if !wiped {
				streamer.Wipe()
			}
			finalText := strings.TrimSpace(finalBuf.String())
			if finalText == "" && workAndAnswer != "" {
				_, _ = fmt.Fprint(outWriter, workAndAnswer)
				finalText = workAndAnswer
			}
			if s.verbose && len(finalText) > 0 {
				_, _ = fmt.Fprintln(outWriter)
			}
			return finalText, nil
		}

		if errors.Is(err, context.Canceled) {
			streamer.Wipe()
			return "", err
		}
		if (isContextOverflowError(err) || errors.Is(err, ai.ErrRepetitionLoop)) && retries < 2 {
			retries++
			streamer.Wipe()
			if wiped && finalBuf.Len() > 0 {
				_, _ = fmt.Fprintln(outWriter)
			}
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

func (s *HarnessSession) RunTurn(ctx context.Context, userText string) error {
	s.turnWaiting.Store(true)
	s.turnGate.Lock()
	s.turnWaiting.Store(false)
	defer func() {
		s.turnGate.Unlock()
		s.triggerBackgroundEmbedding()
	}()

	// Release any idle persistent conversation before opening ephemeral stage forks.
	s.closeConv()

	baseHistory := s.snapshotCompactedHistory()
	streamer := newLiveStreamer(s.outputWriter(), defaultStreamerMaxRunes)

	restorePersistentConv := func() {
		s.closeConv()
		_ = s.Init()
	}

	// Stage 1: Triage & Goal Extraction ("DIRECT:" vs. "GOAL:")
	isDirect, stage1Text, err := s.runStage1Triage(ctx, &baseHistory, userText, streamer)
	if err != nil {
		restorePersistentConv()
		return err
	}

	var executedToolParts []ai.PromptPart
	var finalAnswer string
	trace := &ai.CognitiveTrace{
		IsDirect: isDirect,
	}

	if isDirect {
		finalAnswer = stage1Text
	} else {
		goal := stage1Text
		trace.Goal = goal

		// Stage 2: Expectation Calibration (<detail_level>)
		seedExpectation := func(base []ai.PromptPart) []ai.PromptPart {
			seed := make([]ai.PromptPart, 0, len(base)+2)
			seed = append(seed, base...)
			seed = append(seed,
				ai.PromptPart{Role: ai.RoleUser, Text: userText},
				ai.PromptPart{Role: ai.RoleModel, Text: stageExpectSyntheticWait},
			)
			return seed
		}
		detailLevel, err := s.runEphemeralStage(ctx, &baseHistory, seedExpectation, stageExpectTriggerPrompt, s.systemMessage, "expectation", streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		trace.ExpectedDetail = detailLevel

		effectiveGoal := goal
		if detailLevel != "" {
			effectiveGoal = goal + "\nExpected response detail: " + detailLevel
		}

		seedWithGoal := func(base []ai.PromptPart) []ai.PromptPart {
			seed := make([]ai.PromptPart, 0, len(base)+2)
			seed = append(seed, base...)
			seed = append(seed,
				ai.PromptPart{Role: ai.RoleUser, Text: userText},
				ai.PromptPart{Role: ai.RoleModel, Text: "I need to think first, the goal here is probably: " + effectiveGoal},
			)
			return seed
		}

		// Stage 3: Step Ideation (<steps>)
		steps, err := s.runEphemeralStage(ctx, &baseHistory, seedWithGoal, stage2TriggerPrompt, s.systemMessage, "steps", streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		trace.InitialSteps = steps

		// Stage 4: The Orthogonal Triple-Fork Critique (<relevancy>, <safety>, <grounded>)
		critiqueSysMsg := s.systemMessageWithToolNames()

		relevancy, err := s.runEphemeralStage(ctx, &baseHistory, seedWithGoal, stage3APrefix+steps, critiqueSysMsg, "relevancy", streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		trace.CritiqueRelevancy = relevancy

		safety, err := s.runEphemeralStage(ctx, &baseHistory, seedWithGoal, stage3BPrefix+steps, critiqueSysMsg, "safety", streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		trace.CritiqueSafety = safety

		grounded, err := s.runEphemeralStage(ctx, &baseHistory, seedWithGoal, stage3CPrefix+steps, critiqueSysMsg, "grounded", streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		trace.CritiqueGrounded = grounded

		// Stage 5: The Tribunal Join & Plan Formulation (<plan>)
		seedStage5Plan := func(base []ai.PromptPart) []ai.PromptPart {
			seed := make([]ai.PromptPart, 0, len(base)+4)
			seed = append(seed, base...)
			seed = append(seed,
				ai.PromptPart{Role: ai.RoleUser, Text: userText},
				ai.PromptPart{Role: ai.RoleModel, Text: "I need to think first, the goal here is probably: " + effectiveGoal},
				ai.PromptPart{Role: ai.RoleUser, Text: "Maybe these steps would get us there?\n" + steps},
				ai.PromptPart{
					Role: ai.RoleModel,
					Text: "I have analysed those proposed steps from three angles:\n- Relevancy: " + relevancy + "\n- Safety & Risks: " + safety + "\n- Information Grounding: " + grounded,
				},
			)
			return seed
		}
		plan, err := s.runEphemeralStage(ctx, &baseHistory, seedStage5Plan, stage4TriggerPrompt, s.systemMessage, "plan", streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		trace.SynthesizedPlan = plan

		// Stage 6: Execution ("Let It Rip" — <work_and_answer>)
		toolParts, workAndAnswer, err := s.runStage5Execution(ctx, &baseHistory, userText, effectiveGoal, plan, streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		executedToolParts = toolParts
		trace.RawWorkAndAnswer = workAndAnswer
		trace.DraftIterations = append(trace.DraftIterations, workAndAnswer)

		// Stage 7: Convergence & Re-Formulation Flip
		convergedAnswer, err := s.runStageConvergence(ctx, &baseHistory, userText, detailLevel, workAndAnswer, streamer)
		if err != nil {
			restorePersistentConv()
			return err
		}
		finalAnswer = convergedAnswer
	}

	// Stage 7: Replanting the Iceberg & Syncing Session State
	spokenParts := make([]ai.PromptPart, 0, 2+len(executedToolParts))
	spokenParts = append(spokenParts, ai.PromptPart{Role: ai.RoleUser, Text: userText})
	spokenParts = append(spokenParts, executedToolParts...)
	spokenParts = append(spokenParts, ai.PromptPart{
		Role:           ai.RoleModel,
		Text:           finalAnswer,
		CognitiveTrace: trace,
	})
	s.appendHistory(spokenParts...)

	if err := s.initWithHistory(s.snapshotCompactedHistory()); err != nil {
		return err
	}

	// Check if context compaction is needed
	if s.totalTokensEst > s.compactionLimit && len(s.CompactedHistory) > 4 {
		outWriter := s.outputWriter()
		_ = s.compactContext(ctx, func(text string) {
			_, _ = fmt.Fprint(outWriter, text)
		})
		_, _ = fmt.Fprintln(outWriter)
	}

	return nil
}

func historySize(history []ai.PromptPart) int {
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

func formatPartForSummary(p ai.PromptPart) string {
	const maxPartChars = 1200
	var sb strings.Builder
	sb.WriteString(string(p.Role))
	sb.WriteString(": ")
	if p.Text != "" {
		sb.WriteString(compactString(p.Text, maxPartChars))
	}
	if len(p.ToolCalls) > 0 {
		for _, tc := range p.ToolCalls {
			sb.WriteString(fmt.Sprintf(" [call %s(%s)]", tc.Name, compactString(string(tc.RawArguments), 200)))
		}
	}
	if p.ToolResponse != nil {
		if b, err := json.Marshal(p.ToolResponse); err == nil {
			sb.WriteString(fmt.Sprintf(" [tool %s -> %s]", p.ToolName, compactString(string(b), maxPartChars)))
		}
	}
	sb.WriteByte('\n')
	return sb.String()
}

func (s *HarnessSession) compactContext(ctx context.Context, feedback LogFeedback) error {
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
	parts     []ai.PromptPart
	embedding []float32
}

// groupIntoAtomicUnits partitions a slice of PromptParts into indivisible atomicTurnUnits.
func groupIntoAtomicUnits(history []ai.PromptPart) []atomicTurnUnit {
	var units []atomicTurnUnit
	for i := 0; i < len(history); i++ {
		p := history[i]
		if p.Role == ai.RoleModel && len(p.ToolCalls) > 0 {
			end := i + 1
			for end < len(history) && history[end].Role == ai.RoleTool {
				end++
			}
			slice := append([]ai.PromptPart(nil), history[i:end]...)
			units = append(units, atomicTurnUnit{
				parts:     slice,
				embedding: computeUnitEmbedding(slice),
			})
			i = end - 1
			continue
		}
		slice := []ai.PromptPart{p}
		units = append(units, atomicTurnUnit{
			parts:     slice,
			embedding: computeUnitEmbedding(slice),
		})
	}
	return units
}

// computeUnitEmbedding returns the L2-normalized sum of constituent part embeddings.
func computeUnitEmbedding(parts []ai.PromptPart) []float32 {
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

func (s *HarnessSession) runCompressorPrompt(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if s.compressor != nil {
		return s.compressor(ctx, systemPrompt, userPrompt)
	}
	if s.engine == nil {
		return "", errors.New("no engine configured for summarization")
	}
	summaryConv, err := s.engine.NewConversationWithConfig(ai.ConversationConfig{
		SystemMessage: systemPrompt,
	})
	if err != nil {
		return "", err
	}
	defer summaryConv.Close()

	var summary strings.Builder
	for event, err := range summaryConv.SendStream(ctx, []ai.PromptPart{{Role: ai.RoleUser, Text: userPrompt}}) {
		if err != nil {
			return strings.TrimSpace(summary.String()), err
		}
		if e, ok := event.(ai.TextEvent); ok {
			summary.WriteString(e.Text)
		}
	}
	return strings.TrimSpace(summary.String()), nil
}

// compressTextBlock enforces the word ceiling (40% of input words, min 15) with up to 2 retries
// stepping down by 0.75x on budget overrun, and a deterministic word-truncation escape hatch.
func (s *HarnessSession) compressTextBlock(ctx context.Context, gapText string) string {
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

func (s *HarnessSession) gapTokenBudget() int {
	if s.compactionLimit > 0 {
		return s.compactionLimit / 2
	}
	return 2048
}

func extractToolExplanation(call ai.ToolCall) string {
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

func (s *HarnessSession) largeBlockByteThreshold() int {
	// 1/5 of gapTokenBudget() in tokens (~4 bytes per token).
	thresh := (s.gapTokenBudget() * 4) / 5
	if thresh < 512 {
		thresh = 512
	}
	return thresh
}

func artifactCacheKey(role ai.Role, toolName, explanation, rawContent string) string {
	sum := sha256.Sum256([]byte(string(role) + "\x00" + toolName + "\x00" + explanation + "\x00" + rawContent))
	return hex.EncodeToString(sum[:])
}

// compressLargeBlock collapses a single large unpinned block into a <=20-word gist
// guided by explanation, using artifactCache to avoid redundant LLM calls across turns.
func (s *HarnessSession) compressLargeBlock(ctx context.Context, role ai.Role, toolName, explanation, rawContent string) string {
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
func (s *HarnessSession) collapseUnpinnedLargeBlocks(ctx context.Context, units []atomicTurnUnit, tailStart int, pinned map[int]bool) {
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
		parts := make([]ai.PromptPart, len(units[i].parts))
		copy(parts, units[i].parts)
		for j := range parts {
			p := &parts[j]
			if p.Role == ai.RoleTool {
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
func (s *HarnessSession) summarizeGap(ctx context.Context, units []atomicTurnUnit) string {
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

func (s *HarnessSession) summarizeOversizedText(ctx context.Context, text string, budget int) string {
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
func (s *HarnessSession) projectHistory(ctx context.Context, prior []ai.PromptPart) []ai.PromptPart {
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
			return []ai.PromptPart{{Role: ai.RoleUser, Text: "[Context summary]: " + sumText}}
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
	for tailStart < n && units[tailStart].parts[0].Role != ai.RoleUser {
		tailStart++
	}
	for tailStart < n {
		var tailParts []ai.PromptPart
		for i := tailStart; i < n; i++ {
			tailParts = append(tailParts, units[i].parts...)
		}
		if historySize(tailParts) <= maxTailBytes {
			break
		}
		tailStart++
		for tailStart < n && units[tailStart].parts[0].Role != ai.RoleUser {
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

	var projected []ai.PromptPart
	var currentGap []atomicTurnUnit

	flushGap := func() {
		if len(currentGap) == 0 {
			return
		}
		if sumText := s.summarizeGap(ctx, currentGap); sumText != "" {
			projected = append(projected, ai.PromptPart{
				Role: ai.RoleUser,
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

func (s *HarnessSession) compactContextForRetry(ctx context.Context, pendingParts int, feedback LogFeedback) error {
	// Step 1: Synchronously flush any missing embeddings in CanonicalHistory.
	s.flushEmbeddingsInline()

	before := historySize(s.CompactedHistory)
	if feedback != nil {
		feedback("🗜️ ")
	}

	// Project directly from CanonicalHistory so previously compacted turns can resurrect
	// when the conversation topic returns to them.
	s.historyMu.Lock()
	source := append([]ai.PromptPart(nil), s.CanonicalHistory...)
	if len(source) == 0 && len(s.CompactedHistory) > 0 {
		source = append([]ai.PromptPart(nil), s.CompactedHistory...)
	}
	s.historyMu.Unlock()

	var pending []ai.PromptPart
	prior := source
	if pendingParts > 0 && len(prior) >= pendingParts {
		split := len(prior) - pendingParts
		pending = append([]ai.PromptPart(nil), prior[split:]...)
		prior = prior[:split]
	}

	if len(prior) > 0 {
		prior = s.projectHistory(ctx, prior)
	}

	// If a pending tool output itself exceeds budget on retry, collapse it into a COMPACTED: gist.
	for i := range pending {
		if pending[i].Role == ai.RoleTool && pending[i].ToolResponse != nil {
			if b, err := json.Marshal(pending[i].ToolResponse); err == nil && len(b) > 8192 {
				compacted := s.compressLargeBlock(ctx, pending[i].Role, pending[i].ToolName, pending[i].Explanation, string(b))
				pending[i].ToolResponse = map[string]any{"output": compacted}
			}
		}
	}

	s.historyMu.Lock()
	s.CompactedHistory = append(append([]ai.PromptPart(nil), prior...), pending...)
	after := historySize(s.CompactedHistory)
	s.historyMu.Unlock()
	if feedback != nil {
		feedback(fmt.Sprintf("%d:%d", before, after))
	}

	s.Close()
	// Seed the new conversation with prior history only; RunTurn will send pending turnParts next.
	return s.initWithHistory(prior)
}

// splitHistoryForCompaction partitions history into (older, recent) so that:
// 1. recent starts on a RoleUser boundary (never an orphaned RoleModel/RoleTool),
// 2. recent fits comfortably within half the compaction budget, and
// 3. at least one older part is moved into older when history is large.
func splitHistoryForCompaction(history []ai.PromptPart, compactionLimit int) (older, recent []ai.PromptPart) {
	if len(history) <= 2 {
		if historySize(history) > 4096 {
			return history, nil
		}
		return nil, history
	}
	cutoff := len(history) - 4
	if cutoff < 1 {
		cutoff = 1
	}
	// Advance cutoff to a RoleUser boundary so recent never starts mid-tool-exchange.
	for cutoff < len(history) && history[cutoff].Role != ai.RoleUser {
		cutoff++
	}
	// Also shrink recent if its byte size alone exceeds half the token budget (~2 bytes/token).
	maxRecentBytes := compactionLimit * 2
	if maxRecentBytes <= 0 {
		maxRecentBytes = 8192
	}
	for cutoff < len(history) && historySize(history[cutoff:]) > maxRecentBytes {
		cutoff++
		for cutoff < len(history) && history[cutoff].Role != ai.RoleUser {
			cutoff++
		}
	}
	return history[:cutoff], history[cutoff:]
}

func buildCompactedHistory(recent []ai.PromptPart, summary string) []ai.PromptPart {
	compacted := make([]ai.PromptPart, 1, 1+len(recent))
	compacted[0] = ai.PromptPart{
		Role: ai.RoleUser,
		Text: "[Previous conversation summary]: " + summary,
	}
	return append(compacted, recent...)
}

func compactHistory(history []ai.PromptPart, summary string) []ai.PromptPart {
	if len(history) <= 4 && historySize(history) <= 4096 {
		return history
	}
	older, recent := splitHistoryForCompaction(history, 4096)
	if len(older) == 0 {
		return history
	}
	return buildCompactedHistory(recent, summary)
}
