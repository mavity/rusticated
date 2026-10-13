package main

import (
	"context"
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

	"ai"
)

type partEmbedder interface {
	Embed(parts []ai.PromptPart) ([]float32, error)
}

type textCompressorFunc func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// HarnessSession tracks conversation state, history, and context compaction.
type HarnessSession struct {
	engine           *ai.Engine
	embedder         partEmbedder
	compressor       textCompressorFunc
	conv             *ai.Conversation
	systemMessage    string
	tools            []map[string]any
	compactionLimit  int
	verbose          bool
	CanonicalHistory []ai.PromptPart
	CompactedHistory []ai.PromptPart
	totalTokensEst   int

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
		s.historyMu.Lock()
		s.applyEmbeddingLocked(idx, part, vec, err)
		s.historyMu.Unlock()
		s.turnGate.Unlock()
	}
}

func (s *HarnessSession) applyEmbeddingLocked(idx int, part ai.PromptPart, vec []float32, err error) {
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

func (s *HarnessSession) RunTurn(ctx context.Context, userText string) error {
	s.turnWaiting.Store(true)
	s.turnGate.Lock()
	s.turnWaiting.Store(false)
	defer func() {
		s.turnGate.Unlock()
		s.triggerBackgroundEmbedding()
	}()

	turnParts := []ai.PromptPart{{Role: ai.RoleUser, Text: userText}}
	s.appendHistory(turnParts...)
	s.totalTokensEst += len(userText) / 4

	var retries int
	for {
		var toolCalls []ai.ToolCall
		var generatedText strings.Builder

		var streamErr error
		for event, err := range s.conv.SendStream(ctx, turnParts) {
			if err != nil {
				streamErr = err
				break
			}
			switch e := event.(type) {
			case ai.TextEvent:
				fmt.Print(e.Text)
				generatedText.WriteString(e.Text)
			case ai.ToolCallEvent:
				toolCalls = append(toolCalls, e.ToolCalls...)
			}
		}

		if streamErr != nil {
			isOverflow := isContextOverflowError(streamErr)
			isLoop := errors.Is(streamErr, ai.ErrRepetitionLoop)

			if (isOverflow || isLoop) && retries < 2 {
				retries++
				if generatedText.Len() > 0 {
					fmt.Println()
				}
				err := s.compactContextForRetry(ctx, len(turnParts), func(text string) {
					fmt.Print(text)
				})
				fmt.Println()
				if err != nil {
					return err
				}
				continue // retry the turn seamlessly with the fresh s.conv
			}
			// Any stream failure or cancellation leaves the LiteRT-LM conversation
			// poisoned ("Task failed with state: 7"). Drop the failed turn's
			// uncommitted tail from CompactedHistory and re-initialize s.conv so
			// the next user prompt in the REPL starts on a clean conversation.
			if n := len(turnParts); n > 0 && len(s.CompactedHistory) >= n {
				s.historyMu.Lock()
				s.CompactedHistory = s.CompactedHistory[:len(s.CompactedHistory)-n]
				s.historyMu.Unlock()
			}
			s.Close()
			_ = s.Init()
			return streamErr
		}

		if len(toolCalls) == 0 {
			if s.verbose && generatedText.Len() > 0 {
				fmt.Println()
			}
			s.appendHistory(ai.PromptPart{Role: ai.RoleModel, Text: generatedText.String()})
			s.totalTokensEst += generatedText.Len() / 4
			break
		}

		// Model emitted tool calls: record the model turn (preamble text plus
		// the calls) before the responses, so replayed history stays well-formed.
		s.appendHistory(ai.PromptPart{
			Role:      ai.RoleModel,
			Text:      generatedText.String(),
			ToolCalls: toolCalls,
		})
		s.totalTokensEst += generatedText.Len() / 4
		if b, err := json.Marshal(toolCalls); err == nil {
			s.totalTokensEst += len(b) / 4
		}
		if generatedText.Len() > 0 {
			fmt.Println()
		}

		var toolResponses []ai.PromptPart
		for _, call := range toolCalls {
			result := ExecuteTool(ctx, call, func(text string) {
				fmt.Print(text)
			})
			fmt.Println()

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
		s.appendHistory(toolResponses...)
	}

	// Check if context compaction is needed
	if s.totalTokensEst > s.compactionLimit && len(s.CompactedHistory) > 4 {
		_ = s.compactContext(ctx, func(text string) {
			fmt.Print(text)
		})
		fmt.Println()
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

	// If a pending tool output itself exceeds budget on retry, compress its content via the gap pipeline.
	for i := range pending {
		if pending[i].Role == ai.RoleTool && pending[i].ToolResponse != nil {
			if b, err := json.Marshal(pending[i].ToolResponse); err == nil && len(b) > 8192 {
				compressed := s.summarizeOversizedText(ctx, string(b), s.gapTokenBudget())
				pending[i].ToolResponse = map[string]any{"output": compressed}
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
