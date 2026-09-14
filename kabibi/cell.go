package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Cell represents a single terminal character and its visual state.
type Cell struct {
	R       rune
	FG      lipgloss.Color
	BG      lipgloss.Color
	Bold    bool
	Italic  bool
	Reverse bool
}

// Rect describes a bounded 2D region.
type Rect struct {
	X, Y, W, H int
}

// CellBuf is a bounded 2D buffer of styled terminal cells.
type CellBuf struct {
	Width  int
	Height int
	cells  []Cell
}

func NewCellBuf(w, h int) CellBuf {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return CellBuf{Width: w, Height: h, cells: make([]Cell, w*h)}
}

func (b *CellBuf) index(x, y int) int {
	if x < 0 || y < 0 || x >= b.Width || y >= b.Height {
		return -1
	}
	return y*b.Width + x
}

func (b *CellBuf) Get(x, y int) Cell {
	idx := b.index(x, y)
	if idx < 0 {
		return Cell{}
	}
	return b.cells[idx]
}

func (b *CellBuf) Set(x, y int, c Cell) {
	idx := b.index(x, y)
	if idx >= 0 {
		b.cells[idx] = c
	}
}

func (b *CellBuf) Fill(r Rect, c Cell) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			if r.X+x < 0 || r.Y+y < 0 || r.X+x >= b.Width || r.Y+y >= b.Height {
				continue
			}
			b.Set(r.X+x, r.Y+y, c)
		}
	}
}

func (b *CellBuf) Blit(src CellBuf, x, y int) {
	for sy := 0; sy < src.Height; sy++ {
		for sx := 0; sx < src.Width; sx++ {
			if sx < 0 || sy < 0 {
				continue
			}
			cell := src.Get(sx, sy)
			if cell == (Cell{}) {
				continue
			}
			b.Set(x+sx, y+sy, cell)
		}
	}
}

func Serialize(buf CellBuf) string {
	if buf.Width <= 0 || buf.Height <= 0 {
		return ""
	}

	var b strings.Builder
	for y := 0; y < buf.Height; y++ {
		if y > 0 {
			b.WriteString("\n")
		}
		var currentFG, currentBG lipgloss.Color
		var currentBold, currentItalic, currentReverse bool
		for x := 0; x < buf.Width; x++ {
			cell := buf.Get(x, y)
			if cell.R == 0 {
				cell.R = ' '
			}
			if cell.FG != currentFG || cell.BG != currentBG || cell.Bold != currentBold || cell.Italic != currentItalic || cell.Reverse != currentReverse {
				b.WriteString(styleEscape(cell))
				currentFG = cell.FG
				currentBG = cell.BG
				currentBold = cell.Bold
				currentItalic = cell.Italic
				currentReverse = cell.Reverse
			}
			b.WriteRune(cell.R)
		}
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func styleEscape(cell Cell) string {
	params := make([]string, 0, 5)
	if cell.Bold {
		params = append(params, "1")
	}
	if cell.Italic {
		params = append(params, "3")
	}
	if cell.Reverse {
		params = append(params, "7")
	}
	if cell.FG != "" {
		params = append(params, "38;2;"+colorSpec(cell.FG))
	}
	if cell.BG != "" {
		params = append(params, "48;2;"+colorSpec(cell.BG))
	}
	if len(params) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(params, ";") + "m"
}

func colorSpec(c lipgloss.Color) string {
	// lipgloss.Color is a string-backed type and may be provided as a hex code.
	// If a different format is passed, fall back to a neutral white value.
	s := string(c)
	if strings.HasPrefix(s, "#") && len(s) == 7 {
		return fmt.Sprintf("%d;%d;%d", parseHexByte(s[1:3]), parseHexByte(s[3:5]), parseHexByte(s[5:7]))
	}
	return "255;255;255"
}

func parseHexByte(s string) int {
	var v int
	for _, ch := range s {
		switch {
		case ch >= '0' && ch <= '9':
			v = (v << 4) | int(ch-'0')
		case ch >= 'a' && ch <= 'f':
			v = (v << 4) | int(ch-'a'+10)
		case ch >= 'A' && ch <= 'F':
			v = (v << 4) | int(ch-'A'+10)
		}
	}
	return v
}
