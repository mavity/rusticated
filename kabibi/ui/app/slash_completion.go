package app

import (
	"strings"

	"github.com/mavity/rusticated/kabibi/slashcmd"
	"github.com/mavity/rusticated/kabibi/ui/shell"
)

func slashCompletionCandidates(value string, cursor int, browserAvailable bool) ([]shell.CompletionCandidate, bool) {
	runes := []rune(value)
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(runes) {
		cursor = len(runes)
	}

	start := 0
	for start < len(runes) && strings.ContainsRune(" \t\r\n", runes[start]) {
		start++
	}
	if start == len(runes) || runes[start] != '/' {
		return nil, false
	}

	end := start
	for end < len(runes) && !strings.ContainsRune(" \t\r\n", runes[end]) {
		end++
	}
	if strings.Count(string(runes[start:end]), "/") != 1 || cursor < start || cursor > end {
		return nil, false
	}

	prefix := string(runes[start:cursor])
	suffix := string(runes[cursor:end])
	prefixRunes := []rune(prefix)
	suffixRunes := []rune(suffix)
	var candidates []shell.CompletionCandidate
	lowerPrefix := strings.ToLower(prefix)
	lowerSuffix := strings.ToLower(suffix)
	for _, command := range slashcmd.Commands(browserAvailable) {
		name := []rune(command.Name)
		if len(name) < len(prefixRunes)+len(suffixRunes) ||
			!strings.HasPrefix(strings.ToLower(command.Name), lowerPrefix) ||
			!strings.HasSuffix(strings.ToLower(command.Name), lowerSuffix) {
			continue
		}
		insertEnd := len(name) - len(suffixRunes)
		candidates = append(candidates, shell.CompletionCandidate{
			Display:     command.Name,
			Insert:      string(name[len(prefixRunes):insertEnd]),
			Space:       cursor == end && end == len(runes),
			Description: command.Description,
		})
	}
	return candidates, true
}
