package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/shell"
)

func TestSlashCompletionCandidatesUseSharedSyntax(t *testing.T) {
	candidates, handled := slashCompletionCandidates("/set", len([]rune("/set")), false)
	if !handled || len(candidates) != 1 {
		t.Fatalf("slash completion = (%v, %t), want one handled candidate", candidates, handled)
	}
	if got, want := candidates[0].Display, "/settings"; got != want {
		t.Fatalf("candidate display = %q, want %q", got, want)
	}
	if got, want := candidates[0].Insert, "tings"; got != want {
		t.Fatalf("candidate insertion = %q, want %q", got, want)
	}
	if !candidates[0].Space || candidates[0].Description == "" {
		t.Fatalf("candidate metadata = %#v, want trailing space and description", candidates[0])
	}

	candidates, handled = slashCompletionCandidates("/SET", len([]rune("/SET")), false)
	if !handled || len(candidates) != 1 || candidates[0].Display != "/settings" {
		t.Fatalf("case-insensitive slash completion = (%v, %t), want /settings", candidates, handled)
	}

	candidates, handled = slashCompletionCandidates("/mve", 2, false)
	if !handled || len(candidates) != 1 || candidates[0].Display != "/move" || candidates[0].Insert != "o" {
		t.Fatalf("middle-of-token completion = (%v, %t), want /move inserting o", candidates, handled)
	}
	if candidates[0].Space {
		t.Fatal("middle-of-token completion appended a space")
	}

	candidates, handled = slashCompletionCandidates("/move file", len([]rune("/move file")), false)
	if handled || len(candidates) != 0 {
		t.Fatalf("completion in slash arguments = (%v, %t), want default provider", candidates, handled)
	}
}

func TestSlashCompletionPreservesBrowserFiltering(t *testing.T) {
	contains := func(candidates []shell.CompletionCandidate, name string) bool {
		for _, candidate := range candidates {
			if candidate.Display == name {
				return true
			}
		}
		return false
	}
	withoutBrowser, handled := slashCompletionCandidates("/m", 2, false)
	if !handled || contains(withoutBrowser, "/mount") {
		t.Fatalf("non-browser slash candidates = %v, handled=%t; /mount must be filtered", withoutBrowser, handled)
	}
	withBrowser, handled := slashCompletionCandidates("/m", 2, true)
	if !handled || !contains(withBrowser, "/mount") {
		t.Fatalf("browser slash candidates = %v, handled=%t; /mount must be available", withBrowser, handled)
	}
}

func TestSlashCommandHintsAndUniqueCompletion(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()

	widget.Shell().SetInput("/set")
	deadline := time.Now().Add(2 * time.Second)
	for !widget.Shell().Input().CompletionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !widget.Shell().Input().CompletionVisible() {
		t.Fatal("speculative slash completion did not appear")
	}
	buf, _ := renderAppANSIForTest(t, widget.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{}), 80, 24)
	var footer strings.Builder
	for x := 0; x < buf.Width; x++ {
		footer.WriteString(buf.Get(x, buf.Height-1).Grapheme)
	}
	if !strings.Contains(footer.String(), "/settings") {
		t.Fatalf("slash hints footer %q does not contain /settings", footer.String())
	}
	if !strings.Contains(footer.String(), "Open settings") {
		t.Fatalf("slash hints footer %q does not contain the command description", footer.String())
	}

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	if got := widget.Shell().Input().Value(); got != "/settings " {
		t.Fatalf("input after unique completion = %q, want %q", got, "/settings ")
	}
}

func TestSlashWeakCompletionPromotesAndEscapeRestores(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	widget.Shell().SetInput("/m")

	deadline := time.Now().Add(2 * time.Second)
	for !widget.Shell().Input().CompletionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !widget.Shell().Input().CompletionMenuVisible() {
		t.Fatal("speculative slash candidates did not appear")
	}
	if widget.Shell().Input().CompletionActive() {
		t.Fatal("speculative slash candidates became active without an explicit request")
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab}) {
		t.Fatal("Tab did not promote speculative slash candidates")
	}
	if got, want := widget.Shell().Input().Value(), "/mkdir "; got != want {
		t.Fatalf("first speculative preview = %q, want %q", got, want)
	}
	if !widget.Shell().Input().CompletionActive() {
		t.Fatal("Tab did not activate slash completion")
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
		t.Fatal("Escape did not restore the partial slash input")
	}
	if got, want := widget.Shell().Input().Value(), "/m"; got != want {
		t.Fatalf("Escape-restored slash input = %q, want %q", got, want)
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
		t.Fatal("Escape without completion did not clear slash input")
	}
	if got := widget.Shell().Input().Value(); got != "" {
		t.Fatalf("second Escape left slash input %q, want empty", got)
	}
}

func TestSlashCommandArrowSelectionAndDispatch(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	widget.Shell().SetInput("/m")

	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeySpace, Mod: input.ModCtrl}) {
		t.Fatal("Ctrl+Space did not activate slash completion")
	}
	deadline := time.Now().Add(time.Second)
	for !widget.Shell().Input().CompletionMenuVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !widget.Shell().Input().CompletionMenuVisible() {
		t.Fatal("slash completion menu did not appear")
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyLeft}) {
		t.Fatal("Left did not select a slash completion")
	}
	if got, want := widget.Shell().Input().Value(), "/move "; got != want {
		t.Fatalf("inline slash preview = %q, want %q", got, want)
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not dispatch the selected slash command")
	}
	if widget.Dialog() == nil {
		t.Fatal("Left then Enter did not open /move dialog")
	}
	if widget.PanelsVisible() {
		t.Fatal("/move dialog unexpectedly opened blue panels")
	}
	if got := widget.Shell().Input().Value(); got != "" {
		t.Fatalf("input after running /move = %q, want empty", got)
	}
}

func TestSlashCommandTabCompletesUniqueMatch(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	widget.Shell().SetInput("/mov")

	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab}) {
		t.Fatal("Tab did not request slash completion")
	}
	deadline := time.Now().Add(time.Second)
	for widget.Shell().Input().Value() != "/move " && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := widget.Shell().Input().Value(); got != "/move " {
		t.Fatalf("input after unique completion = %q, want /move ", got)
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not dispatch the completed slash command")
	}
	if widget.Dialog() == nil {
		t.Fatal("Tab then Enter did not open /move dialog")
	}
	if widget.PanelsVisible() {
		t.Fatal("Tab then Enter unexpectedly opened blue panels")
	}
}

func TestSlashCopyOpensPathFormWithoutPanels(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	destDir := filepath.Join(dir, "destination")
	if err := os.WriteFile(src, []byte("copy me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	widget := NewWidget(AppWidgetOptions{WorkingDir: dir})
	defer widget.Close()

	widget.Shell().SetInput("/copy")
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	dlg := widget.Dialog()
	if dlg == nil {
		t.Fatal("/copy did not open a dialog")
	}
	if len(dlg.Fields) != 2 {
		t.Fatalf("/copy dialog has %d fields, want two path fields", len(dlg.Fields))
	}
	if widget.PanelsVisible() {
		t.Fatal("/copy unexpectedly opened blue panels")
	}
	dlg.Fields[0].Input.SetValue("source.txt")
	dlg.Fields[1].Input.SetValue("destination")
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	copied := filepath.Join(destDir, "source.txt")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if widget.Dialog() == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if widget.Dialog() != nil {
		t.Fatal("copy operation did not finish before timeout")
	}
	got, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("read copied file %q: %v", copied, err)
	}
	if string(got) != "copy me" {
		t.Fatalf("copied contents = %q, want %q", got, "copy me")
	}
}

func TestPathInputCompletesRelativeToShellDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	if err := widget.Shell().SetDir(dir); err != nil {
		t.Fatal(err)
	}

	pathInput := widget.newPathInput("sou")
	deadline := time.Now().Add(2 * time.Second)
	for pathInput.Value() != "source.txt" && time.Now().Before(deadline) {
		pathInput.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
		time.Sleep(time.Millisecond)
	}
	if got := pathInput.Value(); got != "source.txt" {
		t.Fatalf("path completion = %q, want %q", got, "source.txt")
	}
}

func TestFileCollisionKeepsProgressDialogAndResumes(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "source")
	dstDir := filepath.Join(root, "destination")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(srcDir, "same.txt")
	target := filepath.Join(dstDir, "same.txt")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	widget := NewWidget(AppWidgetOptions{WorkingDir: root})
	defer widget.Close()
	widget.startFileOp(filemanager.FileOp{Kind: filemanager.OpCopy, Sources: []string{source}, Dest: dstDir})
	progress := widget.Dialog()
	if progress == nil {
		t.Fatal("file operation did not open a progress dialog")
	}

	deadline := time.Now().Add(2 * time.Second)
	for !progress.Collision && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if widget.Dialog() != progress || !progress.Collision {
		t.Fatal("collision replaced the progress dialog instead of expanding it")
	}
	widget.HandleEvent(input.KeyPressEvent{Code: 's', Text: "s"})
	deadline = time.Now().Add(2 * time.Second)
	for widget.Dialog() != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if widget.Dialog() != nil {
		t.Fatal("file operation did not resume and finish after Skip")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "old" {
		t.Fatalf("collision skip changed destination to %q", contents)
	}
}

func TestSlashCommandEnterDoesNotCompletePartialName(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	widget.Shell().SetInput("/set")

	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not process the slash command")
	}
	if widget.Dialog() == nil || len(widget.Dialog().Fields) != 0 {
		t.Fatal("partial slash name was silently completed instead of reporting an unknown command")
	}
	if widget.Editor() != nil {
		t.Fatal("partial slash name opened an editor")
	}
}

func TestSlashCommandCompletionThenPathArgument(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	widget := NewWidget(AppWidgetOptions{WorkingDir: dir})
	defer widget.Close()
	widget.Shell().SetInput("/edi")
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab}) {
		t.Fatal("Tab did not request /edit completion")
	}
	deadline := time.Now().Add(time.Second)
	for widget.Shell().Input().Value() != "/edit " && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got, want := widget.Shell().Input().Value(), "/edit "; got != want {
		t.Fatalf("completed command = %q, want %q", got, want)
	}
	widget.Shell().SetInput("/edit file.txt")

	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not run /edit with its path argument")
	}
	if widget.Editor() == nil {
		t.Fatal("Enter did not run the selected /edit command with its path argument")
	}
	if got := widget.Shell().Input().Value(); got != "" {
		t.Fatalf("input after running /edit = %q, want empty", got)
	}
}

func TestSlashSettingsOpensDialogAndEscapeShowsPanels(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()

	widget.Shell().SetInput("/set")
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab}) {
		t.Fatal("Tab did not request /settings completion")
	}
	deadline := time.Now().Add(time.Second)
	for widget.Shell().Input().Value() != "/settings " && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got, want := widget.Shell().Input().Value(), "/settings "; got != want {
		t.Fatalf("completed slash command = %q, want %q", got, want)
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not dispatch /settings")
	}
	if widget.Dialog() == nil {
		t.Fatal("/settings did not open a dialog")
	}

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	widget.Shell().Input().Reset()
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	if !widget.PanelsVisible() {
		t.Fatal("Escape with empty input and hidden panels did not show the panels")
	}

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	if widget.PanelsVisible() {
		t.Fatal("second Escape with visible panels did not hide the panels")
	}
}
