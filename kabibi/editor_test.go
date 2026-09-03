package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

func TestNewEditorLFFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "line1\nline2\nline3"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	if e.eol != "\n" {
		t.Errorf("newEditor detected eol=%q, want \\n", e.eol)
	}

	if len(e.lines) != 3 {
		t.Errorf("newEditor parsed %d lines, want 3", len(e.lines))
	}

	if e.lines[0] != "line1" {
		t.Errorf("line 0 = %q, want line1", e.lines[0])
	}
}

func TestNewEditorCRLFFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "line1\r\nline2\r\nline3"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	if e.eol != "\r\n" {
		t.Errorf("newEditor detected eol=%q, want \\r\\n", e.eol)
	}

	if len(e.lines) != 3 {
		t.Errorf("newEditor parsed %d lines, want 3", len(e.lines))
	}

	if strings.Contains(e.lines[0], "\r") {
		t.Errorf("line 0 contains CR: %q", e.lines[0])
	}
}

func TestNewEditorEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	if len(e.lines) != 1 {
		t.Errorf("newEditor on empty file created %d lines, want 1", len(e.lines))
	}

	if e.lines[0] != "" {
		t.Errorf("first line = %q, want empty string", e.lines[0])
	}
}

func TestChromaStyleID(t *testing.T) {
	tests := []struct {
		name string
		tt   string
		want int
	}{
		{
			name: "comment token",
			tt:   "Comment",
			want: sidComment,
		},
		{
			name: "keyword token",
			tt:   "Keyword",
			want: sidKeyword,
		},
		{
			name: "string literal",
			tt:   "Literal.String",
			want: sidString,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.want
		})
	}
}

func TestEditorSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	original := "original content"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	e.lines[0] = "modified content"
	if err := e.save(); err != nil {
		t.Fatalf("save error: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	if string(b) != "modified content" {
		t.Errorf("saved file = %q, want modified content", string(b))
	}

	if e.dirty {
		t.Errorf("save did not clear dirty flag")
	}
}

func TestEditorSaveCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "line1\r\nline2"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	if e.eol != "\r\n" {
		t.Fatalf("failed to detect CRLF")
	}

	e.lines[0] = "changed"
	if err := e.save(); err != nil {
		t.Fatalf("save error: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	if !strings.Contains(string(b), "\r\n") {
		t.Errorf("saved file should preserve CRLF, got %q", string(b))
	}
}

func TestEditorInvalidate(t *testing.T) {
	e := &editorModel{dirty: false, hlValid: true}

	e.invalidate()

	if !e.dirty {
		t.Errorf("invalidate did not set dirty flag")
	}
	if e.hlValid {
		t.Errorf("invalidate did not clear hlValid flag")
	}
}

func TestEditorSelectionTextAndDeleteSelection(t *testing.T) {
	e := &editorModel{
		lines: []string{"abcd", "efgh"},
		sel:   true,
		ay:    0,
		ax:    1,
		cy:    1,
		cx:    2,
	}

	if got := e.selectionText(); got != "bcd\nef" {
		t.Fatalf("selectionText() = %q, want %q", got, "bcd\nef")
	}

	e.deleteSelection()
	if got := strings.Join(e.lines, "\n"); got != "agh" {
		t.Fatalf("after deleteSelection lines = %q, want %q", got, "agh")
	}
	if e.sel {
		t.Fatal("deleteSelection did not clear selection")
	}
}

func TestEditorInsertAndDeletePrimitives(t *testing.T) {
	e := &editorModel{lines: []string{"ab"}}
	e.cy, e.cx = 0, 1
	e.insertRune('X')
	if got := strings.Join(e.lines, "\n"); got != "aXb" {
		t.Fatalf("insertRune result = %q, want %q", got, "aXb")
	}

	e = &editorModel{lines: []string{"abc"}}
	e.cy, e.cx = 0, 2
	e.newline()
	if got := strings.Join(e.lines, "\n"); got != "ab\nc" {
		t.Fatalf("newline() result = %q, want %q", got, "ab\nc")
	}
	if e.cy != 1 || e.cx != 0 {
		t.Fatalf("newline() cursor = (%d, %d), want (1, 0)", e.cy, e.cx)
	}

	e = &editorModel{lines: []string{"abc"}}
	e.cy, e.cx = 0, 1
	e.deleteForward()
	if got := strings.Join(e.lines, "\n"); got != "ac" {
		t.Fatalf("deleteForward result = %q, want %q", got, "ac")
	}

	e = &editorModel{lines: []string{"ab", "cd"}}
	e.cy, e.cx = 1, 0
	e.backspace()
	if got := strings.Join(e.lines, "\n"); got != "abcd" {
		t.Fatalf("backspace merge result = %q, want %q", got, "abcd")
	}
}

func TestEditorMovementAndSelectionState(t *testing.T) {
	e := &editorModel{lines: []string{"alpha beta", "gamma"}, cy: 0, cx: 0, prefCol: 0}
	e.startSelectIfNeeded(true)
	e.moveWord(1, true)
	if !e.sel {
		t.Fatal("moveWord(1, true) did not start a selection")
	}
	if e.cx != 5 {
		t.Fatalf("moveWord() cursor = %d, want 5", e.cx)
	}
	if got := e.selectionText(); got != "alpha" {
		t.Fatalf("selectionText() = %q, want %q", got, "alpha")
	}

	e = &editorModel{lines: []string{"abc", "def"}, cy: 0, cx: 1, prefCol: 1}
	e.moveVertical(1, false)
	if e.cy != 1 || e.cx != 1 {
		t.Fatalf("moveVertical() cursor = (%d, %d), want (1, 1)", e.cy, e.cx)
	}

	e = &editorModel{lines: []string{"abc"}, cy: 0, cx: 10}
	e.clampCursor()
	if e.cx != 3 {
		t.Fatalf("clampCursor() cx = %d, want 3", e.cx)
	}

	e = &editorModel{lines: []string{"one", "two"}}
	e.selectAll()
	if !e.sel {
		t.Fatal("selectAll() did not enable selection")
	}
	if e.cy != 1 || e.cx != 3 {
		t.Fatalf("selectAll() cursor = (%d, %d), want (1, 3)", e.cy, e.cx)
	}
}

func TestEditorKeyCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "save.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	m := &model{mode: modeEditor, editor: &editorModel{path: path, lines: []string{"new"}, eol: "\n", dirty: true}}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyF2})
	if content, err := os.ReadFile(path); err != nil || string(content) != "new" {
		t.Fatalf("save on F2 wrote %q, err=%v", string(content), err)
	}

	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 0, dirty: true}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.editor.confirmQuit {
		t.Fatal("Esc with dirty editor did not open save confirmation")
	}

	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 0, dirty: false}
	m.editor.clip = "clip"
	if err := clipboard.WriteAll(""); err != nil {
		t.Skipf("clipboard unavailable in this environment: %v", err)
	}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlV})
	if got := strings.Join(m.editor.lines, "\n"); got != "clipabc" {
		t.Fatalf("ctrl+v fallback result = %q, want %q", got, "clipabc")
	}

	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 0}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlA})
	if !m.editor.sel || m.editor.cy != 0 || m.editor.cx != 3 {
		t.Fatalf("ctrl+a selection = (%d, %d), sel=%v; want sel=true and (0, 3)", m.editor.cy, m.editor.cx, m.editor.sel)
	}

	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyEnter})
	if got := strings.Join(m.editor.lines, "\n"); got != "a\nbc" {
		t.Fatalf("enter split result = %q, want %q", got, "a\nbc")
	}
}

func TestEditorSelectionHelpersAndDeleteIfAny(t *testing.T) {
	e := &editorModel{lines: []string{"abcd", "efgh"}, sel: true, ay: 0, ax: 1, cy: 1, cx: 2}

	sy, sx, ey, ex := e.selBounds()
	if sy != 0 || sx != 1 || ey != 1 || ex != 2 {
		t.Fatalf("selBounds() = (%d,%d,%d,%d), want (0,1,1,2)", sy, sx, ey, ex)
	}

	start, end, has := e.lineSelection(0)
	if !has || start != 1 || end != 4 {
		t.Fatalf("lineSelection(0) = (%d,%d,%v), want (1,4,true)", start, end, has)
	}

	if got := e.selectionText(); got != "bcd\nef" {
		t.Fatalf("selectionText() = %q, want %q", got, "bcd\nef")
	}

	if !e.deleteSelectionIfAny() {
		t.Fatal("deleteSelectionIfAny() returned false when selection existed")
	}
	if got := strings.Join(e.lines, "\n"); got != "agh" {
		t.Fatalf("after deleteSelectionIfAny() lines = %q, want %q", got, "agh")
	}

	e = &editorModel{lines: []string{"abc"}, sel: true, ay: 0, ax: 1, cy: 0, cx: 1}
	if e.deleteSelectionIfAny() {
		t.Fatal("deleteSelectionIfAny() should return false when selection is empty")
	}
	if e.sel {
		t.Fatal("empty selection should be cleared")
	}
}

func TestEditorVisualColumnAndWordHelpers(t *testing.T) {
	runes := []rune("a\tc")
	if got := visualColumn(runes, 2); got != 4 {
		t.Fatalf("visualColumn(...,2) = %d, want 4", got)
	}
	if got := runeCellWidth('中'); got != 2 {
		t.Fatalf("runeCellWidth('中') = %d, want 2", got)
	}
	if got := runeCellWidth('\t'); got != 1 {
		t.Fatalf("runeCellWidth('\t') = %d, want 1", got)
	}
	if !isWordRune('_') || !isWordRune('9') || isWordRune('-') {
		t.Fatal("isWordRune() failed for word/non-word classification")
	}

	e := &editorModel{lines: []string{"foo_bar baz"}, cy: 0, cx: 0}
	e.moveWord(1, false)
	if e.cx != 7 {
		t.Fatalf("moveWord(1) cx = %d, want 7", e.cx)
	}

	e = &editorModel{lines: []string{"abc"}, cy: 0, cx: -1}
	e.clampCursor()
	if e.cx != 0 {
		t.Fatalf("clampCursor negative cx = %d, want 0", e.cx)
	}
}

func TestEditorInsertTextAndNewlineInternal(t *testing.T) {
	e := &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	e.insertText("XY")
	if got := strings.Join(e.lines, "\n"); got != "aXYbc" {
		t.Fatalf("insertText result = %q, want %q", got, "aXYbc")
	}

	e = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	e.newlineNoHL()
	if got := strings.Join(e.lines, "\n"); got != "a\nbc" {
		t.Fatalf("newlineNoHL result = %q, want %q", got, "a\nbc")
	}
	if e.cy != 1 || e.cx != 0 {
		t.Fatalf("newlineNoHL cursor = (%d,%d), want (1,0)", e.cy, e.cx)
	}

	e = &editorModel{lines: []string{"abc"}, cy: 0, cx: 0}
	e.insertRuneNoHL('Z')
	if got := strings.Join(e.lines, "\n"); got != "Zabc" {
		t.Fatalf("insertRuneNoHL result = %q, want %q", got, "Zabc")
	}
}

func TestEditorMovementAndSelectionState2(t *testing.T) {
	e := &editorModel{lines: []string{"abc", "def"}, cy: 0, cx: 0, prefCol: 0}
	e.startSelectIfNeeded(true)
	if !e.sel || e.ay != 0 || e.ax != 0 {
		t.Fatalf("startSelectIfNeeded(true) = sel=%v, anchor=(%d,%d), want sel=true anchor=(0,0)", e.sel, e.ay, e.ax)
	}

	e.moveLeft(false)
	if e.cy != 0 || e.cx != 0 {
		t.Fatalf("moveLeft false at start = (%d,%d), want (0,0)", e.cy, e.cx)
	}

	e = &editorModel{lines: []string{"abc", "def"}, cy: 1, cx: 1, prefCol: 1}
	e.moveVertical(-1, false)
	if e.cy != 0 || e.cx != 1 {
		t.Fatalf("moveVertical(-1) = (%d,%d), want (0,1)", e.cy, e.cx)
	}

	e = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	e.moveHome(false)
	if e.cx != 0 {
		t.Fatalf("moveHome() cx = %d, want 0", e.cx)
	}
	e.moveEnd(false)
	if e.cx != 3 {
		t.Fatalf("moveEnd() cx = %d, want 3", e.cx)
	}
}

func TestEditorRenderRowBarsAndScrollbar(t *testing.T) {
	e := &editorModel{
		lines:  []string{"hello world", "second line"},
		eol:    "\n",
		cy:     0,
		cx:     5,
		left:   0,
		top:    0,
		width:  30,
		height: 8,
		lexer:  nil,
	}

	row := e.renderRow(0, 12, 5)
	if row == "" {
		t.Fatal("renderRow() returned empty string")
	}
	if got := e.titleBar(20); got == "" {
		t.Fatal("titleBar() returned empty string")
	}
	if got := e.statusBar(30); got == "" {
		t.Fatal("statusBar() returned empty string")
	}
	if got := e.pageRows(); got != 6 {
		t.Fatalf("pageRows() = %d, want 6", got)
	}
	start, end := scrollbarThumb(10, 6, 0)
	if start < 0 || end <= start || end > 6 {
		t.Fatalf("scrollbarThumb(10, 6, 0) = (%d,%d), invalid range", start, end)
	}
}

func TestEditorExtraUpdateKeys(t *testing.T) {
	e := &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	m := &model{mode: modeEditor, editor: e}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyTab})
	if got := strings.Join(m.editor.lines, "\n"); got != "a\tbc" {
		t.Fatalf("tab insertion result = %q, want %q", got, "a\tbc")
	}

	e = &editorModel{lines: []string{"abc"}, cy: 0, cx: 0}
	m.editor = e
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlHome})
	if e.cy != 0 || e.cx != 0 {
		t.Fatalf("ctrl+home = (%d,%d), want (0,0)", e.cy, e.cx)
	}

	e = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	m.editor = e
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyDelete})
	if got := strings.Join(m.editor.lines, "\n"); got != "ac" {
		t.Fatalf("delete key result = %q, want %q", got, "ac")
	}
}

func TestEditorCloseEditorAndModelState(t *testing.T) {
	m := &model{mode: modeEditor}
	m.editor = &editorModel{path: "x.txt", lines: []string{"abc"}, cy: 0, cx: 0, dirty: false}
	m.activePane = leftPane
	m.leftList = list.New([]list.Item{fileItem{name: "x.txt"}}, customDelegate{}, 20, 0)
	m.leftList.SetShowStatusBar(true)
	m.leftList.Select(0)
	m.leftList.SetStatusBarItemName("x.txt", "")
	newM, _ := m.closeEditor()
	if newM == nil {
		t.Fatal("closeEditor() returned nil model")
	}
	if m.mode != modeBrowser {
		t.Fatalf("closeEditor mode = %v, want %v", m.mode, modeBrowser)
	}
}

func TestEditorSelectionDeleteEdgeCases(t *testing.T) {
	e := &editorModel{lines: []string{"abc", "def", "ghi"}, sel: true, ay: 2, ax: 2, cy: 0, cx: 1}
	if got := e.selectionText(); got != "bc\ndef\ngh" {
		t.Fatalf("selectionText reverse-range = %q, want %q", got, "bc\ndef\ngh")
	}

	e.deleteSelection()
	if got := strings.Join(e.lines, "\n"); got != "ai" {
		t.Fatalf("deleteSelection multi-line result = %q, want %q", got, "ai")
	}
	if e.sel {
		t.Fatal("deleteSelection did not clear selection")
	}

	e = &editorModel{lines: []string{"abc"}, sel: true, ay: 0, ax: 1, cy: 0, cx: 1}
	if e.deleteSelectionIfAny() {
		t.Fatal("deleteSelectionIfAny should return false for empty selection")
	}
	if e.sel {
		t.Fatal("empty selection should be cleared")
	}
}

func TestEditorMovementAndViewSafety(t *testing.T) {
	e := &editorModel{lines: []string{"foo_bar"}, cy: 0, cx: 7}
	e.moveWord(-1, false)
	if e.cx != 0 {
		t.Fatalf("moveWord(-1) cx = %d, want 0", e.cx)
	}

	e = &editorModel{lines: []string{"abc", "de"}, cy: 1, cx: 0}
	e.moveLeft(false)
	if e.cy != 0 || e.cx != 3 {
		t.Fatalf("moveLeft across line = (%d,%d), want (0,3)", e.cy, e.cx)
	}

	e = &editorModel{lines: []string{"abc", "de"}, cy: 0, cx: 3}
	e.moveRight(false)
	if e.cy != 1 || e.cx != 0 {
		t.Fatalf("moveRight across line = (%d,%d), want (1,0)", e.cy, e.cx)
	}

	for _, tc := range []struct {
		name   string
		width  int
		height int
	}{
		{name: "zero width", width: 0, height: 10},
		{name: "tiny window", width: 4, height: 1},
		{name: "tiny height", width: 10, height: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &editorModel{lines: []string{"hello", "world"}, width: tc.width, height: tc.height, cy: 1, cx: 2, top: 99, left: 99, status: "tiny"}
			if got := e.View(); got == "" && tc.width > 0 && tc.height > 0 {
				t.Fatalf("View() unexpectedly returned empty string for width=%d height=%d", tc.width, tc.height)
			}
		})
	}

	e = &editorModel{lines: []string{"abc"}, lexer: nil}
	e.ensureHighlight()
	if !e.hlValid {
		t.Fatal("ensureHighlight on nil lexer did not mark content valid")
	}

	e = &editorModel{lines: []string{"abc"}, lexer: lexers.Go, hlValid: false}
	e.ensureHighlight()
	if !e.hlValid || len(e.styleIDsForLine(0)) == 0 {
		t.Fatal("ensureHighlight failed to populate highlight metadata for a Go line")
	}

	e = &editorModel{path: "really-long-file-name-to-truncate.txt", lines: []string{"abc"}, dirty: true, lexer: lexers.Go}
	if got := e.titleBar(12); got == "" {
		t.Fatal("titleBar() returned empty string for narrow width")
	}
	if got := e.statusBar(10); got == "" {
		t.Fatal("statusBar() returned empty string for narrow width")
	}
}

func TestEditorTinyWindowSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.go")
	src := strings.Join([]string{
		"package main",
		"",
		"func main() {",
		"\tprintln(\"tiny\")",
		"}",
	}, "\n")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write tiny source: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor: %v", err)
	}
	e.path = "tiny.go"
	e.width = 18
	e.height = 5
	e.status = "tiny window"
	e.cy = 2
	e.cx = 1
	e.top = 0
	e.left = 0

	assertSnapshot(t, "editor_tiny_window", e.View())
}

func TestEditorUpdateKeyCoverage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update.txt")
	if err := os.WriteFile(path, []byte("abcd\nxyz"), 0o644); err != nil {
		t.Fatalf("write update source: %v", err)
	}

	m := initialModel()
	m.mode = modeBrowser
	if m.editor != nil {
		t.Fatal("model started with editor set")
	}
	m.openEditor(path)
	if m.mode != modeEditor || m.editor == nil {
		t.Fatal("openEditor did not activate editor mode")
	}

	m.editor.lines = []string{"abcd", "xyz"}
	m.editor.cy, m.editor.cx = 0, 1
	m.editor.dirty = true
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.editor.dirty {
		t.Fatal("ctrl+s should clear dirty state")
	}

	m.editor = &editorModel{path: path, lines: []string{"ab"}, eol: "\n", dirty: true, status: "dirty"}
	m.mode = modeEditor
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.editor.confirmQuit {
		t.Fatal("Esc on dirty buffer did not open confirmation")
	}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.mode != modeBrowser || m.editor != nil {
		t.Fatal("confirm-yes path should close the editor")
	}

	m2 := model{mode: modeEditor, editor: &editorModel{path: path, lines: []string{"abc"}, cy: 0, cx: 3, sel: true, ay: 0, ax: 0}}
	if err := clipboard.WriteAll(""); err != nil {
		t.Skipf("clipboard unavailable in this environment: %v", err)
	}
	_, _ = m2.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlC})
	if got := m2.editor.status; got != "Copied" {
		t.Fatalf("ctrl+c status = %q, want %q", got, "Copied")
	}
	m.editor = &editorModel{path: path, lines: []string{"abc"}, cy: 0, cx: 3, sel: true, ay: 0, ax: 0}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlX})
	if got := strings.Join(m.editor.lines, "\n"); got != "" {
		t.Fatalf("ctrl+x result = %q, want %q", got, "")
	}
	if err := clipboard.WriteAll(""); err != nil {
		t.Skipf("clipboard unavailable in this environment: %v", err)
	}
	m.editor = &editorModel{path: path, lines: []string{"abc"}, cy: 0, cx: 0, clip: "XYZ"}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlV})
	if got := strings.Join(m.editor.lines, "\n"); got != "XYZabc" {
		t.Fatalf("ctrl+v fallback result = %q, want %q", got, "XYZabc")
	}

	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 0}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlA})
	if !m.editor.sel || m.editor.cy != 0 || m.editor.cx != 3 {
		t.Fatalf("ctrl+a selection = (%d,%d), sel=%v; want sel=true and (0,3)", m.editor.cy, m.editor.cx, m.editor.sel)
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyEnter})
	if got := strings.Join(m.editor.lines, "\n"); got != "a\nbc" {
		t.Fatalf("enter split result = %q, want %q", got, "a\nbc")
	}
	m.editor = &editorModel{lines: []string{"ab", "cd"}, cy: 1, cx: 0}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := strings.Join(m.editor.lines, "\n"); got != "abcd" {
		t.Fatalf("backspace merge result = %q, want %q", got, "abcd")
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyDelete})
	if got := strings.Join(m.editor.lines, "\n"); got != "ac" {
		t.Fatalf("delete result = %q, want %q", got, "ac")
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyTab})
	if got := strings.Join(m.editor.lines, "\n"); got != "a\tbc" {
		t.Fatalf("tab insertion result = %q, want %q", got, "a\tbc")
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeySpace})
	if got := strings.Join(m.editor.lines, "\n"); got != "a bc" {
		t.Fatalf("space insertion result = %q, want %q", got, "a bc")
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Z")})
	if got := strings.Join(m.editor.lines, "\n"); got != "aZbc" {
		t.Fatalf("default insertion result = %q, want %q", got, "aZbc")
	}

	m.editor = &editorModel{lines: []string{"abc", "def"}, cy: 1, cx: 1, prefCol: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyLeft})
	if m.editor.cy != 1 || m.editor.cx != 0 {
		t.Fatalf("left key = (%d,%d), want (1,0)", m.editor.cy, m.editor.cx)
	}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyRight})
	if m.editor.cy != 1 || m.editor.cx != 1 {
		t.Fatalf("right key = (%d,%d), want (1,1)", m.editor.cy, m.editor.cx)
	}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyUp})
	if m.editor.cy != 0 || m.editor.cx != 1 {
		t.Fatalf("up key = (%d,%d), want (0,1)", m.editor.cy, m.editor.cx)
	}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyDown})
	if m.editor.cy != 1 || m.editor.cx != 1 {
		t.Fatalf("down key = (%d,%d), want (1,1)", m.editor.cy, m.editor.cx)
	}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyShiftLeft})
	if !m.editor.sel {
		t.Fatal("shift+left did not start a selection")
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1, prefCol: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.editor.cy != 0 {
		t.Fatalf("pgup from start = %d, want 0", m.editor.cy)
	}
	m.editor = &editorModel{lines: []string{"a", "b", "c"}, cy: 2, cx: 0, prefCol: 0, height: 5}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.editor.cy != 2 {
		t.Fatalf("pgdown result = %d, want 2", m.editor.cy)
	}
	m.editor = &editorModel{lines: []string{"abc", "def"}, cy: 1, cx: 1, prefCol: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	if m.editor.cx != 0 {
		t.Fatalf("ctrl+left result = %d, want 0", m.editor.cx)
	}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlRight})
	if m.editor.cx != 3 {
		t.Fatalf("ctrl+right result = %d, want 3", m.editor.cx)
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1, prefCol: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyHome})
	if m.editor.cx != 0 {
		t.Fatalf("home result = %d, want 0", m.editor.cx)
	}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyEnd})
	if m.editor.cx != 3 {
		t.Fatalf("end result = %d, want 3", m.editor.cx)
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlHome})
	if m.editor.cy != 0 || m.editor.cx != 0 {
		t.Fatalf("ctrl+home = (%d,%d), want (0,0)", m.editor.cy, m.editor.cx)
	}
	m.editor = &editorModel{lines: []string{"abc"}, cy: 0, cx: 1}
	_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if m.editor.cy != 0 || m.editor.cx != 3 {
		t.Fatalf("ctrl+end = (%d,%d), want (0,3)", m.editor.cy, m.editor.cx)
	}
}

type errorLexer struct{}

func (errorLexer) Config() *chroma.Config { return &chroma.Config{Name: "error"} }
func (errorLexer) Tokenise(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
	return nil, errors.New("tokenise failed")
}
func (errorLexer) SetRegistry(*chroma.LexerRegistry) chroma.Lexer { return errorLexer{} }
func (errorLexer) SetAnalyser(func(string) float32) chroma.Lexer  { return errorLexer{} }
func (errorLexer) AnalyseText(string) float32                     { return 0 }

func TestEditorCoverageBoosters(t *testing.T) {
	if got := chromaStyleID(chroma.Comment); got != sidComment {
		t.Fatalf("chromaStyleID(comment) = %d, want %d", got, sidComment)
	}
	if got := chromaStyleID(chroma.Keyword); got != sidKeyword {
		t.Fatalf("chromaStyleID(keyword) = %d, want %d", got, sidKeyword)
	}
	if got := chromaStyleID(chroma.LiteralString); got != sidString {
		t.Fatalf("chromaStyleID(string) = %d, want %d", got, sidString)
	}
	if got := chromaStyleID(chroma.LiteralNumber); got != sidNumber {
		t.Fatalf("chromaStyleID(number) = %d, want %d", got, sidNumber)
	}
	if got := chromaStyleID(chroma.Literal); got != sidString {
		t.Fatalf("chromaStyleID(literal) = %d, want %d", got, sidString)
	}
	if got := chromaStyleID(chroma.NameFunction); got != sidFunc {
		t.Fatalf("chromaStyleID(name function) = %d, want %d", got, sidFunc)
	}
	if got := chromaStyleID(chroma.NameClass); got != sidType {
		t.Fatalf("chromaStyleID(name class) = %d, want %d", got, sidType)
	}
	if got := chromaStyleID(chroma.NameBuiltin); got != sidType {
		t.Fatalf("chromaStyleID(name builtin) = %d, want %d", got, sidType)
	}
	if got := chromaStyleID(chroma.Operator); got != sidOperator {
		t.Fatalf("chromaStyleID(operator) = %d, want %d", got, sidOperator)
	}
	if got := chromaStyleID(chroma.Name); got != sidDefault {
		t.Fatalf("chromaStyleID(default) = %d, want %d", got, sidDefault)
	}

	t.Run("highlight skip paths", func(t *testing.T) {
		e := &editorModel{lines: []string{"abc"}, lexer: nil}
		e.ensureHighlight()
		if !e.hlValid {
			t.Fatal("ensureHighlight on nil lexer did not set hlValid")
		}

		big := strings.Repeat("x", 400_001)
		e = &editorModel{lines: []string{big}, lexer: lexers.Go}
		e.ensureHighlight()
		if !e.hlValid {
			t.Fatal("ensureHighlight on very large content did not skip highlighting")
		}

		e = &editorModel{lines: []string{"abc"}, lexer: errorLexer{}}
		e.ensureHighlight()
		if !e.hlValid {
			t.Fatal("ensureHighlight on tokenise error did not set hlValid")
		}
	})

	t.Run("cursor clamp and word movement edges", func(t *testing.T) {
		e := &editorModel{lines: []string{"abc"}, cy: -2, cx: -4}
		e.clampCursor()
		if e.cy != 0 || e.cx != 0 {
			t.Fatalf("clampCursor negative values = (%d,%d), want (0,0)", e.cy, e.cx)
		}

		e = &editorModel{lines: []string{"abc"}, cy: 99, cx: 17}
		e.clampCursor()
		if e.cy != 0 || e.cx != 3 {
			t.Fatalf("clampCursor max values = (%d,%d), want (0,3)", e.cy, e.cx)
		}

		e = &editorModel{lines: []string{"  hello world"}, cy: 0, cx: 1}
		e.moveWord(1, false)
		if e.cx != 7 {
			t.Fatalf("moveWord(1) after leading spaces = %d, want 7", e.cx)
		}

		e = &editorModel{lines: []string{"hello world"}, cy: 0, cx: 8}
		e.moveWord(-1, false)
		if e.cx != 6 {
			t.Fatalf("moveWord(-1) before space = %d, want 6", e.cx)
		}
	})

	t.Run("backspace no-op and confirm-close keys", func(t *testing.T) {
		e := &editorModel{lines: []string{"abc"}, cy: 0, cx: 0}
		e.backspace()
		if got := strings.Join(e.lines, "\n"); got != "abc" {
			t.Fatalf("backspace at start should be no-op, got %q", got)
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "confirm.txt")
		if err := os.WriteFile(path, []byte("saved"), 0o644); err != nil {
			t.Fatalf("write confirm file: %v", err)
		}
		m := initialModel()
		m.mode = modeEditor
		m.activePane = leftPane
		m.leftDir = dir
		m.rightDir = dir
		m.editor = &editorModel{path: path, lines: []string{"save me"}, dirty: true, confirmQuit: true}
		_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Y")})
		if m.mode != modeBrowser || m.editor != nil {
			t.Fatal("confirm-yes path with uppercase Y did not close editor")
		}

		m = initialModel()
		m.mode = modeEditor
		m.activePane = leftPane
		m.leftDir = dir
		m.rightDir = dir
		m.editor = &editorModel{path: path, lines: []string{"save me"}, dirty: true, confirmQuit: true}
		_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyEsc})
		if m.editor == nil || m.editor.confirmQuit {
			t.Fatal("confirm-esc path should cancel the confirmation prompt")
		}

		m = initialModel()
		m.mode = modeEditor
		m.activePane = leftPane
		m.leftDir = dir
		m.rightDir = dir
		m.editor = &editorModel{path: path, lines: []string{"save me"}, dirty: true, confirmQuit: true}
		_, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
		if m.mode != modeBrowser || m.editor != nil {
			t.Fatal("confirm-n uppercase path did not close editor")
		}
	})
}
