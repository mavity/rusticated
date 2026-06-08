package main

import "github.com/charmbracelet/lipgloss"

// Original Colors
var (
	colorBlue     = lipgloss.Color("4")       // Blue
	colorCyan     = lipgloss.Color("6")       // Cyan
	colorWhite    = lipgloss.Color("7")       // White
	colorBlack    = lipgloss.Color("#000000") // True Black
	colorDarkGray = lipgloss.Color("8")       // Dark Gray (Bright Black)
	colorGray     = lipgloss.Color("242")     // Gray
	colorDimGray  = lipgloss.Color("#4a4a4a") // Very Dark Gray
)

var (
	filePanelStyle = lipgloss.NewStyle().
			Background(colorBlue).
			Foreground(colorWhite).
			Border(lipgloss.NormalBorder()).
			BorderForeground(colorGray).
			BorderBackground(colorBlue)

	filePanelActiveStyle = lipgloss.NewStyle().
				Background(colorBlue).
				Foreground(colorCyan).
				Border(lipgloss.NormalBorder()).
				BorderForeground(colorCyan).
				BorderBackground(colorBlue)

	folderStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Background(colorBlue)

	fileStyle = lipgloss.NewStyle().
			Foreground(colorCyan).
			Background(colorBlue)

	selectedStyle = lipgloss.NewStyle().
			Foreground(colorBlack).
			Background(colorCyan)

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
