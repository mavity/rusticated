package filelist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ai"
	"github.com/mattn/go-runewidth"
)

type quoteSpanTestEmbedder struct {
	mu    sync.Mutex
	texts []string
}

func (e *quoteSpanTestEmbedder) Embed(parts []ai.PromptPart) ([]float32, error) {
	text := ""
	if len(parts) > 0 {
		text = parts[0].Text
	}
	e.mu.Lock()
	e.texts = append(e.texts, text)
	e.mu.Unlock()
	if strings.Contains(text, "target") {
		return []float32{1, 0}, nil
	}
	return []float32{0, 1}, nil
}

func TestRelevantQuoteStopsAtFourLineCoreAndAddsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	var source strings.Builder
	for line := 1; line <= 16; line++ {
		if line == 10 {
			fmt.Fprintf(&source, "%02d target()\n", line)
		} else {
			fmt.Fprintf(&source, "%02d ordinary()\n", line)
		}
	}
	if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	underlying := &quoteSpanTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), underlying)
	defer scheduler.Close()
	quote, startLine, endLine, err := relevantQuote(
		context.Background(),
		fileEntry{relPath: "sample.go", absPath: path},
		[]float32{1, 0},
		scheduler,
		nil,
	)
	if err != nil {
		t.Fatalf("relevantQuote(): %v", err)
	}
	if startLine != 8 || endLine != 13 {
		t.Fatalf("excerpt lines = %d-%d, want 8-13", startLine, endLine)
	}
	for line := 8; line <= 13; line++ {
		if !strings.Contains(quote, fmt.Sprintf("%02d ", line)) {
			t.Errorf("excerpt missing line %d: %q", line, quote)
		}
	}

	underlying.mu.Lock()
	defer underlying.mu.Unlock()
	if len(underlying.texts) != 5 {
		t.Fatalf("embedding calls = %d, want 5 (one chunk and two coarse splits)", len(underlying.texts))
	}
}

func TestReadQuoteExcerptAddsContextWithinFileBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\nfive\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		coreStart     int
		coreEnd       int
		wantStartLine int
		wantEndLine   int
		want          string
	}{
		{name: "start boundary", coreStart: 1, coreEnd: 3, wantStartLine: 1, wantEndLine: 4, want: "one\ntwo\nthree\nfour"},
		{name: "end boundary", coreStart: 4, coreEnd: 5, wantStartLine: 3, wantEndLine: 5, want: "three\nfour\nfive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quote, startLine, endLine, err := readQuoteExcerpt(context.Background(), path, test.coreStart, test.coreEnd)
			if err != nil {
				t.Fatalf("readQuoteExcerpt(): %v", err)
			}
			if startLine != test.wantStartLine || endLine != test.wantEndLine || quote != test.want {
				t.Fatalf("excerpt = (%d-%d, %q), want (%d-%d, %q)",
					startLine, endLine, quote, test.wantStartLine, test.wantEndLine, test.want)
			}
		})
	}
}

func TestFormatMatchPreservesBlankContextLinesAndLineRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blank.txt")
	if err := os.WriteFile(path, []byte("before\n\ncore\n\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	quote, startLine, endLine, err := readQuoteExcerpt(context.Background(), path, 3, 3)
	if err != nil {
		t.Fatalf("readQuoteExcerpt(): %v", err)
	}
	got := FormatMatch(Match{
		Path:           "blank.txt",
		Quote:          quote,
		QuoteStartLine: startLine,
		QuoteEndLine:   endLine,
	}, 120)
	for _, want := range []string{"lines 2-4:", "  ```\n  \n  core\n  \n  ```"} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatMatch() missing %q:\n%s", want, got)
		}
	}
}

func TestFormatMatchRendersMarkdownCodeFence(t *testing.T) {
	match := Match{
		Path:           "sample.go",
		Size:           572,
		Score:          0.73,
		HasScore:       true,
		Quote:          "func run() {\n```\n}",
		QuoteStartLine: 22,
		QuoteEndLine:   27,
	}
	got := FormatMatch(match, 120)
	for _, want := range []string{
		"* 0.73  `sample.go`  572 B, lines 22-27:",
		"  ````go\n",
		"  ```\n",
		"  ````",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatMatch() missing %q:\n%s", want, got)
		}
	}

	unknown := FormatMatch(Match{Path: "notes.txt", Quote: "plain text"}, 120)
	if strings.Contains(unknown, "```text") || !strings.Contains(unknown, "\n  ```\n") {
		t.Fatalf("unknown extension should use a language-less fence:\n%s", unknown)
	}
}

func TestFormatMatchBoundsLongSourceLines(t *testing.T) {
	got := FormatMatch(Match{
		Path:  "wide.rs",
		Quote: strings.Repeat("界", 100),
	}, 24)
	lines := strings.Split(got, "\n")
	if len(lines) < 4 {
		t.Fatalf("FormatMatch() produced incomplete fenced snippet: %q", got)
	}
	if width := runewidth.StringWidth(lines[2]); width > 24 {
		t.Fatalf("source line width = %d, want <= 24: %q", width, lines[2])
	}
}
