package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestURootHeadLineAndByteCounts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "items.txt"), []byte("banana\napple\ncherry\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		command string
		want    string
	}{
		{command: "head -n 2 items.txt", want: "banana\napple\n"},
		{command: "head -c 7 items.txt", want: "banana\n"},
		{command: "seq 1 5 | head -n 3", want: "1\n2\n3\n"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			_, parsed := es.ClassifyInput(tt.command)
			rec, err := es.RunCommand(context.Background(), tt.command, parsed, SourceUserShell, nil)
			if err != nil || rec.ExitCode != 0 || rec.Output != tt.want {
				t.Fatalf("command %q (code=%d, err=%v) output = %q, want %q", tt.command, rec.ExitCode, err, rec.Output, tt.want)
			}
		})
	}
}
