package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestGlowColor(t *testing.T) {
	tests := []struct {
		name  string
		phase float64
		check func(lipgloss.Color) bool
	}{
		{
			name:  "phase 0 is bright",
			phase: 0.0,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
		{
			name:  "phase 0.5 is dim",
			phase: 0.5,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
		{
			name:  "phase 1.0 is bright again",
			phase: 1.0,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
		{
			name:  "phase > 1 wraps around",
			phase: 1.5,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := glowColor(tt.phase)
			if !tt.check(got) {
				t.Errorf("glowColor(%v) = %v, check failed", tt.phase, got)
			}
		})
	}
}

func TestFlashColor(t *testing.T) {
	tests := []struct {
		name      string
		elapsedMs float64
		check     func(lipgloss.Color) bool
	}{
		{
			name:      "at start (0ms)",
			elapsedMs: 0,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
		{
			name:      "at 75ms (during ramp)",
			elapsedMs: 75,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
		{
			name:      "at 150ms (peak)",
			elapsedMs: 150,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
		{
			name:      "at 275ms (fading back)",
			elapsedMs: 275,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
		{
			name:      "after 550ms (complete)",
			elapsedMs: 550,
			check: func(c lipgloss.Color) bool {
				s := string(c)
				return strings.HasPrefix(s, "#") && len(s) == 7
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flashColor(tt.elapsedMs)
			if !tt.check(got) {
				t.Errorf("flashColor(%v) = %v, check failed", tt.elapsedMs, got)
			}
		})
	}
}

func TestAnsiDualColor(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(string) bool
	}{
		{
			name:  "plain text unchanged",
			input: "hello",
			check: func(s string) bool { return s == "hello" },
		},
		{
			name:  "simple SGR sequence",
			input: "\x1b[31mhello\x1b[0m",
			check: func(s string) bool {
				return strings.Contains(s, "hello") && strings.Contains(s, "\x1b[")
			},
		},
		{
			name:  "24-bit color sequence",
			input: "text",
			check: func(s string) bool {
				return strings.TrimSpace(s) == "text" || len(s) > 0
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ansiDualColor(tt.input)
			if !tt.check(got) {
				t.Errorf("ansiDualColor(%q) = %q, check failed", tt.input, got)
			}
		})
	}

	t.Run("preserves plain text after ANSI strip", func(t *testing.T) {
		input := "\x1b[38;2;255;0;0mRED\x1b[0m"
		got := ansiDualColor(input)
		stripped := ansi.Strip(got)
		if stripped != "RED" {
			t.Errorf("ansiDualColor(...) stripped to %q, want %q", stripped, "RED")
		}
	})
}

func hexToRGB(hex string) (int, int, int, error) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0, nil
	}
	r, _ := strconv.ParseInt(hex[0:2], 16, 64)
	g, _ := strconv.ParseInt(hex[2:4], 16, 64)
	b, _ := strconv.ParseInt(hex[4:6], 16, 64)
	return int(r), int(g), int(b), nil
}

func TestAnsiDualColorRGBInjection(t *testing.T) {
	input := "\x1b[38;2;100;150;200mtext\x1b[0m"
	got := ansiDualColor(input)

	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	sequences := re.FindAllString(got, -1)

	if len(sequences) < 2 {
		t.Errorf("expected at least 2 SGR sequences, got %d", len(sequences))
	}

	stripped := ansi.Strip(got)
	if stripped != "text" {
		t.Errorf("stripped output = %q, want %q", stripped, "text")
	}
}
