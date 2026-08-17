//go:build js
// +build js

package main

func initWatchdog() {
	// js/wasm doesn't have parent processes in the same way, stub out.
}
