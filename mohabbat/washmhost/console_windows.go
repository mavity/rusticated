//go:build windows

package main

import (
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

// setupConsoleUtf8 configures the Windows console to UTF-8 (Code Page 65001)
// and enables virtual terminal (VT) sequence processing on stdout and stderr.
func setupConsoleUtf8() func() {
	const cpUTF8 = uint32(65001)

	prevInCP, _ := windows.GetConsoleCP()
	prevOutCP, _ := windows.GetConsoleOutputCP()

	_ = windows.SetConsoleCP(cpUTF8)
	_ = windows.SetConsoleOutputCP(cpUTF8)

	stdoutHandle := windows.Handle(os.Stdout.Fd())
	var prevStdoutMode uint32
	hasStdoutMode := windows.GetConsoleMode(stdoutHandle, &prevStdoutMode) == nil
	if hasStdoutMode {
		_ = windows.SetConsoleMode(stdoutHandle, prevStdoutMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}

	stderrHandle := windows.Handle(os.Stderr.Fd())
	var prevStderrMode uint32
	hasStderrMode := windows.GetConsoleMode(stderrHandle, &prevStderrMode) == nil
	if hasStderrMode {
		_ = windows.SetConsoleMode(stderrHandle, prevStderrMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			if prevInCP != 0 {
				_ = windows.SetConsoleCP(prevInCP)
			}
			if prevOutCP != 0 {
				_ = windows.SetConsoleOutputCP(prevOutCP)
			}
			if hasStdoutMode {
				_ = windows.SetConsoleMode(stdoutHandle, prevStdoutMode)
			}
			if hasStderrMode {
				_ = windows.SetConsoleMode(stderrHandle, prevStderrMode)
			}
		})
	}
}
