package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type pane int

const (
	leftPane pane = iota
	rightPane
	chatPane
)

// Original Colors
var (
	colorBlue     = lipgloss.Color("4")       // Blue
	colorCyan     = lipgloss.Color("6")       // Cyan
	colorWhite    = lipgloss.Color("7")       // White
	colorBlack    = lipgloss.Color("#000000") // True Black
	colorDarkGray = lipgloss.Color("8")       // Dark Gray (Bright Black)
	colorGray     = lipgloss.Color("242")     // Gray
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

	plumeStyle = lipgloss.NewStyle().
			Foreground(colorDarkGray).
			Background(colorBlack)

	promptStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Background(colorBlack)
)

type fileItem struct {
	name  string
	isDir bool
}

func (i fileItem) Title() string       { return i.name }
func (i fileItem) Description() string { return "" }
func (i fileItem) FilterValue() string { return i.name }

type customDelegate struct {
	active bool
}

func (d customDelegate) Height() int                               { return 1 }
func (d customDelegate) Spacing() int                              { return 0 }
func (d customDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d customDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(fileItem)
	if !ok {
		return
	}

	str := fmt.Sprintf(" %-18s ", i.name)
	if len(str) > 20 {
		str = str[:19] + " "
	}

	style := fileStyle
	if i.isDir {
		style = folderStyle
	}

	if d.active && index == m.Index() {
		fmt.Fprint(w, selectedStyle.Render(str))
	} else {
		fmt.Fprint(w, style.Render(str))
	}
}

type model struct {
	leftList   list.Model
	rightList  list.Model
	chatInput  textinput.Model
	shellInput textinput.Model
	chatView   viewport.Model
	activePane pane
	chatOpen   bool
	width      int
	height     int

	leftDir  string
	rightDir string

	plume     []string
	lastTab   time.Time
	chatLines []string
}

func initialModel() model {
	cwd, _ := os.Getwd()

	delegate := customDelegate{}

	li := list.New([]list.Item{}, delegate, 20, 0)
	li.SetShowHelp(false)
	li.SetShowPagination(false)
	li.SetShowStatusBar(false)
	li.SetFilteringEnabled(false)
	li.SetShowTitle(false)

	ri := list.New([]list.Item{}, delegate, 20, 0)
	ri.SetShowHelp(false)
	ri.SetShowPagination(false)
	ri.SetShowStatusBar(false)
	ri.SetFilteringEnabled(false)
	ri.SetShowTitle(false)

	ti := textinput.New()
	ti.Placeholder = "Ask AI..."
	ti.Prompt = "AI> "

	si := textinput.New()
	si.Prompt = "$ "
	si.Focus()

	vp := viewport.New(0, 0)

	m := model{
		leftList:   li,
		rightList:  ri,
		chatInput:  ti,
		shellInput: si,
		chatView:   vp,
		activePane: leftPane,
		chatOpen:   false,
		leftDir:    cwd,
		rightDir:   cwd,
		plume: []string{
			"kabibi-go v0.1.0 starting...",
			"loading file managers...",
			"AI interface ready.",
			"Welcome back, master.",
			"Run 'help' for available commands.",
		},
		chatLines: []string{
			"System: AI slider initialized.",
		},
	}
	m.loadDir(leftPane, cwd)
	m.loadDir(rightPane, cwd)
	return m
}

func (m *model) loadDir(p pane, path string) {
	entries, _ := os.ReadDir(path)
	var items []list.Item
	items = append(items, fileItem{name: "..", isDir: true})
	for _, entry := range entries {
		items = append(items, fileItem{name: entry.Name(), isDir: entry.IsDir()})
	}
	sort.Slice(items, func(i, j int) bool {
		ii, jj := items[i].(fileItem), items[j].(fileItem)
		if ii.name == ".." {
			return true
		}
		if jj.name == ".." {
			return false
		}
		if ii.isDir != jj.isDir {
			return ii.isDir
		}
		return strings.ToLower(ii.name) < strings.ToLower(jj.name)
	})

	if p == leftPane {
		m.leftList.SetItems(items)
		m.leftDir = path
		m.leftList.Title = filepath.Base(path)
	} else {
		m.rightList.SetItems(items)
		m.rightDir = path
		m.rightList.Title = filepath.Base(path)
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m *model) updateDelegates() {
	if m.activePane == leftPane {
		m.leftList.SetDelegate(customDelegate{active: true})
		m.rightList.SetDelegate(customDelegate{active: false})
	} else if m.activePane == rightPane {
		m.leftList.SetDelegate(customDelegate{active: false})
		m.rightList.SetDelegate(customDelegate{active: true})
	} else {
		m.leftList.SetDelegate(customDelegate{active: false})
		m.rightList.SetDelegate(customDelegate{active: false})
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()
		switch key {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "tab":
			if m.chatOpen {
				m.chatOpen = false
				m.activePane = leftPane
				m.chatInput.Blur()
				m.shellInput.Focus()
				m.recalculateLayout()
				m.updateDelegates()
				return m, nil
			}

			// Handle double-tab logic (300ms)
			now := time.Now()
			if now.Sub(m.lastTab) < 300*time.Millisecond {
				m.chatOpen = true
				m.activePane = chatPane
				m.chatInput.Focus()
				m.shellInput.Blur()
				m.lastTab = time.Time{} // Reset
				m.recalculateLayout()
				m.updateDelegates()
				return m, nil
			}
			m.lastTab = now

			// Single tab: toggle between left/right pane
			if m.activePane == leftPane {
				m.activePane = rightPane
			} else {
				m.activePane = leftPane
			}
			m.updateDelegates()
		case "enter":
			if m.chatOpen {
				input := m.chatInput.Value()
				if input != "" {
					m.chatLines = append(m.chatLines, "User: "+input)
					m.chatLines = append(m.chatLines, "AI: I am a mock AI. You said: "+input)
					m.chatView.SetContent(strings.Join(m.chatLines, "\n"))
					m.chatInput.Reset()
					m.chatView.GotoBottom()
				}
			} else {
				input := m.shellInput.Value()
				if input != "" {
					m.plume = append(m.plume, "$ "+input)
					m.plume = append(m.plume, "Command executed (mock).")
					m.shellInput.Reset()
					return m, nil
				}

				var curList *list.Model
				var curDir *string
				var p pane
				if m.activePane == leftPane {
					curList = &m.leftList
					curDir = &m.leftDir
					p = leftPane
				} else {
					curList = &m.rightList
					curDir = &m.rightDir
					p = rightPane
				}

				if item, ok := curList.SelectedItem().(fileItem); ok {
					if item.isDir {
						newPath := filepath.Join(*curDir, item.name)
						m.loadDir(p, newPath)
					} else {
						// Mock shell execution for files
						m.plume = append(m.plume, fmt.Sprintf("$ run %s", item.name))
						m.plume = append(m.plume, fmt.Sprintf("Executed %s successfully.", item.name))
					}
				}
			}
		case "up", "down", "left", "right", "pgup", "pgdown", "home", "end":
			// If chat is open, cursor keys go to chat
			if m.chatOpen && m.activePane == chatPane {
				m.chatInput, cmd = m.chatInput.Update(msg)
				return m, cmd
			}

			// Otherwise they move file manager selection
			var l *list.Model
			if m.activePane == leftPane {
				l = &m.leftList
			} else {
				l = &m.rightList
			}

			switch key {
			case "up":
				l.CursorUp()
			case "down":
				l.CursorDown()
			case "left":
				m.moveCursorHorizontal(-1)
			case "right":
				m.moveCursorHorizontal(1)
			case "pgup":
				m.moveCursorHorizontal(-3) // 3 columns
			case "pgdown":
				m.moveCursorHorizontal(3) // 3 columns
			case "home":
				l.Select(0)
			case "end":
				l.Select(len(l.Items()) - 1)
			}
			m.updateDelegates()
		default:
			if m.chatOpen {
				m.chatInput, cmd = m.chatInput.Update(msg)
			} else {
				m.shellInput, cmd = m.shellInput.Update(msg)
			}
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalculateLayout()
	}

	return m, nil
}

func (m *model) moveCursorHorizontal(dir int) {
	itemsPerCol := m.height - 7
	if itemsPerCol <= 0 {
		return
	}

	var l *list.Model
	if m.activePane == leftPane {
		l = &m.leftList
	} else if m.activePane == rightPane {
		l = &m.rightList
	} else {
		return
	}

	newIdx := l.Index() + (dir * itemsPerCol)
	if newIdx < 0 {
		newIdx = 0
	}
	if newIdx >= len(l.Items()) {
		newIdx = len(l.Items()) - 1
	}
	l.Select(newIdx)
}

func (m *model) recalculateLayout() {
	// Sidebar width logic from original kabibi
	// fn sidebar_chat_width(total_width: u16) -> u16 {
	//     let preferred = ((total_width as u32 * 35) / 100) as u16;
	//     preferred.clamp(30, total_width.saturating_sub(1).max(1))
	// }
	chatFullWidth := (m.width * 35) / 100
	if chatFullWidth < 30 {
		chatFullWidth = 30
	}
	if chatFullWidth >= m.width {
		chatFullWidth = m.width - 1
	}

	peekWidth := 8
	if peekWidth >= m.width-2 {
		peekWidth = m.width - 2
	}
	if peekWidth < 1 {
		peekWidth = 1
	}

	var actualChatWidth int
	var filesWidth int
	if m.chatOpen {
		actualChatWidth = chatFullWidth
		filesWidth = m.width - actualChatWidth
	} else {
		actualChatWidth = peekWidth
		filesWidth = m.width - actualChatWidth
	}

	if filesWidth < 2 {
		filesWidth = 2
	}

	// The panel_top_margin is 1, plume_footer_lines is 4, plus prompt height
	// We'll approximate for now but keep it full width.
	panelHeight := m.height - 6
	if panelHeight < 1 {
		panelHeight = 1
	}

	// Subtract borders (2 for each list) for internal content area
	// Leave 1 extra char for safety in some terminals
	safeFilesWidth := filesWidth - 1
	if safeFilesWidth < 4 {
		safeFilesWidth = 4
	}

	leftWidth := safeFilesWidth / 2
	rightWidth := safeFilesWidth - leftWidth

	m.leftList.SetSize(leftWidth-2, panelHeight-2)
	m.rightList.SetSize(rightWidth-2, panelHeight-2)

	m.chatView.Width = chatFullWidth - 2
	m.chatView.Height = panelHeight - 3
	m.chatInput.Width = chatFullWidth - 3
	m.shellInput.Width = m.width - 3
}

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
	innerH := height - 1   // content area (one line for top border)

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
	if m.width == 0 {
		return "Searching for screen..."
	}

	// 1. Plume (Background) - Clean and normalize lines to prevent "zebra" splitting
	plumeHeight := m.height - 1
	var cleanPlumeLines []string
	for _, pLine := range m.plume {
		// Remove any existing newlines or carriage returns that would double-space
		line := strings.ReplaceAll(pLine, "\n", "")
		line = strings.ReplaceAll(line, "\r", "")
		cleanPlumeLines = append(cleanPlumeLines, line)
	}

	if len(cleanPlumeLines) > plumeHeight {
		cleanPlumeLines = cleanPlumeLines[len(cleanPlumeLines)-plumeHeight:]
	}
	for len(cleanPlumeLines) < plumeHeight {
		cleanPlumeLines = append([]string{""}, cleanPlumeLines...)
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

	panelHeight := m.height - 6
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
		innerH := panelHeight - 1
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

		chat = lipgloss.JoinVertical(lipgloss.Left, topBorder, contentStyle.Render(chatContent))
	} else {
		chatContent := "\n AI>"
		chat = cStyle.Border(lipgloss.NormalBorder(), true, false, true, true).
			Width(actualChatWidth).Height(panelHeight).Render(chatContent)
	}

	// Row of panels
	middleRow := lipgloss.JoinHorizontal(lipgloss.Top, left, right, chat)

	// Stack view
	topOffset := 1
	topPlume := plumeStyle.Width(m.width).Height(topOffset).Render(cleanPlumeLines[0])

	middleLines := strings.Split(middleRow, "\n")
	middleLinesCount := len(middleLines)

	bottomPlumeCount := m.height - topOffset - middleLinesCount - 1
	if bottomPlumeCount < 0 {
		bottomPlumeCount = 0
	}

	bottomLines := cleanPlumeLines[len(cleanPlumeLines)-bottomPlumeCount:]
	bottomPlume := plumeStyle.Width(m.width).Height(bottomPlumeCount).Render(strings.Join(bottomLines, "\n"))

	// Prompt at bottom
	promptPrefix := "$ "
	if m.activePane == chatPane {
		promptPrefix = "AI> "
	}
	prompt := promptStyle.Width(m.width).Height(1).Render(promptPrefix + m.shellInput.View())

	// Use a single strings.Join to avoid any nested JoinVertical logic that might interleave
	finalContent := topPlume + "\n" + middleRow + "\n" + bottomPlume + "\n" + prompt

	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Background(colorBlack).
		Render(finalContent)
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}
