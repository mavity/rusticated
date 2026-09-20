package ui

import (
	"testing"
)

// ── GlowColor Tests ──────────────────────────────────────────────────────────

func TestGlowColor_PhaseZeroUpperBound_ExactChannel229(t *testing.T) {
	color := GlowColor(0.0)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != 229 {
		t.Fatalf("GlowColor(0.0) red channel = %d, want 229", r)
	}
	if g != 229 {
		t.Fatalf("GlowColor(0.0) green channel = %d, want 229", g)
	}
	if b != 229 {
		t.Fatalf("GlowColor(0.0) blue channel = %d, want 229", b)
	}
}

func TestGlowColor_PhaseZeroGrayscaleEquality_RedGreenBlueIdentical(t *testing.T) {
	color := GlowColor(0.0)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != g || g != b {
		t.Fatalf("GlowColor(0.0) channels not equal: r=%d g=%d b=%d", r, g, b)
	}
}

func TestGlowColor_PhaseHalfLowerBound_ExactChannel176(t *testing.T) {
	color := GlowColor(0.5)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != 176 {
		t.Fatalf("GlowColor(0.5) red channel = %d, want 176", r)
	}
	if g != 176 {
		t.Fatalf("GlowColor(0.5) green channel = %d, want 176", g)
	}
	if b != 176 {
		t.Fatalf("GlowColor(0.5) blue channel = %d, want 176", b)
	}
}

func TestGlowColor_PhaseHalfGrayscaleEquality_RedGreenBlueIdentical(t *testing.T) {
	color := GlowColor(0.5)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != g || g != b {
		t.Fatalf("GlowColor(0.5) channels not equal: r=%d g=%d b=%d", r, g, b)
	}
}

func TestGlowColor_PhaseOneReturnValue_ExactChannel229(t *testing.T) {
	color := GlowColor(1.0)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != 229 {
		t.Fatalf("GlowColor(1.0) red channel = %d, want 229", r)
	}
	if g != 229 {
		t.Fatalf("GlowColor(1.0) green channel = %d, want 229", g)
	}
	if b != 229 {
		t.Fatalf("GlowColor(1.0) blue channel = %d, want 229", b)
	}
}

func TestGlowColor_PhaseOverflowWrapAround_Phase1Point25EqualsPhase0Point25(t *testing.T) {
	color025 := GlowColor(0.25)
	color125 := GlowColor(1.25)
	if color025 != color125 {
		t.Fatalf("GlowColor(1.25) = %d, want %d (same as GlowColor(0.25))", color125, color025)
	}
}

func TestGlowColor_ChannelUpperOverflowGuard_NeverExceeds255(t *testing.T) {
	color := GlowColor(2.0)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r > 255 {
		t.Fatalf("GlowColor(2.0) red channel = %d, exceeds 255", r)
	}
	if g > 255 {
		t.Fatalf("GlowColor(2.0) green channel = %d, exceeds 255", g)
	}
	if b > 255 {
		t.Fatalf("GlowColor(2.0) blue channel = %d, exceeds 255", b)
	}
}

func TestGlowColor_ChannelLowerUnderflowGuard_NeverBelowZero(t *testing.T) {
	color := GlowColor(-1.0)
	r := int(uint8((color >> 16) & 0xFF))
	g := int(uint8((color >> 8) & 0xFF))
	b := int(uint8(color & 0xFF))
	if r < 0 {
		t.Fatalf("GlowColor(-1.0) red channel = %d, below 0", r)
	}
	if g < 0 {
		t.Fatalf("GlowColor(-1.0) green channel = %d, below 0", g)
	}
	if b < 0 {
		t.Fatalf("GlowColor(-1.0) blue channel = %d, below 0", b)
	}
}

// ── FlashColor Tests ─────────────────────────────────────────────────────────

func TestFlashColor_TimelineStartValue_ElapsedZero_ExactChannel229(t *testing.T) {
	color := FlashColor(0.0)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != 229 {
		t.Fatalf("FlashColor(0.0) red channel = %d, want 229", r)
	}
	if g != 229 {
		t.Fatalf("FlashColor(0.0) green channel = %d, want 229", g)
	}
	if b != 229 {
		t.Fatalf("FlashColor(0.0) blue channel = %d, want 229", b)
	}
}

func TestFlashColor_RampPhaseMonotonicity_IncreasesBetweenZeroAnd150ms(t *testing.T) {
	prev := uint8((FlashColor(0.0) >> 16) & 0xFF)
	for elapsed := 30.0; elapsed < 150.0; elapsed += 30.0 {
		curr := uint8((FlashColor(elapsed) >> 16) & 0xFF)
		if curr <= prev {
			t.Fatalf("FlashColor(%v) red = %d, not > prev %d", elapsed, curr, prev)
		}
		prev = curr
	}
}

func TestFlashColor_TimelinePeakValue_Elapsed150ms_ExactChannel255(t *testing.T) {
	color := FlashColor(150.0)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != 255 {
		t.Fatalf("FlashColor(150.0) red channel = %d, want 255", r)
	}
	if g != 255 {
		t.Fatalf("FlashColor(150.0) green channel = %d, want 255", g)
	}
	if b != 255 {
		t.Fatalf("FlashColor(150.0) blue channel = %d, want 255", b)
	}
}

func TestFlashColor_FadePhaseMonotonicity_DecreasesBetween150And550ms(t *testing.T) {
	prev := uint8((FlashColor(150.0) >> 16) & 0xFF)
	for elapsed := 200.0; elapsed <= 550.0; elapsed += 50.0 {
		curr := uint8((FlashColor(elapsed) >> 16) & 0xFF)
		if curr >= prev {
			t.Fatalf("FlashColor(%v) red = %d, not < prev %d", elapsed, curr, prev)
		}
		prev = curr
	}
}

func TestFlashColor_TimelineReturnValue_Elapsed550ms_ExactChannel229(t *testing.T) {
	color := FlashColor(550.0)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != 229 {
		t.Fatalf("FlashColor(550.0) red channel = %d, want 229", r)
	}
	if g != 229 {
		t.Fatalf("FlashColor(550.0) green channel = %d, want 229", g)
	}
	if b != 229 {
		t.Fatalf("FlashColor(550.0) blue channel = %d, want 229", b)
	}
}

func TestFlashColor_TimelineClampBehavior_Elapsed1000ms_SafelyClampedAtBaseline(t *testing.T) {
	color := FlashColor(1000.0)
	r := uint8((color >> 16) & 0xFF)
	g := uint8((color >> 8) & 0xFF)
	b := uint8(color & 0xFF)
	if r != 229 {
		t.Fatalf("FlashColor(1000.0) red channel = %d, want 229", r)
	}
	if g != 229 {
		t.Fatalf("FlashColor(1000.0) green channel = %d, want 229", g)
	}
	if b != 229 {
		t.Fatalf("FlashColor(1000.0) blue channel = %d, want 229", b)
	}
}
