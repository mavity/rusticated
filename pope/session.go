package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ai"
)

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

		// Model emitted tool calls: record the model turn (preamble text plus
		// the calls) before the responses, so replayed history stays well-formed.
		s.history = append(s.history, ai.PromptPart{
			Role:      ai.RoleModel,
			Text:      generatedText.String(),
			ToolCalls: toolCalls,
		})
		s.totalTokensEst += generatedText.Len() / 4
		if b, err := json.Marshal(toolCalls); err == nil {
			s.totalTokensEst += len(b) / 4
		}

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
		stream, err := summaryConv.SendStream(ctx, []ai.PromptPart{{Role: ai.RoleUser, Text: historyText.String()}})
		if err == nil {
			for event := range stream {
				if e, ok := event.(ai.TextEvent); ok {
					summary.WriteString(e.Text)
				}
			}
		}

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
