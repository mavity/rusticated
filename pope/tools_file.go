package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type ReadFileInput struct {
	_    struct{} `doc:"Read the contents of a local file at the specified path."`
	Path string   `doc:"The file path to read"`
}

func ReadFile(ctx context.Context, in ReadFileInput) (any, error) {
	if in.Path == "" {
		return nil, errors.New("path argument is required")
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"content": truncateOutput(string(data))}, nil
}

type WriteFileInput struct {
	_       struct{} `doc:"Write or overwrite content to a local file at the specified path."`
	Path    string   `doc:"The file path to write to"`
	Content string   `doc:"The text content to write"`
}

func WriteFile(ctx context.Context, in WriteFileInput) (any, error) {
	if in.Path == "" {
		return nil, errors.New("path argument is required")
	}
	if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(in.Path, []byte(in.Content), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "bytes_written": len(in.Content)}, nil
}
