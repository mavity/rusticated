package filemanager

import "github.com/charmbracelet/x/input"

var macOSABCGlyphs = map[rune]rune{
	'¡': '1',
	'™': '2',
	'£': '3',
	'¢': '4',
	'∞': '5',
	'§': '6',
	'¶': '7',
	'•': '8',
	'ª': '9',
	'º': '0',
	'œ': 'q',
	'∑': 'w',
	'®': 'r',
	'†': 't',
	'¥': 'y',
	'ø': 'o',
	'π': 'p',
	'å': 'a',
	'ß': 's',
	'∂': 'd',
	'ƒ': 'f',
	'©': 'g',
	'˙': 'h',
	'∆': 'j',
	'˚': 'k',
	'¬': 'l',
	'Ω': 'z',
	'≈': 'x',
	'ç': 'c',
	'√': 'v',
	'∫': 'b',
	'µ': 'm',
}

// NormalizeMacOptionGlyph maps unmodified glyphs from macOS's ABC and U.S.
// keyboard layouts to the Alt+base-key events emitted by Kitty terminals.
func NormalizeMacOptionGlyph(ev input.KeyPressEvent, layoutID string) input.KeyPressEvent {
	if layoutID != "com.apple.keylayout.ABC" && layoutID != "com.apple.keylayout.US" {
		return ev
	}
	if ev.Mod != 0 {
		return ev
	}
	text := ev.Text
	if text == "" && ev.Code > 0 {
		text = string(ev.Code)
	}
	runes := []rune(text)
	if len(runes) != 1 {
		return ev
	}
	base, ok := macOSABCGlyphs[runes[0]]
	if !ok {
		return ev
	}
	ev.Code = base
	ev.BaseCode = base
	ev.ShiftedCode = 0
	ev.Mod = input.ModAlt
	ev.Text = string(base)
	return ev
}
