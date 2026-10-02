package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	_ "image/jpeg"
	_ "image/png"
	"os"
)

// Config holds user-facing CLI options plus internal paths resolved by EnsureAssets.
type Config struct {
	ImagePath  string
	DocContext string
	Threshold  float64
	LMBackend  string
	Output     string
	Verbose    bool

	// resolved by EnsureAssets — not exposed as flags
	LMLibPath      string
	TFLLibPath     string
	GemmaModelPath string
	DBModelPath    string
	RecModelPath   string
}

func main() {
	cfg := &Config{}
	flag.StringVar(&cfg.ImagePath, "image", "", "path to input image (JPEG/PNG)")
	flag.StringVar(&cfg.DocContext, "doc-context", "", "surrounding document text for Phase 2 KV-cache prefill (optional)")

	flag.Float64Var(&cfg.Threshold, "threshold", ConfidenceThreshold, "line confidence threshold τ (0–1)")
	flag.StringVar(&cfg.LMBackend, "backend", "auto", "LiteRT-LM inference backend: auto|gpu|npu|cpu")
	flag.StringVar(&cfg.Output, "output", "text", "output format: text|json")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "print progress and debug information")
	flag.Parse()

	if cfg.ImagePath == "" {
		fmt.Fprintln(os.Stderr, "ocr4: -image is required")
		flag.Usage()
		os.Exit(1)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "ocr4:", err)
		os.Exit(1)
	}
}

func run(cfg *Config) error {
	ctx := context.Background()

	// ── Step 2: asset setup (concurrent downloads + validation) ──────────────
	logf := func(msg string) {
		if cfg.Verbose {
			fmt.Println("[assets]", msg)
		}
	}
	if err := EnsureAssets(ctx, cfg, logf); err != nil {
		return fmt.Errorf("asset setup: %w", err)
	}
	if cfg.LMLibPath == "" {
		return fmt.Errorf("LiteRT-LM runtime not available")
	}

	// ── Load image ────────────────────────────────────────────────────────────
	if cfg.Verbose {
		fmt.Println("[phase1] loading image:", cfg.ImagePath)
	}
	img, err := LoadImage(cfg.ImagePath)
	if err != nil {
		return fmt.Errorf("loading image: %w", err)
	}

	// ── Step 3: Phase 1 — TFLite DB detection + SVTR recognition ─────────────
	tflPath := cfg.TFLLibPath
	if tflPath == "" {
		tflPath = cfg.LMLibPath // LiteRT-LM DLL also ships TFLite symbols
	}
	if cfg.DBModelPath == "" || cfg.RecModelPath == "" {
		if cfg.Verbose {
			fmt.Println("[phase1] skipped (OCR models unavailable)")
		}
		return emit(cfg.Output, []TextLine{})
	}
	detector, err := NewTFLiteInterpreter(tflPath, cfg.DBModelPath)
	if err != nil {
		return fmt.Errorf("DB detector init: %w", err)
	}
	defer detector.Close()

	recognizer, err := NewTFLiteInterpreter(tflPath, cfg.RecModelPath)
	if err != nil {
		return fmt.Errorf("recognizer init: %w", err)
	}
	defer recognizer.Close()

	if cfg.Verbose {
		fmt.Println("[phase1] detecting text lines...")
	}
	boxes, err := DetectLines(detector, img)
	if err != nil {
		return fmt.Errorf("line detection: %w", err)
	}
	if cfg.Verbose {
		fmt.Printf("[phase1] detected %d line regions\n", len(boxes))
		for i, box := range boxes {
			fmt.Printf("[phase1]   box[%d]: (%d,%d) -> (%d,%d) size=%dx%d\n",
				i, box.X0, box.Y0, box.X1, box.Y1, box.X1-box.X0, box.Y1-box.Y0)
		}
	}
	// Sort boxes top-to-bottom (by Y0), left-to-right tiebreaker for consistent reading order.
	SortByVertical(boxes)
	if cfg.Verbose {
		fmt.Println("[phase1] sorted by vertical position")
	}
	var lines []TextLine
	lines, err = RecognizeLines(recognizer, img, boxes)
	if err != nil {
		return fmt.Errorf("line recognition: %w", err)
	}
	if cfg.Verbose {
		for i, l := range lines {
			fmt.Printf("[phase1] line[%d] conf=%.3f needsLLM=%v %q\n", i, l.Confidence, l.NeedsLLM, l.Text)
		}
	}

	// ── Phase 2: Gemma 4 LLM refinement (automatic when Gemma assets are available) ──
	// Note: the Mohabbat runner consumes -r; the binary must not gate on a flag it can never see.
	if cfg.Verbose {
		fmt.Printf("[phase2] LMLibPath available=%v GemmaModelPath available=%v\n",
			cfg.LMLibPath != "", cfg.GemmaModelPath != "")
	}
	if cfg.LMLibPath != "" && cfg.GemmaModelPath != "" {
		if err := RefineLines(ctx, cfg, lines, img); err != nil && cfg.Verbose {
			fmt.Printf("[phase2] refinement error: %v\n", err)
		}
	}
	return emit(cfg.Output, lines)
}

// filterNeedsLLM returns the subset of lines flagged for LLM correction.
func filterNeedsLLM(lines []TextLine) []TextLine {
	var out []TextLine
	for _, l := range lines {
		if l.NeedsLLM {
			out = append(out, l)
		}
	}
	return out
}

// emit writes the final line results to stdout.
func emit(format string, lines []TextLine) error {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if lines == nil {
			lines = []TextLine{}
		}
		return enc.Encode(lines)
	}
	for _, l := range lines {
		fmt.Println(l.Text)
	}
	if len(lines) == 0 {
		fmt.Println("(no text lines detected)")
	}
	return nil
}
