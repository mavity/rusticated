package shell

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func BenchmarkShellRenderSettledBlocks(b *testing.B) {
	s := &Shell{}
	for i := 0; i < 100; i++ {
		var output bytes.Buffer
		fmt.Fprintf(&output, "completed command output %03d\n%s", i, strings.Repeat("result ", 12))
		s.blocks = append(s.blocks, CommandBlock{
			Kind:   BlockCommand,
			Source: SourceUserShell,
			Prompt: "$ ",
			Input:  fmt.Sprintf("command-%03d", i),
			Output: output,
			Status: StatusSuccess,
		})
	}
	s.renderStackBlocksLocked(80)

	b.Run("cached_cells", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			benchmarkRenderedBlocks, benchmarkRenderedBlockIndexes = s.renderStackBlocksLocked(80)
		}
	})
	b.Run("ansi_vt_round_trip", func(b *testing.B) {
		compositor := terminal.NewTerminal(1, 1)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rendered := make([]terminal.CellBuf, 0, len(s.blocks))
			for j := range s.blocks {
				block := &s.blocks[j]
				size := block.Measure(ui.Constraints{MaxW: 80})
				ansiFrame := block.Render(ui.Rect{W: 80, H: size.H}, ui.RenderContext{})
				frame, _, _, err := compositor.RenderANSIForComposition(ansiFrame, 80, size.H)
				if err != nil {
					b.Fatal(err)
				}
				rendered = append(rendered, frame)
			}
			benchmarkRenderedBlocks = rendered
		}
	})
}

var benchmarkRenderedBlocks []terminal.CellBuf
var benchmarkRenderedBlockIndexes []int

func TestShellCachedBlockCellsMatchANSIComposition(t *testing.T) {
	block := CommandBlock{
		Kind:   BlockCommand,
		Source: SourceUserShell,
		Prompt: "$ ",
		Input:  "echo 世界",
		Output: bytes.Buffer{},
		Status: StatusSuccess,
	}
	block.Output.WriteString("hello 世界\n")

	want := renderBlockCached(&block, 24)
	ansiFrame := block.Render(ui.Rect{W: 24, H: want.Height}, ui.RenderContext{})
	got, _, _, err := terminal.NewTerminal(25, want.Height).RenderANSIForComposition(ansiFrame, 24, want.Height)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("cached block cells differ from the former ANSI-to-VT composition path")
	}
}
