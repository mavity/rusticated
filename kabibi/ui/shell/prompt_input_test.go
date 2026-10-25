package shell

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func extractRowString(buf ui.CellBuf, y int) string {
	var sb strings.Builder
	for x := 0; x < buf.Width; x++ {
		g := buf.Get(x, y).Grapheme
		if g == "" {
			continue
		}
		sb.WriteString(g)
	}
	return strings.TrimRight(sb.String(), " ")
}

func TestPromptInput_WrapAndShrinkBackToOneLine(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{})

	// Initially empty -> 1 line ("፧")
	sz := p.Measure(ui.Constraints{MaxW: 10, MaxH: 24})
	if sz != (ui.Size{W: 10, H: 1}) {
		t.Fatalf("empty Measure() = %#v, want {W:10, H:1}", sz)
	}

	// Type 9 characters: "፧" (1) + "123456789" (9) = 10 columns -> cursor wraps to row 1 col 0
	for _, r := range "123456789" {
		p.HandleEvent(input.KeyPressEvent{Code: r, Text: string(r)})
	}
	sz = p.Measure(ui.Constraints{MaxW: 10, MaxH: 24})
	if sz.H != 2 {
		t.Fatalf("at exact width 10, Measure().H = %d, want 2 (cursor at col 0 of row 1)", sz.H)
	}
	buf, cur := p.Render(ui.Rect{X: 0, Y: 0, W: 10, H: 24}, ui.RenderContext{Focused: true})
	if buf.Height != 2 || cur.X != 0 || cur.Y != 1 {
		t.Fatalf("at exact width 10, Render() height=%d cur=(%d,%d), want height=2 cur=(0,1)", buf.Height, cur.X, cur.Y)
	}

	// Type 12 more characters -> total 22 columns -> 3 visual rows at width 10
	for _, r := range "abcdefghijkl" {
		p.HandleEvent(input.KeyPressEvent{Code: r, Text: string(r)})
	}
	sz = p.Measure(ui.Constraints{MaxW: 10, MaxH: 24})
	if sz.H != 3 {
		t.Fatalf("after 21 chars, Measure().H = %d, want 3", sz.H)
	}
	buf, cur = p.Render(ui.Rect{X: 0, Y: 0, W: 10, H: 24}, ui.RenderContext{Focused: true})
	if got := extractRowString(buf, 0); got != "፧123456789" {
		t.Fatalf("row 0 = %q, want %q", got, "፧123456789")
	}
	if got := extractRowString(buf, 1); got != "abcdefghij" {
		t.Fatalf("row 1 = %q, want %q", got, "abcdefghij")
	}
	if got := extractRowString(buf, 2); got != "kl" {
		t.Fatalf("row 2 = %q, want %q", got, "kl")
	}
	if cur.X != 2 || cur.Y != 2 {
		t.Fatalf("cursor = (%d,%d), want (2,2)", cur.X, cur.Y)
	}

	// Delete characters with backspace until only "123" remains -> shrinks back to 1 row
	for i := 0; i < 18; i++ {
		p.HandleEvent(input.KeyPressEvent{Code: input.KeyBackspace})
	}
	if got := p.Value(); got != "123" {
		t.Fatalf("Value() after backspaces = %q, want %q", got, "123")
	}
	sz = p.Measure(ui.Constraints{MaxW: 10, MaxH: 24})
	if sz.H != 1 {
		t.Fatalf("after deleting back to '123', Measure().H = %d, want 1", sz.H)
	}
	buf, cur = p.Render(ui.Rect{X: 0, Y: 0, W: 10, H: 24}, ui.RenderContext{Focused: true})
	if buf.Height != 1 || cur.X != 4 || cur.Y != 0 {
		t.Fatalf("after deleting back to '123', Render() height=%d cur=(%d,%d), want height=1 cur=(4,0)", buf.Height, cur.X, cur.Y)
	}
}

func TestPromptInput_HeightCapSixLinesAndThreeQuartersScreen(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{})
	// 100 chars at width 10 -> 11 visual rows un-capped
	p.SetValue(strings.Repeat("x", 100))

	// On a 24-row terminal, 3/4 of 24 = 18, so cap is min(6, 18) = 6 lines.
	sz24 := p.Measure(ui.Constraints{MaxW: 10, MaxH: 24})
	if sz24.H != 6 {
		t.Fatalf("Measure(MaxW:10, MaxH:24).H = %d, want 6 (capped at 6 lines)", sz24.H)
	}
	buf24, cur24 := p.Render(ui.Rect{X: 0, Y: 0, W: 10, H: 24}, ui.RenderContext{Focused: true})
	if buf24.Height != 6 {
		t.Fatalf("Render(W:10, H:24).Height = %d, want 6", buf24.Height)
	}
	// Cursor at the end should be scrolled into view on the last visible row (row 5).
	if cur24.Y != 5 {
		t.Fatalf("cur24.Y = %d, want 5 (bottom of capped 6-row viewport)", cur24.Y)
	}

	// On a 6-row terminal, floor(3/4 * 6) = 4, so cap is min(6, 4) = 4 lines.
	sz6 := p.Measure(ui.Constraints{MaxW: 10, MaxH: 6})
	if sz6.H != 4 {
		t.Fatalf("Measure(MaxW:10, MaxH:6).H = %d, want 4 (3/4 of 6-row screen)", sz6.H)
	}
	buf6, cur6 := p.Render(ui.Rect{X: 0, Y: 0, W: 10, H: 6}, ui.RenderContext{Focused: true})
	if buf6.Height != 4 || cur6.Y != 3 {
		t.Fatalf("Render(W:10, H:6) height=%d curY=%d, want height=4 curY=3", buf6.Height, cur6.Y)
	}
}

func TestPromptInput_DynamicPromptAndWideUnicode(t *testing.T) {
	cwd := "~/proj"
	p := NewPromptInput(PromptInputOptions{
		Prompt: func() string {
			return cwd + " $ "
		},
	})

	if got := p.PromptText(); got != "~/proj $ " {
		t.Fatalf("PromptText() = %q, want %q", got, "~/proj $ ")
	}

	// Update dynamic state; PromptInput reflects new prompt width and text immediately
	cwd = "/very/long/path"
	p.SetValue("你好世界") // 4 wide CJK runes = 8 display columns
	// "/very/long/path $ " is 18 columns + 8 columns = 26 columns -> 2 rows on width 20
	sz := p.Measure(ui.Constraints{MaxW: 20, MaxH: 24})
	if sz.H != 2 {
		t.Fatalf("Measure() with dynamic prompt + CJK = %d, want 2", sz.H)
	}
	buf, cur := p.Render(ui.Rect{X: 0, Y: 0, W: 20, H: 24}, ui.RenderContext{Focused: true})
	// Row 0: "/very/long/path $ 你" (18 + 2 = 20 cols)
	// Row 1: "好世界" (6 cols) -> cursor at (6, 1)
	if got := extractRowString(buf, 0); got != "/very/long/path $ 你" {
		t.Fatalf("row 0 = %q, want %q", got, "/very/long/path $ 你")
	}
	if got := extractRowString(buf, 1); got != "好世界" {
		t.Fatalf("row 1 = %q, want %q", got, "好世界")
	}
	if cur.X != 6 || cur.Y != 1 {
		t.Fatalf("cursor = (%d,%d), want (6,1)", cur.X, cur.Y)
	}
}

func TestPromptInput_PreservesAndInsertsNewlines(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{})
	p.SetValue("if true; then\r\necho ready")
	if got, want := p.Value(), "if true; then\necho ready"; got != want {
		t.Fatalf("SetValue() = %q, want %q", got, want)
	}
	p.SetCursorPos(2)
	p.InsertNewline()
	if got, want := p.Value(), "if\n true; then\necho ready"; got != want {
		t.Fatalf("InsertNewline() = %q, want %q", got, want)
	}

	p.Reset()
	p.HandleEvent(input.PasteEvent("first\nsecond"))
	if got, want := p.Value(), "first\nsecond"; got != want {
		t.Fatalf("pasted value = %q, want %q", got, want)
	}
}

func TestPromptInput_ClassifierGatesBashHighlighting(t *testing.T) {
	sh := New(ShellOptions{AIRunner: &fakeAIRunner{}})
	defer sh.Close()

	p := NewPromptInput(PromptInputOptions{
		Prompt:     func() string { return "$ " },
		StyleInput: sh.styleInput,
	})
	for _, value := range []string{"Name a bridge in Paris", "What's the time?"} {
		p.SetValue(value)
		aiBuf, _ := p.Render(ui.Rect{W: 80, H: 4}, ui.RenderContext{Focused: true})
		for i := range []rune(value) {
			cell := aiBuf.Get(2+i, 0)
			if !cell.Style.HasAttribute(terminal.CharResetForegroundDefault) {
				t.Fatalf("AI text %q at rune %d was syntax-highlighted", value, i)
			}
			if !cell.Style.HasAttribute(terminal.CharBold) || !cell.Style.HasAttribute(terminal.CharItalic) {
				t.Fatalf("AI text %q at rune %d lacks bold italic styling", value, i)
			}
		}
	}

	const shellInput = `echo "hello" | cat $HOME`
	p.SetValue(shellInput)
	shellBuf, _ := p.Render(ui.Rect{W: 80, H: 4}, ui.RenderContext{Focused: true})
	for _, token := range []struct {
		text  string
		style terminal.Style
	}{
		{text: "echo", style: bashKeywordStyle},
		{text: "hello", style: bashStringStyle},
		{text: "|", style: bashOperatorStyle},
		{text: "$HOME", style: bashNumberStyle},
	} {
		runeOffset := len([]rune(shellInput[:strings.Index(shellInput, token.text)]))
		got := shellBuf.Get(2+runeOffset, 0).Style.Foreground()
		if got != token.style.Foreground() {
			t.Errorf("foreground at %q = #%06X, want #%06X", token.text, got, token.style.Foreground())
		}
		if !shellBuf.Get(2+runeOffset, 0).Style.HasAttribute(terminal.CharBold) {
			t.Errorf("command token %q is not bold", token.text)
		}
	}

	const incompleteInput = "if true; then"
	p.SetValue(incompleteInput)
	incompleteBuf, _ := p.Render(ui.Rect{W: 80, H: 4}, ui.RenderContext{Focused: true})
	for _, token := range []struct {
		text  string
		style terminal.Style
	}{
		{text: "if", style: bashKeywordStyle},
		{text: ";", style: bashOperatorStyle},
		{text: "then", style: bashKeywordStyle},
	} {
		runeOffset := len([]rune(incompleteInput[:strings.Index(incompleteInput, token.text)]))
		got := incompleteBuf.Get(2+runeOffset, 0).Style.Foreground()
		if got != token.style.Foreground() {
			t.Errorf("incomplete-input foreground at %q = #%06X, want #%06X",
				token.text, got, token.style.Foreground())
		}
		if !incompleteBuf.Get(2+runeOffset, 0).Style.HasAttribute(terminal.CharBold) {
			t.Errorf("incomplete command token %q is not bold", token.text)
		}
	}
}

func TestBashInputPaletteContrastOnBlackAndWhite(t *testing.T) {
	colors := []struct {
		name  string
		color terminal.Color
	}{
		{name: "keyword", color: bashKeywordStyle.Foreground()},
		{name: "string", color: bashStringStyle.Foreground()},
		{name: "number", color: bashNumberStyle.Foreground()},
		{name: "operator", color: bashOperatorStyle.Foreground()},
		{name: "comment", color: bashCommentStyle.Foreground()},
	}
	backgrounds := []struct {
		name  string
		color terminal.Color
	}{
		{name: "black", color: 0x000000},
		{name: "white", color: 0xFFFFFF},
	}
	for _, foreground := range colors {
		for _, background := range backgrounds {
			ratio := contrastRatio(foreground.color, background.color)
			if ratio < 3 {
				t.Errorf("%s color #%06X contrast on %s = %.2f:1, want at least 3:1",
					foreground.name, foreground.color, background.name, ratio)
			}
		}
	}
}

func contrastRatio(foreground, background terminal.Color) float64 {
	luminance := func(color terminal.Color) float64 {
		channel := func(shift uint) float64 {
			value := float64((uint32(color)>>shift)&0xFF) / 255
			if value <= 0.04045 {
				return value / 12.92
			}
			return math.Pow((value+0.055)/1.055, 2.4)
		}
		return 0.2126*channel(16) + 0.7152*channel(8) + 0.0722*channel(0)
	}
	l1, l2 := luminance(foreground), luminance(background)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func TestPromptInput_ParentOwnsEnterAndCtrlC(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{})
	p.SetValue("echo hi")

	if p.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("PromptInput should not consume Enter; parent handles submission")
	}
	if p.Value() != "echo hi" {
		t.Fatalf("Value() mutated on Enter = %q, want %q", p.Value(), "echo hi")
	}
}

func TestPromptInput_CapturesAndResetsFirstInputAt(t *testing.T) {
	p := NewPromptInput(PromptInputOptions{})
	if !p.FirstInputAt().IsZero() {
		t.Fatalf("initial FirstInputAt() = %v, want zero", p.FirstInputAt())
	}

	p.HandleEvent(input.KeyPressEvent{Code: 'a', Text: "a"})
	first := p.FirstInputAt()
	if first.IsZero() {
		t.Fatal("FirstInputAt() should be set after typing first character")
	}

	p.HandleEvent(input.KeyPressEvent{Code: 'b', Text: "b"})
	if !p.FirstInputAt().Equal(first) {
		t.Fatalf("FirstInputAt() changed on second character: got %v, want %v", p.FirstInputAt(), first)
	}

	// Deleting back to empty clears FirstInputAt
	p.HandleEvent(input.KeyPressEvent{Code: input.KeyBackspace})
	p.HandleEvent(input.KeyPressEvent{Code: input.KeyBackspace})
	if !p.FirstInputAt().IsZero() {
		t.Fatalf("FirstInputAt() after deleting all chars = %v, want zero", p.FirstInputAt())
	}

	// Typing again records a fresh timestamp, and Reset() clears it
	p.HandleEvent(input.KeyPressEvent{Code: 'x', Text: "x"})
	if p.FirstInputAt().IsZero() {
		t.Fatal("FirstInputAt() should be set again after re-typing")
	}
	p.Reset()
	if !p.FirstInputAt().IsZero() {
		t.Fatalf("FirstInputAt() after Reset() = %v, want zero", p.FirstInputAt())
	}
}
