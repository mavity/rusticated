//go:build !wasm

package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func IsAISupported() bool {
	return true
}

func (m *model) runAIInference(userInput string) tea.Cmd {
	msgCh := make(chan tea.Msg, 64)
	m.aiMsgChan = msgCh

	go func() {
		defer close(msgCh)

		cacheDir, err := cacheDirPath()
		if err != nil {
			msgCh <- aiDoneMsg{err: err}
			return
		}

		modelPath := filepath.Join(cacheDir, defaultModelName)
		libDir := filepath.Join(cacheDir, "lib")

		// Find the sidecar and library
		libExt := ".so"
		exeExt := ""
		if os.Getenv("OS") == "Windows_NT" {
			libExt = ".dll"
			exeExt = ".exe"
		}

		libPath := filepath.Join(libDir, "litert_lm_ext"+libExt)
		sidecarName := "litert-lm-sidecar" + exeExt

		// If on Windows ARM64, we prefer the x64 sidecar to load the x64 DLLs via Prism
		if os.Getenv("PROCESSOR_ARCHITECTURE") == "ARM64" || os.Getenv("PROCESSOR_IDENTIFIER") == "ARM64" {
			if _, err := os.Stat("litert-lm-sidecar-x64.exe"); err == nil {
				sidecarName = "litert-lm-sidecar-x64.exe"
			}
		}

		sidecarPath := filepath.Join(".", sidecarName)
		if abs, err := filepath.Abs(sidecarPath); err == nil {
			sidecarPath = abs
		}

		client, err := NewSidecarClient(sidecarPath, libPath)
		if err != nil {
			msgCh <- aiDoneMsg{err: fmt.Errorf("failed to start sidecar: %w", err)}
			return
		}
		defer client.Close()

		engine, err := client.EngineCreate(modelPath, "cpu")
		if err != nil {
			msgCh <- aiDoneMsg{err: fmt.Errorf("failed to create engine: %w", err)}
			return
		}

		conv, err := client.ConversationCreate(engine)
		if err != nil {
			msgCh <- aiDoneMsg{err: fmt.Errorf("failed to create conversation: %w", err)}
			return
		}

		err = client.ConversationSend(conv, userInput, func(token string) {
			if token != "" {
				msgCh <- aiTokenMsg(token)
			}
		})

		if err != nil {
			msgCh <- aiDoneMsg{err: err}
		} else {
			msgCh <- aiDoneMsg{err: nil}
		}
	}()

	return m.watchAIChanCmd()
}
