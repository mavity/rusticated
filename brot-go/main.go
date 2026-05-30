package main

import (
	"fmt"
	"os"
)

func main() {
	washmhostBytes, payloadBytes, err := readPool()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brot: failed to read pool: %v\n", err)
		os.Exit(1)
	}

	exitCode, err := runWashmhost(washmhostBytes, payloadBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brot: washmhost failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(exitCode)
}
