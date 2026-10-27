package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
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
Orchestrates building all workspace projects into self-contained vegetables
	and staging a complete website distribution in release/dist/.

Mohabbat turns Go and Rust projects into single-file polyglot executables (*.bat)
capable of running across Linux, Windows, macOS, and in browsers via washmhost.
This tool executes the build pipeline sequentially to populate release/dist/.
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

	distDir := filepath.Join(ws, "release", "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "🍆  error: failed to create release/dist directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.Remove(filepath.Join(distDir, "vegetables.zip")); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "🍆  error: failed to remove obsolete release archive: %v\n", err)
		os.Exit(1)
	}

	totalStart := time.Now()

	fmt.Println("================================================================================")
	fmt.Printf("🍆  Mohabbat Release Orchestrator v%s\n", meta.Version)
	fmt.Printf("🍆  Workspace:   %s\n", ws)
	fmt.Printf("🍆  Destination: %s\n", distDir)
	fmt.Printf("🍆  Total:       %d vegetables + website distribution\n", len(allTargets))
	fmt.Println("================================================================================")

	artifacts := make([]BuiltArtifact, 0, len(allTargets))

	for i, t := range allTargets {
		idx := i + 1
		destPath := filepath.Join(distDir, t.OutputName)

		fmt.Println("\n--------------------------------------------------------------------------------")
		fmt.Printf("🍆  [%d/%d] Assembling %s -> release/dist/%s [%s]\n", idx, len(allTargets), t.ProjectDir, t.OutputName, t.Language)
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

	// Step 3: Stage website bundle into release/dist
	fmt.Println("\n--------------------------------------------------------------------------------")
	fmt.Println("🍆  [Website Bundle] Staging index.html with embedded icons, README.md, washmhost.js, and .nojekyll")
	fmt.Println("--------------------------------------------------------------------------------")

	siteAssets := []struct {
		src string
		dst string
	}{
		{src: filepath.Join(ws, "README.md"), dst: filepath.Join(distDir, "README.md")},
		{src: filepath.Join(ws, "mohabbat", "washmhost", "js", "browser", "index.js"), dst: filepath.Join(distDir, "washmhost.js")},
		{src: filepath.Join(ws, "mohabbat", "washmhost", "js", "browser", "index.js"), dst: filepath.Join(ws, "kabibi", "vscode", "washmhost.js")},
		{src: filepath.Join(distDir, "kabibi.bat"), dst: filepath.Join(ws, "kabibi", "vscode", "kabibi.bat")},
	}

	for _, sa := range siteAssets {
		if err := os.MkdirAll(filepath.Dir(sa.dst), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "\n🍆  FAILED: create directory for %s: %v\n", sa.dst, err)
			os.Exit(1)
		}
		if err := copyFile(sa.src, sa.dst); err != nil {
			fmt.Fprintf(os.Stderr, "\n🍆  FAILED: copy %s -> %s: %v\n", sa.src, sa.dst, err)
			os.Exit(1)
		}
	}

	if err := stageWebsiteIndex(ws, distDir); err != nil {
		fmt.Fprintf(os.Stderr, "\n🍆  FAILED: stage index.html with embedded icons: %v\n", err)
		os.Exit(1)
	}

	legacyBrowserHost := filepath.Join(distDir, "mohabbat", "washmhost", "js", "browser", "index.js")
	if err := os.Remove(legacyBrowserHost); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "\n🍆  FAILED: remove obsolete browser host copy %s: %v\n", legacyBrowserHost, err)
		os.Exit(1)
	}

	noJekyllPath := filepath.Join(distDir, ".nojekyll")
	if err := os.WriteFile(noJekyllPath, []byte{}, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "\n🍆  FAILED: create .nojekyll: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("🍆  Website bundle staged cleanly in release/dist/")

	totalDuration := time.Since(totalStart).Round(time.Millisecond)
	printBuildSummary(artifacts, totalDuration)
	fmt.Println("🍆  Success: Website and vegetables staged in release/dist/ for GitHub Pages.")
}

const (
	sourceFaviconLink = `<link rel='icon' type='image/svg+xml' href="kabibi/kabibi.svg">`
	faviconPNGSize    = 180
)

func stageWebsiteIndex(ws, distDir string) error {
	indexTemplate, err := os.ReadFile(filepath.Join(ws, "index.html"))
	if err != nil {
		return fmt.Errorf("read index.html: %w", err)
	}
	svg, err := os.ReadFile(filepath.Join(ws, "kabibi", "kabibi.svg"))
	if err != nil {
		return fmt.Errorf("read Kabibi SVG: %w", err)
	}
	index, err := buildIndexWithEmbeddedFavicons(indexTemplate, svg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), index, 0o644); err != nil {
		return fmt.Errorf("write staged index.html: %w", err)
	}
	return nil
}

func buildIndexWithEmbeddedFavicons(indexTemplate, svg []byte) ([]byte, error) {
	if count := bytes.Count(indexTemplate, []byte(sourceFaviconLink)); count != 1 {
		return nil, fmt.Errorf("expected exactly one source favicon link in index.html, found %d", count)
	}

	pngBytes, err := renderSVGToPNG(svg, faviconPNGSize)
	if err != nil {
		return nil, fmt.Errorf("render Apple touch icon: %w", err)
	}

	svgDataURI := "data:image/svg+xml," + percentEncode(strings.Join(strings.Fields(string(svg)), " "))
	pngDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	links := fmt.Sprintf(
		`<link rel="icon" type="image/svg+xml" href="%s">`+"\n  "+`<link rel="apple-touch-icon" type="image/png" sizes="%dx%d" href="%s">`,
		svgDataURI,
		faviconPNGSize,
		faviconPNGSize,
		pngDataURI,
	)
	return bytes.Replace(indexTemplate, []byte(sourceFaviconLink), []byte(links), 1), nil
}

func percentEncode(value string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", rune(c)) {
			encoded.WriteByte(c)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[c>>4])
		encoded.WriteByte(hex[c&0x0f])
	}
	return encoded.String()
}

func renderSVGToPNG(svg []byte, size int) ([]byte, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svg))
	if err != nil {
		return nil, fmt.Errorf("parse SVG: %w", err)
	}
	icon.SetTarget(0, 0, float64(size), float64(size))

	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	scanner := rasterx.NewScannerGV(size, size, canvas, canvas.Bounds())
	dasher := rasterx.NewDasher(size, size, scanner)
	icon.Draw(dasher, 1)

	var pngBuffer bytes.Buffer
	if err := png.Encode(&pngBuffer, canvas); err != nil {
		return nil, fmt.Errorf("encode PNG: %w", err)
	}
	return pngBuffer.Bytes(), nil
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

func printBuildSummary(artifacts []BuiltArtifact, totalDuration time.Duration) {
	fmt.Println("\n====================================================================================================")
	fmt.Printf("🍆  RELEASE SUMMARY (%d vegetables)\n", len(artifacts))
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
	fmt.Println("====================================================================================================")
	fmt.Printf("🍆  Total Vegetables Size: %s (%d files)\n", formatSize(totalVegetablesSize), len(artifacts))
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
