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
	current     string
	done        int64
	total       int64
	fileDone    int64
	fileTotal   int64
	bytesPerSec int64
}

type fileOpDoneMsg struct {
	kind fileOpKind
	err  error
}

// fileOpCollisionMsg is emitted when a destination already exists and the op
// pauses for the user to decide how to proceed.
type fileOpCollisionMsg struct {
	path string
}

// collisionChoice is the user's answer to an overwrite prompt.
type collisionChoice int

const (
	colOverwrite collisionChoice = iota
	colSkip
	colOverwriteAll
	colSkipAll
	colCancel
)

// opSink carries progress accounting and collision resolution through the
// recursive copy/move/delete helpers.
type opSink struct {
	ctx    context.Context
	ch     chan<- tea.Msg
	resume <-chan collisionChoice

	batchDone, batchTotal int64
	fileDone, fileTotal   int64
	curName               string
	start                 time.Time
	last                  time.Time

	overwriteAll, skipAll bool
}

func (s *opSink) beginFile(name string, total int64) {
	s.curName = name
	s.fileDone = 0
	s.fileTotal = total
	s.emit(true)
}

func (s *opSink) add(n int64) {
	s.batchDone += n
	s.fileDone += n
	s.emit(false)
}

func (s *opSink) emit(force bool) {
	if !force && time.Since(s.last) < 40*time.Millisecond {
		return
	}
	s.last = time.Now()
	var rate int64
	if el := time.Since(s.start).Seconds(); el > 0 {
		rate = int64(float64(s.batchDone) / el)
	}
	select {
	case s.ch <- fileOpProgressMsg{
		current:     s.curName,
		done:        s.batchDone,
		total:       s.batchTotal,
		fileDone:    s.fileDone,
		fileTotal:   s.fileTotal,
		bytesPerSec: rate,
	}:
	case <-s.ctx.Done():
	}
}

// decide asks the UI how to handle an existing destination, honouring any
// remembered "apply to all" answer. It returns whether to proceed and an error
// when the whole operation should abort.
func (s *opSink) decide(dst string) (bool, error) {
	if s.overwriteAll {
		return true, nil
	}
	if s.skipAll {
		return false, nil
	}
	select {
	case s.ch <- fileOpCollisionMsg{path: dst}:
	case <-s.ctx.Done():
		return false, s.ctx.Err()
	}
	select {
	case c := <-s.resume:
		switch c {
		case colOverwrite:
			return true, nil
		case colSkip:
			return false, nil
		case colOverwriteAll:
			s.overwriteAll = true
			return true, nil
		case colSkipAll:
			s.skipAll = true
			return false, nil
		default:
			return false, context.Canceled
		}
	case <-s.ctx.Done():
		return false, s.ctx.Err()
	}
}

// startFileOp launches an operation in a goroutine and returns a command that
// waits for the first progress/done message on the op channel.
func (m *model) startFileOp(op fileOp) tea.Cmd {
	ch := make(chan tea.Msg, 128)
	resume := make(chan collisionChoice, 1)
	ctx, cancel := context.WithCancel(context.Background())
	m.opChan = ch
	m.opCancel = cancel
	m.opResume = resume
	m.opActive = true
	m.opCollision = false
	m.opKind = op.kind
	m.opCurrent = ""
	m.opDone = 0
	m.opTotal = 0
	m.opFileDone = 0
	m.opFileTotal = 0
	m.opRate = 0
	go runFileOp(ctx, op, ch, resume)
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
func runFileOp(ctx context.Context, op fileOp, ch chan<- tea.Msg, resume <-chan collisionChoice) {
	defer close(ch)

	total := int64(0)
	for _, src := range op.sources {
		total += pathSize(src)
	}

	now := time.Now()
	s := &opSink{
		ctx:        ctx,
		ch:         ch,
		resume:     resume,
		batchTotal: total,
		start:      now,
		last:       now,
	}
	s.emit(true)

	var err error
	for _, src := range op.sources {
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		switch op.kind {
		case opCopy:
			err = copyPath(s, src, filepath.Join(op.dest, filepath.Base(src)))
		case opMove:
			err = movePath(s, src, filepath.Join(op.dest, filepath.Base(src)))
		case opDelete:
			err = deletePath(s, src)
		}
		if err != nil {
			break
		}
	}

	if err == nil {
		s.batchDone = total
		s.emit(true)
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

func copyPath(s *opSink, src, dst string) error {
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
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			if err := copyPath(s, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if pathExists(dst) {
		proceed, err := s.decide(dst)
		if err != nil {
			return err
		}
		if !proceed {
			s.add(info.Size()) // count skipped bytes so the batch bar still completes
			return nil
		}
	}
	return copyFileContents(s, src, dst, info)
}

func copyFileContents(s *opSink, src, dst string, info os.FileInfo) error {
	s.beginFile(dst, info.Size())
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
		if s.ctx.Err() != nil {
			out.Close()
			return s.ctx.Err()
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			s.add(int64(n))
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

func movePath(s *opSink, src, dst string) error {
	if pathExists(dst) {
		proceed, err := s.decide(dst)
		if err != nil {
			return err
		}
		if !proceed {
			s.add(pathSize(src))
			return nil
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	}
	// Fast path: a plain rename works within the same volume.
	if err := os.Rename(src, dst); err == nil {
		s.beginFile(dst, pathSize(dst))
		s.add(pathSize(dst))
		return nil
	}
	// Fallback for cross-device moves or when the fast path is refused:
	// copy the tree, then remove the source only if the copy succeeded.
	if err := copyPath(s, src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func deletePath(s *opSink, src string) error {
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
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			if err := deletePath(s, filepath.Join(src, e.Name())); err != nil {
				return err
			}
		}
		if err := os.Remove(src); err != nil {
			return err
		}
		s.add(0)
		return nil
	}
	if err := os.Remove(src); err != nil {
		return err
	}
	s.beginFile(src, info.Size())
	s.add(info.Size())
	return nil
}

// pathExists reports whether a path exists on disk.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
