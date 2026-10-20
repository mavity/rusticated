//go:build verbose

package main

import (
	"fmt"
	"log"
	"os"
)

var diagnosticLogger = log.New(
	os.Stderr,
	fmt.Sprintf("[mohabbat:washmhost pid=%d] ", os.Getpid()),
	log.LstdFlags|log.Lmicroseconds,
)

func debugLog(format string, a ...any) {
	diagnosticLogger.Printf(format, a...)
}

func debugLogf(format string, a ...any) {
	debugLog(format, a...)
}

func debugPrintln(a ...any) {
	diagnosticLogger.Println(a...)
}
