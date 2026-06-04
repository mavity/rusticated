package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"mvdan.cc/sh/moreinterp/coreutils"
	"mvdan.cc/sh/v3/interp"
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
	si.Prompt = "$ "
	si.Focus()

	vp := viewport.New(0, 0)

	sw := &SwitchableWriter{}
	r, _ := interp.New(
		interp.Dir(cwd),
		interp.StdIO(nil, sw, sw),
		interp.ExecHandler(coreutils.ExecHandler(interp.DefaultExecHandler(0))),
	)

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
	return tea.Batch(
		textinput.Blink,
	)
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
