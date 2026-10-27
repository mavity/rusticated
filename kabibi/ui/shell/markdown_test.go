package shell

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func TestAssistantMarkdownRendersAndStylesHighlights(t *testing.T) {
	block := CommandBlock{
		Kind:   BlockAITurn,
		Source: SourceUserShell,
		Output: *bytes.NewBufferString("# Heading\n\n## Subheading\n\nA **strong** word, `inline`, and [link](https://example.com).\n\n```go\nfunc main() {}\n```\n"),
		Status: StatusSuccess,
	}
	buf := renderBlock(block, 80)
	text := markdownCellText(buf)
	for _, want := range []string{"Heading", "strong", "inline", "link", "func main"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered Markdown missing %q in %q", want, text)
		}
	}
	for _, syntax := range []string{"**strong**", "`inline`", "```"} {
		if strings.Contains(text, syntax) {
			t.Errorf("rendered Markdown retained source syntax %q in %q", syntax, text)
		}
	}

	colors := []terminal.Color{
		0x58A6FF,
		0xFF7B72,
		0xFFA657,
		0x79C0FF,
		0xD2A8FF,
	}
	var foundColors []string
	seenColors := make(map[terminal.Color]bool)
	for _, cell := range buf.Cells() {
		if color := cell.Style.Foreground(); color != 0 && !seenColors[color] {
			seenColors[color] = true
			foundColors = append(foundColors, fmt.Sprintf("#%06X", color))
		}
	}
	for _, expected := range colors {
		found := false
		for actual := range seenColors {
			if colorsClose(actual, expected) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("rendered output did not use configured highlight color #%06X; got %v", expected, foundColors)
		}
	}
}

func colorsClose(got, want terminal.Color) bool {
	for shift := uint(0); shift < 24; shift += 8 {
		a := int((got >> shift) & 0xFF)
		b := int((want >> shift) & 0xFF)
		if a-b > 1 || b-a > 1 {
			return false
		}
	}
	return true
}

func TestAssistantMarkdownSanitizesTerminalControls(t *testing.T) {
	block := CommandBlock{
		Kind:   BlockAITurn,
		Source: SourceUserShell,
		Output: *bytes.NewBufferString("\x1b[2Jplain response"),
		Status: StatusSuccess,
	}
	text := markdownCellText(renderBlock(block, 80))
	if !strings.Contains(text, "[2Jplain response") {
		t.Fatalf("control-sequence payload should remain visible as text: %q", text)
	}
}

func TestStreamingAssistantMarkdownIsSanitizedButNotRendered(t *testing.T) {
	block := CommandBlock{
		Kind:   BlockAITurn,
		Source: SourceUserShell,
		Output: *bytes.NewBufferString("# **partial**\x1b[2J"),
		Status: StatusActive,
	}
	text := markdownCellText(renderBlock(block, 80))
	if !strings.Contains(text, "# **partial**[2J") {
		t.Fatalf("streaming output should preserve Markdown source while removing controls: %q", text)
	}
}

func markdownCellText(buf terminal.CellBuf) string {
	var out strings.Builder
	for y := 0; y < buf.Height; y++ {
		if y > 0 {
			out.WriteByte('\n')
		}
		for x := 0; x < buf.Width; x++ {
			out.WriteString(buf.Get(x, y).Grapheme)
		}
	}
	return out.String()
}
