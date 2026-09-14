package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirEmpty(t *testing.T) {
	dir := t.TempDir()

	m := initialModel()
	m.loadDir(leftPane, dir, "")

	items := m.dualPane.Left.Items()
	if len(items) != 1 {
		t.Errorf("loadDir on empty directory produced %d items, want 1 (.. entry)", len(items))
	}

	if items[0].name != ".." {
		t.Errorf("first item should be .., got %v", items[0])
	}
}

func TestLoadDirWithFiles(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	m := initialModel()
	m.loadDir(leftPane, dir, "")

	items := m.dualPane.Left.Items()
	if len(items) != 3 {
		t.Errorf("loadDir with 2 files produced %d items, want 3 (.. + 2 files)", len(items))
	}
}

func TestLoadDirDirsSortedFirst(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "z_file.txt"), []byte(""), 0o644)
	os.Mkdir(filepath.Join(dir, "a_dir"), 0o755)

	m := initialModel()
	m.loadDir(leftPane, dir, "")

	items := m.dualPane.Left.Items()
	if len(items) < 2 {
		t.Fatalf("loadDir produced too few items")
	}

	if !items[0].isDir {
		t.Errorf("first item is not a directory, got name=%q isDir=%v", items[0].name, items[0].isDir)
	}
}

func TestLoadDirFocusName(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "file2.txt"), []byte(""), 0o644)

	m := initialModel()
	m.loadDir(leftPane, dir, "file2.txt")

	fi, ok := m.dualPane.Left.SelectedItem()
	if !ok {
		t.Fatalf("no selected item")
	}

	if fi.name != "file2.txt" {
		t.Errorf("selected item = %q, want file2.txt", fi.name)
	}
}

func TestLoadDirFocusMiss(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(""), 0o644)

	m := initialModel()
	m.loadDir(leftPane, dir, "nonexistent.txt")

	fi, ok := m.dualPane.Left.SelectedItem()
	if !ok {
		t.Fatalf("no selected item after focus miss")
	}

	if fi.name == "nonexistent.txt" {
		t.Errorf("focus miss should not select the requested item")
	}
}

func TestLoadDirParentEntry(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte(""), 0o644)

	m := initialModel()
	m.loadDir(leftPane, dir, "")

	items := m.dualPane.Left.Items()
	if len(items) < 1 {
		t.Fatalf("loadDir produced no items")
	}

	if items[0].name != ".." {
		t.Errorf("first item should be '..' for non-root, got %q", items[0].name)
	}
}

func TestLoadDirSetsTitle(t *testing.T) {
	dir := t.TempDir()

	m := initialModel()
	m.loadDir(leftPane, dir, "")

	if m.dualPane.Left.Title == "" {
		t.Errorf("loadDir did not set widget title")
	}
}

func TestLoadDirRightPane(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte(""), 0o644)

	m := initialModel()
	m.loadDir(rightPane, dir, "")

	items := m.dualPane.Right.Items()
	if len(items) != 2 {
		t.Errorf("loadDir on rightPane produced %d items, want 2 (.. + file)", len(items))
	}

	if m.dualPane.Right.Dir() != dir {
		t.Errorf("loadDir on rightPane did not update widget dir")
	}
}
