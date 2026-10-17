package shell

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"ai"
	"github.com/mavity/rusticated/kabibi/tools/fabric"
	"github.com/mavity/rusticated/kabibi/tools/filelist"
	"github.com/mavity/rusticated/kabibi/tools/hashline"
	"github.com/mavity/rusticated/kabibi/tools/ocr"
)

type ReadFileInput struct {
	_           struct{} `doc:"Read a file with optional line ranges, 4-tier search filtering, indentation fabric expansion, and LINE:HASH| hashline anchors for safe editing." emoji:"📖"`
	Path        string   `json:"path" doc:"File path relative to current working directory"`
	StartLine   int      `json:"start_line,omitempty" doc:"1-based start line (optional)"`
	EndLine     int      `json:"end_line,omitempty" doc:"1-based end line (optional)"`
	Search      string   `json:"search,omitempty" doc:"Filter file to blocks matching query. Tries exact -> regex -> fuzzy -> semantic embeddings."`
	SearchMode  string   `json:"search_mode,omitempty" doc:"Search mode: 'auto', 'plain', 'regex', or 'semantic' (default 'auto')"`
	Hashline    *bool    `json:"hashline,omitempty" doc:"Prefix lines with LINE:HASH| (e.g. '42:a3|code') and file header [path#TAG] for safe editing with edit_file. Default true."`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why this file is being read"`
}

type EditFileInput struct {
	_           struct{}          `doc:"Apply atomic, anchor-verified hashline edits (replace, insert_after, insert_before, delete) to a file with ±30-line auto-relocation." emoji:"✏️"`
	Path        string            `json:"path" doc:"File path (may include #TAG from read_file, e.g. 'main.go#8f2a')"`
	Edits       []hashline.EditOp `json:"edits" doc:"List of hashline edit operations applied atomically"`
	Explanation string            `json:"explanation,omitempty" doc:"Brief explanation of the edit"`
}

type WriteFileInput struct {
	_           struct{} `doc:"Write or overwrite a file resolved relative to Kabibi's current shell working directory." emoji:"📝"`
	Path        string   `json:"path" doc:"File path to write (relative to current shell cwd or absolute)"`
	Content     string   `json:"content" doc:"The text content to write to the file"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why this file is being written"`
}

type ListFilesInput struct {
	_             struct{} `doc:"List or search files in the current working directory using doublestar globs ('**/*.go'), fuzzy filename matching, or content search." emoji:"📂"`
	Query         string   `json:"query,omitempty" doc:"Glob pattern (e.g. '**/*.go'), filename query, or concept query. Default '*'."`
	SearchContent bool     `json:"search_content,omitempty" doc:"If true, match query against file contents (exact -> semantic embeddings) instead of just filenames."`
	MaxResults    int      `json:"max_results,omitempty" doc:"Max entries to return (default 50)."`
	Explanation   string   `json:"explanation,omitempty" doc:"Brief explanation of what files are being listed"`
}

type ListDirInput struct {
	_           struct{} `doc:"List files and directories at a path resolved relative to Kabibi's current shell working directory." emoji:"📂"`
	Path        string   `json:"path,omitempty" doc:"Directory path to list (defaults to current shell working directory '.')"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why this directory is being listed"`
}

func resolveShellPath(cwd, target string) string {
	cleanTarget, _ := hashline.SplitPathAndTag(strings.TrimSpace(target))
	if cleanTarget == "" {
		return cwd
	}
	if filepath.IsAbs(cleanTarget) {
		return filepath.Clean(cleanTarget)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return filepath.Clean(filepath.Join(cwd, cleanTarget))
}

func displayRelPath(cwd, absPath string) string {
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, absPath); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(absPath)
}

func isImageFileExtension(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	default:
		return false
	}
}

func inspectImageMetadataLines(absPath string, rawBytes []byte) []string {
	var lines []string
	cfg, format, err := image.DecodeConfig(strings.NewReader(string(rawBytes)))
	if err == nil {
		lines = append(lines, fmt.Sprintf("[image: %s | format: %s | dimensions: %dx%d | size: %d bytes]",
			filepath.Base(absPath), format, cfg.Width, cfg.Height, len(rawBytes)))
	} else {
		lines = append(lines, fmt.Sprintf("[image: %s | size: %d bytes]", filepath.Base(absPath), len(rawBytes)))
	}
	return lines
}

func registerFileTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["read_file"] = ai.NewSessionTool(func(ctx context.Context, tc *ai.ToolContext, in ReadFileInput) (any, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, errors.New("path argument is required")
		}
		cwd := execSess.Dir()
		resolved := resolveShellPath(cwd, in.Path)
		relDisp := displayRelPath(cwd, resolved)

		var feedback ai.LogFeedback
		var embedder ai.PartEmbedder
		if tc != nil {
			feedback = tc.Feedback
			if tc.Session != nil {
				embedder = tc.Session.Embedder()
			}
		}
		if feedback != nil {
			feedback(ai.ToolLead(ReadFileInput{}, filepath.Base(resolved), 18))
		}

		info, err := os.Stat(resolved)
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}

		useHashline := true
		if in.Hashline != nil {
			useHashline = *in.Hashline
		}

		// Transparent Phase 1 TFLite OCR routing when read_file is pointed at .png, .jpg, .webp, or .gif
		if isImageFileExtension(resolved) {
			imgLines := ocr.ExtractImageLines(ctx, resolved, data, false)
			imgRaw := []byte(strings.Join(imgLines, "\n"))
			var imgRanges []hashline.Range
			note := "image OCR"
			if strings.TrimSpace(in.Search) != "" {
				res := fabric.SearchAndExpand(
					resolved,
					info.ModTime().UnixNano(),
					imgLines,
					in.Search,
					in.SearchMode,
					embedder,
					fabric.DefaultTotalBudget,
				)
				imgRanges = res.Ranges
				note = fabric.FormatMatchNote(len(imgLines), res, in.Search)
			}
			formatted := hashline.FormatHashlines(relDisp, imgRaw, imgRanges, note, useHashline)
			if feedback != nil {
				feedback("image ocr")
			}
			return map[string]any{
				"path":      resolved,
				"tag":       hashline.ComputeFileTag(data),
				"content":   formatted,
				"hashlines": formatted,
			}, nil
		}

		allLines := hashline.SplitLines(string(data))
		totalLines := len(allLines)
		tag := hashline.ComputeFileTag(data)

		var ranges []hashline.Range
		var summaryNote string
		var rawSlice string

		if strings.TrimSpace(in.Search) != "" {
			res := fabric.SearchAndExpand(
				resolved,
				info.ModTime().UnixNano(),
				allLines,
				in.Search,
				in.SearchMode,
				embedder,
				fabric.DefaultTotalBudget,
			)
			ranges = res.Ranges
			summaryNote = fabric.FormatMatchNote(totalLines, res, in.Search)
			var matchedText []string
			for _, r := range ranges {
				for ln := r.Start; ln <= r.End && ln <= totalLines; ln++ {
					matchedText = append(matchedText, allLines[ln-1])
				}
			}
			rawSlice = strings.Join(matchedText, "\n")
		} else if in.StartLine > 0 || in.EndLine > 0 {
			start := in.StartLine
			if start <= 0 {
				start = 1
			}
			end := in.EndLine
			if end <= 0 || end > totalLines {
				end = totalLines
			}
			if start <= totalLines && start <= end {
				ranges = []hashline.Range{{Start: start, End: end}}
				rawSlice = strings.Join(allLines[start-1:end], "\n")
				summaryNote = fmt.Sprintf("lines %d-%d of %d", start, end, totalLines)
			} else {
				summaryNote = fmt.Sprintf("empty range %d-%d of %d", start, end, totalLines)
			}
		} else {
			rawSlice = string(data)
			if totalLines > 0 {
				ranges = []hashline.Range{{Start: 1, End: totalLines}}
			}
			summaryNote = fmt.Sprintf("lines 1-%d", totalLines)
		}

		formatted := hashline.FormatHashlines(relDisp, data, ranges, summaryNote, useHashline)
		if feedback != nil {
			feedback(fmt.Sprintf("%d bytes read", len(data)))
		}

		// When explicitly requested with hashline=false or start_line/end_line without hashline flag,
		// return both `content` and `hashlines` so callers inspecting either field succeed.
		contentOut := formatted
		if in.Hashline != nil && !*in.Hashline {
			contentOut = rawSlice
		} else if (in.StartLine > 0 || in.EndLine > 0) && in.Hashline == nil && strings.TrimSpace(in.Search) == "" {
			contentOut = rawSlice
		}

		return map[string]any{
			"path":        resolved,
			"tag":         tag,
			"header":      fmt.Sprintf("[%s#%s]", relDisp, tag),
			"total_lines": totalLines,
			"content":     truncateToolOutput(contentOut),
			"hashlines":   truncateToolOutput(formatted),
		}, nil
	})

	tools["edit_file"] = ai.NewTool(func(ctx context.Context, in EditFileInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, errors.New("path argument is required")
		}
		cwd := execSess.Dir()
		resolved := resolveShellPath(cwd, in.Path)
		relDisp := displayRelPath(cwd, resolved)
		if feedback != nil {
			feedback(ai.ToolLead(EditFileInput{}, filepath.Base(resolved), 18))
		}
		res, err := hashline.ApplyHashlineEdits(resolved, relDisp, in.Edits)
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		if feedback != nil {
			feedback(fmt.Sprintf("%d hunk(s)", res.AppliedHunk))
		}
		return map[string]any{
			"path":            res.Path,
			"tag":             res.Tag,
			"header":          res.Header,
			"applied_hunks":   res.AppliedHunk,
			"relocated_hunks": res.Relocated,
			"snippet":         res.Snippet,
		}, nil
	})

	tools["write_file"] = ai.NewTool(func(ctx context.Context, in WriteFileInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, errors.New("path argument is required")
		}
		cwd := execSess.Dir()
		resolved := resolveShellPath(cwd, in.Path)
		relDisp := displayRelPath(cwd, resolved)
		if feedback != nil {
			feedback(ai.ToolLead(WriteFileInput{}, filepath.Base(resolved), 18))
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		raw := []byte(in.Content)
		if err := os.WriteFile(resolved, raw, 0o644); err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		if feedback != nil {
			feedback("ok")
		}
		tag := hashline.ComputeFileTag(raw)
		return map[string]any{
			"path":          resolved,
			"tag":           tag,
			"header":        fmt.Sprintf("[%s#%s]", relDisp, tag),
			"success":       true,
			"bytes_written": len(in.Content),
		}, nil
	})

	tools["list_files"] = ai.NewSessionTool(func(ctx context.Context, tc *ai.ToolContext, in ListFilesInput) (any, error) {
		q := strings.TrimSpace(in.Query)
		if q == "" {
			q = "*"
		}
		var feedback ai.LogFeedback
		var embedder ai.PartEmbedder
		if tc != nil {
			feedback = tc.Feedback
			if tc.Session != nil {
				embedder = tc.Session.Embedder()
			}
		}
		if feedback != nil {
			feedback(ai.ToolLead(ListFilesInput{}, q, 18))
		}
		table, err := filelist.ListFiles(execSess.Dir(), filelist.Options{
			Query:         q,
			SearchContent: in.SearchContent,
			MaxResults:    in.MaxResults,
			Embedder:      embedder,
		})
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		if feedback != nil {
			feedback("ok")
		}
		return table, nil
	})

	// Backward-compatible alias that also produces the compact list_files output
	tools["list_dir"] = ai.NewSessionTool(func(ctx context.Context, tc *ai.ToolContext, in ListDirInput) (any, error) {
		target := strings.TrimSpace(in.Path)
		if target == "" {
			target = "."
		}
		resolved := resolveShellPath(execSess.Dir(), target)
		var feedback ai.LogFeedback
		if tc != nil {
			feedback = tc.Feedback
		}
		if feedback != nil {
			feedback(ai.ToolLead(ListDirInput{}, filepath.Base(resolved), 18))
		}
		table, err := filelist.ListFiles(resolved, filelist.Options{Query: "*"})
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		if feedback != nil {
			feedback("ok")
		}
		return map[string]any{
			"path":    resolved,
			"listing": table,
		}, nil
	})
}

