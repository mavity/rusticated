package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
)

const saveAlongsideSentinel = "\x01" // sentinel: --save with no arg

// Config holds CLI options plus internal paths resolved by EnsureAssets.
type Config struct {
	ImagePaths []string
	SaveTo     string // \x01=alongside, "*.x"=per-file glob, "path"=single file, ""=no save
	DocContext string
	Threshold  float64
	LMBackend  string
	Output     string
	Verbose    bool

	// resolved by EnsureAssets
	LMLibPath      string
	TFLLibPath     string
	GemmaModelPath string
	DBModelPath    string
	RecModelPath   string
}

func main() {
	cfg := &Config{}
	// Rewrite bare --save (no value) to --save=\x01 before flag parsing.
	rawArgs := rewriteSaveArg(os.Args[1:])
	fs := flag.NewFlagSet("ocr4", flag.ExitOnError)
	fs.Func("image", "image file, directory, or glob (repeatable)", func(v string) error {
		cfg.ImagePaths = append(cfg.ImagePaths, v)
		return nil
	})
	fs.StringVar(&cfg.SaveTo, "save", "", "save output: no arg=alongside input, *.ext=per-file pattern, path=single file")
	fs.StringVar(&cfg.DocContext, "doc-context", "", "surrounding document text for Phase 2 context")
	fs.Float64Var(&cfg.Threshold, "threshold", ConfidenceThreshold, "line confidence threshold τ (0–1)")
	fs.StringVar(&cfg.LMBackend, "backend", "gpu", "LiteRT-LM backend: gpu|npu|cpu|auto")
	fs.StringVar(&cfg.Output, "output", "text", "output format: text|json")
	fs.BoolVar(&cfg.Verbose, "verbose", false, "print progress and debug information")
	fs.Parse(rawArgs)

	if len(cfg.ImagePaths) == 0 {
		fmt.Fprintln(os.Stderr, "ocr4: at least one -image is required")
		fs.Usage()
		os.Exit(1)
	}
	if err := runAll(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "ocr4:", err)
		os.Exit(1)
	}
}

// rewriteSaveArg rewrites bare --save (no value) to --save=\x01 so flag.Parse treats it as optional.
func rewriteSaveArg(args []string) []string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		if (a == "--save" || a == "-save") &&
			(i+1 >= len(args) || strings.HasPrefix(args[i+1], "-")) {
			out = append(out, a+"="+saveAlongsideSentinel)
		} else {
			out = append(out, a)
		}
	}
	return out
}

func runAll(cfg *Config) error {
	ctx := context.Background()
	if err := EnsureAssets(ctx, cfg, func(msg string) {
		if cfg.Verbose {
			fmt.Println("[assets]", msg)
		}
	}); err != nil {
		return fmt.Errorf("asset setup: %w", err)
	}
	if cfg.LMLibPath == "" {
		return fmt.Errorf("LiteRT-LM runtime not available")
	}

	files, err := expandImagePaths(cfg.ImagePaths)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no image files found")
	}

	tflPath := cfg.TFLLibPath
	if tflPath == "" {
		tflPath = cfg.LMLibPath
	}
	if cfg.DBModelPath == "" || cfg.RecModelPath == "" {
		if cfg.Verbose {
			fmt.Println("[phase1] skipped (OCR models unavailable)")
		}
		return nil
	}
	detector, err := NewTFLiteInterpreter(tflPath, cfg.DBModelPath)
	if err != nil {
		return fmt.Errorf("DB detector: %w", err)
	}
	defer detector.Close()
	recognizer, err := NewTFLiteInterpreter(tflPath, cfg.RecModelPath)
	if err != nil {
		return fmt.Errorf("recognizer: %w", err)
	}
	defer recognizer.Close()

	// LM engine is expensive; create once and share across all images.
	var lmEngine *LMEngine
	if cfg.LMLibPath != "" && cfg.GemmaModelPath != "" {
		lmEngine, err = NewLMEngine(cfg.LMLibPath, cfg.GemmaModelPath, cfg.LMBackend)
		if err != nil && cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[phase2] engine init: %v\n", err)
		}
		if lmEngine != nil {
			defer lmEngine.Close()
		}
	}

	isSaveAlongside := cfg.SaveTo == saveAlongsideSentinel
	isSaveGlob := strings.Contains(cfg.SaveTo, "*")
	isSaveFile := cfg.SaveTo != "" && !isSaveAlongside && !isSaveGlob

	// Multiple images + plain path → single combined output file.
	var multiSave *os.File
	if isSaveFile && len(files) > 1 {
		multiSave, err = os.Create(cfg.SaveTo)
		if err != nil {
			return fmt.Errorf("save file: %w", err)
		}
		defer multiSave.Close()
	}

	multi := len(files) > 1
	for idx, imgPath := range files {
		if multi {
			if idx > 0 {
				fmt.Println()
			}
			fmt.Println(filepath.Base(imgPath))
		}
		lines, procErr := processImage(ctx, cfg, detector, recognizer, lmEngine, imgPath)
		if procErr != nil {
			fmt.Fprintf(os.Stderr, "ocr4: %s: %v\n", imgPath, procErr)
			continue
		}
		switch {
		case isSaveAlongside:
			saveLinesFile(strings.TrimSuffix(imgPath, filepath.Ext(imgPath))+".txt", lines)
		case isSaveGlob:
			stem := strings.TrimSuffix(filepath.Base(imgPath), filepath.Ext(imgPath))
			saveLinesFile(strings.ReplaceAll(cfg.SaveTo, "*", stem), lines)
		case multiSave != nil:
			if idx > 0 {
				fmt.Fprintln(multiSave)
			}
			fmt.Fprintln(multiSave, filepath.Base(imgPath))
			for _, l := range lines {
				fmt.Fprintln(multiSave, l.Text)
			}
		case isSaveFile:
			saveLinesFile(cfg.SaveTo, lines)
		}
	}
	return nil
}

// processImage runs Phase 1 + Phase 2 on one image, printing each line as soon as it is ready.
func processImage(ctx context.Context, cfg *Config, detector, recognizer *TFLiteInterpreter, engine *LMEngine, imgPath string) ([]TextLine, error) {
	_ = ctx
	if cfg.Verbose {
		fmt.Println("[phase1] loading image:", imgPath)
	}
	img, err := LoadImage(imgPath)
	if err != nil {
		return nil, fmt.Errorf("loading image: %w", err)
	}
	boxes, err := DetectLines(detector, img, cfg.Verbose)
	if err != nil {
		return nil, fmt.Errorf("line detection: %w", err)
	}
	SortByVertical(boxes)
	lines, err := RecognizeLines(recognizer, img, boxes, cfg.Verbose)
	if err != nil {
		return nil, fmt.Errorf("line recognition: %w", err)
	}
	if cfg.Verbose {
		for i, l := range lines {
			fmt.Printf("[phase1] line[%d] conf=%.3f needsLLM=%v %q\n", i, l.Confidence, l.NeedsLLM, l.Text)
		}
	}

	pageText := buildPageContext(lines)
	isJSON := cfg.Output == "json"

	for i := range lines {
		if lines[i].NeedsLLM && engine != nil {
			if err := RefineOneLine(engine, pageText, &lines[i], img); err != nil && cfg.Verbose {
				fmt.Printf("[phase2] line %d: %v\n", i, err)
			} else if cfg.Verbose && !lines[i].NeedsLLM {
				fmt.Printf("[phase2] line %d refined: %q\n", i, lines[i].Text)
			}
		}
		if !isJSON {
			fmt.Println(lines[i].Text)
		}
	}
	if isJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if lines == nil {
			lines = []TextLine{}
		}
		enc.Encode(lines)
	}
	return lines, nil
}

func saveLinesFile(path string, lines []TextLine) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocr4: save %s: %v\n", path, err)
		return
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l.Text)
	}
}

func expandImagePaths(patterns []string) ([]string, error) {
	var files []string
	seen := make(map[string]bool)
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	for _, p := range patterns {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if !e.IsDir() && isImageExt(e.Name()) {
					add(filepath.Join(p, e.Name()))
				}
			}
		} else if isGlobExpr(p) {
			matches, err := filepath.Glob(p)
			if err != nil {
				return nil, err
			}
			for _, m := range matches {
				if isImageExt(m) {
					add(m)
				}
			}
		} else {
			add(p)
		}
	}
	return files, nil
}

func isGlobExpr(p string) bool { return strings.ContainsAny(p, "*?[") }

func isImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".tif", ".tiff":
		return true
	}
	return false
}
