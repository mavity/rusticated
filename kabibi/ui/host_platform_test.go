package ui

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"

	xterm "github.com/charmbracelet/x/term"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func TestRunCore_UsesInjectedRawModeBoundary(t *testing.T) {
	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdin = reader
	defer func() {
		os.Stdin = oldStdin
		_ = reader.Close()
		_ = writer.Close()
	}()
	_ = writer.Close()

	var out bytes.Buffer
	stopped := make(chan struct{})
	close(stopped)
	h := &Host{root: noopWidget{}, out: bufio.NewWriter(&out), stopCh: stopped}
	called := false
	restored := false

	if err := h.runCore(
		func(fd uintptr) (*xterm.State, error) {
			called = true
			if got, want := int(fd), int(os.Stdin.Fd()); got != want {
				t.Fatalf("makeRawFunc fd = %d, want %d", got, want)
			}
			return &xterm.State{}, nil
		},
		func(fd uintptr, st *xterm.State) error {
			restored = true
			if got, want := int(fd), int(os.Stdin.Fd()); got != want {
				t.Fatalf("restoreFunc fd = %d, want %d", got, want)
			}
			if st == nil {
				t.Fatal("restoreFunc was called with nil terminal state")
			}
			return nil
		},
	); err != nil {
		t.Fatalf("runCore() error = %v", err)
	}

	if !called {
		t.Fatal("runCore() did not call the injected makeRawFunc")
	}
	if !restored {
		t.Fatal("runCore() did not call the injected restoreFunc")
	}
}

func TestRunScrollbackFrame_RendersQueuedText(t *testing.T) {
	var out bytes.Buffer
	h := &Host{
		root:            noopWidget{},
		out:             bufio.NewWriter(&out),
		currBuf:         terminal.NewCellBuf(80, 24),
		prevBuf:         terminal.NewCellBuf(80, 24),
		scrollbackQueue: []string{"hello"},
	}

	h.runScrollbackFrame(func(fd uintptr) (int, int, error) { return 80, 24, nil })

	got := out.String()
	if !strings.Contains(got, "hello") {
		t.Fatalf("scrollback output = %q, want payload to contain %q", got, "hello")
	}
}

func TestRunCore_RePanicsAfterRestore(t *testing.T) {
	var out bytes.Buffer
	restored := false
	closed := make(chan struct{})
	close(closed)

	h := &Host{root: noopWidget{}, out: bufio.NewWriter(&out), stopCh: closed}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("runCore() did not re-panic after makeRawFunc exploded")
		}
		if got := r; got != "widget explosion" {
			t.Fatalf("panic = %v, want %q", got, "widget explosion")
		}
		if !restored {
			t.Fatal("restoreFunc() was not called before the panic was re-raised")
		}
	}()

	_ = h.runCore(
		func(uintptr) (*xterm.State, error) {
			panic("widget explosion")
		},
		func(uintptr, *xterm.State) error {
			restored = true
			return nil
		},
	)
}
