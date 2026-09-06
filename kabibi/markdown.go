package main

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var (
	chatMarkdownMu        sync.Mutex
	chatMarkdownRenderers = map[int]*glamour.TermRenderer{}
)

func chatMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	if width < 1 {
		width = 1
	}
	chatMarkdownMu.Lock()
	defer chatMarkdownMu.Unlock()
	if r, ok := chatMarkdownRenderers[width]; ok {
		return r, nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil, err
	}
	chatMarkdownRenderers[width] = r
	return r, nil
}

func renderChatMarkdown(content string, width int) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	r, err := chatMarkdownRenderer(width)
	if err != nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimRight(out, "\n")
}

func prefixChatBlock(content, firstPrefix, restPrefix string) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return firstPrefix
	}
	lines := strings.Split(content, "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = firstPrefix + lines[i]
		} else {
			lines[i] = restPrefix + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func renderAssistantChatBlock(content string, width int) string {
	if width < 1 {
		width = 1
	}
	indent := width / 3
	if indent > 4 {
		indent = 4
	}
	if indent < 2 {
		indent = 2
	}
	bullet := lipgloss.NewStyle().Foreground(colorYellow).Render("■")
	pad := strings.Repeat(" ", indent-1)
	contentW := width - indent
	if contentW < 1 {
		contentW = 1
	}
	rendered := renderChatMarkdown(content, contentW)
	if rendered == "" {
		rendered = content
	}
	prefixed := prefixChatBlock(rendered, pad+bullet, pad+" ")
	return forceBackground(prefixed, colorDarkGray)
}
