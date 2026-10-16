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

// startWithPty starts cmd under a POSIX PTY (/dev/ptmx).
func startWithPty(cmd *exec.Cmd, cols, rows int) (PtySession, *os.Process, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return openPtmx(cmd, uint16(cols), uint16(rows))
}

func openPtmx(cmd *exec.Cmd, cols, rows uint16) (PtySession, *os.Process, error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	ptsName, err := unlockAndPtsName(ptmx)
	if err != nil {
		_ = ptmx.Close()
		return nil, nil, err
	}

	pts, err := os.OpenFile(ptsName, os.O_RDWR|syscall.O_NOCTTY, 0)
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

// Darwin / BSD ioctl constants alongside Linux TIOCSPTLCK / TIOCGPTN so a single
// unix file cross-compiles cleanly on linux/amd64, linux/arm64, linux/arm, and darwin/arm64.
const (
	linuxTIOCSPTLCK  = 0x40045431
	linuxTIOCGPTN    = 0x80045430
	darwinTIOCPTYGNAME = 0x40807453
	darwinTIOCPTYGRANT = 0x20007454
	darwinTIOCPTYUNLK  = 0x20007452
)

func unlockAndPtsName(ptmx *os.File) (string, error) {
	// Try Linux TIOCSPTLCK + TIOCGPTN first
	var unlock int32 = 0
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), uintptr(linuxTIOCSPTLCK), uintptr(unsafe.Pointer(&unlock)))
	if errno == 0 {
		var ptyNum uint32
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), uintptr(linuxTIOCGPTN), uintptr(unsafe.Pointer(&ptyNum)))
		if errno != 0 {
			return "", fmt.Errorf("ioctl TIOCGPTN: %w", errno)
		}
		return fmt.Sprintf("/dev/pts/%d", ptyNum), nil
	}

	// Fallback to Darwin TIOCPTYGRANT + TIOCPTYUNLK + TIOCPTYGNAME
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), uintptr(darwinTIOCPTYGRANT), 0)
	if errno != 0 {
		return "", fmt.Errorf("ioctl TIOCPTYGRANT: %w", errno)
	}
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), uintptr(darwinTIOCPTYUNLK), 0)
	if errno != 0 {
		return "", fmt.Errorf("ioctl TIOCPTYUNLK: %w", errno)
	}
	var nameBuf [128]byte
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), uintptr(darwinTIOCPTYGNAME), uintptr(unsafe.Pointer(&nameBuf[0])))
	if errno != 0 {
		return "", fmt.Errorf("ioctl TIOCPTYGNAME: %w", errno)
	}
	for i, b := range nameBuf {
		if b == 0 {
			return string(nameBuf[:i]), nil
		}
	}
	return string(nameBuf[:]), nil
}
