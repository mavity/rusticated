//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsPtySession struct {
	hpcon      windows.Handle
	procHandle windows.Handle
	inPipe     *os.File // Parent writes to this -> goes into ConPTY input
	outPipe    *os.File // Parent reads from this -> comes from ConPTY output
	proc       *os.Process
	mu         sync.Mutex
	closed     bool
}

func (s *windowsPtySession) Read(p []byte) (int, error) {
	return s.outPipe.Read(p)
}

func (s *windowsPtySession) Write(p []byte) (int, error) {
	return s.inPipe.Write(p)
}

func (s *windowsPtySession) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.hpcon == 0 {
		return syscall.EINVAL
	}
	return windows.ResizePseudoConsole(s.hpcon, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (s *windowsPtySession) Wait() (int, error) {
	if s.procHandle != 0 {
		_, _ = windows.WaitForSingleObject(s.procHandle, windows.INFINITE)
	}

	s.mu.Lock()
	if s.hpcon != 0 {
		windows.ClosePseudoConsole(s.hpcon)
		s.hpcon = 0
	}
	s.mu.Unlock()

	if s.proc != nil {
		state, err := s.proc.Wait()
		if state != nil {
			return state.ExitCode(), err
		}
		if err != nil {
			return 1, err
		}
	}
	return 0, nil
}

func (s *windowsPtySession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	if s.inPipe != nil {
		_ = s.inPipe.Close()
	}

	if s.hpcon != 0 {
		windows.ClosePseudoConsole(s.hpcon)
		s.hpcon = 0
	}

	if s.outPipe != nil {
		_ = s.outPipe.Close()
	}

	if s.procHandle != 0 {
		_ = windows.CloseHandle(s.procHandle)
		s.procHandle = 0
	}

	return nil
}

// buildWindowsCommandLine quotes arguments into a standard Windows command-line string.
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

// createEnvironmentBlock creates a null-terminated UTF-16 environment block for CreateProcess.
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
	utf16Slice := utf16.Encode([]rune(b.String()))
	if len(utf16Slice) == 0 {
		return nil
	}
	return &utf16Slice[0]
}

// startWithPty starts cmd under Windows ConPTY, setting up a PseudoConsole
// and launching the process using windows.CreateProcess with STARTUPINFOEX.
func startWithPty(cmd *exec.Cmd, cols, rows int) (PtySession, *os.Process, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	// 1. Create pipes for ConPTY communication
	// inPipe: Parent writes to inWrite -> ConPTY reads from inRead
	// outPipe: ConPTY writes to outWrite -> Parent reads from outRead
	var inRead, inWrite windows.Handle
	var outRead, outWrite windows.Handle

	sa := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1, // pipes must be inheritable
	}

	if err := windows.CreatePipe(&inRead, &inWrite, sa, 0); err != nil {
		return nil, nil, fmt.Errorf("create in pipe: %w", err)
	}
	if err := windows.CreatePipe(&outRead, &outWrite, sa, 0); err != nil {
		_ = windows.CloseHandle(inRead)
		_ = windows.CloseHandle(inWrite)
		return nil, nil, fmt.Errorf("create out pipe: %w", err)
	}

	// 2. Create the PseudoConsole
	var hpcon windows.Handle
	coord := windows.Coord{X: int16(cols), Y: int16(rows)}
	err := windows.CreatePseudoConsole(coord, inRead, outWrite, 0, &hpcon)

	// Close the slave ends in the parent process immediately (ConPTY holds references)
	_ = windows.CloseHandle(inRead)
	_ = windows.CloseHandle(outWrite)

	if err != nil {
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, nil, fmt.Errorf("CreatePseudoConsole failed: %w", err)
	}

	// 3. Setup STARTUPINFOEX with PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hpcon)
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, nil, fmt.Errorf("NewProcThreadAttributeList: %w", err)
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
		return nil, nil, fmt.Errorf("UpdateProcThreadAttribute: %w", err)
	}

	var startupInfo windows.StartupInfoEx
	startupInfo.Cb = uint32(unsafe.Sizeof(startupInfo))
	startupInfo.Flags |= windows.STARTF_USESTDHANDLES
	startupInfo.ProcThreadAttributeList = attrList.List()

	// 4. Build command line
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
		return nil, nil, fmt.Errorf("cmdLine UTF16: %w", err)
	}

	var dirUTF16 *uint16
	if cmd.Dir != "" {
		dirUTF16, err = windows.UTF16PtrFromString(cmd.Dir)
		if err != nil {
			windows.ClosePseudoConsole(hpcon)
			_ = windows.CloseHandle(inWrite)
			_ = windows.CloseHandle(outRead)
			return nil, nil, fmt.Errorf("dir UTF16: %w", err)
		}
	}

	envBlock := createEnvironmentBlock(cmd.Env)
	var procInfo windows.ProcessInformation

	creationFlags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)

	// 5. Spawn child process
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
		return nil, nil, fmt.Errorf("CreateProcess failed: %w", err)
	}

	_ = windows.CloseHandle(procInfo.Thread)

	proc, err := os.FindProcess(int(procInfo.ProcessId))
	if err != nil {
		_ = windows.CloseHandle(procInfo.Process)
		windows.ClosePseudoConsole(hpcon)
		_ = windows.CloseHandle(inWrite)
		_ = windows.CloseHandle(outRead)
		return nil, nil, fmt.Errorf("FindProcess failed: %w", err)
	}

	inPipeFile := os.NewFile(uintptr(inWrite), "|pty_in")
	outPipeFile := os.NewFile(uintptr(outRead), "|pty_out")

	session := &windowsPtySession{
		hpcon:      hpcon,
		procHandle: procInfo.Process,
		inPipe:     inPipeFile,
		outPipe:    outPipeFile,
		proc:       proc,
	}

	// Store process on cmd
	cmd.Process = proc

	return session, proc, nil
}
