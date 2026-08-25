package main

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Explicit FAR-inspired palette. These values were selected by hand to match the
// exact desired effect, not derived by nearest-colour tricks.
var (
	colorBlue     = lipgloss.Color("#0000A8") // panel blue
	colorCyan     = lipgloss.Color("#00D9FF") // panel cyan accents
	colorYellow   = lipgloss.Color("#FFD700") // selected / action highlight
	colorNavy     = lipgloss.Color("#000080") // selection text
	colorWhite    = lipgloss.Color("#E5E5E5") // calm off-white UI text
	colorBlack    = lipgloss.Color("#000000") // strong black text + borders
	colorDarkGray = lipgloss.Color("#3A3A3A") // darker neutral UI tone
	colorGray     = lipgloss.Color("#7A7A7A") // mid neutral for dividers
	colorDimGray  = lipgloss.Color("#4A4A4A") // dim gray for subtle markers
)

// Dialog / popup palette — vivid but comfortable, FAR-inspired.
var (
	colorDlgBg     = lipgloss.Color("#E5E5E5") // stark light gray background
	colorDlgBorder = lipgloss.Color("#000000") // black border
	colorDlgText   = lipgloss.Color("#000000") // black text
	colorDlgMuted  = lipgloss.Color("#555555") // muted gray text
	colorBtnFg     = lipgloss.Color("#FFFF55") // active button foreground
	colorBtnBg     = lipgloss.Color("#000000") // active button background
	colorBtnAltBg  = lipgloss.Color("#A0A0A0") // inactive button background
	colorBtnAltFg  = lipgloss.Color("#000000") // inactive button foreground
)

// Explicit ANSI 16 fallbacks for the exact rich colours we intentionally use.
var ansi16Fallback = map[[3]int]string{
	{0, 0, 0}:       "30",
	{0, 0, 168}:     "44",
	{0, 0, 120}:     "44",
	{229, 229, 229}: "47",
	{255, 255, 85}:  "93",
	{160, 160, 160}: "100",
}

func ansiFlatCode(r, g, b int) string {
	return ansi16Fallback[[3]int{r, g, b}]
}

var sgrRe = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// ansiDualColor wraps rich 24-bit sequences with the exact ANSI fallback we
// selected for that colour. Legacy terminals keep the flat fallback while rich
// terminals still apply the exact true-color value.
func ansiDualColor(s string) string {
	return sgrRe.ReplaceAllStringFunc(s, func(seq string) string {
		params := seq[2 : len(seq)-1]
		if params == "" {
			return seq
		}
		toks := strings.Split(params, ";")
		var flats []string
		for i := 0; i < len(toks); i++ {
			if (toks[i] == "38" || toks[i] == "48") && i+4 < len(toks) && toks[i+1] == "2" {
				r, _ := strconv.Atoi(toks[i+2])
				g, _ := strconv.Atoi(toks[i+3])
				b, _ := strconv.Atoi(toks[i+4])
				if flat := ansiFlatCode(r, g, b); flat != "" {
					flats = append(flats, flat)
				}
				i += 4
			}
		}
		if len(flats) == 0 {
			return seq
		}
		return "\x1b[" + strings.Join(flats, ";") + "m" + seq
	})
}

var (
	filePanelStyle = lipgloss.NewStyle().
			Background(colorBlue).
			Foreground(colorWhite).
			Border(lipgloss.NormalBorder()).
			BorderForeground(colorGray).
			BorderBackground(colorBlue)

	filePanelActiveStyle = lipgloss.NewStyle().
				Background(colorBlue).
				Foreground(colorYellow).
				Border(lipgloss.NormalBorder()).
				BorderForeground(colorGray).
				BorderBackground(colorBlue)

	folderStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Background(colorBlue)

	fileStyle = lipgloss.NewStyle().
			Foreground(colorCyan).
			Background(colorBlue)

	selectedFileStyle = lipgloss.NewStyle().
				Foreground(colorNavy).
				Background(colorYellow)

	selectedFolderStyle = lipgloss.NewStyle().
				Foreground(colorDarkGray).
				Background(colorYellow)

	selectedStyle = selectedFileStyle // fallback

	markedStyle = lipgloss.NewStyle().
			Foreground(colorYellow).
			Background(colorBlue).
			Bold(true)

	activeTitleStyle = lipgloss.NewStyle().
				Foreground(colorDarkGray).
				Background(colorYellow).
				Bold(true)

	inactiveSelectedStyle = lipgloss.NewStyle().
				Foreground(colorWhite).
				Background(colorBlue)

	chatStyle = lipgloss.NewStyle().
			Background(colorDarkGray).
			Foreground(colorWhite).
			Border(lipgloss.NormalBorder()).
			BorderForeground(colorGray).
			BorderBackground(colorDarkGray)

	plumeStyle = lipgloss.NewStyle()

	promptStyle = lipgloss.NewStyle()
)
