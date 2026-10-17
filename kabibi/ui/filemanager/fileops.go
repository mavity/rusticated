package filemanager

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"
)

type FileOpKind int

const (
	OpCopy FileOpKind = iota
	OpMove
	OpDelete

	opCopy   = OpCopy
	opMove   = OpMove
	opDelete = OpDelete
)

type fileOpKind = FileOpKind

func (k FileOpKind) Verb() string {
	switch k {
	case OpCopy:
		return "Copying"
	case OpMove:
		return "Moving"
	case OpDelete:
		return "Deleting"
	}
	return "Working"
}

// FileOp describes a requested filesystem operation.
type FileOp struct {
	Kind    FileOpKind
	Sources []string // absolute source paths
	Dest    string   // destination directory (copy/move only)
}

type fileOp = FileOp

type FileOpProgress struct {
	Current     string
	Done        int64
	Total       int64
	FileDone    int64
	FileTotal   int64
	BytesPerSec int64
}

type FileOpDone struct {
	Kind FileOpKind
	Err  error
}

// CollisionChoice is the user's answer to an overwrite prompt.
type CollisionChoice int

const (
	ColOverwrite CollisionChoice = iota
	ColSkip
	ColOverwriteAll
	ColSkipAll
	ColCancel

	colOverwrite    = ColOverwrite
	colSkip         = ColSkip
	colOverwriteAll = ColOverwriteAll
	colSkipAll      = ColSkipAll
	colCancel       = ColCancel
)

type collisionChoice = CollisionChoice

// opSink carries progress accounting and collision resolution through the
// recursive copy/move/delete helpers.
type opSink struct {
	ctx        context.Context
	onProgress func(FileOpProgress)
	onCollide  func(path string)
	resume     <-chan CollisionChoice

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
	if s.onProgress != nil {
		s.onProgress(FileOpProgress{
			Current:     s.curName,
			Done:        s.batchDone,
			Total:       s.batchTotal,
			FileDone:    s.fileDone,
			FileTotal:   s.fileTotal,
			BytesPerSec: rate,
		})
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
	if s.onCollide == nil {
		return true, nil
	}
	s.onCollide(dst)
	select {
	case c := <-s.resume:
		switch c {
		case ColOverwrite:
			return true, nil
		case ColSkip:
			return false, nil
		case ColOverwriteAll:
			s.overwriteAll = true
			return true, nil
		case ColSkipAll:
			s.skipAll = true
			return false, nil
		default:
			return false, context.Canceled
		}
	case <-s.ctx.Done():
		return false, s.ctx.Err()
	}
}

// RunFileOp performs the operation synchronously (or in a caller goroutine),
// invoking onProgress, onCollide, and returning any error.
func RunFileOp(
	ctx context.Context,
	op FileOp,
	onProgress func(FileOpProgress),
	onCollide func(path string),
	resume <-chan CollisionChoice,
) error {
	total := int64(0)
	for _, src := range op.Sources {
		total += pathSize(src)
	}

	now := time.Now()
	s := &opSink{
		ctx:        ctx,
		onProgress: onProgress,
		onCollide:  onCollide,
		resume:     resume,
		batchTotal: total,
		start:      now,
		last:       now,
	}
	s.emit(true)

	var err error
	for _, src := range op.Sources {
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		switch op.Kind {
		case OpCopy:
			err = copyPath(s, src, filepath.Join(op.Dest, filepath.Base(src)))
		case OpMove:
			err = movePath(s, src, filepath.Join(op.Dest, filepath.Base(src)))
		case OpDelete:
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
	return err
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

// PathExists reports whether a path exists on disk.
func PathExists(path string) bool {
	return pathExists(path)
}
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
