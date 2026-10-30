package main

import (
	"reflect"
	"testing"
)

func TestNormalizeRunArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		testMode bool
		want     []string
	}{
		{
			name:     "strip separator and rejoin split test flags",
			args:     []string{"--", "-test", ".v", "-test", ".timeout=90s"},
			testMode: true,
			want:     []string{"-test.v", "-test.timeout=90s"},
		},
		{
			name: "preserve ordinary run arguments",
			args: []string{"--", "-c", "echo hello"},
			want: []string{"-c", "echo hello"},
		},
		{
			name:     "preserve intact test flags",
			args:     []string{"-test.v", "-test.timeout=90s"},
			testMode: true,
			want:     []string{"-test.v", "-test.timeout=90s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRunArgs(tt.args, tt.testMode); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalizeRunArgs(%q, %t) = %q, want %q", tt.args, tt.testMode, got, tt.want)
			}
		})
	}
}
