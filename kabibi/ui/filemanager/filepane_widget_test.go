package filemanager

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type mockFSService struct {
	entries []FSEntry
}

func (m *mockFSService) ReadDir(path string) ([]FSEntry, error) {
	out := make([]FSEntry, len(m.entries))
	copy(out, m.entries)
	return out, nil
}

func Serialize(buf CellBuf) string {
	return terminal.ToANSI(buf)
}

func TestFilePaneWidgetMeasureAndRender(t *testing.T) {
	pane := NewFilePaneWidget("demo")
	pane.SetItemsBridge([]fileItem{
		{name: "alpha"},
		{name: "beta"},
		{name: "gamma"},
	}, 1)
	pane.SetActive(true)

	if got := pane.Measure(Constraints{MaxW: 18, MaxH: 8}); got.W != 18 || got.H != 8 {
		t.Fatalf("Measure returned unexpected size: %#v", got)
	}

	pane.Layout(Rect{X: 0, Y: 0, W: 18, H: 8})
	buf := pane.RenderBuf()
	if buf.Width != 18 || buf.Height != 8 {
		t.Fatalf("Render produced unexpected size: %#v", buf)
	}

	out := Serialize(buf)
	if !strings.Contains(out, "demo") {
		t.Fatalf("render output should include pane title, got %q", ansi.Strip(out))
	}
	if !strings.Contains(out, "beta") {
		t.Fatalf("render output should include selected item, got %q", ansi.Strip(out))
	}
}

func TestFilePaneWidgetClipsLongNames(t *testing.T) {
	pane := NewFilePaneWidget("long")
	pane.SetItemsBridge([]fileItem{{name: "this-is-a-very-long-name"}}, 0)
	pane.Layout(Rect{X: 0, Y: 0, W: 12, H: 5})
	out := Serialize(pane.RenderBuf())
	if strings.Contains(out, "this-is-a-very-long-name") {
		t.Fatalf("render output should clip overlong names, got %q", ansi.Strip(out))
	}
	if !strings.Contains(out, "this") || !strings.Contains(out, "\u2026") {
		t.Fatalf("render output should keep truncated prefix with ellipsis, got %q", ansi.Strip(out))
	}
}

func TestFilePaneWidgetNavigationDownUp(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}}
	pane := NewFilePaneWidget("nav")
	pane.SetService(svc)
	if err := pane.LoadDir("/test"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	// items: ["..", "alpha", "beta", "gamma"], selectedIdx=0
	if item, _ := pane.SelectedItem(); item.name != ".." {
		t.Fatalf("initial selection should be '..', got %q", item.name)
	}

	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	if item, _ := pane.SelectedItem(); item.name != "alpha" {
		t.Fatalf("after 1 down, want alpha; got %q", item.name)
	}

	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyDown}) // gamma
	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyDown}) // clamped
	if item, _ := pane.SelectedItem(); item.name != "gamma" {
		t.Fatalf("down past end should clamp; got %q", item.name)
	}

	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyUp})
	if item, _ := pane.SelectedItem(); item.name != "beta" {
		t.Fatalf("after up from gamma, want beta; got %q", item.name)
	}
}

func TestFilePaneWidgetEnterDirectoryChangesDir(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{
		{Name: "subdir", IsDir: true},
	}}
	pane := NewFilePaneWidget("root")
	pane.SetService(svc)
	root := filepath.Join(string(filepath.Separator) + "root")
	_ = pane.LoadDir(root)

	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyDown}) // select "subdir"
	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	want := filepath.Join(root, "subdir")
	if pane.Dir() != want {
		t.Fatalf("Enter on dir should change Dir; got %q want %q", pane.Dir(), want)
	}
}

func TestFilePaneWidgetEnterFileCallsCallback(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{{Name: "readme.txt"}}}
	pane := NewFilePaneWidget("files")
	pane.SetService(svc)
	docs := filepath.Join(string(filepath.Separator) + "docs")
	_ = pane.LoadDir(docs)

	var opened string
	pane.OnOpenFile = func(path string) { opened = path }

	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyDown}) // select readme.txt
	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	want := filepath.Join(docs, "readme.txt")
	if opened != want {
		t.Fatalf("OnOpenFile should receive full path; got %q want %q", opened, want)
	}
}

func TestIsExecutableFileUsesPlatformRules(t *testing.T) {
	dir := t.TempDir()

	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".EXE;.COM")

		executable := filepath.Join(dir, "tool.exe")
		if err := os.WriteFile(executable, []byte("binary"), 0o600); err != nil {
			t.Fatalf("WriteFile executable: %v", err)
		}
		if !IsExecutableFile(executable) {
			t.Fatal("PATHEXT extension should be executable on Windows")
		}

		script := filepath.Join(dir, "tool.sh")
		if err := os.WriteFile(script, []byte("echo hi"), 0o600); err != nil {
			t.Fatalf("WriteFile shell script: %v", err)
		}
		if !IsExecutableFile(script) {
			t.Fatal(".sh files should be executable Kabibi scripts on Windows")
		}

		nonExecutable := filepath.Join(dir, "notes.txt")
		if err := os.WriteFile(nonExecutable, []byte("notes"), 0o600); err != nil {
			t.Fatalf("WriteFile non-executable: %v", err)
		}
		if IsExecutableFile(nonExecutable) {
			t.Fatal("extension absent from PATHEXT should not be executable on Windows")
		}
		return
	}

	file := filepath.Join(dir, "tool.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if IsExecutableFile(file) {
		t.Fatal("Unix file without execute permission should not be executable")
	}
	if err := os.Chmod(file, 0o700); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if !IsExecutableFile(file) {
		t.Fatal("Unix file with execute permission should be executable")
	}
}

func TestDualPaneWidgetStartsWithLeftPaneActive(t *testing.T) {
	dp := NewDualPaneWidget()

	if !dp.Left.IsActive() {
		t.Fatal("left pane should start active")
	}
	if dp.Right.IsActive() {
		t.Fatal("right pane should start inactive")
	}
	if dp.ActivePaneIndex() != 0 {
		t.Fatalf("active pane index = %d, want 0", dp.ActivePaneIndex())
	}
}

func TestDualPaneWidgetTabCyclesActivePane(t *testing.T) {
	dp := NewDualPaneWidget()
	dp.SetActivePaneBridge(0)

	if !dp.Left.IsActive() {
		t.Fatal("left pane should start active")
	}

	dp.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	if !dp.Right.IsActive() || dp.Left.IsActive() {
		t.Fatal("Tab should make right pane active")
	}

	dp.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	if !dp.Left.IsActive() || dp.Right.IsActive() {
		t.Fatal("second Tab should return left pane active")
	}
}

func TestDualPaneWidgetLayoutHasSingleDividerColumn(t *testing.T) {
	dp := NewDualPaneWidget()
	dp.Layout(Rect{X: 0, Y: 0, W: 20, H: 10})

	if dp.Right.X != dp.Left.X+dp.Left.Width+1 {
		t.Fatalf("right pane should start after a single divider column: left=(x=%d,w=%d) right=(x=%d)", dp.Left.X, dp.Left.Width, dp.Right.X)
	}
	if dp.Left.Width+dp.Right.Width+1 != dp.Width {
		t.Fatalf("total width should include exactly one divider column: left=%d right=%d divider=1 total=%d", dp.Left.Width, dp.Right.Width, dp.Width)
	}

	buf := dp.RenderBuf()
	dividerX := dp.Right.X - 1
	for y := 0; y < dp.Height; y++ {
		wantGrapheme := "│"
		wantSide := " "
		if y == 0 {
			wantGrapheme = "┬"
			wantSide = "─"
		} else if y == dp.Height-1 {
			wantGrapheme = "┴"
			wantSide = "─"
		}
		if cell := buf.Get(dividerX, y); cell.Grapheme != wantGrapheme || cell.Style.Foreground() != colorGray || cell.Style.Background() != colorBlue {
			t.Fatalf("divider column junction at x=%d,y=%d want %q, got %#v", dividerX, y, wantGrapheme, cell)
		}
		if cell := buf.Get(dp.Left.X+dp.Left.Width-1, y); cell.Grapheme != wantSide || cell.Style.Background() != colorBlue {
			t.Fatalf("left border next to divider at x=%d,y=%d want %q, got %#v", dp.Left.X+dp.Left.Width-1, y, wantSide, cell)
		}
		if cell := buf.Get(dp.Right.X, y); cell.Grapheme != wantSide || cell.Style.Background() != colorBlue {
			t.Fatalf("right border next to divider at x=%d,y=%d want %q, got %#v", dp.Right.X, y, wantSide, cell)
		}
	}
}

func TestDualPaneWidgetRoutesKeysToActiveChild(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{{Name: "a"}, {Name: "b"}}}

	dp := NewDualPaneWidget()
	dp.Left.SetService(svc)
	_ = dp.Left.LoadDir("/left")
	dp.Right.SetService(svc)
	_ = dp.Right.LoadDir("/right")
	dp.SetActivePaneBridge(0)

	dp.HandleEvent(input.KeyPressEvent{Code: input.KeyDown}) // should move left selection only

	if item, _ := dp.Left.SelectedItem(); item.name != "a" {
		t.Fatalf("Down should move left selection; got %q", item.name)
	}
	if item, _ := dp.Right.SelectedItem(); item.name != ".." {
		t.Fatalf("Right pane selection should be unchanged; got %q", item.name)
	}
}

func TestFilePaneWidgetActiveInactiveRender(t *testing.T) {
	active := NewFilePaneWidget("active")
	active.SetItemsBridge([]fileItem{{name: "one"}, {name: "two"}}, 0)
	active.SetActive(true)
	active.Layout(Rect{X: 0, Y: 0, W: 22, H: 8})
	actBuf := active.RenderBuf()

	inactive := NewFilePaneWidget("inactive")
	inactive.SetItemsBridge([]fileItem{{name: "one"}, {name: "two"}}, 0)
	inactive.SetActive(false)
	inactive.Layout(Rect{X: 0, Y: 0, W: 22, H: 8})
	inactBuf := inactive.RenderBuf()

	// Active pane highlights selected row 1 in yellow background; inactive pane keeps blue background.
	if got := actBuf.Get(2, 1).Style.Background(); got != colorYellow {
		t.Fatalf("active selected row background = 0x%06X, want 0x%06X", got, colorYellow)
	}
	if got := inactBuf.Get(2, 1).Style.Background(); got != colorBlue {
		t.Fatalf("inactive selected row background = 0x%06X, want 0x%06X", got, colorBlue)
	}
}

func TestDualPaneWidget_DividerJunctions(t *testing.T) {
	dp := NewDualPaneWidget()
	dp.Layout(Rect{X: 0, Y: 0, W: 80, H: 20})
	buf := dp.RenderBuf()

	dividerX := dp.Left.Width
	if dividerX <= 0 || dividerX >= 80 {
		t.Fatalf("unexpected dividerX: %d", dividerX)
	}

	// Top junction: ┬ at dividerX, ─ on left and right
	if got := buf.Get(dividerX, 0).Grapheme; got != "┬" {
		t.Errorf("top junction grapheme at (%d, 0) = %q, want %q", dividerX, got, "┬")
	}
	if got := buf.Get(dividerX-1, 0).Grapheme; got != "─" {
		t.Errorf("top border left of junction at (%d, 0) = %q, want %q", dividerX-1, got, "─")
	}
	if got := buf.Get(dividerX+1, 0).Grapheme; got != "─" {
		t.Errorf("top border right of junction at (%d, 0) = %q, want %q", dividerX+1, got, "─")
	}

	// Bottom junction: ┴ at dividerX, ─ on left and right
	if got := buf.Get(dividerX, 19).Grapheme; got != "┴" {
		t.Errorf("bottom junction grapheme at (%d, 19) = %q, want %q", dividerX, got, "┴")
	}
	if got := buf.Get(dividerX-1, 19).Grapheme; got != "─" {
		t.Errorf("bottom border left of junction at (%d, 19) = %q, want %q", dividerX-1, got, "─")
	}
	if got := buf.Get(dividerX+1, 19).Grapheme; got != "─" {
		t.Errorf("bottom border right of junction at (%d, 19) = %q, want %q", dividerX+1, got, "─")
	}

	// Middle divider: │
	if got := buf.Get(dividerX, 5).Grapheme; got != "│" {
		t.Errorf("middle divider grapheme at (%d, 5) = %q, want %q", dividerX, got, "│")
	}
}
