package terminal

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Terminal is a stateful virtual terminal. Its owner must serialize all access;
// the underlying emulator is not safe for concurrent use.
type Terminal struct {
	emulator       *vt.Emulator
	width          int
	height         int
	cursorVisible  bool
	alternate      bool
	retainedHeight int
}

// NewTerminal creates a stateful terminal with LNM and Unicode-core modes enabled.
func NewTerminal(width, height int) *Terminal {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	t := &Terminal{
		width:         width,
		height:        height,
		cursorVisible: true,
	}
	t.emulator = vt.NewEmulator(width, height)
	t.emulator.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			t.cursorVisible = visible
		},
		AltScreen: func(active bool) {
			t.alternate = active
		},
	})
	_, _ = t.emulator.WriteString(ansi.SetMode(ansi.ModeUnicodeCore) + ansi.SetMode(ansi.ModeLineFeedNewLine))
	return t
}

// Write feeds bytes unchanged to the emulator.
func (t *Terminal) Write(p []byte) (int, error) {
	if t == nil || t.emulator == nil {
		return 0, nil
	}
	n, err := t.emulator.Write(p)
	t.retainSnapshotHeight()
	return n, err
}

// WriteString feeds the string unchanged to the emulator.
func (t *Terminal) WriteString(s string) (int, error) {
	if t == nil || t.emulator == nil {
		return 0, nil
	}
	n, err := t.emulator.WriteString(s)
	t.retainSnapshotHeight()
	return n, err
}

func (t *Terminal) retainSnapshotHeight() {
	// Resize can clear the emulator's touched-row metadata while retaining its cells.
	if height := calculateGridHeight(t.emulator); height > t.retainedHeight {
		t.retainedHeight = height
	}
}

// Resize changes the terminal dimensions without replaying prior output.
func (t *Terminal) Resize(width, height int) {
	if t == nil || t.emulator == nil {
		return
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	if width == t.width && height == t.height {
		return
	}
	t.retainSnapshotHeight()
	t.width, t.height = width, height
	t.emulator.Resize(width, height)
}

// Size returns the current terminal dimensions.
func (t *Terminal) Size() (width, height int) {
	if t == nil {
		return 0, 0
	}
	return t.width, t.height
}

// IsAltScreen reports whether the emulator is currently displaying its alternate screen.
func (t *Terminal) IsAltScreen() bool {
	return t != nil && t.alternate
}

// ScrollbackLen returns the number of lines currently retained in scrollback.
func (t *Terminal) ScrollbackLen() int {
	if t == nil || t.emulator == nil {
		return 0
	}
	return t.emulator.ScrollbackLen()
}

// Snapshot returns the current terminal contents and cursor position.
func (t *Terminal) Snapshot() (CellBuf, CursorPos) {
	if t == nil || t.emulator == nil {
		return CellBuf{}, CursorPos{}
	}
	height := calculateGridHeight(t.emulator)
	if height < t.retainedHeight {
		height = t.retainedHeight
	}
	pos := t.emulator.CursorPosition()
	cur := CursorPos{
		X:       pos.X,
		Y:       t.emulator.ScrollbackLen() + pos.Y,
		Visible: t.cursorVisible,
	}
	if height == 0 {
		return NewCellBuf(t.width, 0), cur
	}
	return populateCellBufDirect(t.emulator, t.width, height), cur
}
