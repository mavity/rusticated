//go:build linux

package ui

import "syscall"

const (
	ioctlGetTermios uintptr = syscall.TCGETS
	ioctlSetTermios uintptr = syscall.TCSETS
)
