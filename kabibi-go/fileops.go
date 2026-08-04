package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type fileOpKind int

const (
	opCopy fileOpKind = iota
	opMove
	opDelete
)

func (k fileOpKind) verb() string {
	switch k {
	case opCopy:
		return "Copying"
	case opMove:
		return "Moving"
	case opDelete:
		return "Deleting"
	}
	return "Working"
}

// fileOp describes a requested filesystem operation.
type fileOp struct {
	kind    fileOpKind
	sources []string // absolute source paths
	dest    string   // destination directory (copy/move only)
}

type fileOpProgressMsg struct {
	current string
	done    int64
	total   int64
}

type fileOpDoneMsg struct {
	kind fileOpKind
	err  error
}

// startFileOp launches an operation in a goroutine and returns a command that
// waits for the first progress/done message on the op channel.
func (m *model) startFileOp(op fileOp) tea.Cmd {
	ch := make(chan tea.Msg, 128)
	ctx, cancel := context.WithCancel(context.Background())
	m.opChan = ch
	m.opCancel = cancel
	m.opActive = true
	m.opKind = op.kind
	m.opCurrent = ""
	m.opDone = 0
	m.opTotal = 0
	go runFileOp(ctx, op, ch)
	return m.watchFileOpCmd()
}

func (m *model) watchFileOpCmd() tea.Cmd {
	ch := m.opChan
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return fileOpDoneMsg{}
		}
		return msg
	}
}

// runFileOp performs the operation, emitting throttled progress and a final
// done message, then closes the channel.
func runFileOp(ctx context.Context, op fileOp, ch chan<- tea.Msg) {
	defer close(ch)

	total := int64(0)
	for _, src := range op.sources {
		total += pathSize(src)
	}

	var done int64
	last := time.Now()
	emit := func(current string, force bool) {
		if force || time.Since(last) > 40*time.Millisecond {
			last = time.Now()
			select {
			case ch <- fileOpProgressMsg{current: current, done: done, total: total}:
			case <-ctx.Done():
			}
		}
	}
	emit("", true)

	report := func(current string, n int64) {
		done += n
		emit(current, false)
	}

	var err error
	for _, src := range op.sources {
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		switch op.kind {
		case opCopy:
			err = copyPath(ctx, src, filepath.Join(op.dest, filepath.Base(src)), report)
		case opMove:
			err = movePath(ctx, src, filepath.Join(op.dest, filepath.Base(src)), report)
		case opDelete:
			err = deletePath(ctx, src, report)
		}
		if err != nil {
			break
		}
	}

	if err == nil {
		done = total
		emit("", true)
	}
	select {
	case ch <- fileOpDoneMsg{kind: op.kind, err: err}:
	case <-ctx.Done():
	}
}

// pathSize returns the total byte size of a file or directory tree.
func pathSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if total == 0 {
		total = 1 // avoid divide-by-zero and give empty dirs a tick
	}
	return total
}

func copyPath(ctx context.Context, src, dst string, report func(string, int64)) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := copyPath(ctx, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), report); err != nil {
				return err
			}
		}
		return nil
	}
	return copyFileContents(ctx, src, dst, info, report)
}

func copyFileContents(ctx context.Context, src, dst string, info os.FileInfo, report func(string, int64)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}

	buf := make([]byte, 256*1024)
	for {
		if ctx.Err() != nil {
			out.Close()
			return ctx.Err()
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			report(src, int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	return out.Close()
}

func movePath(ctx context.Context, src, dst string, report func(string, int64)) error {
	// Fast path: a plain rename works within the same volume.
	if err := os.Rename(src, dst); err == nil {
		report(src, pathSize(dst))
		return nil
	}
	// Fallback for cross-device moves or when the fast path is refused:
	// copy the tree, then remove the source only if the copy succeeded.
	if err := copyPath(ctx, src, dst, report); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func deletePath(ctx context.Context, src string, report func(string, int64)) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := deletePath(ctx, filepath.Join(src, e.Name()), report); err != nil {
				return err
			}
		}
		if err := os.Remove(src); err != nil {
			return err
		}
		report(src, 0)
		return nil
	}
	if err := os.Remove(src); err != nil {
		return err
	}
	report(src, info.Size())
	return nil
}

// pathExists reports whether a path exists on disk.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
