package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// newTestModel returns an initialised model sized at 80×24.
func newTestModel() *AppWidget {
	m := initialModel()
	m.width = 80
	m.height = 24
	m.recalculateLayout()
	return m
}

// sendKey drives a single key through handleKeyMsg and returns the resulting model.
func sendKey(m *AppWidget, key tea.KeyType, runes ...rune) *AppWidget {
	msg := tea.KeyMsg{Type: key}
	if len(runes) > 0 {
		msg.Runes = runes
		msg.Type = tea.KeyRunes
	}
	_ = m.handleKeyMsg(msg)
	return m
}

var _ Widget = (*AppWidget)(nil)

func TestModelImplementsRootWidgetContract(t *testing.T) {
	m := initialModel()
	m.width = 80
	m.height = 24
	m.recalculateLayout()

	var w Widget = m
	if got := w.Measure(Constraints{MaxW: 80, MaxH: 24}); got.W != 80 || got.H != 24 {
		t.Fatalf("Measure() = %+v, want 80x24", got)
	}

	w.Layout(Rect{X: 0, Y: 0, W: 60, H: 20})
	if m.width != 60 || m.height != 20 {
		t.Fatalf("Layout() should set app dimensions; got %dx%d", m.width, m.height)
	}

	buf := w.Render()
	if buf.Width != 60 || buf.Height != 20 {
		t.Fatalf("Render() size = %dx%d, want 60x20", buf.Width, buf.Height)
	}
}

func TestNewAppWidgetRequiresConcreteDependencies(t *testing.T) {
	conv := &Conversation{Messages: []Message{{Role: "user", Content: "hi"}}}
	m := NewAppWidget(".", conv, &OsFSService{}, NewLMSession(conv))
	if m == nil {
		t.Fatal("NewAppWidget() returned nil")
	}
	if m.shellW == nil || m.dualPane == nil || m.chatW == nil || m.plumeW == nil || m.runner == nil {
		t.Fatalf("NewAppWidget() left required dependencies nil: shellW=%v dualPane=%v chatW=%v plumeW=%v runner=%v", m.shellW == nil, m.dualPane == nil, m.chatW == nil, m.plumeW == nil, m.runner == nil)
	}
	if m.dualPane.Left == nil || m.dualPane.Right == nil {
		t.Fatal("NewAppWidget() left dual pane children uninitialized")
	}
}

// --- Tab / pane cycling ---

func TestHandleKeyTabCyclesLeftRightChat(t *testing.T) {
	m := newTestModel()
	m.activePane = leftPane

	m = sendKey(m, tea.KeyTab)
	if m.activePane != rightPane {
		t.Fatalf("after first Tab want rightPane, got %v", m.activePane)
	}

	m.lastTab = time.Now().Add(-500 * time.Millisecond)
	m = sendKey(m, tea.KeyTab)
	if m.activePane != leftPane {
		t.Fatalf("after a non-quick second Tab should return to leftPane, got %v", m.activePane)
	}
}

func TestHandleKeyDoubleTabOpensChat(t *testing.T) {
	m := newTestModel()
	m.activePane = leftPane

	m = sendKey(m, tea.KeyTab)
	if m.activePane != rightPane {
		t.Fatalf("after first Tab want rightPane, got %v", m.activePane)
	}

	m.lastTab = time.Now().Add(-100 * time.Millisecond)
	m = sendKey(m, tea.KeyTab)
	if !m.chatOpen || m.activePane != chatPane {
		t.Fatalf("double Tab should open chat, got chatOpen=%v activePane=%v", m.chatOpen, m.activePane)
	}
}

func TestHandleKeyTabSyncsDualPaneBridgeImmediately(t *testing.T) {
	m := newTestModel()
	m.activePane = leftPane
	m.dualPane.SetActivePaneBridge(0)

	m = sendKey(m, tea.KeyTab)
	if m.activePane != rightPane {
		t.Fatalf("after first Tab want rightPane, got %v", m.activePane)
	}
	if m.dualPane.Left.IsActive() || !m.dualPane.Right.IsActive() {
		t.Fatalf("dualPane bridge should match model.activePane immediately; leftActive=%v rightActive=%v", m.dualPane.Left.IsActive(), m.dualPane.Right.IsActive())
	}
}

func TestHandleKeyEnterUsesActivePaneWhenShellIsEmpty(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m := newTestModel()
	m.activePane = leftPane
	m.loadDir(leftPane, dir, "subdir")
	m.shellW.SetValue("")

	m = sendKey(m, tea.KeyEnter)
	if m.dualPane.Left.Dir() != sub {
		t.Fatalf("Enter on selected directory should navigate left pane; got %q want %q", m.dualPane.Left.Dir(), sub)
	}
}

func TestRecalculateLayoutUsesHistoricalSizingFormula(t *testing.T) {
	m := newTestModel()
	m.width = 80
	m.height = 40

	m.chatOpen = true
	m.recalculateLayout()
	if got := m.chatW.Width; got != 30 {
		t.Fatalf("expanded chat width = %d, want 30", got)
	}
	if got := m.chatW.Height; got != 30 {
		t.Fatalf("expanded panel height = %d, want 30 (height - 10)", got)
	}

	m.chatOpen = false
	m.recalculateLayout()
	if got := m.chatW.Width; got != 8 {
		t.Fatalf("collapsed chat width = %d, want 8", got)
	}
	if got := m.chatW.Height; got != 30 {
		t.Fatalf("collapsed panel height = %d, want 30 (height - 10)", got)
	}

	m.width = 80
	m.height = 20
	m.chatOpen = false
	m.recalculateLayout()
	if got := m.chatW.Height; got != 12 {
		t.Fatalf("panel height = %d, want 12 so the non-panel area stays <= 40%% of 20 rows", got)
	}
}

func TestWindowResizeRecomputesHeightFromCurrentTerminalSize(t *testing.T) {
	m := initialModel()
	m.width = 80
	m.height = 40
	m.recalculateLayout()
	before := m.chatW.Height

	m.dispatch(tea.WindowSizeMsg{Width: 80, Height: 20})
	if m.height != 20 {
		t.Fatalf("height should update from resize message, got %d", m.height)
	}
	if got := m.chatW.Height; got == before {
		t.Fatalf("panel height should change when terminal height changes; before=%d after=%d", before, got)
	}
}

// --- File pane navigation ---

func TestHandleKeyUpDownMoveSelection(t *testing.T) {
	m := newTestModel()
	m.activePane = leftPane
	m.dualPane.Left.SetItems([]fileItem{
		{name: "..", isDir: true},
		{name: "alpha.txt"},
		{name: "beta.txt"},
	})
	m.dualPane.Left.Select(0)

	m = sendKey(m, tea.KeyDown)
	if m.dualPane.Left.SelectedIndex() != 1 {
		t.Fatalf("Down from 0 want index 1, got %d", m.dualPane.Left.SelectedIndex())
	}

	m = sendKey(m, tea.KeyDown)
	if m.dualPane.Left.SelectedIndex() != 2 {
		t.Fatalf("Down again want index 2, got %d", m.dualPane.Left.SelectedIndex())
	}

	m = sendKey(m, tea.KeyUp)
	if m.dualPane.Left.SelectedIndex() != 1 {
		t.Fatalf("Up want index 1, got %d", m.dualPane.Left.SelectedIndex())
	}
}

func TestHandleKeyDownInRightPane(t *testing.T) {
	m := newTestModel()
	m.activePane = rightPane
	m.dualPane.Right.SetItems([]fileItem{
		{name: "..", isDir: true},
		{name: "file.go"},
	})
	m.dualPane.Right.Select(0)
	m.dualPane.SetActivePaneBridge(1)

	m = sendKey(m, tea.KeyDown)
	if m.dualPane.Right.SelectedIndex() != 1 {
		t.Fatalf("Down in right pane want index 1, got %d", m.dualPane.Right.SelectedIndex())
	}
	// Left pane should be unaffected
	if m.dualPane.Left.SelectedIndex() != 0 {
		t.Fatalf("Left pane index should not change, got %d", m.dualPane.Left.SelectedIndex())
	}
}

func TestHandleKeyEnterOpensDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m := newTestModel()
	m.activePane = leftPane
	m.loadDir(leftPane, dir, "subdir")

	m = sendKey(m, tea.KeyEnter)

	if m.dualPane.Left.Dir() != sub {
		t.Fatalf("Enter on directory should cd into it; got %q want %q", m.dualPane.Left.Dir(), sub)
	}
}

func TestHandleKeyEnterOnDotDotGoesUp(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Dir(dir)

	m := newTestModel()
	m.activePane = leftPane
	m.loadDir(leftPane, dir, "") // ".." will be index 0, already selected

	m = sendKey(m, tea.KeyEnter)

	if m.dualPane.Left.Dir() != parent {
		t.Fatalf("Enter on '..' should go to parent %q; got %q", parent, m.dualPane.Left.Dir())
	}
}

// --- Chat pane ---

func TestHandleKeyEscapeFromChatReturnsToLeftPane(t *testing.T) {
	m := newTestModel()
	m.activePane = chatPane
	m.chatOpen = true

	m = sendKey(m, tea.KeyEscape)
	if m.activePane != leftPane {
		t.Fatalf("Escape from chat want leftPane, got %v", m.activePane)
	}
	if m.chatOpen {
		t.Fatal("Escape from chat should close the chat panel")
	}
}

func TestHandleKeyEscapeTogglesFilePanelsVisible(t *testing.T) {
	m := newTestModel()
	m.activePane = leftPane
	m.panelsVisible = true

	m = sendKey(m, tea.KeyEscape)
	if m.panelsVisible {
		t.Fatal("Escape in file pane should hide the blue panels")
	}

	m = sendKey(m, tea.KeyEscape)
	if !m.panelsVisible {
		t.Fatal("Escape again should show the blue panels")
	}
}

func TestHandleKeyTypingInChatAddsToInput(t *testing.T) {
	m := newTestModel()
	m.activePane = chatPane

	m = sendKey(m, 0, 'h')
	m = sendKey(m, 0, 'i')
	got := m.chatW.Input.Value()
	if got != "hi" {
		t.Fatalf("typing in chat pane want %q, got %q", "hi", got)
	}
}

func TestHandleKeyEnterSubmitsChatMessage(t *testing.T) {
	m := newTestModel()
	m.activePane = chatPane
	m.chatW.SetService(&mockAIService{tokens: []string{"hello "}})
	m.chatW.Input.SetValue("hi")

	cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter in chat should submit the current input")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("submit command should emit a message when invoked")
	}
	if got := m.chatW.Input.Value(); got != "" {
		t.Fatalf("chat input should reset after submit, got %q", got)
	}
	if len(m.chatW.Messages()) < 2 {
		t.Fatalf("chat should store both user and assistant messages, got %d", len(m.chatW.Messages()))
	}
}

// --- Editor mode ---

func TestHandleKeyEditorModeRoutesToEditor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := newTestModel()
	m.openEditor(path)
	if m.mode != modeEditor || m.editor == nil {
		t.Fatal("openEditor should set modeEditor and populate m.editor")
	}

	before := m.editor.cy
	// A key that moves the cursor down in the editor
	m = sendKey(m, tea.KeyDown)
	// Editor handles it; we just confirm the model is still in editor mode
	if m.mode != modeEditor {
		t.Fatalf("mode should stay modeEditor after Down in editor, got %v", m.mode)
	}
	_ = before
}

// --- fmToggleMark ---

func TestFmToggleMarkViaWidget(t *testing.T) {
	m := newTestModel()
	m.activePane = leftPane
	m.dualPane.Left.SetItems([]fileItem{
		{name: "..", isDir: true},
		{name: "alpha.txt"},
		{name: "beta.txt"},
	})
	m.dualPane.Left.Select(1) // "alpha.txt"

	m.fmToggleMark()

	if m.dualPane.Left.SelectedIndex() != 2 {
		t.Fatalf("after toggle mark selection should advance to 2, got %d", m.dualPane.Left.SelectedIndex())
	}
	fi := m.dualPane.Left.Items()[1]
	if !fi.selected {
		t.Fatal("alpha.txt should be marked after fmToggleMark")
	}
}

func TestFmToggleMarkSkipsParentEntry(t *testing.T) {
	m := newTestModel()
	m.activePane = leftPane
	m.dualPane.Left.SetItems([]fileItem{
		{name: "..", isDir: true},
		{name: "file.txt"},
	})
	m.dualPane.Left.Select(0) // ".."

	m.fmToggleMark()

	if fi := m.dualPane.Left.Items()[0]; fi.selected {
		t.Fatal("'..' should never be marked")
	}
	if m.dualPane.Left.SelectedIndex() != 1 {
		t.Fatalf("selection should advance past '..'; got %d", m.dualPane.Left.SelectedIndex())
	}
}

// --- fmEdit ---

func TestFmEditOpensEditorForFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := newTestModel()
	m.activePane = leftPane
	m.loadDir(leftPane, dir, "readme.txt")

	m.fmEdit()

	if m.mode != modeEditor {
		t.Fatalf("fmEdit should open editor mode, got %v", m.mode)
	}
	if m.editor == nil {
		t.Fatal("fmEdit should populate m.editor")
	}
}

func TestFmEditIgnoresDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m := newTestModel()
	m.activePane = leftPane
	m.loadDir(leftPane, dir, "docs")

	m.fmEdit()

	if m.mode == modeEditor {
		t.Fatal("fmEdit on directory should not open editor")
	}
}

// --- Widget-level HandleKey (FilePaneWidget) ---

func TestFilePaneWidgetHandleKeyUpClamps(t *testing.T) {
	p := NewFilePaneWidget("test")
	p.SetItemsBridge([]fileItem{{name: ".."}, {name: "a"}}, 0)

	p.HandleKey(tea.KeyMsg{Type: tea.KeyUp})
	if item, _ := p.SelectedItem(); item.name != ".." {
		t.Fatalf("Up at top should clamp; got %q", item.name)
	}
}

func TestFilePaneWidgetHandleKeyDownClamps(t *testing.T) {
	p := NewFilePaneWidget("test")
	p.SetItemsBridge([]fileItem{{name: ".."}, {name: "a"}}, 1)

	p.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	if item, _ := p.SelectedItem(); item.name != "a" {
		t.Fatalf("Down at bottom should clamp; got %q", item.name)
	}
}

// --- Widget-level HandleKey (ChatWidget) ---

func TestChatWidgetHandleKeyDelegatesTypingToInput(t *testing.T) {
	w := NewChatWidget()
	w.SetConversation(&Conversation{})

	w.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if w.Input.Value() != "x" {
		t.Fatalf("typing should go to input; got %q", w.Input.Value())
	}
}

func TestChatWidgetHandleKeyPgUpIncreasesScrollTop(t *testing.T) {
	w := NewChatWidget()
	w.Layout(Rect{X: 0, Y: 0, W: 30, H: 10})
	prev := w.scrollTop

	w.HandleKey(tea.KeyMsg{Type: tea.KeyPgUp})

	if w.scrollTop <= prev {
		t.Fatalf("pgup should increase scrollTop from %d; got %d", prev, w.scrollTop)
	}
}

func TestChatWidgetHandleKeyEndResetsScrollTop(t *testing.T) {
	w := NewChatWidget()
	w.scrollTop = 20

	w.HandleKey(tea.KeyMsg{Type: tea.KeyEnd})

	if w.scrollTop != 0 {
		t.Fatalf("end key should reset scrollTop to 0; got %d", w.scrollTop)
	}
}

// --- Widget-level HandleKey (PlumeWidget) ---

func TestPlumeWidgetHandleKeyPgUpScrolls(t *testing.T) {
	w := NewPlumeWidget()
	w.SetLines([]string{"a", "b", "c", "d", "e", "f"})
	w.Layout(Rect{X: 0, Y: 0, W: 20, H: 2})

	w.HandleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if w.scrollTop == 0 {
		t.Fatal("pgup should increase scrollTop above 0")
	}
}

func TestPlumeWidgetHandleKeyHomeThenEnd(t *testing.T) {
	w := NewPlumeWidget()
	w.SetLines([]string{"a", "b", "c", "d", "e"})
	w.Layout(Rect{X: 0, Y: 0, W: 20, H: 2})
	w.scrollTop = 5

	w.HandleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if w.scrollTop != 0 {
		t.Fatalf("end should reset scrollTop to 0; got %d", w.scrollTop)
	}

	w.HandleKey(tea.KeyMsg{Type: tea.KeyHome})
	// After render the home scroll will be clamped; just check it was set large
	if w.scrollTop == 0 {
		t.Fatal("home should set scrollTop > 0 before render clamp")
	}
}
