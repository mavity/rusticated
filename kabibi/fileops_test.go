package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPathSizeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	got := pathSize(path)
	if got != int64(len(content)) {
		t.Errorf("pathSize(%q) = %d, want %d", path, got, len(content))
	}
}

func TestPathSizeEmptyDir(t *testing.T) {
	dir := t.TempDir()

	got := pathSize(dir)
	if got != 1 {
		t.Errorf("pathSize on empty dir = %d, want 1 (minimum)", got)
	}
}

func TestPathSizeDirWithFiles(t *testing.T) {
	dir := t.TempDir()

	file1 := filepath.Join(dir, "file1.txt")
	file2 := filepath.Join(dir, "file2.txt")

	os.WriteFile(file1, []byte("hello"), 0o644)
	os.WriteFile(file2, []byte("world!"), 0o644)

	got := pathSize(dir)
	if got != 11 {
		t.Errorf("pathSize on dir with files = %d, want 11", got)
	}
}

func TestPathSizeNestedDir(t *testing.T) {
	dir := t.TempDir()

	subdir := filepath.Join(dir, "subdir")
	os.Mkdir(subdir, 0o755)

	os.WriteFile(filepath.Join(dir, "root.txt"), []byte("root"), 0o644)
	os.WriteFile(filepath.Join(subdir, "sub.txt"), []byte("sub"), 0o644)

	got := pathSize(dir)
	if got != 7 {
		t.Errorf("pathSize on nested dir = %d, want 7", got)
	}
}

func TestPathExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if pathExists(path) {
		t.Errorf("pathExists on non-existent path returned true")
	}

	os.WriteFile(path, []byte("content"), 0o644)

	if !pathExists(path) {
		t.Errorf("pathExists on existing file returned false")
	}
}

func TestCopyFilePath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "dest.txt")

	content := []byte("hello world")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	ch := make(chan tea.Msg, 10)
	sink := &opSink{
		ctx:        context.Background(),
		ch:         ch,
		resume:     make(chan collisionChoice),
		batchTotal: pathSize(src),
	}

	if err := copyPath(sink, src, dst); err != nil {
		t.Fatalf("copyPath error: %v", err)
	}

	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	if string(b) != string(content) {
		t.Errorf("copied file content = %q, want %q", string(b), string(content))
	}
}

func TestCopyDirectory(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	dstDir := filepath.Join(dir, "dst")

	os.Mkdir(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0o644)

	ch := make(chan tea.Msg, 10)
	sink := &opSink{
		ctx:        context.Background(),
		ch:         ch,
		resume:     make(chan collisionChoice),
		batchTotal: pathSize(srcDir),
	}

	if err := copyPath(sink, srcDir, dstDir); err != nil {
		t.Fatalf("copyPath error: %v", err)
	}

	if !pathExists(filepath.Join(dstDir, "file.txt")) {
		t.Errorf("copied directory does not contain file.txt")
	}
}

func TestDeleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	os.WriteFile(path, []byte("content"), 0o644)

	ch := make(chan tea.Msg, 10)
	sink := &opSink{
		ctx:    context.Background(),
		ch:     ch,
		resume: make(chan collisionChoice),
	}

	if err := deletePath(sink, path); err != nil {
		t.Fatalf("deletePath error: %v", err)
	}

	if pathExists(path) {
		t.Errorf("deletePath did not remove file")
	}
}

func TestDeleteDirectory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")

	os.Mkdir(subdir, 0o755)
	os.WriteFile(filepath.Join(subdir, "file.txt"), []byte("content"), 0o644)

	ch := make(chan tea.Msg, 10)
	sink := &opSink{
		ctx:    context.Background(),
		ch:     ch,
		resume: make(chan collisionChoice),
	}

	if err := deletePath(sink, subdir); err != nil {
		t.Fatalf("deletePath error: %v", err)
	}

	if pathExists(subdir) {
		t.Errorf("deletePath did not remove directory tree")
	}
}

func TestMovePath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "dest.txt")

	content := []byte("hello")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	ch := make(chan tea.Msg, 10)
	sink := &opSink{
		ctx:    context.Background(),
		ch:     ch,
		resume: make(chan collisionChoice),
	}

	if err := movePath(sink, src, dst); err != nil {
		t.Fatalf("movePath error: %v", err)
	}

	if pathExists(src) {
		t.Errorf("movePath did not remove source")
	}

	if !pathExists(dst) {
		t.Errorf("movePath did not create destination")
	}

	b, _ := os.ReadFile(dst)
	if string(b) != string(content) {
		t.Errorf("moved file content = %q, want %q", string(b), string(content))
	}
}
