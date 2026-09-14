package main

import (
	"context"
	"sync"
	"time"

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
	engine   uint64
	conv     uint64
	Messages []Message
}

type fileItem struct {
	name     string
	isDir    bool
	selected bool
}

type AppWidget struct {
	shellW        *ShellInputWidget
	dualPane      *DualPaneWidget
	plumeW        *PlumeWidget
	chatW         *ChatWidget
	activePane    pane
	chatOpen      bool
	panelsVisible bool
	width         int
	height        int

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

	streamMu  sync.Mutex // protects conversation message writes from callback goroutine
	streamGen uint64     // incremented on each new stream; stale callbacks compare and drop

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
