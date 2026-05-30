package main

import (
	"fmt"
	"os"
	"runtime"
)

func runtimeGOARCH() string { return runtime.GOARCH }

func main() {
	exe, _ := os.Executable()
	fi, _ := os.Stat(exe)
	var sz int64
	if fi != nil {
		sz = fi.Size()
	}
	fmt.Fprintf(os.Stderr, "[brot] exe=%s size=%d arch=%s\n", exe, sz, runtimeGOARCH())
	fmt.Fprintf(os.Stderr, "[brot] meta: pool_len=%d wh_off=%d wh_len=%d pl_off=%d pl_len=%d\n",
		mohabbatMeta.PoolLen, mohabbatMeta.WashmhostOffset, mohabbatMeta.WashmhostLen,
		mohabbatMeta.PayloadOffset, mohabbatMeta.PayloadLen)

	washmhostBytes, payloadBytes, err := readPool()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brot: failed to read pool: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[brot] extracted: wh=%d bytes, payload=%d bytes\n", len(washmhostBytes), len(payloadBytes))

	exitCode, err := runWashmhost(washmhostBytes, payloadBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brot: washmhost failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[brot] washmhost returned %d\n", exitCode)

	os.Exit(exitCode)
}
