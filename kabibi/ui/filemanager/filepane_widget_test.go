package filemanager

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

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
	buf := pane.Render()
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
	out := Serialize(pane.Render())
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

	pane.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	if item, _ := pane.SelectedItem(); item.name != "alpha" {
		t.Fatalf("after 1 down, want alpha; got %q", item.name)
	}

	pane.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	pane.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // gamma
	pane.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // clamped
	if item, _ := pane.SelectedItem(); item.name != "gamma" {
		t.Fatalf("down past end should clamp; got %q", item.name)
	}

	pane.HandleKey(tea.KeyMsg{Type: tea.KeyUp})
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

	pane.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // select "subdir"
	pane.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

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

	pane.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // select readme.txt
	pane.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	want := filepath.Join(docs, "readme.txt")
	if opened != want {
		t.Fatalf("OnOpenFile should receive full path; got %q want %q", opened, want)
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

	dp.HandleKey(tea.KeyMsg{Type: tea.KeyTab})
	if !dp.Right.IsActive() || dp.Left.IsActive() {
		t.Fatal("Tab should make right pane active")
	}

	dp.HandleKey(tea.KeyMsg{Type: tea.KeyTab})
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

	buf := dp.Render()
	dividerX := dp.Right.X - 1
	for y := 0; y < dp.Height; y++ {
		if cell := buf.Get(dividerX, y); cell.R != '│' || cell.FG != colorGray || cell.BG != colorBlue {
			t.Fatalf("divider column should be a single gray-on-blue pipe at x=%d,y=%d, got %#v", dividerX, y, cell)
		}
		if cell := buf.Get(dp.Left.X+dp.Left.Width-1, y); cell.R != ' ' || cell.BG != colorBlue {
			t.Fatalf("left pane should not retain its right border at x=%d,y=%d, got %#v", dp.Left.X+dp.Left.Width-1, y, cell)
		}
		if cell := buf.Get(dp.Right.X, y); cell.R != ' ' || cell.BG != colorBlue {
			t.Fatalf("right pane should not retain its left border at x=%d,y=%d, got %#v", dp.Right.X, y, cell)
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

	dp.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // should move left selection only

	if item, _ := dp.Left.SelectedItem(); item.name != "a" {
		t.Fatalf("Down should move left selection; got %q", item.name)
	}
	if item, _ := dp.Right.SelectedItem(); item.name != ".." {
		t.Fatalf("Right pane selection should be unchanged; got %q", item.name)
	}
}

func TestFilePaneWidgetActiveInactiveSnapshot(t *testing.T) {
	active := NewFilePaneWidget("active")
	active.SetItemsBridge([]fileItem{{name: "one"}, {name: "two"}}, 0)
	active.SetActive(true)

	inactive := NewFilePaneWidget("inactive")
	inactive.SetItemsBridge([]fileItem{{name: "one"}, {name: "two"}}, 0)
	inactive.SetActive(false)

	assertWidgetSnapshot(t, "filepane_active", active, Rect{X: 0, Y: 0, W: 22, H: 8})
	assertWidgetSnapshot(t, "filepane_inactive", inactive, Rect{X: 0, Y: 0, W: 22, H: 8})
}
