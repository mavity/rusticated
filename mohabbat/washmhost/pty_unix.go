//go:build !windows && !js && !wasip1

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

type unixPtySession struct {
	master *os.File
	mu     sync.Mutex
	closed bool
}

func (s *unixPtySession) Read(p []byte) (int, error) {
	n, err := s.master.Read(p)
	if err != nil {
		// Linux returns EIO when slave closes; treat as EOF
		if err == syscall.EIO || err.Error() == "input/output error" {
			return n, io.EOF
		}
		return n, err
	}
	return n, nil
}

func (s *unixPtySession) Write(p []byte) (int, error) {
	return s.master.Write(p)
}

func (s *unixPtySession) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.master == nil {
		return syscall.EINVAL
	}
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{
		Row: uint16(rows),
		Col: uint16(cols),
	}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		s.master.Fd(),
		syscall.TIOCSWINSZ,
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func (s *unixPtySession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.master != nil {
		return s.master.Close()
	}
	return nil
}

// startWithPty starts cmd under POSIX PTY using openpty syscall.
func startWithPty(cmd *exec.Cmd, cols, rows int) (PtySession, *os.Process, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{
		Row: uint16(rows),
		Col: uint16(cols),
	}

	var masterFd, slaveFd int32
	// openpty(&master, &slave, nil, nil, &winsize)
	_, _, errno := syscall.Syscall6(
		syscall.SYS_OPENPTY,
		uintptr(unsafe.Pointer(&masterFd)),
		uintptr(unsafe.Pointer(&slaveFd)),
		0,
		0,
		uintptr(unsafe.Pointer(&ws)),
		0,
	)
	if errno != 0 {
		// Fallback for systems where SYS_OPENPTY is not a direct syscall (e.g. Linux /dev/ptmx)
		return openPtmxFallback(cmd, ws.Col, ws.Row)
	}

	master := os.NewFile(uintptr(masterFd), "/dev/ptmx")
	slave := os.NewFile(uintptr(slaveFd), "/dev/pts")
	defer slave.Close()

	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = int(slave.Fd())

	err := cmd.Start()
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}

	session := &unixPtySession{
		master: master,
	}
	return session, cmd.Process, nil
}

func openPtmxFallback(cmd *exec.Cmd, cols, rows uint16) (PtySession, *os.Process, error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	// grantpt + unlockpt via ioctl if needed on Linux
	var unlock int32 = 0
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock)))

	var ptyNum int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&ptyNum)))
	if errno != 0 {
		_ = ptmx.Close()
		return nil, nil, fmt.Errorf("ioctl TIOCGPTN: %w", errno)
	}

	ptsName := fmt.Sprintf("/dev/pts/%d", ptyNum)
	pts, err := os.OpenFile(ptsName, os.O_RDWR, 0)
	if err != nil {
		_ = ptmx.Close()
		return nil, nil, fmt.Errorf("open %s: %w", ptsName, err)
	}
	defer pts.Close()

	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{
		Row: rows,
		Col: cols,
	}
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))

	cmd.Stdin = pts
	cmd.Stdout = pts
	cmd.Stderr = pts
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = int(pts.Fd())

	err = cmd.Start()
	if err != nil {
		_ = ptmx.Close()
		return nil, nil, err
	}

	session := &unixPtySession{
		master: ptmx,
	}
	return session, cmd.Process, nil
}
