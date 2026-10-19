package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mavity/rusticated/kabibi/ui/terminal"
	"pty"
)

// ManagedPTY tracks a PTY-backed process spawned via the AI's exec tool,
// supporting timeout promotion into the background registry (`bgProcs`)
// and subsequent interaction via `term_read`, `term_input`, and `term_kill`.
type ManagedPTY struct {
	ID        int
	Command   string
	Cwd       string
	Cols      int
	Rows      int
	StartedAt time.Time

	mu         sync.Mutex
	ptyHandle  pty.PTY
	cmd        *exec.Cmd
	rawBuf     bytes.Buffer
	readOffset int // byte watermark for `only_new` reads
	exited     bool
	exitCode   int
	exitErr    error
	finishedAt time.Time
	notifyCh   chan struct{}
	doneCh     chan struct{}
	onChunk    func(chunk []byte)
	onFinish   func(exitCode int, elapsed time.Duration)
}

func (m *ManagedPTY) signalUpdateLocked() {
	if m.notifyCh != nil {
		close(m.notifyCh)
		m.notifyCh = make(chan struct{})
	}
}

func (m *ManagedPTY) appendOutput(p []byte) {
	if len(p) == 0 {
		return
	}
	m.mu.Lock()
	m.rawBuf.Write(p)
	cb := m.onChunk
	m.signalUpdateLocked()
	m.mu.Unlock()
	if cb != nil {
		cb(p)
	}
}

func (m *ManagedPTY) markExited(code int, err error) {
	m.mu.Lock()
	if m.exited {
		m.mu.Unlock()
		return
	}
	m.exited = true
	m.exitCode = code
	m.exitErr = err
	m.finishedAt = time.Now()
	elapsed := m.finishedAt.Sub(m.StartedAt)
	cb := m.onFinish
	m.signalUpdateLocked()
	close(m.doneCh)
	m.mu.Unlock()
	if cb != nil {
		cb(code, elapsed)
	}
}

// CleanBytesToText renders raw PTY output through a virtual terminal CellBuf
// (`terminal.FromANSIString`) of width `cols` so `\r` spinner redraws, backspaces, and
// cursor jumps collapse into the settled 2D character grid.
// When keepANSI is false (default), it extracts plain settled text with right-margin
// spaces trimmed. When keepANSI is true, it strips foreground/background colors
// while preserving bold/underline attributes via `terminal.ToANSI`.
func CleanBytesToText(raw []byte, cols int, keepANSI bool) string {
	if len(raw) == 0 {
		return ""
	}
	if cols <= 0 {
		cols = 78
	}
	buf := terminal.FromANSIString(string(raw), cols)
	return CleanCellBufToText(buf, keepANSI)
}

// CleanCellBufToText converts a rendered terminal.CellBuf into token-frugal text.
func CleanCellBufToText(buf terminal.CellBuf, keepANSI bool) string {
	if buf.Width <= 0 || buf.Height <= 0 {
		return ""
	}
	buf = trimTrailingWhitespaceRows(buf)
	if buf.Height == 0 {
		return ""
	}
	if keepANSI {
		cloned := terminal.NewCellBuf(buf.Width, buf.Height)
		for y := 0; y < buf.Height; y++ {
			for x := 0; x < buf.Width; x++ {
				c := buf.Get(x, y)
				// Zero out FG/BG colors while keeping font attributes (bold, underline, etc.)
				attrs := c.Style.Attributes() | terminal.CharResetForegroundDefault | terminal.CharResetBackgroundDefault
				c.Style = terminal.NewStyle(0, 0, attrs)
				cloned.Set(x, y, c)
			}
		}
		ansi := terminal.ToANSI(cloned)
		ansi = strings.ReplaceAll(ansi, "\r\n", "\n")
		ansi = strings.ReplaceAll(ansi, "\x1b[K", "")
		ansi = strings.TrimSuffix(ansi, "\x1b[0m")
		ansi = strings.TrimSuffix(ansi, "\x1b[m")
		return strings.TrimRight(ansi, " \t\n")
	}

	var sb strings.Builder
	for y := 0; y < buf.Height; y++ {
		var rowBuilder strings.Builder
		for x := 0; x < buf.Width; x++ {
			c := buf.Get(x, y)
			if c.Grapheme == "" {
				continue
			}
			rowBuilder.WriteString(c.Grapheme)
		}
		rowStr := strings.TrimRight(rowBuilder.String(), " ")
		sb.WriteString(rowStr)
		if y+1 < buf.Height {
			lastCell := buf.Get(buf.Width-1, y)
			if !lastCell.Style.HasAttribute(terminal.CharSoftWrap) {
				sb.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func trimTrailingWhitespaceRows(buf terminal.CellBuf) terminal.CellBuf {
	lastContentRow := -1
	for y := buf.Height - 1; y >= 0; y-- {
		for x := 0; x < buf.Width; x++ {
			if strings.TrimSpace(buf.Get(x, y).Grapheme) != "" {
				lastContentRow = y
				break
			}
		}
		if lastContentRow >= 0 {
			break
		}
	}
	if lastContentRow == buf.Height-1 {
		return buf
	}

	trimmed := terminal.NewCellBuf(buf.Width, lastContentRow+1)
	for y := 0; y <= lastContentRow; y++ {
		for x := 0; x < buf.Width; x++ {
			trimmed.Set(x, y, buf.Get(x, y))
		}
	}
	return trimmed
}

// Snapshot reads the current settled output from the ManagedPTY.
func (m *ManagedPTY) Snapshot(onlyNew bool, keepANSI bool) (text string, exited bool, exitCode int, elapsed time.Duration) {
	m.mu.Lock()
	allBytes := m.rawBuf.Bytes()
	startOff := 0
	if onlyNew && m.readOffset > 0 && m.readOffset <= len(allBytes) {
		// If the new chunk contains full-screen clear sequences (\x1b[2J or \x1b[H),
		// render the full buffer so the TUI screen state is accurate.
		delta := allBytes[m.readOffset:]
		if !bytes.Contains(delta, []byte("\x1b[2J")) && !bytes.Contains(delta, []byte("\x1b[H")) {
			startOff = m.readOffset
		}
	}
	slice := append([]byte(nil), allBytes[startOff:]...)
	m.readOffset = len(allBytes)
	exited = m.exited
	exitCode = m.exitCode
	cols := m.Cols
	if exited {
		elapsed = m.finishedAt.Sub(m.StartedAt)
	} else {
		elapsed = time.Since(m.StartedAt)
	}
	m.mu.Unlock()

	text = CleanBytesToText(slice, cols, keepANSI)
	return text, exited, exitCode, elapsed
}

// WaitForActivity waits up to waitDur for new output or process exit.
func (m *ManagedPTY) WaitForActivity(ctx context.Context, waitDur time.Duration) {
	if waitDur <= 0 {
		return
	}
	m.mu.Lock()
	if m.exited || m.readOffset < m.rawBuf.Len() {
		m.mu.Unlock()
		return
	}
	notifyCh := m.notifyCh
	doneCh := m.doneCh
	m.mu.Unlock()

	timer := time.NewTimer(waitDur)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-doneCh:
	case <-notifyCh:
		// Give a brief 25ms coalescing window so burst writes settle
		time.Sleep(25 * time.Millisecond)
	case <-timer.C:
	}
}

// WriteKeys translates symbolic key tokens (e.g. `<enter>`, `<ctrl+c>`, `<up>`)
// or literal text and writes them to the underlying PTY handle.
func (m *ManagedPTY) WriteKeys(keys string) error {
	m.mu.Lock()
	exited := m.exited
	p := m.ptyHandle
	m.mu.Unlock()
	if exited || p == nil {
		return fmt.Errorf("process #%d is not running or has no interactive PTY", m.ID)
	}
	translated := TranslateSymbolicKeys(keys)
	_, err := p.Write(translated)
	return err
}

// Kill terminates or interrupts the managed process.
func (m *ManagedPTY) Kill(sigName string) error {
	m.mu.Lock()
	exited := m.exited
	p := m.ptyHandle
	cmd := m.cmd
	doneCh := m.doneCh
	m.mu.Unlock()

	if exited {
		return nil
	}

	sig := strings.ToUpper(strings.TrimSpace(sigName))
	if sig == "SIGINT" && p != nil {
		// Send Ctrl+C over the PTY line discipline first
		_, _ = p.Write([]byte{0x03})
		select {
		case <-doneCh:
			return nil
		case <-time.After(150 * time.Millisecond):
		}
	}

	if cmd != nil && cmd.Process != nil {
		if sig == "SIGINT" && runtime.GOOS != "windows" {
			_ = cmd.Process.Signal(os.Interrupt)
		} else {
			_ = cmd.Process.Kill()
		}
	}
	if p != nil {
		_ = p.Close()
	}

	select {
	case <-doneCh:
	case <-time.After(500 * time.Millisecond):
		m.markExited(137, nil)
	}
	return nil
}

var symbolicKeyMap = []struct {
	token string
	bytes string
}{
	{"<enter>", "\r"},
	{"<return>", "\r"},
	{"<cr>", "\r"},
	{"<lf>", "\n"},
	{"<tab>", "\t"},
	{"<esc>", "\x1b"},
	{"<escape>", "\x1b"},
	{"<space>", " "},
	{"<bs>", "\x7f"},
	{"<backspace>", "\x7f"},
	{"<ctrl+c>", "\x03"},
	{"<ctrl-c>", "\x03"},
	{"<ctrl+d>", "\x04"},
	{"<ctrl-d>", "\x04"},
	{"<ctrl+z>", "\x1a"},
	{"<ctrl-z>", "\x1a"},
	{"<ctrl+l>", "\x0c"},
	{"<up>", "\x1b[A"},
	{"<down>", "\x1b[B"},
	{"<right>", "\x1b[C"},
	{"<left>", "\x1b[D"},
	{"<home>", "\x1b[H"},
	{"<end>", "\x1b[F"},
	{"<pgup>", "\x1b[5~"},
	{"<pgdn>", "\x1b[6~"},
	{"<delete>", "\x1b[3~"},
}

// TranslateSymbolicKeys replaces `<enter>`, `<ctrl+c>`, `<up>`, etc. (case-insensitive)
// with their corresponding terminal byte sequences.
func TranslateSymbolicKeys(input string) []byte {
	if !strings.Contains(input, "<") {
		return []byte(input)
	}
	var out bytes.Buffer
	i := 0
	for i < len(input) {
		if input[i] == '<' {
			end := strings.IndexByte(input[i:], '>')
			if end > 1 {
				cand := strings.ToLower(input[i : i+end+1])
				matched := false
				for _, kv := range symbolicKeyMap {
					if cand == kv.token {
						out.WriteString(kv.bytes)
						i += end + 1
						matched = true
						break
					}
				}
				if matched {
					continue
				}
			}
		}
		out.WriteByte(input[i])
		i++
	}
	return out.Bytes()
}

func resolveHostShellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		if bashPath, err := exec.LookPath("bash.exe"); err == nil {
			return exec.Command(bashPath, "-c", command)
		}
		if shPath, err := exec.LookPath("sh.exe"); err == nil {
			return exec.Command(shPath, "-c", command)
		}
		return exec.Command("cmd.exe", "/C", command)
	}
	if bashPath, err := exec.LookPath("bash"); err == nil {
		return exec.Command(bashPath, "-c", command)
	}
	return exec.Command("sh", "-c", command)
}
