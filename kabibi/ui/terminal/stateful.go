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
	t.initEmulator()
	return t
}

func (t *Terminal) initEmulator() {
	t.emulator = vt.NewEmulator(t.width, t.height)
	t.emulator.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			t.cursorVisible = visible
		},
		AltScreen: func(active bool) {
			t.alternate = active
		},
	})
	_, _ = t.emulator.WriteString(ansi.SetMode(ansi.ModeUnicodeCore) + ansi.SetMode(ansi.ModeLineFeedNewLine))
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

// Reset starts a fresh frame at the requested size while retaining the emulator
// and its parser/screen allocations.
func (t *Terminal) Reset(width, height int) {
	if t == nil {
		return
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	if t.emulator == nil {
		t.width, t.height = width, height
		t.cursorVisible = true
		t.initEmulator()
	}
	if width != t.width || height != t.height {
		t.emulator.Resize(width, height)
	}
	t.width, t.height = width, height
	t.cursorVisible = false
	t.alternate = false
	t.retainedHeight = 0
	_, _ = t.emulator.WriteString(
		"\x18\x1bc" +
			ansi.SetMode(ansi.ModeUnicodeCore) +
			ansi.SetMode(ansi.ModeLineFeedNewLine) +
			"\x1b[?25l",
	)
	t.emulator.ClearScrollback()
}

// Size returns the current terminal dimensions.
func (t *Terminal) Size() (width, height int) {
	if t == nil {
		return 0, 0
	}
	return t.width, t.height
}

// RenderANSI interprets one widget frame in a clean, fixed-size terminal and
// returns the visible viewport and its cursor. Unlike Snapshot, the returned
// buffer is always exactly width by height and excludes scrollback.
func (t *Terminal) RenderANSI(text string, width, height int) (CellBuf, CursorPos, error) {
	buf, cursor, _, err := t.RenderANSIWithExtent(text, width, height)
	return buf, cursor, err
}

// RenderANSIWithExtent interprets one widget frame in a clean, fixed-size
// terminal and also reports the rendered extent before the unused viewport.
func (t *Terminal) RenderANSIWithExtent(text string, width, height int) (CellBuf, CursorPos, int, error) {
	if t == nil {
		return CellBuf{}, CursorPos{}, 0, nil
	}
	t.Reset(width, height)
	if _, err := t.WriteString(text); err != nil {
		return CellBuf{}, CursorPos{}, 0, err
	}
	extent := calculateGridHeight(t.emulator)
	if extent < t.retainedHeight {
		extent = t.retainedHeight
	}
	if extent > t.height {
		extent = t.height
	}
	cursor := t.viewportCursor()
	if cursor.Y+1 > extent {
		extent = cursor.Y + 1
	}
	if extent > t.height {
		extent = t.height
	}
	return t.ViewportSnapshot(), cursor, extent, nil
}

// RenderANSIForComposition parses a frame with one spare column to avoid
// terminal-emulator wide-glyph loss at the right margin, then returns the
// requested viewport width.
func (t *Terminal) RenderANSIForComposition(text string, width, height int) (CellBuf, CursorPos, int, error) {
	if width <= 0 {
		return CellBuf{}, CursorPos{}, 0, nil
	}
	frame, cursor, extent, err := t.RenderANSIWithExtent(text, width+1, height)
	if err != nil {
		return CellBuf{}, CursorPos{}, 0, err
	}
	if extent > height {
		extent = height
	}
	cropped := NewCellBuf(width, height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cropped.Set(x, y, frame.Get(x, y))
		}
	}
	if cursor.X >= width {
		cursor.X = width - 1
	}
	return cropped, cursor, extent, nil
}

// ViewportSnapshot returns exactly the emulator's visible screen, excluding
// scrollback retained by Snapshot.
func (t *Terminal) ViewportSnapshot() CellBuf {
	if t == nil || t.emulator == nil {
		return CellBuf{}
	}
	buf := NewCellBuf(t.width, t.height)
	for y := 0; y < t.height; y++ {
		for x := 0; x < t.width; x++ {
			buf.Set(x, y, convertUVCellToCell(t.emulator.CellAt(x, y)))
		}
	}
	return buf
}

func (t *Terminal) viewportCursor() CursorPos {
	if t == nil || t.emulator == nil {
		return CursorPos{}
	}
	pos := t.emulator.CursorPosition()
	return CursorPos{X: pos.X, Y: pos.Y, Visible: t.cursorVisible}
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
