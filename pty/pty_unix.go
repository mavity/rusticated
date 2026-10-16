//go:build !windows && !js && !wasip1

package pty

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

type unixPty struct {
	master *os.File
	cmd    *exec.Cmd
	mu     sync.Mutex
	closed bool
}

func (p *unixPty) Read(b []byte) (int, error) {
	n, err := p.master.Read(b)
	if err != nil {
		if err == syscall.EIO || err.Error() == "input/output error" {
			return n, io.EOF
		}
		return n, err
	}
	return n, nil
}

func (p *unixPty) Write(b []byte) (int, error) {
	return p.master.Write(b)
}

func (p *unixPty) Fd() uintptr {
	return p.master.Fd()
}

func (p *unixPty) Resize(cols, rows int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.master == nil {
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
		p.master.Fd(),
		syscall.TIOCSWINSZ,
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func (p *unixPty) Wait() error {
	if p.cmd != nil {
		return p.cmd.Wait()
	}
	return nil
}

func (p *unixPty) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.master != nil {
		return p.master.Close()
	}
	return nil
}

// Start executes cmd attached to a newly allocated POSIX PTY.
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
		return nil, err
	}

	return &unixPty{master: master, cmd: cmd}, nil
}

func openPtmxFallback(cmd *exec.Cmd, cols, rows uint16) (PTY, error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	var unlock int32 = 0
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock)))

	var ptyNum int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&ptyNum)))
	if errno != 0 {
		_ = ptmx.Close()
		return nil, fmt.Errorf("ioctl TIOCGPTN: %w", errno)
	}

	ptsName := fmt.Sprintf("/dev/pts/%d", ptyNum)
	pts, err := os.OpenFile(ptsName, os.O_RDWR, 0)
	if err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("open %s: %w", ptsName, err)
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
		return nil, err
	}

	return &unixPty{master: ptmx, cmd: cmd}, nil
}
