package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type ReadFileInput struct {
	_           struct{} `doc:"Read the contents of a local file at the specified path." emoji:"📖"`
	Path        string   `doc:"The file path to read"`
	Explanation string   `doc:"Brief explanation of why this file is being read and what information is sought"`
}

func ReadFile(ctx context.Context, in ReadFileInput, feedback LogFeedback) (any, error) {
	if in.Path == "" {
		return nil, errors.New("path argument is required")
	}
	if feedback != nil {
		feedback(toolLead(ReadFileInput{}, filepath.Base(in.Path), 15))
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		if feedback != nil {
			feedback("failed")
		}
		return nil, err
	}
	if feedback != nil {
		feedback(fmt.Sprintf("%d bytes read", len(data)))
	}
	return map[string]any{"content": truncateOutput(string(data))}, nil
}

type WriteFileInput struct {
	_           struct{} `doc:"Write or overwrite content to a local file at the specified path." emoji:"📝"`
	Path        string   `doc:"The file path to write to"`
	Content     string   `doc:"The text content to write"`
	Explanation string   `doc:"Brief explanation of why this file is being written"`
}

func WriteFile(ctx context.Context, in WriteFileInput, feedback LogFeedback) (any, error) {
	if in.Path == "" {
		return nil, errors.New("path argument is required")
	}
	if feedback != nil {
		feedback(toolLead(WriteFileInput{}, filepath.Base(in.Path), 15))
	}
	if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
		if feedback != nil {
			feedback("failed")
		}
		return nil, err
	}
	if err := os.WriteFile(in.Path, []byte(in.Content), 0o644); err != nil {
		if feedback != nil {
			feedback("failed")
		}
		return nil, err
	}
	if feedback != nil {
		feedback("ok")
	}
	return map[string]any{"success": true, "bytes_written": len(in.Content)}, nil
}
