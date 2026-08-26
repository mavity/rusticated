package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"mvdan.cc/sh/v3/interp"
)

type pane int

const (
	leftPane pane = iota
	rightPane
	chatPane
)

// Message represents a single conversation message.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Conversation holds the state of an AI conversation session.
// Engine and conv are opaque handles to the LiteRT backend.
type Conversation struct {
	engine   uintptr
	conv     uintptr
	Messages []Message
}

type fileItem struct {
	name     string
	isDir    bool
	selected bool
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
		if i.isDir {
			fmt.Fprint(w, selectedFolderStyle.Render(str))
		} else {
			fmt.Fprint(w, selectedFileStyle.Render(str))
		}
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
	conversation      *Conversation
	lastExhaustHeight int
	isInitialized     bool
	quitting          bool
	runner            *interp.Runner
	shellOut          *SwitchableWriter
	isThinking        bool
	firstTokenRecv    bool      // first AI token arrived (thinking → streaming)
	animStart         time.Time // when current glow animation began
	flashActive       bool      // completion flash in progress
	flashStart        time.Time // when flash began

	litertReady           bool
	gemmaReady            bool
	assetsReady           bool
	isDownloading         bool
	litertDownloadPercent int
	litertDownloadDetails string
	gemmaDownloadPercent  int
	gemmaDownloadDetails  string
	assetError            string
	assetProgress         <-chan assetProgressMsg
	assetDone             <-chan tea.Msg
	aiMsgChan             <-chan tea.Msg

	// File-manager extensions
	mode      uiMode
	dialog    *dialogState
	editor    *editorModel
	opChan    <-chan tea.Msg
	opCancel  context.CancelFunc
	opActive  bool
	opKind    fileOpKind
	opCurrent string
	opDone    int64
	opTotal   int64

	// Copy/move collision resolution + richer progress.
	opResume    chan collisionChoice
	opCollision bool
	opFileDone  int64
	opFileTotal int64
	opRate      int64
}
