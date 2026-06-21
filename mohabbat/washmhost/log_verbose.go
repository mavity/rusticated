//go:build verbose

package main

import "fmt"

func debugLog(format string, a ...any) {
	fmt.Printf(format, a...)
}

func debugLogf(format string, a ...any) {
	fmt.Printf(format, a...)
}

func debugPrintln(a ...any) {
	fmt.Println(a...)
}
