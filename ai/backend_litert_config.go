package ai

import (
	"encoding/json"
	"fmt"
)

func buildLiteRTConversationConfig(cfg ConversationConfig) (systemJSON, toolsJSON, historyJSON string, err error) {
	if cfg.SystemMessage != "" {
		system, marshalErr := json.Marshal(map[string]string{
			"type": "text",
			"text": cfg.SystemMessage,
		})
		if marshalErr != nil {
			return "", "", "", fmt.Errorf("encode LiteRT system message: %w", marshalErr)
		}
		systemJSON = string(system)
	}
	if len(cfg.Tools) > 0 {
		tools, marshalErr := json.Marshal(cfg.Tools)
		if marshalErr != nil {
			return "", "", "", fmt.Errorf("encode LiteRT tools: %w", marshalErr)
		}
		toolsJSON = string(tools)
	}
	if len(cfg.History) > 0 {
		historyJSON, err = buildHistoryJSON(cfg.History)
		if err != nil {
			return "", "", "", fmt.Errorf("encode LiteRT history: %w", err)
		}
	}
	return systemJSON, toolsJSON, historyJSON, nil
}
