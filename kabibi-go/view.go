package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

func expandTabs(s string, tabWidth int) string {
	var b strings.Builder
	column := 0
	for _, r := range s {
		if r == '\t' {
			spaces := tabWidth - (column % tabWidth)
			for i := 0; i < spaces; i++ {
				b.WriteByte(' ')
				column++
			}
		} else {
			b.WriteRune(r)
			column += runewidth.RuneWidth(r)
		}
	}
	return b.String()
}

func dirTitleName(path string) string {
	path = filepath.Clean(path)
	trimmed := strings.TrimRight(path, "/\\")
	if trimmed == "" || trimmed == "." {
		// Handle root or relative current
		if path == "" || path == "." {
			return " . "
		}
		return " / "
	}
	// On Windows, filepath.Clean("C:\\") is "C:\\"
	// filepath.Base("C:\\") is "\"
	base := filepath.Base(path)
	if base == "\\" || base == "/" {
		// If it's a root, show the whole thing or just the drive
		return " " + path + " "
	}
	return " " + base + " "
}

func renderProgressBar(percentage, width int, bg lipgloss.Color, finished bool) string {
	if width <= 0 {
		return ""
	}
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}

	if finished {
		return lipgloss.NewStyle().Background(bg).Foreground(colorDimGray).Render(strings.Repeat("█", width))
	}

	// Octa-fractional characters
	octal := []string{"▏", "▎", "▍", "▌", "▋", "▊", "▉"}

	totalUnits := width * 8
	filledUnits := (percentage * totalUnits) / 100
	fullBlocks := filledUnits / 8
	remainder := filledUnits % 8

	// The user wants a specific gradient width algorithm:
	// gradientWidth = min(numFilled, max(4, numFilled/2))
	numFilled := float64(filledUnits) / 8.0

	gradientWidth := numFilled / 2.0
	if gradientWidth < 4.0 {
		gradientWidth = 4.0
	}
	if gradientWidth > numFilled {
		gradientWidth = numFilled
	}

	gradientStart := numFilled - gradientWidth

	getGradientColor := func(i int) string {
		startGrey := 0x55 // Medium-Dark Grey

		fi := float64(i)
		if fi < gradientStart {
			return fmt.Sprintf("#%02x%02x%02x", startGrey, startGrey, startGrey)
		}

		// Interpolate from gradientStart to numFilled
		div := numFilled - gradientStart
		if div <= 0 {
			return "#000000"
		}

		ratio := (fi - gradientStart) / div
		if ratio > 1 {
			ratio = 1
		}

		val := int(float64(startGrey) * (1.0 - ratio))
		return fmt.Sprintf("#%02x%02x%02x", val, val, val)
	}

	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < fullBlocks {
			color := getGradientColor(i)
			// Explicitly force black for the last block if it's the tip
			if i == fullBlocks-1 && remainder == 0 {
				color = "#000000"
			}
			b.WriteString(lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(color)).Render("█"))
		} else if i == fullBlocks && remainder > 0 {
			// fractional part is the tip
			color := getGradientColor(i)
			b.WriteString(lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(color)).Render(octal[remainder-1]))
		} else {
			b.WriteString(lipgloss.NewStyle().Background(bg).Render(" "))
		}
	}
	return b.String()
}

func truncateStringToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	var b strings.Builder
	current := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if current+rw > width-1 {
			break
		}
		b.WriteRune(r)
		current += rw
	}
	if current < width {
		return b.String()
	}
	return b.String() + "…"
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
					if item.isDir {
						style = selectedFolderStyle
					} else {
						style = selectedFileStyle
					}
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
	if m.quitting {
		var output []string
		for _, line := range m.plume {
			// Expand tabs correctly based on tab stops (typically 8 in terminals)
			expanded := expandTabs(line, 8)

			// Pad to m.width to ensure any panel backgrounds are "wiped".
			// Use runewidth to handle multi-byte/wide characters correctly.
			w := runewidth.StringWidth(expanded)
			if w < m.width {
				expanded += strings.Repeat(" ", m.width-w)
			}
			output = append(output, expanded)
		}
		return strings.Join(output, "\n")
	}

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
		leftBColor = colorGray
	} else if m.activePane == rightPane {
		rightStyle = filePanelActiveStyle
		rightTStyle = activeTitleStyle
		rightBColor = colorGray
	}

	// Managed height constraints: filling screen minus top/bottom margins
	topReserved := 2
	promptReserved := 1
	footerHeight := m.height / 5 // Proportion of rows below
	if footerHeight < 3 {
		footerHeight = 3
	}

	panelHeight := m.height - (topReserved + promptReserved + footerHeight)
	if panelHeight < 5 {
		panelHeight = 5
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
		var progressLines []string
		if m.isDownloading || !m.assetsReady || len(m.pendingPrompts) > 0 {
			detailsWidth := m.chatView.Width - 19
			if detailsWidth < 10 {
				detailsWidth = 10
			}
			barWidth := detailsWidth - 16
			if barWidth < 10 {
				barWidth = 10
			}

			litertDetails := m.litertDownloadDetails
			if litertDetails == "" {
				litertDetails = "pending"
			}
			litertDetails = truncateStringToWidth(litertDetails, detailsWidth-barWidth)

			gemmaDetails := m.gemmaDownloadDetails
			if gemmaDetails == "" {
				gemmaDetails = "pending"
			}
			gemmaDetails = truncateStringToWidth(gemmaDetails, detailsWidth-barWidth)

			lineStyle := lipgloss.NewStyle().Background(colorDarkGray).Foreground(colorWhite)
			spacer := lineStyle.Render("  ")

			labelStyle := lipgloss.NewStyle().Background(colorDarkGray).Foreground(lipgloss.Color("7"))
			litertLabel := labelStyle.Render(fmt.Sprintf("%9s", "litertlm"))
			gemmaLabel := labelStyle.Render(fmt.Sprintf("%9s", "gemma"))

			litertPct := lineStyle.Render(fmt.Sprintf("%3d%%", m.litertDownloadPercent))
			gemmaPct := lineStyle.Render(fmt.Sprintf("%3d%%", m.gemmaDownloadPercent))

			litertDet := lineStyle.Render(litertDetails)
			gemmaDet := lineStyle.Render(gemmaDetails)

			litertLine := fmt.Sprintf("%s%s%s%s%s %s", litertLabel, spacer, renderProgressBar(m.litertDownloadPercent, barWidth, colorDarkGray, m.litertReady), spacer, litertPct, litertDet)
			gemmaLine := fmt.Sprintf("%s%s%s%s%s %s", gemmaLabel, spacer, renderProgressBar(m.gemmaDownloadPercent, barWidth, colorDarkGray, m.gemmaReady), spacer, gemmaPct, gemmaDet)

			progressLines = append(progressLines, forceBackground(litertLine, colorDarkGray))
			progressLines = append(progressLines, forceBackground(gemmaLine, colorDarkGray))
			if !m.isDownloading && !m.assetsReady {
				progressLines = append(progressLines, forceBackground("waiting for runtime assets...", colorDarkGray))
			}
			if len(m.pendingPrompts) > 0 {
				progressLines = append(progressLines, forceBackground(fmt.Sprintf("Queued prompts: %d", len(m.pendingPrompts)), colorDarkGray))
			}
		}
		progressView := strings.Join(progressLines, "\n")
		progressView = forceBackground(progressView, colorDarkGray)
		chatViewHeight := panelHeight - 3 - len(progressLines)
		if chatViewHeight < 1 {
			chatViewHeight = 1
		}
		m.chatView.Height = chatViewHeight

		chatContent := lipgloss.JoinVertical(lipgloss.Left,
			m.chatView.View(),
			progressView,
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
	totalPlumeCount := len(cleanPlumeLines)
	var exhaustLines []string
	var footerLines []string

	// Calculate indices to fit screen exactly
	footerStart := totalPlumeCount - footerHeight
	if footerStart < 0 {
		footerStart = 0
	}

	occludedStart := footerStart - panelHeight
	if occludedStart < 0 {
		occludedStart = 0
	}

	// We only want to show exhaust lines that fit in the topReserved space
	exhaustStart := occludedStart - topReserved
	if exhaustStart < 0 {
		exhaustStart = 0
	}

	footerLines = cleanPlumeLines[footerStart:]
	exhaustLines = cleanPlumeLines[exhaustStart:occludedStart]

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

func forceBackground(s string, bg lipgloss.Color) string {
	if s == "" {
		return ""
	}
	// background color escape code for terminal
	// lipgloss.Color can be a string like "#333333" or "8"
	// We'll use a dummy render to see what lipgloss produces
	dummy := lipgloss.NewStyle().Background(bg).Render(" ")
	// Result is ESC[48;2;R;G;Bm   ESC[0m or ESC[48;5;Nm   ESC[0m
	// We want everything before the space.
	idx := strings.Index(dummy, " ")
	if idx == -1 {
		return s
	}
	bgCode := dummy[:idx]

	// We want to append this code after any reset \x1b[0m
	// Also ensure it starts with the background code.
	res := bgCode + strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+bgCode)
	// Avoid leaking the background code if it was appended at the very end
	if strings.HasSuffix(res, bgCode) {
		res = strings.TrimSuffix(res, bgCode)
	}
	return res
}
