package ui

import (
	"math"

	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

var (
	FilePanelStyle        = terminal.NewStyle(terminal.Color(0x0000A8), terminal.Color(0xE5E5E5), 0)
	FilePanelActiveStyle  = terminal.NewStyle(terminal.Color(0xFFD700), terminal.Color(0x0000A8), 0)
	FolderStyle           = terminal.NewStyle(terminal.Color(0xE5E5E5), terminal.Color(0x0000A8), 0)
	FileStyle             = terminal.NewStyle(terminal.Color(0x00D9FF), terminal.Color(0x0000A8), 0)
	SelectedFileStyle     = terminal.NewStyle(terminal.Color(0x000080), terminal.Color(0xFFD700), 0)
	SelectedFolderStyle   = terminal.NewStyle(terminal.Color(0x3A3A3A), terminal.Color(0xFFD700), 0)
	SelectedStyle         = SelectedFileStyle // fallback
	MarkedStyle           = terminal.NewStyle(terminal.Color(0xFFD700), terminal.Color(0x0000A8), terminal.CharBold)
	ActiveTitleStyle      = terminal.NewStyle(terminal.Color(0x3A3A3A), terminal.Color(0xFFD700), terminal.CharBold)
	InactiveSelectedStyle = terminal.NewStyle(terminal.Color(0xE5E5E5), terminal.Color(0x0000A8), 0)
	ChatStyle             = terminal.NewStyle(terminal.Color(0xE5E5E5), terminal.Color(0x3A3A3A), 0)
	PlumeStyle            = terminal.NewStyle(0, 0, 0)
	PromptStyle           = terminal.NewStyle(0, 0, 0)
)

// GlowColor returns an interpolated color for the breathing glow animation.
// phase is 0..1 within one oscillation period; the color dips from White down
// to GlowDim at phase 0.5 and back.
func GlowColor(phase float64) terminal.Color {
	t := 0.5 * (1.0 - math.Cos(2*math.Pi*phase)) // 0..1..0
	v := int(229.0 - t*53.0)                     // #E5E5E5 (229) → #B0B0B0 (176)
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return terminal.Color(uint32(v)<<16 | uint32(v)<<8 | uint32(v))
}

// FlashColor returns an interpolated color for the completion flash.
// elapsedMs is time since flash started; jumps to FlashHi then fades back.
func FlashColor(elapsedMs float64) terminal.Color {
	if elapsedMs < 150 {
		t := elapsedMs / 150.0
		v := int(229.0 + t*26.0) // #E5E5E5 → #FFFFFF
		if v > 255 {
			v = 255
		}
		return terminal.Color(uint32(v)<<16 | uint32(v)<<8 | uint32(v))
	}
	t := (elapsedMs - 150.0) / 400.0 // fade back over 400ms
	if t > 1.0 {
		t = 1.0
	}
	v := int(255.0 - t*26.0) // #FFFFFF → #E5E5E5
	if v < 0 {
		v = 0
	}
	return terminal.Color(uint32(v)<<16 | uint32(v)<<8 | uint32(v))
}
