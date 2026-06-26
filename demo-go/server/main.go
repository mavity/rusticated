//go:build wasip1

package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	args := os.Args[1:]
	addr := "127.0.0.1:8080"
	root := "."

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			if i+1 < len(args) {
				addr = args[i+1]
				i++
			}
		default:
			if root == "." {
				root = args[i]
			}
		}
	}

	fs := http.FileServer(http.Dir(root))
	http.Handle("/", fs)

	fmt.Fprintf(os.Stderr, "server: serving %s on %s\n", root, addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}
