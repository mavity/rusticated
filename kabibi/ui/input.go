package ui

// Special key constants. Values start at 1000 to avoid clashing with Unicode
// code points that could appear as KeyEvent.Rune.
const (
	KeyUp = iota + 1000
	KeyDown
	KeyRight
	KeyLeft
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeyInsert
	KeyDelete
	KeyBackspace
	KeyEnter
	KeyTab
	KeyEscape
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
)

// parseInputBytes decodes a raw stdin buffer into a slice of KeyEvent and
// MouseEvent values. The parser handles:
//   - Printable UTF-8 runes
//   - ASCII control characters (Ctrl+letter)
//   - Common VT escape sequences (arrows, F-keys, Home/End, PgUp/PgDn, Delete)
//   - X10 mouse events (\x1b[M + 3 bytes)
func parseInputBytes(b []byte) []any {
	var events []any
	for i := 0; i < len(b); {
		switch {
		case b[i] == 0x1b && i+1 < len(b):
			// Escape sequence.
			n, ev := parseEscape(b[i:])
			if ev != nil {
				events = append(events, ev)
			}
			i += n

		case b[i] == 0x1b:
			// Lone ESC key.
			events = append(events, KeyEvent{Key: KeyEscape})
			i++

		case b[i] == 0x7f:
			// DEL / Backspace.
			events = append(events, KeyEvent{Key: KeyBackspace})
			i++

		case b[i] == '\r' || b[i] == '\n':
			events = append(events, KeyEvent{Key: KeyEnter, Rune: '\n'})
			i++

		case b[i] == '\t':
			events = append(events, KeyEvent{Key: KeyTab, Rune: '\t'})
			i++

		case b[i] < 0x20:
			// Ctrl+letter: byte value = letter - 0x40.
			events = append(events, KeyEvent{
				Rune:      rune(b[i] + 0x40),
				Modifiers: ModCtrl,
			})
			i++

		default:
			// UTF-8 rune.
			r, sz := decodeRune(b[i:])
			events = append(events, KeyEvent{Rune: r})
			i += sz
		}
	}
	return events
}

// parseEscape parses one escape sequence starting at b[0] == '\x1b'.
// Returns (bytes consumed, event or nil).
func parseEscape(b []byte) (int, any) {
	if len(b) < 2 {
		return 1, KeyEvent{Key: KeyEscape}
	}

	// X10 mouse: \x1b[M + 3 raw bytes.
	if len(b) >= 6 && b[1] == '[' && b[2] == 'M' {
		cb := b[3]
		cx := int(b[4]) - 32
		cy := int(b[5]) - 32
		btn := cb & 0x03
		var action MouseAction
		switch cb & 0xC0 {
		case 0x40:
			action = MouseWheel
		default:
			if btn == 3 {
				action = MouseRelease
			} else {
				action = MousePress
			}
		}
		mods := parseMouseMods(cb)
		return 6, MouseEvent{X: cx - 1, Y: cy - 1, Button: btn, Action: action, Modifiers: mods}
	}

	// CSI sequences: \x1b[...
	if b[1] == '[' {
		return parseCsi(b)
	}

	// SS3 sequences: \x1bO... (used by some terminals for F1-F4 and arrows)
	if b[1] == 'O' && len(b) >= 3 {
		switch b[2] {
		case 'A':
			return 3, KeyEvent{Key: KeyUp}
		case 'B':
			return 3, KeyEvent{Key: KeyDown}
		case 'C':
			return 3, KeyEvent{Key: KeyRight}
		case 'D':
			return 3, KeyEvent{Key: KeyLeft}
		case 'H':
			return 3, KeyEvent{Key: KeyHome}
		case 'F':
			return 3, KeyEvent{Key: KeyEnd}
		case 'P':
			return 3, KeyEvent{Key: KeyF1}
		case 'Q':
			return 3, KeyEvent{Key: KeyF2}
		case 'R':
			return 3, KeyEvent{Key: KeyF3}
		case 'S':
			return 3, KeyEvent{Key: KeyF4}
		}
		return 3, nil
	}

	// Alt+key: \x1b + printable byte.
	if b[1] >= 0x20 && b[1] < 0x7f {
		r, sz := decodeRune(b[1:])
		return 1 + sz, KeyEvent{Rune: r, Modifiers: ModAlt}
	}

	return 2, nil
}

// parseCsi parses a CSI sequence \x1b[... and returns its key event.
func parseCsi(b []byte) (int, any) {
	// Collect the parameter string up to the final byte.
	end := 2
	for end < len(b) && (b[end] >= 0x30 && b[end] <= 0x3f) {
		end++
	}
	if end >= len(b) {
		return end, nil
	}
	final := b[end]
	param := string(b[2:end])
	total := end + 1

	switch final {
	case 'A':
		return total, KeyEvent{Key: KeyUp}
	case 'B':
		return total, KeyEvent{Key: KeyDown}
	case 'C':
		return total, KeyEvent{Key: KeyRight}
	case 'D':
		return total, KeyEvent{Key: KeyLeft}
	case 'H':
		return total, KeyEvent{Key: KeyHome}
	case 'F':
		return total, KeyEvent{Key: KeyEnd}
	case '~':
		switch param {
		case "1", "7":
			return total, KeyEvent{Key: KeyHome}
		case "2":
			return total, KeyEvent{Key: KeyInsert}
		case "3":
			return total, KeyEvent{Key: KeyDelete}
		case "4", "8":
			return total, KeyEvent{Key: KeyEnd}
		case "5":
			return total, KeyEvent{Key: KeyPageUp}
		case "6":
			return total, KeyEvent{Key: KeyPageDown}
		case "11":
			return total, KeyEvent{Key: KeyF1}
		case "12":
			return total, KeyEvent{Key: KeyF2}
		case "13":
			return total, KeyEvent{Key: KeyF3}
		case "14":
			return total, KeyEvent{Key: KeyF4}
		case "15":
			return total, KeyEvent{Key: KeyF5}
		case "17":
			return total, KeyEvent{Key: KeyF6}
		case "18":
			return total, KeyEvent{Key: KeyF7}
		case "19":
			return total, KeyEvent{Key: KeyF8}
		case "20":
			return total, KeyEvent{Key: KeyF9}
		case "21":
			return total, KeyEvent{Key: KeyF10}
		case "23":
			return total, KeyEvent{Key: KeyF11}
		case "24":
			return total, KeyEvent{Key: KeyF12}
		}
	}

	return total, nil
}

func parseMouseMods(cb byte) KeyModifier {
	var m KeyModifier
	if cb&0x04 != 0 {
		m |= ModShift
	}
	if cb&0x08 != 0 {
		m |= ModAlt
	}
	if cb&0x10 != 0 {
		m |= ModCtrl
	}
	return m
}

// decodeRune decodes the first UTF-8 rune from b. Returns (rune, bytes consumed).
func decodeRune(b []byte) (rune, int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	if c < 0x80 {
		return rune(c), 1
	}
	var size int
	switch {
	case c < 0xE0:
		size = 2
	case c < 0xF0:
		size = 3
	default:
		size = 4
	}
	if len(b) < size {
		return rune(c), 1
	}
	var r rune
	switch size {
	case 2:
		r = rune(c&0x1F)<<6 | rune(b[1]&0x3F)
	case 3:
		r = rune(c&0x0F)<<12 | rune(b[1]&0x3F)<<6 | rune(b[2]&0x3F)
	case 4:
		r = rune(c&0x07)<<18 | rune(b[1]&0x3F)<<12 | rune(b[2]&0x3F)<<6 | rune(b[3]&0x3F)
	}
	return r, size
}
