package shell

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
)

func TestCompletionContextRecognizesCommandPositions(t *testing.T) {
	tests := []struct {
		input      string
		cursor     int
		commandPos bool
		command    string
		prefix     string
		redirect   bool
	}{
		{input: "", cursor: 0, commandPos: true},
		{input: "echo ", cursor: 5, command: "echo"},
		{input: "echo hi; ", cursor: 9, commandPos: true},
		{input: "echo hi | ", cursor: 10, commandPos: true},
		{input: "if ", cursor: 3, commandPos: true},
		{input: "if true; then ", cursor: 14, commandPos: true},
		{input: "NAME=value ", cursor: 11, commandPos: true},
		{input: "echo result > ", cursor: 14, command: "echo", redirect: true},
		{input: "echo foo", cursor: 6, command: "echo", prefix: "f"},
		{input: "echo hi; git sta", cursor: 16, command: "git", prefix: "sta"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := completionContextAt(tt.input, tt.cursor)
			if got.commandPosition != tt.commandPos || got.command != tt.command ||
				got.prefix != tt.prefix || got.redirect != tt.redirect {
				t.Fatalf("context = %#v", got)
			}
		})
	}
}

func TestCompletionContextIgnoresCommentsAndArithmeticExpressions(t *testing.T) {
	comment := "cat # not a file"
	if got := completionContextAt(comment, len([]rune(comment))); got.command != "" || got.commandPosition {
		t.Fatalf("completion context inside comment = %#v", got)
	}
	arithmetic := "cat $((1+2))"
	if got := completionContextAt(arithmetic, len([]rune("cat $((1+"))); got.command != "" || got.commandPosition {
		t.Fatalf("completion context inside arithmetic expression = %#v", got)
	}
}

func TestCompletePathUsesFilesystemAndAddsDirectorySeparator(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "subdir", "child.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "submit.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := completePath(completionContext{prefix: "sub"}, root, false)
	if len(got) != 2 {
		t.Fatalf("completion count = %d, want 2", len(got))
	}
	if got[0].Display != "subdir" || got[0].Insert != "dir/" || got[0].Space {
		t.Fatalf("directory completion = %#v", got[0])
	}
	if got[1].Display != "submit.txt" || got[1].Insert != "mit.txt" || !got[1].Space {
		t.Fatalf("file completion = %#v", got[1])
	}
	got = completePath(completionContext{prefix: "subdir/ch"}, root, false)
	if len(got) != 1 || got[0].Display != "subdir/child.txt" || got[0].Insert != "ild.txt" {
		t.Fatalf("completion below a forward-slash directory prefix = %#v", got)
	}
	got = completePath(completionContext{}, root, false)
	for _, candidate := range got {
		if candidate.Display == ".secret" {
			t.Fatal("hidden file was offered for an empty prefix")
		}
	}
	got = completePath(completionContext{prefix: "."}, root, false)
	foundHidden := false
	for _, candidate := range got {
		foundHidden = foundHidden || candidate.Display == ".secret"
	}
	if !foundHidden {
		t.Fatalf("hidden file was not offered for a dot prefix: %#v", got)
	}
}

func TestCompletionDescriptionHeuristics(t *testing.T) {
	tests := []struct {
		description string
		kind        completionKind
		choices     string
	}{
		{"File path relative to current working directory", completionFile, ""},
		{"Root directory to search", completionDirectory, ""},
		{"Pattern or regular expression to match", completionValue, ""},
		{"Status: (none|xfer|progress)", completionValue, "none,xfer,progress"},
	}
	for _, tt := range tests {
		if got := inferCompletionValueKind(tt.description); got != tt.kind {
			t.Errorf("inferCompletionValueKind(%q) = %v, want %v", tt.description, got, tt.kind)
		}
		if got := strings.Join(completionDescriptionChoices(tt.description), ","); got != tt.choices {
			t.Errorf("completionDescriptionChoices(%q) = %q, want %q", tt.description, got, tt.choices)
		}
	}
}

func TestRuntimeURootProvidersCompleteFlagsAndPaths(t *testing.T) {
	if len(commandSyntaxRegistry) <= len(AllURootCommandNames())/2 {
		t.Fatalf("runtime FlagSet providers cover %d of %d u-root command names; want a majority",
			len(commandSyntaxRegistry), len(AllURootCommandNames()))
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "target-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "input.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kappa.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(ShellOptions{WorkingDir: root, DisableAI: true})
	defer s.Close()

	assertCandidate := func(line, want string) {
		t.Helper()
		got := s.completeInput(line, len([]rune(line)))
		for _, candidate := range got {
			if candidate.Display == want {
				return
			}
		}
		t.Fatalf("completion for %q = %#v, want %q", line, got, want)
	}
	assertCandidate("cat -", "-u")
	assertCandidate("cat ./input", "input.bin")
	assertCandidate("cat in", "input.bin")
	assertCandidate("ls ka", "kappa.txt")
	assertCandidate("cd tar", "target-dir")
	assertCandidate("cp ka", "kappa.txt")
	assertCandidate("cp --", "--reflink")
	assertCandidate("cp --target-directory ", "target-dir")
	assertCandidate("tar -f ", "input.bin")
	assertCandidate("wc in", "input.bin")
	if got := s.completeInput("sleep 1", len([]rune("sleep 1"))); len(got) != 0 {
		t.Fatalf("non-path positional argument unexpectedly suggested paths: %#v", got)
	}
}

func TestRuntimeFlagSetRegistryUsesFreshDefinitions(t *testing.T) {
	for name, factory := range commandSyntaxRegistry {
		first := factory()
		second := factory()
		if first.standard == nil && first.pflag == nil {
			t.Errorf("%q provider returned no FlagSet", name)
			continue
		}
		if first.standard != nil && first.standard == second.standard ||
			first.pflag != nil && first.pflag == second.pflag {
			t.Errorf("%q provider reused a mutable FlagSet", name)
		}
	}
}

func TestShellCompletionOffersGitAndCommandsAfterSeparator(t *testing.T) {
	s := New(ShellOptions{WorkingDir: t.TempDir(), DisableAI: true})
	defer s.Close()

	got := s.completeInput("echo ok; git sta", len([]rune("echo ok; git sta")))
	var foundStatus, foundStash bool
	for _, candidate := range got {
		foundStatus = foundStatus || candidate.Display == "status"
		foundStash = foundStash || candidate.Display == "stash"
	}
	if !foundStatus || !foundStash {
		t.Fatalf("Git subcommand completions = %#v", got)
	}

	got = s.completeInput("if true; then ec", len([]rune("if true; then ec")))
	foundEcho := false
	for _, candidate := range got {
		foundEcho = foundEcho || candidate.Display == "echo"
	}
	if !foundEcho {
		t.Fatalf("command-position completion after then = %#v", got)
	}
}

func TestGitGlobalCompletionUsesRuntimeParserState(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(ShellOptions{WorkingDir: root, DisableAI: true})
	defer s.Close()

	assertCandidate := func(line, want string) {
		t.Helper()
		got := s.completeInput(line, len([]rune(line)))
		for _, candidate := range got {
			if candidate.Display == want {
				return
			}
		}
		t.Fatalf("completion for %q = %#v, want %q", line, got, want)
	}
	assertCandidate("git -C ", "repo")
	assertCandidate("git --git-dir ", "repo")
	assertCandidate("git --work-tree=", "repo")
	assertCandidate("git -c ", "core.editor=")
}

func TestGitNotesCompletionUsesRuntimeSyntax(t *testing.T) {
	s := New(ShellOptions{WorkingDir: t.TempDir(), DisableAI: true})
	defer s.Close()

	assertCandidate := func(line, want string) {
		t.Helper()
		got := s.completeInput(line, len([]rune(line)))
		for _, candidate := range got {
			if candidate.Display == want {
				return
			}
		}
		t.Fatalf("completion for %q = %#v, want %q", line, got, want)
	}
	assertCandidate("git notes ", "append")
	assertCandidate("git notes --r", "--ref")
	assertCandidate("git notes add --", "--message")
	assertCandidate("git notes add --", "--force")
	assertCandidate("git replace --", "--graft")

	force, message, targets, err := parseGitNoteMutation([]string{"--message=note", "--force", "HEAD"})
	if err != nil || !force || message != "note" || len(targets) != 1 || targets[0] != "HEAD" {
		t.Fatalf("parseGitNoteMutation() = %v, %q, %#v, %v", force, message, targets, err)
	}
}

func TestShellTabAndCtrlSpaceRequestExplicitCompletion(t *testing.T) {
	for _, key := range []input.KeyPressEvent{
		{Code: input.KeyTab},
		{Code: input.KeySpace, Mod: input.ModCtrl},
	} {
		s := New(ShellOptions{WorkingDir: t.TempDir(), DisableAI: true})
		s.SetInput("git st")
		if !s.HandleEvent(key) {
			_ = s.Close()
			t.Fatalf("completion key %q was not handled", key.String())
		}
		if !s.Input().CompletionActive() {
			_ = s.Close()
			t.Fatalf("completion key %q did not activate options", key.String())
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShellCompletionHandlesWebPathsAndNestedShellCommands(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "screenshot.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(ShellOptions{WorkingDir: root, DisableAI: true})
	defer s.Close()

	got := s.completeInput("web shot ", len([]rune("web shot ")))
	if len(got) != 1 || got[0].Display != "screenshot.png" {
		t.Fatalf("web screenshot path completions = %#v", got)
	}
	got = s.completeInput("web open ", len([]rune("web open ")))
	if len(got) != 3 || got[0].Display != "--back" {
		t.Fatalf("web navigation choices = %#v", got)
	}
	got = s.completeInput("web shot --", len([]rune("web shot --")))
	if len(got) != 3 || got[0].Display != "--adorn" || got[2].Display != "--nick" {
		t.Fatalf("web screenshot options = %#v", got)
	}
	got = s.completeInput("cdp --nick ", len([]rune("cdp --nick ")))
	if len(got) != 1 || got[0].Display != "main" {
		t.Fatalf("CDP nickname completions = %#v", got)
	}
	nested := "sh -c 'git sta"
	got = s.completeInput(nested, len([]rune(nested)))
	var foundStatus bool
	for _, candidate := range got {
		foundStatus = foundStatus || candidate.Display == "status"
	}
	if !foundStatus {
		t.Fatalf("nested shell command completions = %#v, context=%#v", got,
			completionContextAt(nested, len([]rune(nested))))
	}
}

func TestDelegatedAndRuntimeArgumentCompletions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(ShellOptions{WorkingDir: root, DisableAI: true})
	defer s.Close()

	assertCandidate := func(line, want string) {
		t.Helper()
		got := s.completeInput(line, len([]rune(line)))
		for _, candidate := range got {
			if candidate.Display == want {
				return
			}
		}
		ctx := completionContextAt(line, len([]rune(line)))
		t.Fatalf("completion for %q = %#v, context=%#v, want %q", line, got, ctx, want)
	}
	assertCandidate("ip address ", "show")
	assertCandidate("tar -f ", "input.bin")
}

func TestAICompletionUsesRuntimeToolSchemas(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(ShellOptions{WorkingDir: root, DisableAI: true})
	defer s.Close()

	assertCandidate := func(line, want string) {
		t.Helper()
		got := s.completeInput(line, len([]rune(line)))
		for _, candidate := range got {
			if candidate.Display == want {
				return
			}
		}
		ctx := completionContextAt(line, len([]rune(line)))
		t.Fatalf("completion for %q = %#v, context=%#v, want %q", line, got, ctx, want)
	}
	assertCandidate("ai find-", "find_files")
	assertCandidate("ai read_file --pa", "--path")
	assertCandidate("ai read_file --path ", "notes.txt")
	assertCandidate("ai read_file --hashline tru", "true")
	assertCandidate("ai find_files --find_p", "--find-paths")
	assertCandidate("ai login git", "github-copilot")
}

func TestPromptInputExplicitCompletionCyclesAndAccepts(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{
		Complete: func(string, int) []completionCandidate {
			return []completionCandidate{
				{Display: "status", Insert: "atus", Space: true},
				{Display: "stash", Insert: "ash", Space: true},
			}
		},
	})
	p.SetValue("git st")
	if !p.RequestCompletion() || !p.CompletionActive() {
		t.Fatal("explicit completion did not activate candidate selection")
	}
	waitForExplicitCompletion(t, p)
	if p.Value() != "git st" {
		t.Fatalf("completion changed input before acceptance: %q", p.Value())
	}
	if !p.HandleEvent(input.KeyPressEvent{Code: input.KeyRight}) {
		t.Fatal("right arrow did not select the next candidate")
	}
	if !p.AcceptCompletion() {
		t.Fatal("Enter acceptance did not accept the selected candidate")
	}
	if got, want := p.Value(), "git stash "; got != want {
		t.Fatalf("accepted input = %q, want %q", got, want)
	}
}

func TestPromptInputExplicitSingleCompletionAcceptsImmediately(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{
		Complete: func(string, int) []completionCandidate {
			return []completionCandidate{{Display: "status", Insert: "atus", Space: true}}
		},
	})
	p.SetValue("git st")
	if !p.RequestCompletion() {
		t.Fatal("explicit completion request was not handled")
	}
	deadline := time.Now().Add(time.Second)
	for p.Value() == "git st" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got, want := p.Value(), "git status "; got != want {
		t.Fatalf("single completion = %q, want %q", got, want)
	}
}

func waitForExplicitCompletion(t *testing.T, p *PromptInput) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		ready := p.completion != nil && !p.completion.pending
		p.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("explicit completion provider did not finish")
}

func TestPromptInputCursorMovementDoesNotTriggerSpeculation(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{
		Complete: func(string, int) []completionCandidate {
			return []completionCandidate{{Display: "status", Insert: "atus", Space: true}}
		},
	})
	p.SetValue("git st")
	p.SetCursorPos(3)
	time.Sleep(speculativeCompletionDelay + 20*time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completion != nil {
		t.Fatal("cursor movement triggered speculative completion")
	}
}

func TestPromptInputDiscardsLateExplicitCompletion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	p := NewPromptInput(PromptInputOptions{
		Complete: func(string, int) []completionCandidate {
			startOnce.Do(func() { close(started) })
			<-release
			return []completionCandidate{{Display: "status", Insert: "atus", Space: true}}
		},
	})
	p.SetValue("git st")
	if !p.RequestCompletion() {
		t.Fatal("explicit completion request was not handled")
	}
	<-started
	p.SetValue("git stash")
	close(release)
	time.Sleep(10 * time.Millisecond)
	if got, want := p.Value(), "git stash"; got != want {
		t.Fatalf("late completion changed input to %q, want %q", got, want)
	}
	if p.CompletionVisible() {
		t.Fatal("late completion was displayed for changed input")
	}
}

func TestPromptInputEscapeDismissesCompletion(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{})
	p.SetValue("git st")
	p.mu.Lock()
	p.completion = &promptCompletionState{
		candidates: []completionCandidate{{Display: "status", Insert: "atus", Space: true}},
		active:     true,
	}
	p.mu.Unlock()
	if !p.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
		t.Fatal("Escape did not dismiss completion")
	}
	if p.CompletionVisible() {
		t.Fatal("completion remained visible after Escape")
	}
	if got, want := p.Value(), "git st"; got != want {
		t.Fatalf("Escape changed input = %q, want %q", got, want)
	}
}

func TestPromptCompletionRowReusesReservedFooter(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{})
	p.SetValue("git st")
	p.mu.Lock()
	p.completion = &promptCompletionState{
		candidates: []completionCandidate{
			{Display: "status", Insert: "atus", Space: true},
			{Display: "stash", Insert: "ash", Space: true},
		},
		speculative: true,
	}
	p.mu.Unlock()
	if got := p.Measure(ui.Constraints{MaxW: 40, MaxH: 24}).H; got != 2 {
		t.Fatalf("prompt with choices has height %d, want input plus one choice row", got)
	}

	s := New(ShellOptions{WorkingDir: t.TempDir(), DisableAI: true})
	defer s.Close()
	before := s.computeStackHeight(40, 24)
	s.input.mu.Lock()
	s.input.completion = clonePromptCompletion(p.completion)
	s.input.mu.Unlock()
	if after := s.computeStackHeight(40, 24); after != before {
		t.Fatalf("completion row changed shell height from %d to %d", before, after)
	}
}

func TestPromptInputSpeculativeGhostAndRightArrowPrecedence(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{Prompt: func() string { return "$ " }})
	p.SetValue("git st")
	p.mu.Lock()
	p.completion = &promptCompletionState{
		candidates:  []completionCandidate{{Display: "status", Insert: "atus", Space: true}},
		speculative: true,
	}
	p.mu.Unlock()
	buf, _ := renderANSIForTest(t, p.Render(ui.Rect{W: 40, H: 4}, ui.RenderContext{}), 40, 4)
	if got := extractRowString(buf, 0); got != "$ git status" {
		t.Fatalf("ghost input = %q, want %q", got, "$ git status")
	}
	if got := buf.Get(8, 0).Style.Foreground(); got != 0x808080 {
		t.Fatalf("ghost foreground = %#x, want gray", got)
	}
	if p.Value() != "git st" {
		t.Fatalf("ghost rendering changed input: %q", p.Value())
	}

	p.SetValue("git st-tail")
	p.SetCursorPos(len([]rune("git st")))
	p.mu.Lock()
	p.completion = &promptCompletionState{
		candidates:  []completionCandidate{{Display: "status", Insert: "atus", Space: true}},
		speculative: true,
	}
	p.mu.Unlock()
	if !p.HandleEvent(input.KeyPressEvent{Code: input.KeyRight}) {
		t.Fatal("Right Arrow was not handled")
	}
	if got := p.Value(); got != "git st-tail" {
		t.Fatalf("Right Arrow with suffix changed input: %q", got)
	}
	if got := p.CursorPos(); got != len([]rune("git st"))+1 {
		t.Fatalf("cursor after Right Arrow = %d", got)
	}

	p.SetValue("git st")
	p.mu.Lock()
	p.completion = &promptCompletionState{
		candidates:  []completionCandidate{{Display: "status", Insert: "atus", Space: true}},
		speculative: true,
	}
	p.mu.Unlock()
	if !p.HandleEvent(input.KeyPressEvent{Code: input.KeyRight}) {
		t.Fatal("Right Arrow did not accept unique completion")
	}
	if got, want := p.Value(), "git status "; got != want {
		t.Fatalf("Right Arrow accepted value = %q, want %q", got, want)
	}
}

func TestPromptInputSpeculativeCompletionIsNotSubmittedValue(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{
		Complete: func(string, int) []completionCandidate {
			return []completionCandidate{{Display: "status", Insert: "atus", Space: true}}
		},
	})
	p.SetValue("git st")
	deadline := time.Now().Add(2 * speculativeCompletionDelay)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		ready := p.completion != nil && p.completion.speculative
		p.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	ready := p.completion != nil && p.completion.speculative
	p.mu.Unlock()
	if !ready {
		t.Fatal("speculative completion did not run")
	}
	if got := p.Value(); got != "git st" {
		t.Fatalf("speculative hint mutated submitted value: %q", got)
	}
	if p.CompletionActive() {
		t.Fatal("speculative hint was active before Tab")
	}
	_ = p.Render(ui.Rect{W: 40, H: 4}, ui.RenderContext{})
	if !p.RequestCompletion() {
		t.Fatal("Tab did not accept speculative completion")
	}
	if got, want := p.Value(), "git status "; got != want {
		t.Fatalf("accepted speculative value = %q, want %q", got, want)
	}
}

func TestPromptInputSpeculativeMultipleChoicesRequireActivation(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{
		Complete: func(string, int) []completionCandidate {
			return []completionCandidate{
				{Display: "status", Insert: "atus", Space: true},
				{Display: "stash", Insert: "ash", Space: true},
			}
		},
	})
	p.SetValue("git st")
	deadline := time.Now().Add(2 * speculativeCompletionDelay)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		ready := p.completion != nil && p.completion.speculative
		p.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	ready := p.completion != nil && p.completion.speculative
	p.mu.Unlock()
	if !ready {
		t.Fatal("multiple speculative options were not rendered")
	}
	if p.AcceptCompletion() {
		t.Fatal("Enter accepted a speculative option without activation")
	}
	if p.Value() != "git st" {
		t.Fatalf("speculative choices changed input: %q", p.Value())
	}
	if !p.RequestCompletion() || !p.CompletionActive() {
		t.Fatal("Tab did not activate speculative choices")
	}
}
