package filemanager

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
	"golang.org/x/text/number"
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

func renderFilePaneForTest(t *testing.T, pane *FilePaneWidget, rect Rect, focused bool) CellBuf {
	t.Helper()
	text := pane.Render(rect, RenderContext{Focused: focused})
	frame, _, extent, err := terminal.NewTerminal(rect.W, rect.H).RenderANSIForComposition(text, rect.W, rect.H)
	if err != nil {
		t.Fatal(err)
	}
	buf := terminal.NewCellBuf(rect.W, extent)
	copy(buf.Cells(), frame.Cells()[:extent*rect.W])
	return buf
}

func TestFilePaneWidgetMeasureAndRender(t *testing.T) {
	pane := NewFilePaneWidget("demo")
	pane.SetItemsBridge([]fileItem{
		{name: "alpha"},
		{name: "beta"},
		{name: "gamma"},
	}, 1)
	if got := pane.Measure(Constraints{MaxW: 18, MaxH: 8}); got.W != 18 || got.H != 8 {
		t.Fatalf("Measure returned unexpected size: %#v", got)
	}

	buf := renderFilePaneForTest(t, pane, Rect{X: 0, Y: 0, W: 18, H: 8}, true)
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

func numberedFileItems(count int) []fileItem {
	items := make([]fileItem, count)
	for i := range items {
		items[i] = fileItem{name: fmt.Sprintf("item%02d", i)}
	}
	return items
}

func TestFilePaneWidgetHorizontalNavigationScrollsOneColumn(t *testing.T) {
	pane := NewFilePaneWidget("columns")
	pane.SetItemsBridge(numberedFileItems(24), 0)
	pane.Layout(Rect{W: 60, H: 8})

	for i := 0; i < 3; i++ {
		pane.HandleEvent(input.KeyPressEvent{Code: input.KeyRight})
	}
	if got, want := pane.SelectedIndex(), 12; got != want {
		t.Fatalf("selected index after moving right three columns = %d, want %d", got, want)
	}
	if got, want := pane.firstVisibleCol, 1; got != want {
		t.Fatalf("first visible column = %d, want one-column scroll to %d", got, want)
	}

	rendered := ansi.Strip(Serialize(pane.RenderBuf()))
	for _, visible := range []string{"item04", "item15", "item12"} {
		if !strings.Contains(rendered, visible) {
			t.Errorf("rendered viewport missing %q: %q", visible, rendered)
		}
	}
	for _, hidden := range []string{"item00", "item16"} {
		if strings.Contains(rendered, hidden) {
			t.Errorf("rendered viewport unexpectedly contains %q: %q", hidden, rendered)
		}
	}
}

func TestFilePaneWidgetPageNavigationMovesWholeVisiblePage(t *testing.T) {
	pane := NewFilePaneWidget("pages")
	pane.SetItemsBridge(numberedFileItems(30), 0)
	pane.Layout(Rect{W: 60, H: 8})

	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyPgDown})
	if got, want := pane.SelectedIndex(), 12; got != want {
		t.Fatalf("selected index after PgDown = %d, want one visible page (%d)", got, want)
	}
	if got, want := pane.firstVisibleCol, 3; got != want {
		t.Fatalf("first visible column after PgDown = %d, want %d", got, want)
	}
	rendered := ansi.Strip(Serialize(pane.RenderBuf()))
	for _, visible := range []string{"item12", "item23"} {
		if !strings.Contains(rendered, visible) {
			t.Errorf("page after PgDown missing %q: %q", visible, rendered)
		}
	}
	for _, hidden := range []string{"item00", "item24"} {
		if strings.Contains(rendered, hidden) {
			t.Errorf("page after PgDown unexpectedly contains %q: %q", hidden, rendered)
		}
	}

	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyPgUp})
	if got, want := pane.SelectedIndex(), 0; got != want {
		t.Fatalf("selected index after PgUp = %d, want %d", got, want)
	}
	if got, want := pane.firstVisibleCol, 0; got != want {
		t.Fatalf("first visible column after PgUp = %d, want %d", got, want)
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

func TestOsFSServiceReadDirMetadataAndNonRecursiveTotals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.bin"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("WriteFile one.bin: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("Mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.bin"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("WriteFile nested.bin: %v", err)
	}

	entries, err := (&OsFSService{}).ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var one FSEntry
	for _, entry := range entries {
		if entry.Name == "one.bin" {
			one = entry
		}
	}
	if one.Size != 5 || one.Mode.Perm() == 0 {
		t.Fatalf("one.bin metadata = size %d, mode %v; want size 5 and permissions", one.Size, one.Mode)
	}

	pane := NewFilePaneWidget("files")
	if err := pane.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	count, size := pane.fileTotals()
	if count != 1 || size != 5 {
		t.Fatalf("directory totals = (%d, %d), want (1, 5) without descending", count, size)
	}
}

func TestFilePaneWidgetRefreshesSelectedMetadataAndTotal(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{
		{Name: "alpha", Size: 10, Mode: 0o644},
		{Name: "beta", Size: 20, Mode: 0o644},
	}}
	pane := NewFilePaneWidget("files")
	pane.SetService(svc)
	if err := pane.LoadDir("/files"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.SelectByName("alpha")
	if _, total := pane.fileTotals(); total != 30 {
		t.Fatalf("initial total = %d, want 30", total)
	}

	svc.entries[0].Size = 100
	svc.entries[0].Mode = 0o444
	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyUp})

	item, ok := pane.SelectedItem()
	if !ok || item.Name() != "alpha" {
		t.Fatalf("selected item after navigation = %q, want alpha", item.Name())
	}
	if item.Size() != 100 || item.Mode().Perm() != 0o444 {
		t.Fatalf("refreshed metadata = size %d, mode %v; want size 100 and mode 0444", item.Size(), item.Mode())
	}
	if _, total := pane.fileTotals(); total != 120 {
		t.Fatalf("updated total = %d, want 120", total)
	}
}

func TestFilePaneWidgetLstatsSelectedFileAfterNavigation(t *testing.T) {
	dir := t.TempDir()
	alpha := filepath.Join(dir, "alpha")
	beta := filepath.Join(dir, "beta")
	if err := os.WriteFile(alpha, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("WriteFile alpha: %v", err)
	}
	if err := os.WriteFile(beta, []byte("12345"), 0o644); err != nil {
		t.Fatalf("WriteFile beta: %v", err)
	}

	pane := NewFilePaneWidget("files")
	if err := pane.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.SelectByName("alpha")
	if _, total := pane.fileTotals(); total != 15 {
		t.Fatalf("initial total = %d, want 15", total)
	}
	if err := os.WriteFile(alpha, []byte(strings.Repeat("a", 100)), 0o444); err != nil {
		t.Fatalf("WriteFile updated alpha: %v", err)
	}
	if err := os.Chmod(alpha, 0o444); err != nil {
		t.Fatalf("Chmod updated alpha: %v", err)
	}

	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	pane.HandleEvent(input.KeyPressEvent{Code: input.KeyUp})
	item, ok := pane.SelectedItem()
	if !ok || item.Name() != "alpha" {
		t.Fatalf("selected item after navigation = %q, want alpha", item.Name())
	}
	if item.Size() != 100 || item.Mode().Perm()&0o222 != 0 {
		t.Fatalf("selected metadata = size %d, mode %v; want size 100 and read-only", item.Size(), item.Mode())
	}
	if _, total := pane.fileTotals(); total != 105 {
		t.Fatalf("updated total = %d, want 105", total)
	}
}

func TestNumberPrinterUsesLocaleEnvironmentOrCommaFallback(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LC_NUMERIC", "en_US.UTF-8")
	t.Setenv("LANG", "en_US")
	if got := numberPrinterFromEnv().Sprintf("%v", number.Decimal(uint64(1234567))); got != "1.234.567" {
		t.Fatalf("German locale number = %q, want 1.234.567", got)
	}

	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_NUMERIC", "")
	t.Setenv("LANG", "")
	if got := numberPrinterFromEnv().Sprintf("%v", number.Decimal(uint64(1234567))); got != "1,234,567" {
		t.Fatalf("fallback number = %q, want 1,234,567", got)
	}
}

func TestFilePaneWidgetRendersTotalAndSelectedDetails(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("LC_NUMERIC", "")
	t.Setenv("LANG", "")
	svc := &mockFSService{entries: []FSEntry{{
		Name: ".sample.bin",
		Size: 1234,
		Mode: 0o444,
	}}}
	pane := NewFilePaneWidget("files")
	pane.SetService(svc)
	if err := pane.LoadDir("/files"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.SelectByName(".sample.bin")
	pane.Layout(Rect{X: 0, Y: 0, W: 60, H: 8})

	buf := pane.RenderBuf()
	if got := buf.Get(1, 5).Grapheme; got != "─" {
		t.Fatalf("detail separator = %q, want horizontal line", got)
	}
	if got := buf.Get(0, 5).Grapheme; got != "├" {
		t.Fatalf("left detail separator join = %q, want ├", got)
	}
	if got := buf.Get(buf.Width-1, 5).Grapheme; got != "┤" {
		t.Fatalf("right detail separator join = %q, want ┤", got)
	}
	var details strings.Builder
	for x := 1; x < buf.Width-1; x++ {
		details.WriteString(buf.Get(x, 6).Grapheme)
	}
	for _, want := range []string{".sample.bin", "[R,H]", "r--r--r--", "1,234b"} {
		if !strings.Contains(details.String(), want) {
			t.Fatalf("selected details %q do not contain %q", details.String(), want)
		}
	}
	suffix := "[R,H] -r--r--r-- 1,234b"
	trimmedDetails := strings.TrimRight(details.String(), " ")
	if !strings.HasPrefix(strings.TrimLeft(trimmedDetails, " "), ".sample.bin") || !strings.HasSuffix(trimmedDetails, suffix) {
		t.Fatalf("selected detail should left-align the name and right-align %q: %q", suffix, details.String())
	}
	suffixStart := buf.Width - 1 - len(suffix)
	if got := buf.Get(suffixStart, 6).Style.Foreground(); got != colorGray {
		t.Fatalf("attribute color = 0x%06X, want muted gray", got)
	}
	sizeStart := suffixStart + len("[R,H] -r--r--r-- ")
	if got := buf.Get(sizeStart, 6).Style.Foreground(); got != colorBrightCyan {
		t.Fatalf("size digit color = 0x%06X, want bright cyan", got)
	}
	if got := buf.Get(sizeStart+1, 6).Style.Foreground(); got != colorGray {
		t.Fatalf("size group separator color = 0x%06X, want muted gray", got)
	}
	if got := buf.Get(buf.Width-2, 6).Style.Foreground(); got != colorGray {
		t.Fatalf("size suffix color = 0x%06X, want muted gray", got)
	}
	if got := buf.Get(4, 7).Grapheme; got != "[" {
		t.Fatalf("summary begins at x=4 with [, got %q", got)
	}
	if got := buf.Get(5, 7).Style.Foreground(); got != colorBrightCyan {
		t.Fatalf("summary number color = 0x%06X, want bright cyan", got)
	}
	if got := buf.Get(10, 7).Style.Foreground(); got != colorGray {
		t.Fatalf("locale group separator color = 0x%06X, want border color", got)
	}
	if got := buf.Get(7, 7).Grapheme; got != "─" {
		t.Fatalf("summary count/size separator = %q, want horizontal line", got)
	}

	pane.BeginSearch(input.KeyPressEvent{Code: 'z', Mod: input.ModAlt})
	buf = pane.RenderBuf()
	if got := buf.Get(4, 7).Grapheme; got != "─" {
		t.Fatalf("total should be hidden while search is active; got %q at summary position", got)
	}
}

func TestFilePaneWidgetParentDirectoryDetailsUseMetadataWithoutSize(t *testing.T) {
	parentDir := filepath.Join(t.TempDir(), "parent")
	dir := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create child directory: %v", err)
	}
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("stat parent directory: %v", err)
	}

	pane := NewFilePaneWidget("files")
	pane.SetService(&OsFSService{})
	if err := pane.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.Layout(Rect{X: 0, Y: 0, W: 60, H: 8})
	buf := pane.RenderBuf()

	var details strings.Builder
	for x := 1; x < buf.Width-1; x++ {
		details.WriteString(buf.Get(x, 6).Grapheme)
	}
	got := strings.TrimRight(details.String(), " ")
	wantName := filepath.Base(parentDir)
	wantSuffix := parentInfo.Mode().Perm().String()
	if !strings.HasPrefix(strings.TrimLeft(got, " "), wantName) {
		t.Fatalf("parent details should show its actual name %q: %q", wantName, details.String())
	}
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("parent details should show its actual attributes %q: %q", wantSuffix, details.String())
	}
	if strings.Contains(got, "[D]") {
		t.Fatalf("parent details should not show a directory indicator: %q", details.String())
	}
	if strings.HasSuffix(got, "b") {
		t.Fatalf("directory details should omit size: %q", details.String())
	}
}

func TestFilePaneWidgetRefreshKeepsMissingSelectionSortPosition(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{
		{Name: "alpha"},
		{Name: "bravo"},
		{Name: "charlie"},
		{Name: "delta"},
		{Name: "echo"},
		{Name: "foxtrot"},
	}}
	pane := NewFilePaneWidget("files")
	pane.SetService(svc)
	if err := pane.LoadDir("/files"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.SelectByName("delta")

	svc.entries = []FSEntry{
		{Name: "charlie"},
		{Name: "foxtrot"},
		{Name: "golf"},
		{Name: "hotel"},
		{Name: "india"},
	}
	if err := pane.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	item, ok := pane.SelectedItem()
	if !ok || item.Name() != "foxtrot" {
		t.Fatalf("selection after refresh = %q, want item at deleted delta's sort position (foxtrot)", item.Name())
	}
}

func TestFilePaneWidgetRefreshClampsMissingFinalSelection(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{
		{Name: "alpha"},
		{Name: "bravo"},
		{Name: "charlie"},
	}}
	pane := NewFilePaneWidget("files")
	pane.SetService(svc)
	if err := pane.LoadDir("/files"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.SelectByName("charlie")

	svc.entries = []FSEntry{
		{Name: "alpha"},
		{Name: "bravo"},
	}
	if err := pane.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	item, ok := pane.SelectedItem()
	if !ok || item.Name() != "bravo" {
		t.Fatalf("selection after refresh = %q, want last remaining item bravo", item.Name())
	}
}

func TestFilePaneWidgetInstantSearchPrefersPrefixAndWraps(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{
		{Name: "alpha"},
		{Name: "tango"},
		{Name: "test"},
		{Name: "tst-file"},
		{Name: "zeta"},
	}}
	pane := NewFilePaneWidget("files")
	pane.SetService(svc)
	if err := pane.LoadDir("/files"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.SelectByName("tango")

	if !pane.BeginSearch(input.KeyPressEvent{Code: 't', Mod: input.ModAlt}) {
		t.Fatal("Alt+t did not start instant search")
	}
	if item, _ := pane.SelectedItem(); item.Name() != "test" {
		t.Fatalf("initial prefix match = %q, want test", item.Name())
	}
	if !pane.HandleSearchEvent(input.KeyPressEvent{Code: 's', Text: "s"}) {
		t.Fatal("typing into search was not handled")
	}
	if item, _ := pane.SelectedItem(); item.Name() != "tst-file" {
		t.Fatalf("exact prefix should beat fuzzy match = %q, want tst-file", item.Name())
	}

	pane.CloseSearch()
	pane.SelectByName("zeta")
	if !pane.BeginSearch(input.KeyPressEvent{Code: 'a', Mod: input.ModAlt}) {
		t.Fatal("Alt+a did not start instant search")
	}
	if item, _ := pane.SelectedItem(); item.Name() != "alpha" {
		t.Fatalf("search should wrap forward from zeta; got %q, want alpha", item.Name())
	}
}

func TestFilePaneWidgetInstantSearchFuzzyNoMatchAndClear(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{
		{Name: "alpha"},
		{Name: "tango"},
		{Name: "test"},
	}}
	pane := NewFilePaneWidget("files")
	pane.SetService(svc)
	if err := pane.LoadDir("/files"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.SelectByName("alpha")

	if !pane.BeginSearch(input.KeyPressEvent{Code: 't', Mod: input.ModAlt}) {
		t.Fatal("Alt+t did not start instant search")
	}
	pane.HandleSearchEvent(input.KeyPressEvent{Code: 's', Text: "s"})
	pane.HandleSearchEvent(input.KeyPressEvent{Code: 't', Text: "t"})
	if item, _ := pane.SelectedItem(); item.Name() != "test" {
		t.Fatalf("fuzzy subsequence match = %q, want test", item.Name())
	}

	pane.HandleSearchEvent(input.KeyPressEvent{Code: 'z', Text: "z"})
	if item, _ := pane.SelectedItem(); item.Name() != "test" {
		t.Fatalf("selection on no-match query = %q, want last match test", item.Name())
	}
	if pane.searchMatch {
		t.Fatal("search should report no match after unmatched input")
	}

	for range pane.searchText {
		pane.HandleSearchEvent(input.KeyPressEvent{Code: input.KeyBackspace})
	}
	if item, _ := pane.SelectedItem(); item.Name() != "alpha" {
		t.Fatalf("clearing query should restore initial selection alpha, got %q", item.Name())
	}
}

func TestFilePaneWidgetInstantSearchEditsAtCaret(t *testing.T) {
	svc := &mockFSService{entries: []FSEntry{
		{Name: "alpha"},
		{Name: "tango"},
		{Name: "test"},
		{Name: "xray"},
	}}
	pane := NewFilePaneWidget("files")
	pane.SetService(svc)
	if err := pane.LoadDir("/files"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	pane.SelectByName("alpha")
	pane.BeginSearch(input.KeyPressEvent{Code: 't', Mod: input.ModAlt})

	pane.HandleSearchEvent(input.KeyPressEvent{Code: input.KeyLeft})
	pane.HandleSearchEvent(input.KeyPressEvent{Code: 'x', Text: "x"})
	if item, _ := pane.SelectedItem(); item.Name() != "tango" {
		t.Fatalf("unmatched edited query moved selection to %q, want last match tango", item.Name())
	}

	pane.HandleSearchEvent(input.KeyPressEvent{Code: input.KeyDelete})
	if item, _ := pane.SelectedItem(); item.Name() != "xray" {
		t.Fatalf("Delete at caret should leave query x and select xray; got %q", item.Name())
	}
	pane.HandleSearchEvent(input.KeyPressEvent{Code: input.KeyBackspace})
	if item, _ := pane.SelectedItem(); item.Name() != "alpha" {
		t.Fatalf("clearing edited query should restore alpha, got %q", item.Name())
	}
}

func TestFilePaneWidgetInstantSearchOverlayRendersOnBottomBorder(t *testing.T) {
	pane := NewFilePaneWidget("files")
	pane.SetItemsBridge([]fileItem{{name: "test"}}, 0)
	pane.Layout(Rect{X: 0, Y: 0, W: 20, H: 5})
	if !pane.BeginSearch(input.KeyPressEvent{Code: 'z', Mod: input.ModAlt}) {
		t.Fatal("Alt+z did not start instant search")
	}

	buf := pane.RenderBuf()
	if got := buf.Get(18, 4).Grapheme; got != "]" {
		t.Fatalf("search widget should end inside the bottom-right border; got %q", got)
	}
	if got := buf.Get(17, 4).Style.Foreground(); got != colorCyan {
		t.Fatalf("non-matching query foreground = 0x%06X, want cyan 0x%06X", got, colorCyan)
	}

	pane.CloseSearch()
	pane.SelectByName("test")
	if !pane.BeginSearch(input.KeyPressEvent{Code: 't', Mod: input.ModAlt}) {
		t.Fatal("Alt+t did not start instant search")
	}
	buf = pane.RenderBuf()
	if got := buf.Get(15, 4).Style.Foreground(); got != colorYellow {
		t.Fatalf("matching query foreground = 0x%06X, want yellow 0x%06X", got, colorYellow)
	}
}

func TestDualPaneWidgetStartsWithLeftPaneActive(t *testing.T) {
	dp := NewDualPaneWidget()

	if dp.ActivePaneIndex() != 0 {
		t.Fatalf("active pane index = %d, want 0", dp.ActivePaneIndex())
	}
}

func TestDualPaneWidgetTabCyclesActivePane(t *testing.T) {
	dp := NewDualPaneWidget()
	dp.SetActivePaneBridge(0)

	dp.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	if dp.ActivePaneIndex() != 1 {
		t.Fatal("Tab should make right pane active")
	}

	dp.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	if dp.ActivePaneIndex() != 0 {
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
		} else if y == dp.Height-3 {
			wantGrapheme = "┼"
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
	actBuf := renderFilePaneForTest(t, active, Rect{X: 0, Y: 0, W: 22, H: 8}, true)

	inactive := NewFilePaneWidget("inactive")
	inactive.SetItemsBridge([]fileItem{{name: "one"}, {name: "two"}}, 0)
	inactBuf := renderFilePaneForTest(t, inactive, Rect{X: 0, Y: 0, W: 22, H: 8}, false)

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
