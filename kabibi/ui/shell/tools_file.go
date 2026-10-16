package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ai"
)

type ReadFileInput struct {
	_           struct{} `doc:"Read the contents of a file resolved relative to Kabibi's current shell working directory." emoji:"📖"`
	Path        string   `json:"path" doc:"File path to read (relative to current shell cwd or absolute)"`
	StartLine   int      `json:"start_line,omitempty" doc:"Optional 1-based start line (inclusive)"`
	EndLine     int      `json:"end_line,omitempty" doc:"Optional 1-based end line (inclusive)"`
	Explanation string   `json:"explanation" doc:"Brief explanation of why this file is being read and what information is sought"`
}

type WriteFileInput struct {
	_           struct{} `doc:"Write or overwrite a file resolved relative to Kabibi's current shell working directory." emoji:"📝"`
	Path        string   `json:"path" doc:"File path to write (relative to current shell cwd or absolute)"`
	Content     string   `json:"content" doc:"The text content to write to the file"`
	Explanation string   `json:"explanation" doc:"Brief explanation of why this file is being written"`
}

type ListDirInput struct {
	_           struct{} `doc:"List files and directories at a path resolved relative to Kabibi's current shell working directory." emoji:"📂"`
	Path        string   `json:"path,omitempty" doc:"Directory path to list (defaults to current shell working directory '.')"`
	Explanation string   `json:"explanation" doc:"Brief explanation of why this directory is being listed"`
}

func resolveShellPath(cwd, target string) string {
	if target == "" {
		return cwd
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return filepath.Clean(filepath.Join(cwd, target))
}

func registerFileTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["read_file"] = ai.NewTool(func(ctx context.Context, in ReadFileInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, errors.New("path argument is required")
		}
		resolved := resolveShellPath(execSess.Dir(), in.Path)
		if feedback != nil {
			feedback(ai.ToolLead(ReadFileInput{}, filepath.Base(resolved), 18))
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		content := string(data)
		if in.StartLine > 0 || in.EndLine > 0 {
			lines := strings.Split(content, "\n")
			start := in.StartLine
			if start <= 0 {
				start = 1
			}
			end := in.EndLine
			if end <= 0 || end > len(lines) {
				end = len(lines)
			}
			if start > len(lines) {
				content = ""
			} else if start <= end {
				content = strings.Join(lines[start-1:end], "\n")
			}
		}
		if feedback != nil {
			feedback(fmt.Sprintf("%d bytes read", len(data)))
		}
		return map[string]any{
			"path":    resolved,
			"content": truncateToolOutput(content),
		}, nil
	})

	tools["write_file"] = ai.NewTool(func(ctx context.Context, in WriteFileInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, errors.New("path argument is required")
		}
		resolved := resolveShellPath(execSess.Dir(), in.Path)
		if feedback != nil {
			feedback(ai.ToolLead(WriteFileInput{}, filepath.Base(resolved), 18))
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		if err := os.WriteFile(resolved, []byte(in.Content), 0o644); err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		if feedback != nil {
			feedback("ok")
		}
		return map[string]any{
			"path":          resolved,
			"success":       true,
			"bytes_written": len(in.Content),
		}, nil
	})

	tools["list_dir"] = ai.NewTool(func(ctx context.Context, in ListDirInput, feedback ai.LogFeedback) (any, error) {
		target := in.Path
		if strings.TrimSpace(target) == "" {
			target = "."
		}
		resolved := resolveShellPath(execSess.Dir(), target)
		if feedback != nil {
			feedback(ai.ToolLead(ListDirInput{}, filepath.Base(resolved), 18))
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		type item struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
			Size  int64  `json:"size,omitempty"`
		}
		items := make([]item, 0, len(entries))
		for _, e := range entries {
			var sz int64
			if info, err := e.Info(); err == nil && !e.IsDir() {
				sz = info.Size()
			}
			items = append(items, item{
				Name:  e.Name(),
				IsDir: e.IsDir(),
				Size:  sz,
			})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].IsDir != items[j].IsDir {
				return items[i].IsDir
			}
			return items[i].Name < items[j].Name
		})
		if feedback != nil {
			feedback(fmt.Sprintf("%d entries", len(items)))
		}
		return map[string]any{
			"path":    resolved,
			"entries": items,
		}, nil
	})
}
