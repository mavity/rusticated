//go:build windows && !wasip1

package pty

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsPty struct {
	hpcon      windows.Handle
	procHandle windows.Handle
	inPipe     *os.File
	outPipe    *os.File
	proc       *os.Process
	mu         sync.Mutex
	closed     bool
}

func (p *windowsPty) Read(b []byte) (int, error) {
	return p.outPipe.Read(b)
}

func (p *windowsPty) Write(b []byte) (int, error) {
	return p.inPipe.Write(b)
}

func (p *windowsPty) Fd() uintptr {
	return p.outPipe.Fd()
}

func (p *windowsPty) Resize(cols, rows int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.hpcon == 0 {
		return syscall.EINVAL
	}
	return windows.ResizePseudoConsole(p.hpcon, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (p *windowsPty) Wait() error {
	if p.procHandle != 0 {
		_, _ = windows.WaitForSingleObject(p.procHandle, windows.INFINITE)
	}

	p.mu.Lock()
	if p.hpcon != 0 {
		windows.ClosePseudoConsole(p.hpcon)
		p.hpcon = 0
	}
	p.mu.Unlock()

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

func (p *windowsPty) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true

	if p.inPipe != nil {
		_ = p.inPipe.Close()
	}
	if p.hpcon != 0 {
		windows.ClosePseudoConsole(p.hpcon)
		p.hpcon = 0
	}
	if p.outPipe != nil {
		_ = p.outPipe.Close()
	}
	if p.procHandle != 0 {
		_ = windows.CloseHandle(p.procHandle)
		p.procHandle = 0
	}
	return nil
}

func buildWindowsCommandLine(args []string) string {
	if len(args) == 0 {
		return ""
	}
	var b strings.Builder
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if !strings.ContainsAny(arg, " \t\n\v\"") && arg != "" {
			b.WriteString(arg)
			continue
		}
		b.WriteByte('"')
		for j := 0; j < len(arg); j++ {
			backslashes := 0
			for j < len(arg) && arg[j] == '\\' {
				backslashes++
				j++
			}
			if j == len(arg) {
				b.WriteString(strings.Repeat("\\", backslashes*2))
				break
			}
			if arg[j] == '"' {
				b.WriteString(strings.Repeat("\\", backslashes*2+1))
				b.WriteByte('"')
			} else {
				b.WriteString(strings.Repeat("\\", backslashes))
				b.WriteByte(arg[j])
			}
		}
		b.WriteByte('"')
	}
	return b.String()
}

func createEnvironmentBlock(env []string) *uint16 {
	if len(env) == 0 {
		return nil
	}
	var b strings.Builder
	for _, kv := range env {
		b.WriteString(kv)
		b.WriteByte(0)
	}
	b.WriteByte(0)
	utf16, err := windows.UTF16FromString(b.String())
	if err != nil {
		return nil
	}
	return &utf16[0]
}

// Start executes cmd attached to a newly allocated Windows ConPTY.
func Start(cmd *exec.Cmd, opts ...Options) (PTY, error) {
	cols := 80
	rows := 24
	if len(opts) > 0 {
		if opts[0].Cols > 0 {
			cols = opts[0].Cols
		}
		if opts[0].Rows > 0 {
			rows = opts[0].Rows
		}
		if opts[0].Term != "" {
			cmd.Env = append(cmd.Environ(), "TERM="+opts[0].Term)
		}
	}

	var inRead, inWrite windows.Handle
	var outRead, outWrite windows.Handle

	sa := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}

	if err := windows.CreatePipe(&inRead, &inWrite, sa, 0); err != nil {
		return nil, fmt.Errorf("create in pipe: %w", err)
	}
	if err := windows.CreatePipe(&outRead, &outWrite, sa, 0); err != nil {
		_ = windows.CloseHandle(inRead)
		_ = windows.CloseHandle(inWrite)
		return nil, fmt.Errorf("create out pipe: %w", err)
	}

	var hpcon windows.Handle
	coord := windows.Coord{X: int16(cols), Y: int16(rows)}
	err := windows.CreatePseudoConsole(coord, inRead, outWrite, 0, &hpcon)

	_ = windows.CloseHandle(inRead)
	_ = windows.CloseHandle(outWrite)

	if err != nil {
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, fmt.Errorf("CreatePseudoConsole failed: %w", err)
	}

	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hpcon)
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, fmt.Errorf("NewProcThreadAttributeList: %w", err)
	}
	defer attrList.Delete()

	const PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE = 0x00020016
	if err := attrList.Update(
		PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(hpcon),
		unsafe.Sizeof(hpcon),
	); err != nil {
		windows.ClosePseudoConsole(hpcon)
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, fmt.Errorf("UpdateProcThreadAttribute: %w", err)
	}

	var startupInfo windows.StartupInfoEx
	startupInfo.Cb = uint32(unsafe.Sizeof(startupInfo))
	startupInfo.Flags |= windows.STARTF_USESTDHANDLES
	startupInfo.ProcThreadAttributeList = attrList.List()

	fullArgs := cmd.Args
	if len(fullArgs) == 0 {
		fullArgs = []string{cmd.Path}
	}
	cmdLineStr := buildWindowsCommandLine(fullArgs)
	cmdLineUTF16, err := windows.UTF16PtrFromString(cmdLineStr)
	if err != nil {
		windows.ClosePseudoConsole(hpcon)
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, fmt.Errorf("cmdLine UTF16: %w", err)
	}

	var dirUTF16 *uint16
	if cmd.Dir != "" {
		dirUTF16, err = windows.UTF16PtrFromString(cmd.Dir)
		if err != nil {
			windows.ClosePseudoConsole(hpcon)
			_ = windows.CloseHandle(inWrite)
			_ = windows.CloseHandle(outRead)
			return nil, fmt.Errorf("dir UTF16: %w", err)
		}
	}

	envBlock := createEnvironmentBlock(cmd.Environ())
	var procInfo windows.ProcessInformation

	creationFlags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT)

	err = windows.CreateProcess(
		nil,
		cmdLineUTF16,
		nil,
		nil,
		false,
		creationFlags,
		envBlock,
		dirUTF16,
		&startupInfo.StartupInfo,
		&procInfo,
	)
	if err != nil {
		windows.ClosePseudoConsole(hpcon)
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, fmt.Errorf("CreateProcess failed: %w", err)
	}

	_ = windows.CloseHandle(procInfo.Thread)

	proc, err := os.FindProcess(int(procInfo.ProcessId))
	if err != nil {
		_ = windows.CloseHandle(procInfo.Process)
		windows.ClosePseudoConsole(hpcon)
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, fmt.Errorf("FindProcess failed: %w", err)
	}

	inPipeFile := os.NewFile(uintptr(inWrite), "|pty_in")
	outPipeFile := os.NewFile(uintptr(outRead), "|pty_out")

	cmd.Process = proc

	return &windowsPty{
		hpcon:      hpcon,
		procHandle: procInfo.Process,
		inPipe:     inPipeFile,
		outPipe:    outPipeFile,
		proc:       proc,
	}, nil
}
