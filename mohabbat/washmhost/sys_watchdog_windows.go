//go:build windows
// +build windows

package main

import (
	"os"
	"syscall"
)

func initWatchdog() {
	ppid := os.Getppid()
	if ppid == 1 || ppid == 0 {
		os.Exit(1)
	}

	go func() {
		// SYNCHRONIZE = 0x00100000
		h, err := syscall.OpenProcess(0x00100000, false, uint32(ppid))
		if err != nil {
			os.Exit(1)
		}
		s, err := syscall.WaitForSingleObject(h, syscall.INFINITE)
		if err == nil && s == syscall.WAIT_OBJECT_0 {
			os.Exit(1)
		}
		os.Exit(1)
	}()
}
