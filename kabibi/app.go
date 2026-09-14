package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// Message types for app-level events

type animTickMsg time.Time
type assetProgressMsg struct {
	Stage   string
	Percent int
	Details string
}
type assetReadyMsg struct {
	Stage string
}
type assetErrorMsg struct {
	Stage string
	err   error
}

func animTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg {
		return animTickMsg(t)
	})
}

func initialModel() *AppWidget {
	conv := &Conversation{Messages: []Message{}}
	return NewAppWidget("", conv, &OsFSService{}, NewLMSession(conv))
}

func NewAppWidget(cwd string, conv *Conversation, fs FSService, ai AIService) *AppWidget {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if conv == nil {
		conv = &Conversation{Messages: []Message{}}
	}
	if conv.Messages == nil {
		conv.Messages = []Message{}
	}
	if fs == nil {
		panic("AppWidget requires a non-nil FSService")
	}
	if ai == nil {
		panic("AppWidget requires a non-nil AIService")
	}

	displayModel := strings.TrimSuffix(ActiveModelName(), ".litertlm")
	m := &AppWidget{
		activePane:    leftPane,
		chatOpen:      false,
		panelsVisible: true,
		width:         80,
		height:        24,
		shellOut:      &SwitchableWriter{},
		plume: []string{
			"Kabibi shell:  'help' for available commands.",
		},
		conversation:      conv,
		lastExhaustHeight: 0,
		isInitialized:     false,
	}

	m.shellW = NewShellInputWidget()
	m.shellW.Focus()
	m.dualPane = NewDualPaneWidget()
	m.dualPane.SetFSService(fs)
	m.plumeW = NewPlumeWidget()
	m.chatW = NewChatWidget()
	m.chatW.Input.SetPlaceholder("ask " + displayModel)
	m.chatW.SetConversation(conv)
	m.chatW.SetService(ai)
	m.plumeW.SetLines(m.plume)

	ctx := context.Background()
	var err error
	m.runner, err = createRunner(ctx, os.Stdin, m.shellOut, m.shellOut, cwd, nil)
	if err != nil {
		panic("AppWidget requires a valid shell runner: " + err.Error())
	}

	m.loadDir(leftPane, cwd, "")
	m.loadDir(rightPane, cwd, "")
	m.refreshPrompt()

	m.recalculateLayout()
	return m
}

func (m *AppWidget) Init() tea.Cmd {
	// Host-level setup (EnterAltScreen, mouse) is the AppHost's responsibility.
	return nil
}

func (m *AppWidget) Measure(c Constraints) Size {
	w := m.width
	if w <= 0 {
		w = c.MaxW
	}
	if w <= 0 {
		w = 80
	}
	h := m.height
	if h <= 0 {
		h = c.MaxH
	}
	if h <= 0 {
		h = 24
	}
	return Size{W: w, H: h}
}

func (m *AppWidget) Layout(r Rect) {
	m.width = r.W
	m.height = r.H
	m.recalculateLayout()
}

func (m *AppWidget) Render() CellBuf {
	return m.rootCellBuf()
}

// dispatch handles an incoming Tea message and returns the resulting command.
// It contains all application logic; the Tea model boundary lives in AppHost.
func (m *AppWidget) dispatch(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalculateLayout()
		return nil

	case animTickMsg:
		if m.quitting {
			return nil
		}
		if m.flashActive {
			elapsedMs := float64(time.Since(m.flashStart).Milliseconds())
			if elapsedMs >= 550 {
				m.flashActive = false
				return nil
			}
			return animTickCmd()
		}
		if m.isThinking {
			return animTickCmd()
		}
		return nil

	case aiRepaintMsg:
		return nil

	case aiDoneMsg:
		m.isThinking = false
		m.firstTokenRecv = false
		if msg.err != nil {
			m.conversation.Messages = append(m.conversation.Messages, Message{
				Role:    "system",
				Content: "Error: " + msg.err.Error(),
			})
		}
		return nil

	case assetProgressMsg:
		return nil

	case assetReadyMsg:
		return nil

	case assetErrorMsg:
		return nil

	case tea.KeyMsg:
		return m.handleKeyMsg(msg)

	case tea.MouseMsg:
		return m.handleMouseMsg(msg)

	case shellResultMsg:
		lines := []string{msg.input}
		if msg.err != nil {
			lines = append(lines, fmt.Sprintf("Error: %v", msg.err))
		}
		lines = append(lines, msg.output...)
		return m.AddPlume(lines...)
	}

	return nil
}

func (m *AppWidget) viewFrame() string {
	return ansiDualColor(Serialize(m.rootCellBuf()))
}

func (m *AppWidget) rootCellBuf() CellBuf {
	if m.width <= 0 || m.height <= 0 {
		return NewCellBuf(m.width, m.height)
	}

	buf := NewCellBuf(m.width, m.height)
	buf.Fill(Rect{X: 0, Y: 0, W: m.width, H: m.height}, Cell{BG: colorDarkGray})

	if m.quitting {
		if m.plumeW != nil {
			m.plumeW.Layout(Rect{X: 0, Y: 0, W: m.width, H: m.height})
			buf.Blit(m.plumeW.Render(), 0, 0)
		}
		return buf
	}

	if m.plumeW != nil {
		buf.Blit(m.plumeW.Render(), m.plumeW.X, m.plumeW.Y)
	}
	if m.panelsVisible && m.dualPane != nil {
		buf.Blit(m.dualPane.Render(), m.dualPane.X, m.dualPane.Y)
	}
	if m.chatW != nil {
		buf.Blit(m.chatW.Render(), m.chatW.X, m.chatW.Y)
	}

	m.renderShellPromptInto(&buf)

	if m.mode == modeDialog && m.dialog != nil {
		dlg := m.buildDialogWidget()
		if dlg != nil {
			sz := dlg.Measure(Constraints{MaxW: m.width, MaxH: m.height})
			if sz.W > m.width {
				sz.W = m.width
			}
			if sz.H > m.height {
				sz.H = m.height
			}
			x := (m.width - sz.W) / 2
			y := (m.height - sz.H) / 2
			if x < 0 {
				x = 0
			}
			if y < 0 {
				y = 0
			}
			dlg.Layout(Rect{X: x, Y: y, W: sz.W, H: sz.H})
			buf.Blit(dlg.Render(), x, y)
		}
	}

	return buf
}

func (m *AppWidget) renderShellPromptInto(buf *CellBuf) {
	if m.quitting || m.shellW == nil {
		return
	}
	row := buf.Height - 1
	if row < 0 {
		row = 0
	}
	m.shellW.Layout(Rect{W: buf.Width})
	buf.Blit(m.shellW.Render(), 0, row)
}

func (m *AppWidget) recalculateLayout() {
	chatFullWidth := (m.width * 35) / 100
	if chatFullWidth < 30 {
		chatFullWidth = 30
	}
	if chatFullWidth >= m.width {
		chatFullWidth = m.width - 1
	}
	if chatFullWidth < 1 {
		chatFullWidth = 1
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

	panelHeight := m.height - 10
	if panelHeight < 5 {
		panelHeight = 5
	}

	// Keep the non-panel area to at most 40% of the view height. This preserves the
	// existing 10-line policy unless the viewport is so short that the lower shell/history
	// region would otherwise exceed the historical 40% ceiling.
	maxPeekHeight := int(float64(m.height) * 0.4)
	if m.height-panelHeight > maxPeekHeight {
		panelHeight = m.height - maxPeekHeight
	}

	plumeH := m.height - panelHeight - 1
	if plumeH < 2 {
		plumeH = 2
	}
	if plumeH > 6 {
		plumeH = 6
	}

	safeFilesWidth := filesWidth - 1
	if safeFilesWidth < 4 {
		safeFilesWidth = 4
	}

	leftWidth := safeFilesWidth / 2
	_ = safeFilesWidth - leftWidth // rightWidth unused after list removal

	m.shellW.Layout(Rect{W: m.width})
	m.chatW.SetConversation(m.conversation)

	dualPaneY := 0
	m.dualPane.Layout(Rect{X: 0, Y: dualPaneY, W: filesWidth, H: panelHeight})

	plumeY := panelHeight
	m.plumeW.SetLines(m.plume)
	m.plumeW.Layout(Rect{X: 0, Y: plumeY, W: m.width, H: plumeH})

	m.chatW.Layout(Rect{X: m.width - actualChatWidth, Y: dualPaneY, W: actualChatWidth, H: panelHeight})
}

func (m *AppWidget) AddPlume(lines ...string) tea.Cmd {
	var cmds []tea.Cmd
	for _, line := range lines {
		m.plume = append(m.plume, line)
		m.plumeW.appendLine(line)

		if len(m.plume) > 120 {
			released := m.plume[0]
			m.plume = m.plume[1:]
			cmds = append(cmds, tea.Println(released))
		}
	}

	return tea.Batch(cmds...)
}

func (m *AppWidget) refreshPrompt() {
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
		m.shellW.Prompt = base + " $ "
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

	m.shellW.Prompt = res
}

// activePaneWidget returns the FilePaneWidget for the currently active file pane,
// or nil when chat is active.
func (m *AppWidget) activePaneWidget() *FilePaneWidget {
	if m == nil || m.dualPane == nil {
		return nil
	}
	if m.activePane == chatPane {
		return nil
	}
	return m.dualPane.ActivePane()
}

func (m *AppWidget) moveCursorHorizontal(dir int) {
	if m == nil || m.dualPane == nil || m.activePane == chatPane {
		return
	}
	m.dualPane.MoveCursorHorizontal(dir, m.height)
}

func (m *AppWidget) loadDir(p pane, path string, focusName string) {
	if m == nil || m.dualPane == nil {
		return
	}
	m.dualPane.LoadDirForPane(p, path, focusName)
}

func (m *AppWidget) watchAssetProgressCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.assetDone:
			return msg
		case msg := <-m.assetProgress:
			return msg
		}
	}
}

func (m *AppWidget) handleKeyMsg(msg tea.KeyMsg) tea.Cmd {
	switch m.mode {
	case modeEditor:
		if m.editor == nil {
			m.mode = modeBrowser
			return nil
		}
		if m.editor.OnClose == nil {
			m.editor.OnClose = func() tea.Cmd { return m.closeEditor() }
		}
		return m.editor.HandleKey(msg)
	case modeDialog:
		if m.dialog != nil {
			return m.updateDialog(msg)
		}
		return nil
	default: // modeBrowser
		// FIRST: Check if this is shell input (typing or Enter)
		// Shell input should work globally when not in editor/dialog/chat mode
		if !m.chatOpen && m.activePane != chatPane {
			switch msg.Type {
			case tea.KeyRunes:
				return m.shellW.HandleKey(msg)
			case tea.KeyEnter:
				input := m.shellW.Value()
				if input != "" {
					m.shellW.SetValue("")
					if handled, cmd := m.handleShellBuiltin(input); handled {
						return cmd
					}
					return m.runShellCommand(input)
				}
				// If shell is empty, DON'T intercept Enter - let panes handle it
				// (falls through to pane-specific handling below)
			case tea.KeyBackspace:
				return m.shellW.HandleKey(msg)
			}
		}

		// THEN: Global keys (work across all panes)
		switch msg.Type {
		case tea.KeyTab:
			if m.chatOpen {
				return nil
			}
			if !m.lastTab.IsZero() && time.Since(m.lastTab) < 300*time.Millisecond {
				m.lastTab = time.Time{}
				m.enterChatMode()
				return nil
			}
			m.lastTab = time.Now()
			if m.dualPane == nil {
				return nil
			}
			m.dualPane.HandleKey(msg)
			if m.dualPane.ActivePaneIndex() == 0 {
				m.activePane = leftPane
			} else {
				m.activePane = rightPane
			}
			return nil
		case tea.KeyEscape:
			if m.chatOpen || m.activePane == chatPane {
				m.leaveChatMode()
				return nil
			}
			if m.panelsVisible {
				m.panelsVisible = false
				if m.shellW != nil {
					m.shellW.Focus()
				}
				m.recalculateLayout()
				return nil
			}
			m.panelsVisible = true
			if m.shellW != nil {
				m.shellW.Focus()
			}
			m.recalculateLayout()
			return nil
		}

		// FINALLY: Pane-specific input routing to new widget architecture
		switch m.activePane {
		case leftPane, rightPane:
			return m.dualPane.HandleKey(msg)

		case chatPane:
			if msg.Type == tea.KeyEscape {
				m.activePane = leftPane
				m.dualPane.SetActivePaneBridge(0)
				return nil
			}
			return m.chatW.HandleKey(msg)
		}
	}
	return nil
}

func (m *AppWidget) handleMouseMsg(msg tea.MouseMsg) tea.Cmd {
	// Placeholder: full routing logic will go here
	return nil
}

// File operation methods
func (m *AppWidget) fmTransfer(kind fileOpKind) tea.Cmd {
	// Placeholder: transfer files (opCopy, opMove)
	return nil
}

func (m *AppWidget) fmDelete() tea.Cmd {
	// Placeholder: delete files
	return nil
}

func (m *AppWidget) fmRename() tea.Cmd {
	// Placeholder: rename files
	return nil
}

func (m *AppWidget) fmMkdir() tea.Cmd {
	// Placeholder: make directory
	return nil
}

func (m *AppWidget) fmEdit() tea.Cmd {
	w := m.activePaneWidget()
	if w == nil {
		return nil
	}
	fi, ok := w.SelectedItem()
	if !ok || fi.isDir || fi.name == ".." {
		return nil
	}
	return m.openEditor(filepath.Join(w.Dir(), fi.name))
}

func (m *AppWidget) fmToggleMark() tea.Cmd {
	w := m.activePaneWidget()
	if w == nil {
		return nil
	}
	idx := w.SelectedIndex()
	w.MarkToggle(idx)
	if idx+1 < len(w.Items()) {
		w.Select(idx + 1)
	}
	return nil
}

// Helper functions

func truncateStringToWidth(s string, maxWidth int) string {
	// Calculate display width of the input string
	displayWidth := runewidth.StringWidth(s)

	// If it fits, return as-is
	if displayWidth <= maxWidth {
		return s
	}

	// Need to truncate: reserve space for ellipsis
	effectiveWidth := maxWidth - 1
	if effectiveWidth <= 0 {
		return ""
	}

	width := 0
	result := ""
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if width+w > effectiveWidth {
			break
		}
		result += string(r)
		width += w
	}

	// Trim trailing spaces
	return strings.TrimRight(result, " ")
}
