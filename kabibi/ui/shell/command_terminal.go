package shell

import (
	"context"
	"errors"
	"io"
	"sync"
)

var (
	ErrTerminalInputQueueFull  = errors.New("command terminal input queue is full")
	ErrNoActiveCommandTerminal = errors.New("no active command terminal")
)

// CommandTerminal provides shell-level terminal capabilities without requiring
// in-process commands to own a widget or an operating-system PTY.
type CommandTerminal struct {
	ctx context.Context

	mu             sync.RWMutex
	cols           int
	rows           int
	stdin          chan []byte
	keys           chan []byte
	writeMu        sync.Mutex
	controlReaders int

	stdinMu  sync.Mutex
	stdinBuf []byte
	keysMu   sync.Mutex
	keysBuf  []byte
}

func newCommandTerminal(ctx context.Context, cols, rows int) *CommandTerminal {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return &CommandTerminal{
		ctx:   ctx,
		cols:  cols,
		rows:  rows,
		stdin: make(chan []byte, 128),
		keys:  make(chan []byte, 128),
	}
}

// Size returns the current logical terminal frame.
func (t *CommandTerminal) Size() (cols, rows int) {
	if t == nil {
		return 0, 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cols, t.rows
}

// Resize updates the logical terminal frame.
func (t *CommandTerminal) Resize(cols, rows int) {
	if t == nil {
		return
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	t.mu.Unlock()
}

// WriteInput routes user input to the active controlling-terminal consumer, or
// to command stdin when no such consumer is registered.
func (t *CommandTerminal) WriteInput(p []byte) error {
	if t == nil || len(p) == 0 {
		return nil
	}
	chunk := append([]byte(nil), p...)
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	select {
	case <-t.ctx.Done():
		return t.ctx.Err()
	default:
	}
	if t.controlReaders > 0 {
		if len(t.keys) == cap(t.keys) {
			return ErrTerminalInputQueueFull
		}
		t.keys <- chunk
		return nil
	}
	if len(t.stdin) == cap(t.stdin) {
		return ErrTerminalInputQueueFull
	}
	t.stdin <- chunk
	return nil
}

// SubscribeControlInput routes subsequent user input to ReadControlInput until
// the returned release function is called.
func (t *CommandTerminal) SubscribeControlInput() func() {
	if t == nil {
		return func() {}
	}
	t.writeMu.Lock()
	t.controlReaders++
	t.writeMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.writeMu.Lock()
			t.controlReaders--
			if t.controlReaders == 0 {
				t.keysMu.Lock()
				t.keysBuf = nil
				for {
					select {
					case <-t.keys:
					default:
						t.keysMu.Unlock()
						t.writeMu.Unlock()
						return
					}
				}
			}
			t.writeMu.Unlock()
		})
	}
}

// ReadInput reads bytes supplied as command stdin.
func (t *CommandTerminal) ReadInput(p []byte) (int, error) {
	if t == nil {
		return 0, io.EOF
	}
	return readTerminalInput(t.ctx, t.ctx, t.stdin, &t.stdinMu, &t.stdinBuf, p)
}

// Read implements io.Reader using the command's terminal stdin endpoint.
func (t *CommandTerminal) Read(p []byte) (int, error) {
	return t.ReadInput(p)
}

// ReadControlInput reads controlling-terminal keys independently of command stdin.
func (t *CommandTerminal) ReadControlInput(p []byte) (int, error) {
	if t == nil {
		return 0, io.EOF
	}
	return readTerminalInput(t.ctx, t.ctx, t.keys, &t.keysMu, &t.keysBuf, p)
}

// ReadControlInputContext reads controlling-terminal keys and also stops when
// the caller's context is canceled.
func (t *CommandTerminal) ReadControlInputContext(ctx context.Context, p []byte) (int, error) {
	if t == nil {
		return 0, io.EOF
	}
	return readTerminalInput(ctx, t.ctx, t.keys, &t.keysMu, &t.keysBuf, p)
}

func readTerminalInput(ctx, terminalCtx context.Context, input <-chan []byte, mu *sync.Mutex, pending *[]byte, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	mu.Lock()
	defer mu.Unlock()
	for len(*pending) == 0 {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-terminalCtx.Done():
			return 0, terminalCtx.Err()
		case chunk := <-input:
			*pending = chunk
		}
	}
	n := copy(p, *pending)
	*pending = (*pending)[n:]
	if len(*pending) == 0 {
		*pending = nil
	}
	return n, nil
}
