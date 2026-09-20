//go:build darwin

package ui

import "syscall"

const (
	ioctlGetTermios uintptr = syscall.TIOCGETA
	ioctlSetTermios uintptr = syscall.TIOCSETA
)
