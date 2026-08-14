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

func runAIPrompt(userInput string, onToken func(string)) error {
	cacheDir, err := cacheDirPath()
	if err != nil {
		return err
	}

	modelPath := filepath.Join(cacheDir, defaultModelName)
	libDir := filepath.Join(cacheDir, "lib")

	// Find the sidecar and library
	libExt := ".so"
	exeExt := ""
	if HostOS() == "windows" {
		libExt = ".dll"
		exeExt = ".exe"
	}

	libPath := filepath.Join(libDir, "litert_lm_ext"+libExt)
	sidecarName := "litert-lm-sidecar" + exeExt

	// If on Windows ARM64, we prefer the x64 sidecar to load the x64 DLLs via Prism
	if HostArch() == "arm64" && HostOS() == "windows" {
		sidecarName = "litert-lm-sidecar-x64.exe"
	}

	// Usually the sidecar lives alongside the downloaded extension in the cache directory
	sidecarPath := filepath.Join(libDir, sidecarName)
	if fi, err := os.Stat(sidecarPath); err != nil || fi.IsDir() {
		// Fallback to exactly where the command was run, or system PATH
		sidecarPath = sidecarName

		if exe, err := os.Executable(); err == nil {
			localCand := filepath.Join(filepath.Dir(exe), sidecarName)
			if fi, err := os.Stat(localCand); err == nil && !fi.IsDir() {
				sidecarPath = localCand
			}
		}
	}

	if abs, err := filepath.Abs(sidecarPath); err == nil {
		sidecarPath = abs
	}

	client, err := NewSidecarClient(sidecarPath, libPath)
	if err != nil {
		return fmt.Errorf("failed to start sidecar: %w", err)
	}
	defer client.Close()

	engine, err := client.EngineCreate(modelPath, "cpu")
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}

	conv, err := client.ConversationCreate(engine)
	if err != nil {
		return fmt.Errorf("failed to create conversation: %w", err)
	}

	return client.ConversationSend(conv, userInput, onToken)
}

func (m *model) runAIInference(userInput string) tea.Cmd {
	msgCh := make(chan tea.Msg, 64)
	m.aiMsgChan = msgCh

	go func() {
		defer close(msgCh)

		err := runAIPrompt(userInput, func(token string) {
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
