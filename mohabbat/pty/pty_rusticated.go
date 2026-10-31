//go:build wasip1

package pty

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

type rusticatedPty struct {
	file *os.File
	proc *os.Process
	fd   int
}

func (p *rusticatedPty) Read(b []byte) (int, error) {
	return p.file.Read(b)
}

func (p *rusticatedPty) Write(b []byte) (int, error) {
	return p.file.Write(b)
}

func (p *rusticatedPty) Fd() uintptr {
	return uintptr(p.fd)
}

func (p *rusticatedPty) Resize(cols, rows int) error {
	return syscall.RawTtySetSize(p.fd, cols, rows)
}

func (p *rusticatedPty) Wait() error {
	if p.proc != nil {
		state, err := p.proc.Wait()
		if err != nil {
			return err
		}
		if !state.Success() {
			return &exec.ExitError{ProcessState: state}
		}
	}
	return nil
}

func (p *rusticatedPty) Close() error {
	return p.file.Close()
}

// Start executes cmd attached to a newly allocated PTY via the rusticated WASM ABI.
func Start(cmd *exec.Cmd, opts ...Options) (PTY, error) {
	cols := 80
	rows := 24
	term := "xterm-256color"
	if len(opts) > 0 {
		if opts[0].Cols > 0 {
			cols = opts[0].Cols
		}
		if opts[0].Rows > 0 {
			rows = opts[0].Rows
		}
		if opts[0].Term != "" {
			term = opts[0].Term
		}
	}

	program := cmd.Path
	if program == "" && len(cmd.Args) > 0 {
		program = cmd.Args[0]
	}

	args := cmd.Args
	if len(args) > 0 {
		args = args[1:]
	}

	envVars := cmd.Environ()
	cwd := cmd.Dir

	// Build rusticated spawn configuration buffer:
	// Format: program\0arg1\0arg2\0\0env1=v1\0\0cwd\0\0pty:cols,rows,term\0
	var buf bytes.Buffer
	buf.WriteString(program)
	buf.WriteByte(0)

	for _, a := range args {
		buf.WriteString(a)
		buf.WriteByte(0)
	}
	buf.WriteByte(0) // end of args

	for _, e := range envVars {
		buf.WriteString(e)
		buf.WriteByte(0)
	}
	buf.WriteByte(0) // end of env

	if cwd != "" {
		buf.WriteString(cwd)
		buf.WriteByte(0)
	}
	buf.WriteByte(0) // end of cwd

	ptySpec := "pty:" + strconv.Itoa(cols) + "," + strconv.Itoa(rows) + "," + term
	buf.WriteString(ptySpec)
	buf.WriteByte(0)

	childHandle, ptyFd, err := syscall.ProcessSpawnWithPty(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("ProcessSpawnWithPty: %w", err)
	}

	// Create os.File from guest fd
	f := os.NewFile(uintptr(ptyFd), "/dev/ptmx")

	// Store process on cmd
	cmd.Process, _ = os.FindProcess(int(childHandle))

	return &rusticatedPty{
		file: f,
		proc: cmd.Process,
		fd:   ptyFd,
	}, nil
}
