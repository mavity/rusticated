package ui

import (
	"testing"

	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// ─────────────────────────────────────────────────────────────────────────────
// Phase 1: Black-Box Interrogation (Function-by-Function Contracts)
// ─────────────────────────────────────────────────────────────────────────────

// TestGlowColorBoundaryContract verifies mathematical contract bounds.
// GlowColor(phase) must:
// - Stay within [0, 255] for each R, G, B channel
// - Return grayscale (R == G == B) at all phases
// - Oscillate between #E5E5E5 (229) and #B0B0B0 (176)
func TestGlowColorBoundaryContract(t *testing.T) {
	tests := []struct {
		phase   float64
		name    string
		wantMin int // minimum expected channel value
		wantMax int // maximum expected channel value
	}{
		{0.0, "phase 0: start peak", 176, 229},
		{0.25, "phase 0.25: ascending fade", 176, 229},
		{0.5, "phase 0.5: trough minimum", 176, 176},
		{0.75, "phase 0.75: descending fade", 176, 229},
		{1.0, "phase 1.0: cycle return", 176, 229},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GlowColor(tt.phase)
			r := byte(got >> 16)
			g := byte(got >> 8)
			b := byte(got)

			// Contract 1: Grayscale (no color).
			if r != g || g != b {
				t.Errorf("GlowColor(%v) = RGB(%d, %d, %d), not grayscale", tt.phase, r, g, b)
			}

			// Contract 2: Within oscillation bounds [176, 229].
			if int(r) < tt.wantMin || int(r) > tt.wantMax {
				t.Errorf("GlowColor(%v) channel = %d, want [%d, %d]", tt.phase, r, tt.wantMin, tt.wantMax)
			}

			// Contract 3: No overflow (already enforced by test range, but explicit).
			if r < 0 || r > 255 {
				t.Errorf("GlowColor(%v) channel overflow: %d", tt.phase, r)
			}
		})
	}
}

// TestGlowColorPhaseWrap verifies symmetry and wrap-around.
// phase > 1.0 should wrap (mathematically equivalent to phase % 1.0).
func TestGlowColorPhaseWrap(t *testing.T) {
	c1 := GlowColor(0.25)
	c2 := GlowColor(1.25) // wraps to 0.25
	if c1 != c2 {
		t.Errorf("GlowColor(0.25) != GlowColor(1.25): %v != %v", c1, c2)
	}
}

// TestFlashColorTimelineContract verifies exact ramp timing.
// FlashColor(elapsedMs) must:
// - Jump to white (#FFFFFF) by elapsedMs=150
// - Return to #E5E5E5 by elapsedMs=550 (150 + 400)
// - Stay within [229, 255] for the entire timeline
func TestFlashColorTimelineContract(t *testing.T) {
	tests := []struct {
		name      string
		elapsedMs float64
		phase     string // "ramp", "peak", "fade"
		wantMin   int
		wantMax   int
	}{
		{"t=0ms: start", 0.0, "ramp", 229, 229},
		{"t=75ms: mid-ramp", 75.0, "ramp", 229, 255},
		{"t=150ms: peak white", 150.0, "peak", 255, 255},
		{"t=275ms: mid-fade", 275.0, "fade", 229, 255},
		{"t=550ms: return steady", 550.0, "fade", 229, 229},
		{"t=1000ms: clamped steady", 1000.0, "fade", 229, 229},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FlashColor(tt.elapsedMs)
			r := byte(got >> 16)
			g := byte(got >> 8)
			b := byte(got)

			// Contract 1: Grayscale.
			if r != g || g != b {
				t.Errorf("FlashColor(%v) = RGB(%d, %d, %d), not grayscale", tt.elapsedMs, r, g, b)
			}

			// Contract 2: Within timeline bounds.
			if int(r) < tt.wantMin || int(r) > tt.wantMax {
				t.Errorf("FlashColor(%v)[%s] channel = %d, want [%d, %d]",
					tt.elapsedMs, tt.phase, r, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// TestFlashColorMonotonicity verifies monotonic progression through phases.
// Ramp phase (0-150ms) must increase monotonically.
// Fade phase (150-550ms) must decrease monotonically.
func TestFlashColorMonotonicity(t *testing.T) {
	// Ramp phase: monotonic increase.
	for i := 0.0; i < 150.0; i += 15.0 {
		c1 := FlashColor(i)
		c2 := FlashColor(i + 15.0)
		v1 := byte(c1 >> 16)
		v2 := byte(c2 >> 16)
		if v2 < v1 {
			t.Errorf("FlashColor ramp phase non-monotonic: FlashColor(%.0f)=%d > FlashColor(%.0f)=%d",
				i, v1, i+15.0, v2)
		}
	}

	// Fade phase: monotonic decrease.
	for i := 150.0; i < 550.0; i += 40.0 {
		c1 := FlashColor(i)
		c2 := FlashColor(i + 40.0)
		v1 := byte(c1 >> 16)
		v2 := byte(c2 >> 16)
		if v2 > v1 {
			t.Errorf("FlashColor fade phase non-monotonic: FlashColor(%.0f)=%d > FlashColor(%.0f)=%d",
				i, v1, i+40.0, v2)
		}
	}
}

// TestGlowColorSampling verifies the exact oscillation curve.
// At phase=0.0 and 1.0, should be maximum (229).
// At phase=0.5, should be minimum (176).
func TestGlowColorExactSampling(t *testing.T) {
	c0 := GlowColor(0.0)
	c05 := GlowColor(0.5)
	c1 := GlowColor(1.0)

	v0 := byte(c0 >> 16)
	v05 := byte(c05 >> 16)
	v1 := byte(c1 >> 16)

	if v0 != 229 {
		t.Errorf("GlowColor(0.0) = %d, want 229", v0)
	}
	if v05 != 176 {
		t.Errorf("GlowColor(0.5) = %d, want 176", v05)
	}
	if v1 != 229 {
		t.Errorf("GlowColor(1.0) = %d, want 229", v1)
	}
}

// TestFlashColorExactSampling verifies the exact ramp timeline.
func TestFlashColorExactSampling(t *testing.T) {
	c0 := FlashColor(0.0)
	c150 := FlashColor(150.0)
	c550 := FlashColor(550.0)

	v0 := byte(c0 >> 16)
	v150 := byte(c150 >> 16)
	v550 := byte(c550 >> 16)

	if v0 != 229 {
		t.Errorf("FlashColor(0.0) = %d, want 229", v0)
	}
	if v150 != 255 {
		t.Errorf("FlashColor(150.0) = %d, want 255", v150)
	}
	if v550 != 229 {
		t.Errorf("FlashColor(550.0) = %d, want 229", v550)
	}
}

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
