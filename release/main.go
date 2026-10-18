package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"mohabbat/mohabbat"
)

type Target struct {
	ProjectDir string
	OutputName string
	Language   string
	Desc       string
}

var allTargets = []Target{
	{ProjectDir: ".", OutputName: "mohab.bat", Language: "Go", Desc: "Self-hosting Mohabbat compiler and vegetable orchestrator"},
	{ProjectDir: "kabibi", OutputName: "kabibi.bat", Language: "Go", Desc: "Flagship interactive shell with built-in AI assistant"},
	{ProjectDir: "demo", OutputName: "demo.bat", Language: "Rust", Desc: "Rust asynchronous I/O, terminal control, and cancellation demo"},
	{ProjectDir: "demo/loch", OutputName: "loch.bat", Language: "Rust", Desc: "Dual-panel terminal file manager built on Ratatui"},
	{ProjectDir: "kabibi-rust", OutputName: "kabibi-rust.bat", Language: "Rust", Desc: "Rust prototype of the interactive shell and side-panel interface"},
	{ProjectDir: "demo-go", OutputName: "demo-go.bat", Language: "Go", Desc: "Go asynchronous file operations and cancelable network requests"},
	{ProjectDir: "demo-go/trivial", OutputName: "trivial.bat", Language: "Go", Desc: "Minimal hello-world showing baseline vegetable startup footprint"},
	{ProjectDir: "demo-go/curl", OutputName: "curl.bat", Language: "Go", Desc: "Universal HTTP/HTTPS client running inside rusticated WASM"},
	{ProjectDir: "demo-go/server", OutputName: "server.bat", Language: "Go", Desc: "Lightweight HTTP server running inside rusticated WASM"},
	{ProjectDir: "ocr4", OutputName: "ocr4.bat", Language: "Go", Desc: "OCR image processing engine with neural model runtime"},
	{ProjectDir: "pope", OutputName: "pope.bat", Language: "Go", Desc: "Terminal agent runner and automated tool invocation engine"},
}

func printHelp(version string) {
	fmt.Printf(`Mohabbat Release Builder v%s
Orchestrates building all workspace projects into self-contained vegetables.

Mohabbat turns Go and Rust projects into single-file polyglot executables (*.bat)
capable of running across Linux, Windows, macOS, and Node.js. This tool executes
the canonical build pipeline sequentially to populate the release/ directory.
The first build error immediately halts the batch.

USAGE:
    go run ./release [-v|--verbose]

FLAGS:
    -v, --verbose    Forward verbose build output from compilers and linkers.
    -h, --help       Show this help text.

TARGET VEGETABLES:
`, version)

	for _, t := range allTargets {
		fmt.Printf("    %-16s [%-4s]  %-18s %s\n", t.OutputName, t.Language, t.ProjectDir, t.Desc)
	}
}

func main() {
	ws, err := mohabbat.ResolveWorkspace("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "🍆  error: could not locate workspace root: %v\n", err)
		os.Exit(1)
	}

	meta := mohabbat.GetBuildMetadata(ws)

	verbose := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "-h", "--help":
			printHelp(meta.Version)
			os.Exit(0)
		case "-v", "--verbose":
			verbose = true
		default:
			fmt.Fprintf(os.Stderr, "🍆  error: unrecognized argument %q (use --help for usage)\n", arg)
			os.Exit(1)
		}
	}

	releaseDir := filepath.Join(ws, "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "🍆  error: failed to create release directory: %v\n", err)
		os.Exit(1)
	}

	totalStart := time.Now()

	fmt.Println("================================================================================")
	fmt.Printf("🍆  Mohabbat Release Orchestrator v%s\n", meta.Version)
	fmt.Printf("🍆  Workspace:   %s\n", ws)
	fmt.Printf("🍆  Destination: %s\n", releaseDir)
	fmt.Printf("🍆  Total:       %d vegetables\n", len(allTargets))
	fmt.Println("================================================================================")

	rootMohabPath := filepath.Join(ws, "mohab.bat")
	releaseMohabPath := filepath.Join(releaseDir, "mohab.bat")

	// Step 1: mohab.bat
	fmt.Println("\n--------------------------------------------------------------------------------")
	fmt.Println("🍆  [1/11] Building core compiler: mohab.bat")
	fmt.Println("--------------------------------------------------------------------------------")

	t0 := time.Now()
	var cmd *exec.Cmd
	if verbose {
		cmd = exec.Command("go", "run", ".", "-v")
	} else {
		cmd = exec.Command("go", "run", ".")
	}
	cmd.Dir = ws
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n🍆  FAILED: mohab.bat build failed after %s: %v\n", time.Since(t0).Round(time.Millisecond), err)
		os.Exit(1)
	}

	if err := copyFile(rootMohabPath, releaseMohabPath); err != nil {
		fmt.Fprintf(os.Stderr, "\n🍆  FAILED: copy mohab.bat to release/: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("🍆  Completed mohab.bat in %s\n", time.Since(t0).Round(time.Millisecond))

	// Step 2: Remaining targets
	for i, t := range allTargets[1:] {
		idx := i + 2
		destPath := filepath.Join(releaseDir, t.OutputName)

		fmt.Println("\n--------------------------------------------------------------------------------")
		fmt.Printf("🍆  [%d/11] Assembling %s -> release/%s [%s]\n", idx, t.ProjectDir, t.OutputName, t.Language)
		fmt.Println("--------------------------------------------------------------------------------")

		tStep := time.Now()
		args := []string{"run", ".", t.ProjectDir, "-o", destPath}
		if verbose {
			args = append(args, "-v")
		}

		subCmd := exec.Command("go", args...)
		subCmd.Dir = ws
		subCmd.Stdin = os.Stdin
		subCmd.Stdout = os.Stdout
		subCmd.Stderr = os.Stderr

		if err := subCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "\n🍆  FAILED: %s build failed after %s: %v\n", t.OutputName, time.Since(tStep).Round(time.Millisecond), err)
			os.Exit(1)
		}
		fmt.Printf("🍆  Completed %s in %s\n", t.OutputName, time.Since(tStep).Round(time.Millisecond))
	}

	totalDuration := time.Since(totalStart).Round(time.Millisecond)
	fmt.Println("\n================================================================================")
	fmt.Printf("🍆  Success: All 11 vegetables built cleanly into release/ in %s\n", totalDuration)
	fmt.Println("================================================================================")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
