package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai"
)

// HarnessSession tracks conversation state, history, and context compaction.
type HarnessSession struct {
	engine           *ai.Engine
	conv             *ai.Conversation
	systemMessage    string
	tools            []map[string]any
	compactionLimit  int
	verbose          bool
	CanonicalHistory []ai.PromptPart
	CompactedHistory []ai.PromptPart
	totalTokensEst   int
}

func (s *HarnessSession) appendHistory(parts ...ai.PromptPart) {
	s.CanonicalHistory = append(s.CanonicalHistory, parts...)
	s.CompactedHistory = append(s.CompactedHistory, parts...)
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
	return nil
}

func (s *HarnessSession) Close() {
	if s.conv != nil {
		s.conv.Close()
	}
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
				s.CompactedHistory = s.CompactedHistory[:len(s.CompactedHistory)-n]
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

	s.CompactedHistory = append(append([]ai.PromptPart(nil), prior...), pending...)
	after := historySize(s.CompactedHistory)
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
