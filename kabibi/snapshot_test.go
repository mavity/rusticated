package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const updateSnapshotsEnv = "UPDATE_SNAPSHOTS"

func snapshotPath(name string) string {
	return filepath.Join("snapshots", name+".snap")
}

func assertSnapshot(t *testing.T, name, got string) {
	t.Helper()
	path := snapshotPath(name)
	if os.Getenv(updateSnapshotsEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir snapshot dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write snapshot %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("snapshot %s is missing; rerun with UPDATE_SNAPSHOTS=1 to generate it", path)
		}
		t.Fatalf("read snapshot %s: %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("snapshot mismatch for %s\n--- WANT ---\n%s\n--- GOT ---\n%s", name, string(want), got)
	}
}

func TestEditorViewSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.go")
	src := strings.Join([]string{
		"package main",
		"",
		"import \"fmt\"",
		"",
		"func main() {",
		"\tfmt.Println(\"hello, kabibi\")",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write demo source: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor: %v", err)
	}
	e.path = "demo.go"
	e.width = 80
	e.height = 20
	e.status = "snapshot stable"
	e.cy = 3
	e.cx = 7
	e.top = 0
	e.left = 0

	assertSnapshot(t, "editor_view", e.View())
}

func TestModelViewSnapshot(t *testing.T) {
	modelVal := initialModel()
	m := &modelVal
	m.width = 100
	m.height = 28
	m.chatView.Width = 24
	m.chatView.Height = 6

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	m.leftDir = dir
	m.rightDir = dir
	m.loadDir(leftPane, dir, "alpha.txt")
	m.loadDir(rightPane, dir, "beta.go")
	m.activePane = leftPane
	m.plume = []string{"Kabibi shell:  'help' for available commands."}

	assertSnapshot(t, "model_view", m.View())
}

func TestEditorSelectionSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.go")
	src := strings.Join([]string{
		"package main",
		"",
		"func main() {",
		"\tmsg := \"hello\"",
		"\tprintln(msg)",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write selection source: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor: %v", err)
	}
	e.path = "selection.go"
	e.width = 64
	e.height = 12
	e.status = "selected text"
	e.sel = true
	e.ay, e.ax = 2, 1
	e.cy, e.cx = 4, 10
	e.top = 0
	e.left = 0

	assertSnapshot(t, "editor_selection", e.View())
}

func TestEditorScrollAndDirtySnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scrolled.go")
	src := strings.Join([]string{
		"package main",
		"",
		"import \"fmt\"",
		"",
		"func main() {",
		"\tfor i := 0; i < 3; i++ {",
		"\t\tfmt.Println(\"kabibi\")",
		"\t}",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write scrolled source: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor: %v", err)
	}
	e.path = "scrolled.go"
	e.dirty = true
	e.width = 60
	e.height = 10
	e.status = "dirty buffer"
	e.top = 2
	e.left = 10
	e.cy = 5
	e.cx = 12

	assertSnapshot(t, "editor_scrolled_dirty", e.View())
}

func TestEditorNarrowWindowSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "narrow.go")
	src := strings.Join([]string{
		"package main",
		"",
		"func greet(name string) string {",
		"\treturn \"hi, \" + name",
		"}",
		"",
		"func main() {",
		"\tprintln(greet(\"kabibi\"))",
		"}",
	}, "\n")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write narrow source: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor: %v", err)
	}
	e.path = "narrow.go"
	e.width = 28
	e.height = 7
	e.status = "narrow"
	e.cy = 3
	e.cx = 5
	e.top = 0
	e.left = 0

	assertSnapshot(t, "editor_narrow", e.View())
}

func TestScrollbarThumb(t *testing.T) {
	tests := []struct {
		name    string
		total   int
		visible int
		top     int
		want    [2]int
	}{
		{name: "single page", total: 10, visible: 10, top: 0, want: [2]int{0, 10}},
		{name: "partial scroll", total: 50, visible: 10, top: 20, want: [2]int{4, 6}},
		{name: "small list", total: 3, visible: 5, top: 0, want: [2]int{0, 5}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd := scrollbarThumb(tt.total, tt.visible, tt.top)
			if gotStart != tt.want[0] || gotEnd != tt.want[1] {
				t.Fatalf("scrollbarThumb(%d, %d, %d) = (%d, %d), want (%d, %d)", tt.total, tt.visible, tt.top, gotStart, gotEnd, tt.want[0], tt.want[1])
			}
		})
	}
}
