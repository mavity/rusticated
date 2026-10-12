package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func getToolDeclarations() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read the contents of a local file at the specified path.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "The file path to read"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write_file",
				"description": "Write or overwrite content to a local file at the specified path.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "The file path to write to"},
						"content": map[string]any{"type": "string", "description": "The text content to write"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "exec",
				"description": "Execute a shell command on the host system and return its output.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string", "description": "The shell command to run"},
					},
					"required": []string{"command"},
				},
			},
		},
	}
}

func executeTool(name string, args map[string]any) map[string]any {
	switch name {
	case "read_file":
		path, _ := args["path"].(string)
		if path == "" {
			return map[string]any{"error": "path argument is required"}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		content := string(data)
		if len(content) > 32768 {
			content = content[:32768] + "\n...[truncated]"
		}
		return map[string]any{"content": content}

	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if path == "" {
			return map[string]any{"error": "path argument is required"}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return map[string]any{"error": err.Error()}
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"success": true, "bytes_written": len(content)}

	case "exec":
		cmdStr, _ := args["command"].(string)
		if cmdStr == "" {
			return map[string]any{"error": "command argument is required"}
		}
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd.exe", "/c", cmdStr)
		} else {
			cmd = exec.Command("sh", "-c", cmdStr)
		}
		out, err := cmd.CombinedOutput()
		outStr := string(out)
		if len(outStr) > 32768 {
			outStr = outStr[:32768] + "\n...[truncated]"
		}
		if err != nil {
			return map[string]any{"output": outStr, "error": err.Error()}
		}
		return map[string]any{"output": outStr}

	default:
		return map[string]any{"error": fmt.Sprintf("unknown tool: %s", name)}
	}
}
