package shell

import (
	"fmt"
	"io"
	"sync"

	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type liveOutputState struct {
	pendingMu sync.Mutex
	pending   []byte
	spare     []byte

	raw          []byte
	terminal     *terminal.Terminal
	termOffset   int
	generation   uint64
	finalized    bool
	frozen       terminal.CellBuf
	frozenCursor terminal.CursorPos
	writeErr     error
}

func newLiveOutputState(cols, rows int) *liveOutputState {
	return &liveOutputState{
		terminal: terminal.NewTerminal(cols, rows),
	}
}

func (s *liveOutputState) enqueue(p []byte) {
	if s == nil || len(p) == 0 {
		return
	}
	s.pendingMu.Lock()
	s.pending = append(s.pending, p...)
	s.pendingMu.Unlock()
}

func (s *liveOutputState) takePending() []byte {
	if s == nil {
		return nil
	}
	s.pendingMu.Lock()
	batch := s.pending
	if len(batch) == 0 {
		s.pendingMu.Unlock()
		return nil
	}
	s.pending = s.spare[:0]
	s.spare = nil
	s.pendingMu.Unlock()
	return batch
}

func (s *liveOutputState) recycle(batch []byte) {
	if s == nil || batch == nil {
		return
	}
	s.pendingMu.Lock()
	if cap(batch) > cap(s.spare) {
		s.spare = batch[:0]
	}
	s.pendingMu.Unlock()
}

func (s *liveOutputState) drain(completed bool) error {
	if s == nil {
		return nil
	}
	if batch := s.takePending(); len(batch) > 0 {
		s.raw = append(s.raw, batch...)
		if s.terminal != nil {
			n, err := s.terminal.Write(batch)
			if err != nil {
				s.writeErr = fmt.Errorf("write command output to terminal emulator: %w", err)
			} else if n != len(batch) {
				s.writeErr = io.ErrShortWrite
			} else {
				s.termOffset += n
			}
		}
		s.generation++
		s.recycle(batch)
		if s.writeErr != nil {
			return s.writeErr
		}
	}
	if completed && !s.finalized {
		s.finalized = true
		if s.terminal != nil {
			s.frozen, s.frozenCursor = s.terminal.Snapshot()
			s.frozenCursor.Visible = false
		}
		s.generation++
	}
	return nil
}
