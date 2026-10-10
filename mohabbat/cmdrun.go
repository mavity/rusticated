package mohabbat

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// cmdOutputMu serialises the writing of a failed command's captured output so
// that, even if several buffered commands finish around the same time, each
// one's output block is printed contiguously rather than interleaved.
var cmdOutputMu sync.Mutex

// runCmd runs cmd and controls how its stdout/stderr reach the terminal.
//
//   - verbose: the command streams live to os.Stdout/os.Stderr, exactly as the
//     build pipeline behaved historically.
//   - not verbose (default): the command's output is captured into a buffer and
//     discarded on success. On failure the captured output is replayed under a
//     labelled header so the user sees precisely which step failed and its full
//     error context, without the noise of every successful build.
//
// label identifies the step for the failure header (e.g. "go build kabibi").
func runCmd(cmd *exec.Cmd, label string, verbose bool) error {
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil && buf.Len() > 0 {
		cmdOutputMu.Lock()
		fmt.Fprintf(os.Stderr, "🍆  --- output from %s ---\n", label)
		os.Stderr.Write(buf.Bytes())
		if b := buf.Bytes(); len(b) > 0 && b[len(b)-1] != '\n' {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintf(os.Stderr, "🍆  --- end output from %s ---\n", label)
		cmdOutputMu.Unlock()
	}
	return err
}

// vlogf prints a 🍆 progress line only in verbose mode. Successful builds are
// silent by default; warnings and errors should use fmt directly so they are
// always shown.
func vlogf(verbose bool, format string, a ...any) {
	if verbose {
		fmt.Printf(format, a...)
	}
}
