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
	cfg := ai.ConversationConfig{
		SystemMessage: s.systemMessage,
		Tools:         s.tools,
		History:       s.CompactedHistory,
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
			isOverflow := strings.Contains(streamErr.Error(), "exceeds available state entries")
			isLoop := errors.Is(streamErr, ai.ErrRepetitionLoop)

			if (isOverflow || isLoop) && retries < 2 {
				retries++
				if s.verbose {
					fmt.Printf("\n[stream recovery (%v); compacting and retrying (%d/2)]\n", streamErr, retries)
				}
				if err := s.compactContext(ctx); err != nil {
					return err
				}
				continue // retry the turn seamlessly with the fresh s.conv
			}
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
		_ = s.compactContext(ctx)
	}

	return nil
}

func (s *HarnessSession) compactContext(ctx context.Context) error {
	if s.verbose {
		fmt.Println("\n[context compaction: summarizing earlier history...]")
	}
	if len(s.CompactedHistory) > 4 {
		cutoff := len(s.CompactedHistory) - 4
		older := s.CompactedHistory[:cutoff]

		var historyText strings.Builder
		for _, p := range older {
			historyText.WriteString(fmt.Sprintf("%s: %s\n", p.Role, p.Text))
		}

		summaryConv, err := s.engine.NewConversationWithConfig(ai.ConversationConfig{
			SystemMessage: "You are a concise conversation summarizer. Summarize the key facts from the conversation below in 2-3 sentences.",
		})
		if err == nil {
			var summary strings.Builder
			for event, err := range summaryConv.SendStream(ctx, []ai.PromptPart{{Role: ai.RoleUser, Text: historyText.String()}}) {
				if err != nil {
					break
				}
				if e, ok := event.(ai.TextEvent); ok {
					summary.WriteString(e.Text)
				}
			}
			summaryConv.Close()

			sumText := strings.TrimSpace(summary.String())
			if sumText != "" {
				if s.verbose {
					fmt.Printf("[context compacted summary: %s]\n", sumText)
				}
				s.CompactedHistory = compactHistory(s.CompactedHistory, sumText)
				s.totalTokensEst = len(sumText)/4 + 200
			}
		}
	}

	s.Close()
	return s.Init()
}

func compactHistory(history []ai.PromptPart, summary string) []ai.PromptPart {
	if len(history) <= 4 {
		return history
	}
	cutoff := len(history) - 4
	recent := history[cutoff:]
	compacted := make([]ai.PromptPart, 1, 1+len(recent))
	compacted[0] = ai.PromptPart{
		Role: ai.RoleUser,
		Text: "[Previous conversation summary]: " + summary,
	}
	return append(compacted, recent...)
}
