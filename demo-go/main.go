//go:build wasip1

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
	_ "unsafe"
)

func main() {
	// ── 1. Environment Diagnostics ─────────────────────────────────────────
	cwd, err := os.Getwd()
	if err != nil {
		println("getwd error:", err.Error())
		cwd = "(unknown)"
	}
	println("cwd=", cwd)

	println("DEBUG: calling stat on .")
	fi, err := os.Stat(".")
	if err != nil {
		println("stat(.) error:", err.Error())
	} else {
		println("stat(.) success: name=", fi.Name(), " size=", fi.Size())
	}
	os.Stdout.Sync()

	fmt.Printf("PWD=%s\n", os.Getenv("PWD"))
	fmt.Printf("TempDir=%s\n", os.TempDir())

	for _, dir := range []string{".", os.TempDir()} {
		if dir == "" {
			continue
		}
		fmt.Printf("DEBUG: calling ReadDir(%s)\n", dir)
		os.Stdout.Sync()
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Printf("readdir(%s): %v\n", dir, err)
			continue
		}
		fmt.Printf("DEBUG: ReadDir(%s) returned %d entries\n", dir, len(entries))
		fmt.Printf("ls %s:\n", dir)
		for _, e := range entries {
			fmt.Printf("  %s\n", e.Name())
		}
	}

	// ── 2. Timed Input ─────────────────────────────────────────────────────
	fmt.Printf("input (5s timeout): ")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		data string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 256)
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			ch <- result{data: string(buf[:n])}
		} else {
			ch <- result{err: err}
		}
	}()

	select {
	case r := <-ch:
		if r.err != nil && r.err != io.EOF {
			fmt.Printf("input error: %v\n", r.err)
		} else {
			fmt.Printf("got: %q\n", r.data)
		}
	case <-ctx.Done():
		fmt.Printf("timeout\n")
	}

	// ── 3. File I/O ────────────────────────────────────────────────────────
	const demoFile = "rusticated_demo.txt"
	const demoContent = "rusticated demo file contents\n"
	if err := os.WriteFile(demoFile, []byte(demoContent), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "writefile: %v\n", err)
	} else {
		data, err := os.ReadFile(demoFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "readfile: %v\n", err)
		} else {
			last := data[len(data)-1]
			fmt.Printf("demo file last byte: %q\n", last)
		}
	}

	// ── 4. Metadata ────────────────────────────────────────────────────────
	exePath := os.Args[0]
	fi, err = os.Stat(exePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stat %s: %v\n", exePath, err)
	} else {
		fmt.Printf("exe mtime: %s\n", fi.ModTime().Format(time.RFC3339))
	}
}
