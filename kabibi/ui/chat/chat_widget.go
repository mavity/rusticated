package ai

import (
	"context"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// ChatInputWidget wraps textinput.Model in the widget contract used by the
// parent UI. It supports multi-line input sizing and can accept Shift+Enter as a
// newline command without treating it as a submit action.
type ChatInputWidget struct {
	model  textinput.Model
	value  string
	width  int
	height int
}

func NewChatInputWidget() *ChatInputWidget {
	ti := textinput.New()
	ti.Prompt = ""
	return &ChatInputWidget{model: ti}
}

func (w *ChatInputWidget) SetValue(s string) {
	if w == nil {
		return
	}
	w.value = strings.ReplaceAll(s, "\r", "")
	w.model.SetValue(normalizeSingleLine(w.value))
}

func (w *ChatInputWidget) Value() string {
	if w == nil {
		return ""
	}
	if w.value == "" {
		w.value = strings.ReplaceAll(w.model.Value(), "\r", "")
	}
	return w.value
}

func (w *ChatInputWidget) Reset() {
	if w == nil {
		return
	}
	w.value = ""
	w.model.Reset()
}

func (w *ChatInputWidget) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if c.MaxW <= 0 {
		c.MaxW = 20
	}
	if c.MaxH <= 0 {
		c.MaxH = 4
	}

	h := 1
	v := strings.ReplaceAll(w.Value(), "\r", "")
	if v != "" {
		h = strings.Count(v, "\n") + 1
	}
	if h < 1 {
		h = 1
	}
	if h > c.MaxH {
		h = c.MaxH
	}
	return Size{W: c.MaxW, H: h}
}

func (w *ChatInputWidget) Layout(r Rect) {
	if w == nil {
		return
	}
	w.width = r.W
	w.height = r.H
}

func (w *ChatInputWidget) Render() CellBuf {
	if w == nil {
		return NewCellBuf(0, 0)
	}
	width := w.width
	if width <= 0 {
		width = 20
	}
	height := w.height
	if height <= 0 {
		height = 1
	}
	buf := NewCellBuf(width, height)
	buf.Fill(Rect{X: 0, Y: 0, W: width, H: height}, Cell{FG: colorWhite, BG: colorDarkGray})

	lines := strings.Split(strings.ReplaceAll(w.Value(), "\r", ""), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		lines = []string{""}
	}
	for i, line := range lines {
		if i >= height {
			break
		}
		renderLineIntoBuf(buf, 0, i, line, width, colorWhite, colorDarkGray)
	}
	return buf
}

func (w *ChatInputWidget) HandleKey(msg tea.KeyMsg) tea.Cmd {
	if w == nil {
		return nil
	}
	if msg.Type == tea.KeyEnter && (msg.Alt || msg.String() == "shift+enter") {
		w.value += "\n"
		w.model.SetValue(normalizeSingleLine(w.value))
		return nil
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		w.value += string(msg.Runes)
		w.model.SetValue(normalizeSingleLine(w.value))
		return nil
	}
	if msg.Type == tea.KeyBackspace {
		if rs := []rune(w.value); len(rs) > 0 {
			w.value = string(rs[:len(rs)-1])
		}
		w.model.SetValue(normalizeSingleLine(w.value))
		return nil
	}
	if msg.Type == tea.KeyDelete {
		if rs := []rune(w.value); len(rs) > 0 {
			w.value = string(rs[:len(rs)-1])
		}
		w.model.SetValue(normalizeSingleLine(w.value))
		return nil
	}
	return nil
}

func normalizeSingleLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ")
}

func (w *ChatInputWidget) Focus() {
	if w == nil {
		return
	}
	w.model.Focus()
}

func (w *ChatInputWidget) Blur() {
	if w == nil {
		return
	}
	w.model.Blur()
}

func (w *ChatInputWidget) SetPlaceholder(s string) {
	if w == nil {
		return
	}
	w.model.Placeholder = s
}

func (w *ChatInputWidget) HandleMouse(msg tea.MouseMsg) tea.Cmd { return nil }

// ChatWidget renders the conversation history and composes the input area at the
// bottom without requiring the legacy string-based view to know anything about it.
type ChatWidget struct {
	Conversation *Conversation
	Input        *ChatInputWidget
	Width        int
	Height       int
	X            int
	Y            int

	service     AIService
	scrollTop   int // rows from bottom; 0 = latest messages visible
	mu          sync.Mutex
	cache       CellBuf
	cacheW      int
	cacheH      int
	cacheVer    uint64
	cacheScroll int
}

func NewChatWidget() *ChatWidget {
	return &ChatWidget{Input: NewChatInputWidget()}
}

func (w *ChatWidget) SetConversation(c *Conversation) {
	if w == nil {
		return
	}
	w.Conversation = c
}

func (w *ChatWidget) SetService(s AIService) {
	if w == nil {
		return
	}
	w.service = s
}

// chatStreamDoneMsg is posted when a Submit stream finishes.
type chatStreamDoneMsg struct{}

// Submit adds a user message, starts an assistant response via AIService, and
// returns a tea.Cmd that streams tokens into the conversation.
func (w *ChatWidget) Submit(ctx context.Context, text string) tea.Cmd {
	if w == nil || text == "" {
		return nil
	}
	if w.Conversation == nil {
		w.Conversation = &Conversation{}
	}
	if w.Input != nil {
		w.Input.Reset()
	}
	w.mu.Lock()
	w.Conversation.Messages = append(w.Conversation.Messages,
		Message{Role: "user", Content: text},
		Message{Role: "assistant", Content: ""},
	)
	assistantIdx := len(w.Conversation.Messages) - 1
	w.scrollTop = 0
	w.mu.Unlock()
	if AppProgram != nil {
		AppProgram.Send(aiRepaintMsg{})
	}
	if w.service == nil {
		w.service = NewLMSession(w.Conversation)
	}

	svc := w.service
	return func() tea.Msg {
		if err := ensureLiteRTFunc(ctx, nil); err != nil {
			w.mu.Lock()
			w.Conversation.Messages = append(w.Conversation.Messages, Message{Role: "system", Content: "AI runtime unavailable: " + err.Error()})
			w.mu.Unlock()
			if AppProgram != nil {
				AppProgram.Send(aiRepaintMsg{})
			}
			return chatStreamDoneMsg{}
		}
		if err := ensureGemmaFunc(ctx, nil); err != nil {
			w.mu.Lock()
			w.Conversation.Messages = append(w.Conversation.Messages, Message{Role: "system", Content: "AI model unavailable: " + err.Error()})
			w.mu.Unlock()
			if AppProgram != nil {
				AppProgram.Send(aiRepaintMsg{})
			}
			return chatStreamDoneMsg{}
		}
		_ = svc.SendMessage(ctx, text, func(token string) {
			w.mu.Lock()
			if assistantIdx < len(w.Conversation.Messages) {
				w.Conversation.Messages[assistantIdx].Content += token
			}
			w.mu.Unlock()
			if AppProgram != nil {
				AppProgram.Send(aiRepaintMsg{})
			}
		})
		if AppProgram != nil {
			AppProgram.Send(chatStreamDoneMsg{})
		}
		return chatStreamDoneMsg{}
	}
}

// Messages returns a snapshot of the current conversation messages.
func (w *ChatWidget) Messages() []Message {
	if w == nil || w.Conversation == nil {
		return nil
	}
	w.mu.Lock()
	out := make([]Message, len(w.Conversation.Messages))
	copy(out, w.Conversation.Messages)
	w.mu.Unlock()
	return out
}
func (w *ChatWidget) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if c.MaxW <= 0 {
		c.MaxW = 40
	}
	if c.MaxH <= 0 {
		c.MaxH = 10
	}

	inputH := 1
	if w.Input != nil {
		inputH = w.Input.Measure(Constraints{MaxW: c.MaxW, MaxH: c.MaxH}).H
	}

	lines := 0
	if w.Conversation != nil {
		for _, msg := range w.Conversation.Messages {
			lines += countDisplayLines(msg.Content, c.MaxW)
		}
	}
	if lines < 1 {
		lines = 1
	}
	total := lines + inputH
	if total > c.MaxH {
		total = c.MaxH
	}
	if total < 1 {
		total = 1
	}
	return Size{W: c.MaxW, H: total}
}

func (w *ChatWidget) Layout(r Rect) {
	if w == nil {
		return
	}
	w.X = r.X
	w.Y = r.Y
	w.Width = r.W
	w.Height = r.H
	if w.Input != nil {
		inputH := 1
		if w.Height > 1 {
			inputH = minInt(w.Height/4, w.Height)
			if inputH < 1 {
				inputH = 1
			}
		}
		if inputH > w.Height {
			inputH = w.Height
		}
		w.Input.Layout(Rect{X: r.X, Y: r.Y + r.H - inputH, W: r.W, H: inputH})
	}
}

func (w *ChatWidget) Render() CellBuf {
	if w == nil {
		return NewCellBuf(0, 0)
	}
	width := w.Width
	if width <= 0 {
		width = 20
	}
	height := w.Height
	if height <= 0 {
		height = 1
	}

	version := uint64(0)
	if w.Conversation != nil {
		w.mu.Lock()
		for i, msg := range w.Conversation.Messages {
			version += uint64(i+1) * uint64(len(msg.Content)+1)
		}
		w.mu.Unlock()
	}
	if w.Input != nil {
		version ^= uint64(len(w.Input.Value()))
	}
	if w.cacheW == width && w.cacheH == height && w.cacheVer == version && w.cacheScroll == w.scrollTop && w.cache.Width == width && w.cache.Height == height {
		return w.cache
	}

	buf := NewCellBuf(width, height)
	buf.Fill(Rect{X: 0, Y: 0, W: width, H: height}, Cell{FG: colorWhite, BG: colorDarkGray})
	if width >= 2 && height >= 2 {
		for x := 1; x < width-1; x++ {
			buf.Set(x, 0, Cell{R: '─', FG: colorGray, BG: colorDarkGray})
			buf.Set(x, height-1, Cell{R: '─', FG: colorGray, BG: colorDarkGray})
		}
		for y := 1; y < height-1; y++ {
			buf.Set(0, y, Cell{R: '│', FG: colorGray, BG: colorDarkGray})
			buf.Set(width-1, y, Cell{R: '│', FG: colorGray, BG: colorDarkGray})
		}
		buf.Set(0, 0, Cell{R: '┌', FG: colorGray, BG: colorDarkGray})
		buf.Set(width-1, 0, Cell{R: '┐', FG: colorGray, BG: colorDarkGray})
		buf.Set(0, height-1, Cell{R: '└', FG: colorGray, BG: colorDarkGray})
		buf.Set(width-1, height-1, Cell{R: '┘', FG: colorGray, BG: colorDarkGray})
	}

	inputH := 1
	if w.Input != nil {
		inputH = minInt(w.Input.Measure(Constraints{MaxW: width, MaxH: height}).H, height)
		if inputH < 1 {
			inputH = 1
		}
		if inputH > height-2 { // Reserve space for top and bottom borders
			inputH = height - 2
		}
		if inputH <= 0 {
			inputH = 0
		}
		if inputH > 0 {
			// Place input just above the bottom border
			inputY := height - inputH - 1
			w.Input.Layout(Rect{X: 1, Y: inputY, W: width - 2, H: inputH})
			inputBuf := w.Input.Render()
			buf.Blit(inputBuf, 1, inputY)
		}
	}

	availableHistory := height - inputH - 2 // Account for top border and bottom border
	if availableHistory < 0 {
		availableHistory = 0
	}

	if w.Conversation != nil {
		w.mu.Lock()
		rows := make([]string, 0, len(w.Conversation.Messages)*2)
		for _, msg := range w.Conversation.Messages {
			text := stripANSI(msg.Content)
			text = strings.ReplaceAll(text, "\r", "")
			for _, line := range strings.Split(text, "\n") {
				if line == "" && len(rows) > 0 {
					rows = append(rows, "")
					continue
				}
				rows = append(rows, line)
			}
		}
		w.mu.Unlock()

		maxScroll := len(rows) - availableHistory
		if maxScroll < 0 {
			maxScroll = 0
		}
		if w.scrollTop > maxScroll {
			w.scrollTop = maxScroll
		}
		end := len(rows) - w.scrollTop
		if end < 0 {
			end = 0
		}
		start := end - availableHistory
		if start < 0 {
			start = 0
		}
		for i, line := range rows[start:end] {
			renderLineIntoBuf(buf, 1, i+1, line, width-2, colorWhite, colorDarkGray)
		}
	}

	w.cache = buf
	w.cacheW = width
	w.cacheH = height
	w.cacheVer = version
	w.cacheScroll = w.scrollTop
	return buf
}

func (w *ChatWidget) HandleKey(msg tea.KeyMsg) tea.Cmd {
	if w == nil {
		return nil
	}
	if msg.Type == tea.KeyEnter && !msg.Alt && msg.String() != "shift+enter" {
		text := strings.TrimSpace(w.Input.Value())
		if text != "" {
			return w.Submit(context.Background(), text)
		}
		return nil
	}
	switch msg.String() {
	case "pgup":
		w.scrollTop += 10
	case "pgdown":
		if w.scrollTop > 10 {
			w.scrollTop -= 10
		} else {
			w.scrollTop = 0
		}
	case "home":
		w.scrollTop = 1<<31 - 1 // clamped to maxScroll in Render
	case "end":
		w.scrollTop = 0
	default:
		if w.Input != nil {
			return w.Input.HandleKey(msg)
		}
	}
	return nil
}

func (w *ChatWidget) HandleMouse(msg tea.MouseMsg) tea.Cmd { return nil }

func renderLineIntoBuf(dst CellBuf, x, y int, text string, width int, fg, bg lipgloss.Color) {
	if width <= 0 || y < 0 || y >= dst.Height {
		return
	}
	line := strings.ReplaceAll(stripANSI(text), "\r", "")
	if line == "" {
		for px := 0; px < width; px++ {
			dst.Set(x+px, y, Cell{FG: fg, BG: bg})
		}
		return
	}
	col := 0
	for _, r := range line {
		rw := runewidth.RuneWidth(r)
		if rw <= 0 {
			continue
		}
		if col+rw > width {
			break
		}
		dst.Set(x+col, y, Cell{R: r, FG: fg, BG: bg})
		col += rw
	}
	for col < width {
		dst.Set(x+col, y, Cell{FG: fg, BG: bg})
		col++
	}
}

func countDisplayLines(s string, width int) int {
	if width <= 0 {
		return 1
	}
	text := stripANSI(s)
	text = strings.ReplaceAll(text, "\r", "")
	if text == "" {
		return 1
	}
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			count++
			continue
		}
		current := 0
		for _, r := range line {
			rw := runewidth.RuneWidth(r)
			if rw <= 0 {
				continue
			}
			if current+rw > width {
				count++
				current = 0
			}
			current += rw
		}
		if current > 0 || count == 0 {
			count++
		}
	}
	if count == 0 {
		count = 1
	}
	return count
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ Widget = (*ChatWidget)(nil)
var _ InputWidget = (*ChatInputWidget)(nil)
var _ Widget = (*ChatInputWidget)(nil)
