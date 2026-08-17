//go:build !windows && !js
// +build !windows,!js

package main

import (
	"os"
	"time"
)

func initWatchdog() {
	ppid := os.Getppid()
	if ppid == 1 || ppid == 0 {
		os.Exit(1)
	}

	go func() {
		for {
			time.Sleep(2 * time.Second)
			currentPpid := os.Getppid()
			if currentPpid != ppid || currentPpid == 1 {
				os.Exit(1)
			}
		}
	}()
}
