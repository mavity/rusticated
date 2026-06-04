package main

import (
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type pane int

const (
	leftPane pane = iota
	rightPane
	chatPane
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

	plume             []string
	lastTab           time.Time
	chatLines         []string
	lastExhaustHeight int
	isInitialized     bool
	quitting          bool
}
