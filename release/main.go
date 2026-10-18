package main

import (
	"archive/zip"
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

type BuiltArtifact struct {
	Name     string
	Language string
	Size     int64
	Duration time.Duration
	Desc     string
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

	artifacts := make([]BuiltArtifact, 0, len(allTargets))

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

	mohabStat, err := os.Stat(releaseMohabPath)
	var mohabSize int64
	if err == nil {
		mohabSize = mohabStat.Size()
	}
	d0 := time.Since(t0)
	artifacts = append(artifacts, BuiltArtifact{
		Name:     "mohab.bat",
		Language: allTargets[0].Language,
		Size:     mohabSize,
		Duration: d0,
		Desc:     allTargets[0].Desc,
	})
	fmt.Printf("🍆  Completed mohab.bat (%s) in %s\n", formatSize(mohabSize), d0.Round(time.Millisecond))

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

		destStat, err := os.Stat(destPath)
		var destSize int64
		if err == nil {
			destSize = destStat.Size()
		}
		dStep := time.Since(tStep)
		artifacts = append(artifacts, BuiltArtifact{
			Name:     t.OutputName,
			Language: t.Language,
			Size:     destSize,
			Duration: dStep,
			Desc:     t.Desc,
		})
		fmt.Printf("🍆  Completed %s (%s) in %s\n", t.OutputName, formatSize(destSize), dStep.Round(time.Millisecond))
	}

	// Step 3: Bundle all *.bat vegetables into release/vegetables.zip
	fmt.Println("\n--------------------------------------------------------------------------------")
	fmt.Println("🍆  [Zip Archive] Packaging all vegetables into release/vegetables.zip")
	fmt.Println("--------------------------------------------------------------------------------")

	tZip := time.Now()
	zipPath := filepath.Join(releaseDir, "vegetables.zip")
	if err := createReleaseZip(zipPath, releaseDir, allTargets); err != nil {
		fmt.Fprintf(os.Stderr, "\n🍆  FAILED: create vegetables.zip: %v\n", err)
		os.Exit(1)
	}

	zipStat, err := os.Stat(zipPath)
	var zipSize int64
	if err == nil {
		zipSize = zipStat.Size()
	}
	dZip := time.Since(tZip)
	fmt.Printf("🍆  Completed vegetables.zip (%s) in %s\n", formatSize(zipSize), dZip.Round(time.Millisecond))

	totalDuration := time.Since(totalStart).Round(time.Millisecond)

	// Step 4: Summary output table
	printBuildSummary(artifacts, BuiltArtifact{
		Name:     "vegetables.zip",
		Language: "Zip",
		Size:     zipSize,
		Duration: dZip,
		Desc:     fmt.Sprintf("Complete archive containing all %d vegetables", len(allTargets)),
	}, totalDuration)
}

func createReleaseZip(zipPath, releaseDir string, targets []Target) error {
	zFile, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("create zip file: %w", err)
	}
	defer zFile.Close()

	zw := zip.NewWriter(zFile)
	defer zw.Close()

	for _, t := range targets {
		batPath := filepath.Join(releaseDir, t.OutputName)
		info, err := os.Stat(batPath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", t.OutputName, err)
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return fmt.Errorf("create header for %s: %w", t.OutputName, err)
		}

		header.Name = t.OutputName
		header.Method = zip.Deflate
		header.SetMode(0755)

		w, err := zw.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("create zip entry %s: %w", t.OutputName, err)
		}

		f, err := os.Open(batPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", t.OutputName, err)
		}

		_, err = io.Copy(w, f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("compress %s: %w", t.OutputName, err)
		}
	}

	return nil
}

func formatSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func printBuildSummary(artifacts []BuiltArtifact, zipArtifact BuiltArtifact, totalDuration time.Duration) {
	fmt.Println("\n====================================================================================================")
	fmt.Printf("🍆  RELEASE SUMMARY (%d vegetables + 1 zip archive)\n", len(artifacts))
	fmt.Println("====================================================================================================")
	fmt.Printf("  %-18s %-6s %-12s %-10s %s\n", "Target", "Lang", "Size", "Time", "Description")
	fmt.Println("  --------------------------------------------------------------------------------------------------")

	var totalVegetablesSize int64
	for _, a := range artifacts {
		totalVegetablesSize += a.Size
		fmt.Printf("  %-18s %-6s %-12s %-10s %s\n",
			a.Name,
			a.Language,
			formatSize(a.Size),
			a.Duration.Round(time.Millisecond).String(),
			a.Desc,
		)
	}

	fmt.Println("  --------------------------------------------------------------------------------------------------")
	fmt.Printf("  %-18s %-6s %-12s %-10s %s\n",
		zipArtifact.Name,
		zipArtifact.Language,
		formatSize(zipArtifact.Size),
		zipArtifact.Duration.Round(time.Millisecond).String(),
		zipArtifact.Desc,
	)
	fmt.Println("====================================================================================================")
	fmt.Printf("🍆  Total Vegetables Size: %s (%d files)\n", formatSize(totalVegetablesSize), len(artifacts))
	fmt.Printf("🍆  Zip Archive Size:      %s (%s)\n", formatSize(zipArtifact.Size), zipArtifact.Name)
	fmt.Printf("🍆  Total Elapsed Time:    %s\n", totalDuration)
	fmt.Println("====================================================================================================")
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
