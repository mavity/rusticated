//go:build wasip1

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: curl <url>")
		os.Exit(1)
	}
	url := args[0]

	t0 := time.Now()
	fmt.Fprintf(os.Stderr, "[T+%.3f] starting http.Get %s\n", 0.0, url)

	resp, err := http.Get(url)
	fmt.Fprintf(os.Stderr, "[T+%.3f] http.Get returned\n", time.Since(t0).Seconds())
	if err != nil {
		fmt.Fprintf(os.Stderr, "curl: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "curl: HTTP %s\n", resp.Status)
		os.Exit(1)
	}

	_, err = io.Copy(os.Stdout, resp.Body)
	fmt.Fprintf(os.Stderr, "[T+%.3f] body copied\n", time.Since(t0).Seconds())
	if err != nil {
		fmt.Fprintf(os.Stderr, "curl: read body: %v\n", err)
		os.Exit(1)
	}
}
