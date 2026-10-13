package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"ai"
)

type partEmbedder interface {
	Embed(parts []ai.PromptPart) ([]float32, error)
}

// HarnessSession tracks conversation state, history, and context compaction.
type HarnessSession struct {
	engine           *ai.Engine
	embedder         partEmbedder
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
		s.historyMu.Unlock()
		s.turnGate.Unlock()
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

func (s *HarnessSession) compactContextForRetry(ctx context.Context, pendingParts int, feedback LogFeedback) error {
	before := historySize(s.CompactedHistory)
	if feedback != nil {
		feedback("🗜️ ")
	}

	// Separate any in-flight turnParts (which RunTurn is about to re-send via
	// SendStream) from the prior history that should be compacted and seeded.
	var pending []ai.PromptPart
	prior := s.CompactedHistory
	if pendingParts > 0 && len(prior) >= pendingParts {
		split := len(prior) - pendingParts
		pending = append([]ai.PromptPart(nil), prior[split:]...)
		prior = prior[:split]
	}

	if len(prior) > 0 {
		older, recent := splitHistoryForCompaction(prior, s.compactionLimit)

		var historyText strings.Builder
		for _, p := range older {
			historyText.WriteString(formatPartForSummary(p))
		}
		promptText := historyText.String()
		if len(promptText) > 12000 {
			promptText = promptText[len(promptText)-12000:]
		}

		var sumText string
		if promptText != "" && s.engine != nil {
			summaryConv, err := s.engine.NewConversationWithConfig(ai.ConversationConfig{
				SystemMessage: "You are a concise conversation summarizer. Summarize the key facts from the conversation below in 2-3 sentences.",
			})
			if err == nil {
				var summary strings.Builder
				for event, err := range summaryConv.SendStream(ctx, []ai.PromptPart{{Role: ai.RoleUser, Text: promptText}}) {
					if err != nil {
						break
					}
					if e, ok := event.(ai.TextEvent); ok {
						summary.WriteString(e.Text)
					}
				}
				summaryConv.Close()
				sumText = strings.TrimSpace(summary.String())
			}
		}
		if sumText == "" && len(older) > 0 {
			// Fallback deterministic digest if summarizer call failed or returned empty.
			sumText = compactString(strings.TrimSpace(promptText), 600)
		}
		if sumText != "" {
			prior = buildCompactedHistory(recent, sumText)
		}
	}

	// Truncate oversized pending tool outputs if the pending turn itself exceeds budget.
	for i := range pending {
		if pending[i].Role == ai.RoleTool && pending[i].ToolResponse != nil {
			if b, err := json.Marshal(pending[i].ToolResponse); err == nil && len(b) > 8192 {
				pending[i].ToolResponse = map[string]any{"output": compactString(string(b), 8192) + "\n...[compacted]"}
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
