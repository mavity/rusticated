package main

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

func dirTitleName(path string) string {
	trimmed := strings.TrimRight(path, "/\\")
	if trimmed == "" {
		return " / "
	}
	base := filepath.Base(trimmed)
	return " " + base + " "
}

func renderPanelWithTitle(m *model, p pane, title string, titleStyle lipgloss.Style, panelStyle lipgloss.Style, borderColor lipgloss.TerminalColor, height int) string {
	var l *list.Model
	var files []fileItem
	var active bool
	bg := colorBlue

	if p == leftPane {
		l = &m.leftList
		active = m.activePane == leftPane
	} else if p == rightPane {
		l = &m.rightList
		active = m.activePane == rightPane
	}

	for _, item := range l.Items() {
		if fi, ok := item.(fileItem); ok {
			files = append(files, fi)
		}
	}

	width := l.Width() + 2 // inner width + borders
	innerW := width - 2    // content area
	innerH := height - 2   // content area (one for top border, one for bottom border)

	// Create top border manually
	border := lipgloss.NormalBorder()
	bStyle := lipgloss.NewStyle().
		Foreground(borderColor).
		Background(bg)

	tWidth := lipgloss.Width(title)
	sideWidth := (innerW - tWidth) / 2
	if sideWidth < 0 {
		sideWidth = 0
	}

	leftSide := strings.Repeat(border.Top, sideWidth)
	rightSide := strings.Repeat(border.Top, innerW-tWidth-sideWidth)
	if innerW-tWidth-sideWidth < 0 {
		rightSide = ""
	}

	topBorder := bStyle.Render(border.TopLeft+leftSide) +
		titleStyle.Render(title) +
		bStyle.Render(rightSide+border.TopRight)

	// Content Rendering (Multi-column)
	numCols := innerW / 18
	if numCols < 1 {
		numCols = 1
	}
	itemsPerCol := innerH
	if itemsPerCol < 1 {
		itemsPerCol = 1
	}
	itemsPerPage := numCols * itemsPerCol

	selectedIdx := l.Index()
	page := selectedIdx / itemsPerPage
	startIdx := page * itemsPerPage

	var columns []string
	colWidth := innerW / numCols

	for c := 0; c < numCols; c++ {
		var colLines []string
		for r := 0; r < itemsPerCol; r++ {
			idx := startIdx + c*itemsPerCol + r
			var line string
			if idx < len(files) {
				item := files[idx]
				style := fileStyle
				if item.isDir {
					style = folderStyle
				}
				if active && idx == selectedIdx {
					style = selectedStyle
				}
				name := item.name
				if len(name) > colWidth-2 {
					name = name[:colWidth-3] + "…"
				}
				// Use explicit Width to pad so we don't rely on fmt.Sprintf as much
				line = style.Copy().Width(colWidth).Render(" " + name)
			} else {
				line = lipgloss.NewStyle().Background(bg).Width(colWidth).Render(" ")
			}
			colLines = append(colLines, line)
		}
		columns = append(columns, strings.Join(colLines, "\n"))
	}

	content := lipgloss.JoinHorizontal(lipgloss.Top, columns...)

	// Apply side/bottom borders
	contentStyle := panelStyle.Copy().
		Border(lipgloss.NormalBorder(), false, true, true, true).
		Height(innerH).
		Width(innerW).
		Background(bg).
		BorderBackground(bg)

	return topBorder + "\n" + contentStyle.Render(content)
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Searching for screen..."
	}

	// 1. Plume (Background) - Clean and normalize lines to prevent "zebra" splitting
	var cleanPlumeLines []string
	for _, pLine := range m.plume {
		// Remove any existing newlines or carriage returns that would double-space
		line := strings.ReplaceAll(pLine, "\n", "")
		line = strings.ReplaceAll(line, "\r", "")
		cleanPlumeLines = append(cleanPlumeLines, line)
	}

	// 2. Prepare Panels
	activeTitleStyle := lipgloss.NewStyle().Foreground(colorWhite).Background(colorCyan).Bold(true)
	inactiveTitleStyle := lipgloss.NewStyle().Foreground(colorWhite).Background(colorBlue)

	leftStyle := filePanelStyle
	rightStyle := filePanelStyle
	leftTStyle := inactiveTitleStyle
	rightTStyle := inactiveTitleStyle
	leftBColor := colorGray
	rightBColor := colorGray

	if m.activePane == leftPane {
		leftStyle = filePanelActiveStyle
		leftTStyle = activeTitleStyle
		leftBColor = colorCyan
	} else if m.activePane == rightPane {
		rightStyle = filePanelActiveStyle
		rightTStyle = activeTitleStyle
		rightBColor = colorCyan
	}

	// Managed height constraints
	panelHeight := m.height - 10
	if panelHeight < 5 {
		panelHeight = 5
	}
	if panelHeight > 15 {
		panelHeight = 15
	}

	left := renderPanelWithTitle(&m, leftPane, dirTitleName(m.leftDir), leftTStyle, leftStyle, leftBColor, panelHeight)
	right := renderPanelWithTitle(&m, rightPane, dirTitleName(m.rightDir), rightTStyle, rightStyle, rightBColor, panelHeight)

	// Chat Panel
	peekWidth := 8
	chatFullWidth := m.chatView.Width + 2
	var actualChatWidth int
	if m.chatOpen {
		actualChatWidth = chatFullWidth
	} else {
		actualChatWidth = peekWidth
	}

	cStyle := chatStyle
	if m.chatOpen && m.activePane == chatPane {
		cStyle = cStyle.BorderForeground(colorWhite)
	}

	var chat string
	chatTitle := " AI Chat "
	if !m.chatOpen {
		chatTitle = " AI> "
	}

	cBorderColor := colorGray
	if m.chatOpen && m.activePane == chatPane {
		cBorderColor = colorWhite
	}

	if m.chatOpen {
		chatContent := lipgloss.JoinVertical(lipgloss.Left,
			m.chatView.View(),
			m.chatInput.View(),
		)
		width := chatFullWidth
		innerW := width - 2
		innerH := panelHeight - 2
		bg := colorDarkGray

		border := lipgloss.NormalBorder()
		bStyle := lipgloss.NewStyle().Foreground(cBorderColor).Background(bg)
		tStyle := lipgloss.NewStyle().Foreground(colorWhite).Background(bg)
		if m.activePane == chatPane {
			tStyle = activeTitleStyle
		}

		tWidth := lipgloss.Width(chatTitle)
		sideWidth := (innerW - tWidth) / 2
		leftSide := strings.Repeat(border.Top, sideWidth)
		rightSide := strings.Repeat(border.Top, innerW-tWidth-sideWidth)

		topBorder := bStyle.Render(border.TopLeft+leftSide) +
			tStyle.Render(chatTitle) +
			bStyle.Render(rightSide+border.TopRight)

		contentStyle := cStyle.Copy().
			Border(lipgloss.NormalBorder(), false, true, true, true).
			Height(innerH).
			Width(innerW).
			BorderForeground(cBorderColor).
			BorderBackground(bg).
			Background(bg)

		chat = topBorder + "\n" + contentStyle.Render(chatContent)
	} else {
		chatContent := "\n AI>"
		chat = chatStyle.Copy().
			Border(lipgloss.NormalBorder(), true, false, true, true).
			BorderForeground(cBorderColor).
			Width(actualChatWidth).
			Height(panelHeight - 2). // height includes content, borders add 2
			Render(chatContent)
	}

	// Row of panels
	middleRow := lipgloss.JoinHorizontal(lipgloss.Top, left, right, chat)

	// Plume Partitioning
	// Available height below panels:
	footerHeight := 5

	totalPlumeCount := len(cleanPlumeLines)
	var exhaustLines []string
	var footerLines []string

	// Split the plume into three parts:
	// 1. Footer: Visible immediately below panels
	// 2. Occluded: Hidden "behind" the panels (equal to panelHeight)
	// 3. Exhaust: Visible above panels

	// Calculate indices
	footerStart := totalPlumeCount - footerHeight
	if footerStart < 0 {
		footerStart = 0
	}

	occludedStart := footerStart - panelHeight
	if occludedStart < 0 {
		occludedStart = 0
	}

	footerLines = cleanPlumeLines[footerStart:]
	// occludedLines := cleanPlumeLines[occludedStart:footerStart] // These are not rendered
	exhaustLines = cleanPlumeLines[:occludedStart]

	pStyle := plumeStyle.Copy().Width(m.width)

	// Render Exhaust (above panels)
	var paddedExhaust []string
	for _, l := range exhaustLines {
		paddedExhaust = append(paddedExhaust, pStyle.Render(l))
	}
	exhaustView := strings.Join(paddedExhaust, "\n")

	// Render Footer (below panels)
	var paddedFooter []string
	for _, l := range footerLines {
		paddedFooter = append(paddedFooter, pStyle.Render(l))
	}
	// Pad footer to maintain fixed height below panels so panels stay in
	// a consistent vertical relationship with the prompt.
	if len(paddedFooter) < footerHeight {
		padding := footerHeight - len(paddedFooter)
		for i := 0; i < padding; i++ {
			paddedFooter = append([]string{pStyle.Render("")}, paddedFooter...)
		}
	}
	footerView := strings.Join(paddedFooter, "\n")

	// Prompt at bottom
	promptPrefix := "$ "
	if m.activePane == chatPane {
		promptPrefix = "AI> "
	}
	prompt := promptStyle.Copy().Width(m.width).Render(promptPrefix + m.shellInput.View())

	var components []string
	components = append(components, exhaustView)
	components = append(components, middleRow, footerView, prompt)

	return lipgloss.JoinVertical(lipgloss.Left, components...)
}
