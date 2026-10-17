package fabric

import (
	"strings"
	"testing"

	"github.com/mavity/rusticated/kabibi/tools/hashline"
)

func TestSearchAndExpand_FabricExpansionAndEllipsis(t *testing.T) {
	src := strings.Join([]string{
		"package demo",
		"",
		"// FirstHandler processes initial requests.",
		"func FirstHandler() {",
		"\tx := 1",
		"\ty := 2",
		"\tactivePTY := start()",
		"\t_ = x + y",
		"}",
		"",
		"func UnrelatedMiddle() {",
		"\ta := 10",
		"\tb := 20",
		"\tc := 30",
		"\t_ = a + b + c",
		"}",
		"",
		"func SecondHandler() {",
		"\tmu.Lock()",
		"\tactivePTY = nil",
		"\tmu.Unlock()",
		"}",
	}, "\n")

	lines := hashline.SplitLines(src)
	res := SearchAndExpand("demo.go", 1, lines, "activePTY", "auto", nil, 50)
	if len(res.MatchLines) != 2 {
		t.Fatalf("expected 2 matches for activePTY, got %v", res.MatchLines)
	}
	if res.TierUsed != "plain" {
		t.Fatalf("expected plain tier, got %q", res.TierUsed)
	}
	if len(res.Ranges) != 2 {
		t.Fatalf("expected 2 disjoint structural slices, got %+v", res.Ranges)
	}

	out := hashline.FormatHashlines("demo.go", []byte(src), res.Ranges, FormatMatchNote(len(lines), res, "activePTY"), true)
	if !strings.Contains(out, "func FirstHandler()") {
		t.Fatalf("expected fabric expansion to capture enclosing FirstHandler header:\n%s", out)
	}
	if !strings.Contains(out, "func SecondHandler()") {
		t.Fatalf("expected fabric expansion to capture enclosing SecondHandler header:\n%s", out)
	}
	if strings.Contains(out, "UnrelatedMiddle") {
		t.Fatalf("expected UnrelatedMiddle to be folded out:\n%s", out)
	}
	if !strings.Contains(out, "\n…\n") {
		t.Fatalf("expected Unicode ellipsis '…' separator between disjoint slices:\n%s", out)
	}
}

func TestSearchAndExpand_FuzzyEscalation(t *testing.T) {
	lines := []string{
		"func calculateVisualRows(block CommandBlock, width int) int {",
		"\treturn renderBlock(block, width).Height",
		"}",
	}
	// Slight typo in "calculateVisulRows" -> Tier 1 & 2 miss, Tier 3 fuzzy hits!
	res := SearchAndExpand("widget.go", 1, lines, "calculateVisulRows", "auto", nil, 30)
	if len(res.MatchLines) != 1 || res.MatchLines[0] != 1 {
		t.Fatalf("expected fuzzy match on line 1, got %+v", res)
	}
	if res.TierUsed != "fuzzy" {
		t.Fatalf("expected fuzzy tier, got %q", res.TierUsed)
	}
}
