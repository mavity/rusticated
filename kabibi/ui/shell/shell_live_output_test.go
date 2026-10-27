package shell

import (
	"bytes"
	"io"
	"testing"

	"github.com/mavity/rusticated/kabibi/ui"
)

type liveOutputTestPTY struct {
	resizeCalls int
	cols        int
	rows        int
}

func (p *liveOutputTestPTY) Read([]byte) (int, error)       { return 0, io.EOF }
func (p *liveOutputTestPTY) Write(data []byte) (int, error) { return len(data), nil }
func (p *liveOutputTestPTY) Close() error                   { return nil }
func (p *liveOutputTestPTY) Wait() error                    { return nil }
func (p *liveOutputTestPTY) Fd() uintptr                    { return 0 }
func (p *liveOutputTestPTY) Resize(cols, rows int) error {
	p.resizeCalls++
	p.cols, p.rows = cols, rows
	return nil
}

func TestShellRenderTracksLiveTerminalCursorAndResizesOnlyWhenFrameChanges(t *testing.T) {
	state := newLiveOutputState(40, 11)
	state.enqueue([]byte("first\r\nsecond\x1b[2;4H"))
	childPTY := &liveOutputTestPTY{}
	session := &ExecutionSession{
		activePTY: childPTY,
		ptyCols:   40,
		ptyRows:   11,
	}
	s := &Shell{
		mode:        ModeStack,
		input:       NewPromptInput(PromptInputOptions{}),
		execSess:    session,
		running:     true,
		activeBlock: 0,
		blocks: []CommandBlock{{
			Kind:       BlockCommand,
			Prompt:     "$ ",
			Input:      "interactive",
			Output:     bytes.Buffer{},
			Status:     StatusActive,
			liveOutput: state,
		}},
	}

	_, cursor := renderANSIForTest(t, s.Render(ui.Rect{W: 40, H: 12}, ui.RenderContext{Focused: true}), 40, 12)
	if cursor != (ui.CursorPos{X: 3, Y: 2, Visible: true}) {
		t.Fatalf("active cursor = %+v, want {X:3 Y:2 Visible:true}", cursor)
	}
	s.Render(ui.Rect{W: 40, H: 12}, ui.RenderContext{})
	if childPTY.resizeCalls != 0 {
		t.Fatalf("unchanged frame resized the child PTY %d times", childPTY.resizeCalls)
	}

	s.Render(ui.Rect{W: 50, H: 15}, ui.RenderContext{})
	if childPTY.resizeCalls != 1 || childPTY.cols != 50 || childPTY.rows != 14 {
		t.Fatalf("resized child PTY = %dx%d after %d calls, want 50x14 after one call",
			childPTY.cols, childPTY.rows, childPTY.resizeCalls)
	}
	if cols, rows := state.terminal.Size(); cols != 50 || rows != 14 {
		t.Fatalf("live terminal size = %dx%d, want 50x14", cols, rows)
	}

	resized, _ := state.terminal.Snapshot()
	if resized.Get(0, 0).Grapheme != "f" || resized.Get(0, 1).Grapheme != "s" {
		t.Fatalf("live terminal lost existing output on resize: size=%dx%d raw=%q row 0 starts %q, row 1 starts %q",
			resized.Width, resized.Height, state.raw, resized.Get(0, 0).Grapheme, resized.Get(0, 1).Grapheme)
	}
	state.enqueue([]byte("\r\nthird"))
	s.Render(ui.Rect{W: 50, H: 15}, ui.RenderContext{})
	if childPTY.resizeCalls != 1 {
		t.Fatalf("unchanged resized frame triggered %d child PTY resizes", childPTY.resizeCalls-1)
	}
	afterWrite, _ := state.terminal.Snapshot()
	if afterWrite.Get(0, 2).Grapheme != "t" {
		t.Fatalf("post-resize output starts with %q at row 2, want %q",
			afterWrite.Get(0, 2).Grapheme, "t")
	}
}

func TestShellCompletedLiveOutputHidesCursorTrimsBlankRowsAndKeepsFrame(t *testing.T) {
	state := newLiveOutputState(40, 8)
	state.enqueue([]byte("node>\x1b[5;1H"))
	s := &Shell{
		mode:        ModeStack,
		input:       NewPromptInput(PromptInputOptions{}),
		running:     true,
		activeBlock: 0,
		blocks: []CommandBlock{{
			Kind:       BlockCommand,
			Prompt:     "$ ",
			Input:      "node",
			Output:     bytes.Buffer{},
			Status:     StatusActive,
			liveOutput: state,
		}},
	}

	_, activeCursor := renderANSIForTest(t, s.Render(ui.Rect{W: 40, H: 9}, ui.RenderContext{Focused: true}), 40, 9)
	if !activeCursor.Visible || activeCursor.Y != 5 {
		_, terminalCursor := state.terminal.Snapshot()
		t.Fatalf("active cursor = %+v, want visible cursor on row 5; terminal cursor=%+v",
			activeCursor, terminalCursor)
	}

	s.mu.Lock()
	s.blocks[0].Status = StatusSuccess
	s.mu.Unlock()
	_, completedCursor := renderANSIForTest(t, s.Render(ui.Rect{W: 40, H: 9}, ui.RenderContext{}), 40, 9)
	if completedCursor.Visible {
		t.Fatalf("completed block retained a visible shell cursor at %+v", completedCursor)
	}
	if !state.finalized || state.frozenCursor.Visible {
		t.Fatalf("completed terminal finalized=%t frozen cursor=%+v, want finalized with hidden cursor",
			state.finalized, state.frozenCursor)
	}
	completed := renderBlockCached(&s.blocks[0], 40)
	if completed.Height != 2 {
		t.Fatalf("completed block height = %d, want prompt and output only (2 rows)", completed.Height)
	}
	if s.blocks[0].cachedCursor.Visible {
		t.Fatal("completed block cache retained a visible cursor")
	}

	s.Render(ui.Rect{W: 60, H: 12}, ui.RenderContext{})
	if cols, rows := state.terminal.Size(); cols != 40 || rows != 8 {
		t.Fatalf("completed terminal frame changed to %dx%d, want original 40x8", cols, rows)
	}
}
