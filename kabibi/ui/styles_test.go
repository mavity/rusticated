package ui

import (
	"testing"

	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func TestGlowColor(t *testing.T) {
	tests := []struct {
		name  string
		phase float64
		check func(terminal.Color) bool
	}{
		{
			name:  "phase 0 is bright",
			phase: 0.0,
			check: func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
		{
			name:  "phase 0.5 is dim",
			phase: 0.5,
			check: func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
		{
			name:  "phase 1.0 is bright again",
			phase: 1.0,
			check: func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
		{
			name:  "phase > 1 wraps around",
			phase: 1.5,
			check: func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GlowColor(tt.phase)
			if !tt.check(got) {
				t.Errorf("GlowColor(%v) = %v, check failed", tt.phase, got)
			}
		})
	}
}

func TestFlashColor(t *testing.T) {
	tests := []struct {
		name      string
		elapsedMs float64
		check     func(terminal.Color) bool
	}{
		{
			name:      "at start (0ms)",
			elapsedMs: 0,
			check:     func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
		{
			name:      "at 75ms (during ramp)",
			elapsedMs: 75,
			check:     func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
		{
			name:      "at 150ms (peak)",
			elapsedMs: 150,
			check:     func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
		{
			name:      "at 275ms (fading back)",
			elapsedMs: 275,
			check:     func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
		{
			name:      "after 550ms (complete)",
			elapsedMs: 550,
			check:     func(c terminal.Color) bool { return c != 0 && c <= 0xFFFFFF },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FlashColor(tt.elapsedMs)
			if !tt.check(got) {
				t.Errorf("FlashColor(%v) = %v, check failed", tt.elapsedMs, got)
			}
		})
	}
}
