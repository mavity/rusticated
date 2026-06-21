package main

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

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
	si.Focus()

	vp := viewport.New(0, 0)

	sw := &SwitchableWriter{}
	r, _ := createRunner(context.Background(), nil, sw, sw, cwd, nil)

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
		lastExhaustHeight: 0,
		isInitialized:     false,
		runner:            r,
		shellOut:          sw,
	}
	m.loadDir(leftPane, cwd, "")
	m.loadDir(rightPane, cwd, "")
	m.refreshPrompt()
	return m
}

func (m *model) refreshPrompt() {
	if m.runner == nil {
		m.shellInput.Prompt = "$ "
		return
	}

	dir := m.runner.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}

	var ps1 string
	if v, ok := m.runner.Vars["PS1"]; ok {
		ps1 = v.Str
	} else if m.runner.Env != nil {
		if ev := m.runner.Env.Get("PS1"); ev.IsSet() {
			ps1 = ev.Str
		}
	}

	if ps1 == "" {
		ps1 = os.Getenv("PS1")
	}

	if ps1 == "" {
		base := filepath.Base(dir)
		if dir == "/" || dir == "\\" {
			base = dir
		}
		m.shellInput.Prompt = base + " $ "
		return
	}

	res := ps1
	res = strings.ReplaceAll(res, "\\w", dir)
	base := filepath.Base(dir)
	if dir == "/" || dir == "\\" {
		base = dir
	}
	res = strings.ReplaceAll(res, "\\W", base)

	uName := "user"
	if u, err := user.Current(); err == nil {
		uName = u.Username
		if idx := strings.LastIndex(uName, "\\"); idx >= 0 {
			uName = uName[idx+1:]
		}
	}
	res = strings.ReplaceAll(res, "\\u", uName)
	res = strings.ReplaceAll(res, "\\$", "$")

	m.shellInput.Prompt = res
}


func (m *model) loadDir(p pane, path string, focusName string) {
	path = filepath.Clean(path)
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

	var l *list.Model
	var d *string
	if p == leftPane {
		l = &m.leftList
		d = &m.leftDir
	} else {
		l = &m.rightList
		d = &m.rightDir
	}

	l.SetItems(items)
	*d = path
	l.Title = filepath.Base(path)

	// Focus handling
	l.Select(0) // Default to first item (usually "..")
	if focusName != "" {
		for i, item := range items {
			if fi, ok := item.(fileItem); ok && fi.name == focusName {
				l.Select(i)
				break
			}
		}
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.checkAssetsCmd(),
	)
}

func (m *model) syncChatView() {
	if m.chatView.Width <= 0 {
		return
	}
	style := lipgloss.NewStyle().Width(m.chatView.Width)
	var wrapped []string
	for _, line := range m.chatLines {
		wrapped = append(wrapped, style.Render(line))
	}
	m.chatView.SetContent(strings.Join(wrapped, "\n"))
	m.chatView.GotoBottom()
}

func (m *model) watchAssetProgressCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.assetDone:
			return msg
		case msg := <-m.assetProgress:
			return msg
		}
	}
}

func (m *model) watchAIChanCmd() tea.Cmd {
	return func() tea.Msg {
		return <-m.aiMsgChan
	}
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

	// Dynamic panel height that leaves room for history
	panelHeight := m.height - 10
	if panelHeight < 5 {
		panelHeight = 5
	}
	if panelHeight > 15 {
		panelHeight = 15
	}

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
	m.syncChatView()
}

func (m *model) AddPlume(lines ...string) tea.Cmd {
	var cmds []tea.Cmd
	for _, line := range lines {
		m.plume = append(m.plume, line)

		if len(m.plume) > 120 {
			released := m.plume[0]
			m.plume = m.plume[1:]
			// Print the actual line to the permanent scrollback.
			// This also pushes the TUI up.
			cmds = append(cmds, tea.Println(released))
		}
	}

	return tea.Batch(cmds...)
}
