package main

import (
	"os"
	"runtime"
	"testing"
)

func TestLookupEnvValue(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		key   string
		want  string
		found bool
	}{
		{
			name:  "exact match in env",
			env:   map[string]string{"PATH": "/usr/bin"},
			key:   "PATH",
			want:  "/usr/bin",
			found: true,
		},
		{
			name:  "case-insensitive match",
			env:   map[string]string{"Path": "/usr/bin"},
			key:   "PATH",
			want:  "/usr/bin",
			found: true,
		},
		{
			name:  "missing key in env, but present in os.Environ",
			env:   map[string]string{"OTHER": "value"},
			key:   "NONEXISTENT_KEY_FOR_TEST_12345",
			want:  "",
			found: false,
		},
		{
			name:  "nil env falls back to os.Environ",
			env:   nil,
			key:   "PATH",
			want:  os.Getenv("PATH"),
			found: len(os.Getenv("PATH")) > 0,
		},
		{
			name:  "empty env map",
			env:   map[string]string{},
			key:   "NONEXISTENT_VAR_12345",
			want:  "",
			found: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := lookupEnvValue(tt.env, tt.key)
			if found != tt.found {
				t.Errorf("lookupEnvValue(..., %q) found=%v, want %v", tt.key, found, tt.found)
			}
			if found && got != tt.want {
				t.Errorf("lookupEnvValue(..., %q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestExpandWindowsEnvVars(t *testing.T) {
	tests := []struct {
		name  string
		value string
		env   map[string]string
		want  string
	}{
		{
			name:  "no variables",
			value: "C:\\Program Files",
			env:   map[string]string{},
			want:  "C:\\Program Files",
		},
		{
			name:  "single variable",
			value: "%USERPROFILE%\\Desktop",
			env:   map[string]string{"USERPROFILE": "C:\\Users\\test"},
			want:  "C:\\Users\\test\\Desktop",
		},
		{
			name:  "undefined variable left unchanged",
			value: "%UNDEFINED%\\path",
			env:   map[string]string{},
			want:  "%UNDEFINED%\\path",
		},
		{
			name:  "case-insensitive expansion",
			value: "%tools%\\git",
			env:   map[string]string{"TOOLS": "C:\\tools"},
			want:  "C:\\tools\\git",
		},
		{
			name:  "empty string unchanged",
			value: "",
			env:   map[string]string{},
			want:  "",
		},
		{
			name:  "nested expansion",
			value: "%A%\\%B%",
			env:   map[string]string{"A": "X", "B": "Y"},
			want:  "X\\Y",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandWindowsEnvVars(tt.value, tt.env)
			if got != tt.want {
				t.Errorf("expandWindowsEnvVars(%q, ...) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestIsWindowsLikeEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "empty env uses runtime.GOOS",
			env:  map[string]string{},
			want: runtime.GOOS == "windows",
		},
		{
			name: "explicit GOOS=windows",
			env:  map[string]string{"GOOS": "windows"},
			want: true,
		},
		{
			name: "on windows, env override doesn't matter (runtime.GOOS checked first)",
			env:  map[string]string{"GOOS": "linux"},
			want: runtime.GOOS == "windows", // Will be true on Windows
		},
		{
			name: "case-insensitive GOOS",
			env:  map[string]string{"GOOS": "Windows"},
			want: true,
		},
		{
			name: "GOHOSTOS override",
			env:  map[string]string{"GOHOSTOS": "windows"},
			want: true,
		},
		{
			name: "OS fallback (legacy)",
			env:  map[string]string{"OS": "Windows_NT"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWindowsLikeEnv(tt.env)
			if got != tt.want {
				t.Errorf("isWindowsLikeEnv(...) = %v, want %v", got, tt.want)
			}
		})
	}
}
