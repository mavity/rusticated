package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestURootTailLineAndByteCounts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "items.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
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
		{command: "tail -n 2 items.txt", want: "three\nfour\n"},
		{command: "tail -c 6 items.txt", want: "\nfour\n"},
		{command: "echo -e 'one\\ntwo\\nthree' | tail -n 2", want: "two\nthree\n"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			_, parsed := es.ClassifyInput(tt.command)
			rec, err := es.RunCommand(context.Background(), tt.command, parsed, SourceUserShell, nil)
			if err != nil || rec.ExitCode != 0 || rec.Output != tt.want {
				t.Fatalf("command %q (code=%d, err=%v) output = %q, want %q", tt.command, rec.ExitCode, err, rec.Output, tt.want)
			}
		})
	}

	_, parsed := es.ClassifyInput("timeout -t 100ms tail -f -n 1 items.txt")
	rec, err := es.RunCommand(context.Background(), "timeout -t 100ms tail -f -n 1 items.txt", parsed, SourceUserShell, nil)
	if rec.ExitCode != 124 || rec.Output != "four\n" {
		t.Fatalf("tail follow timeout (code=%d, err=%v) output = %q, want code 124 and %q", rec.ExitCode, err, rec.Output, "four\n")
	}
}
