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

	items := m.leftList.Items()
	if len(items) != 1 {
		t.Errorf("loadDir on empty directory produced %d items, want 1 (.. entry)", len(items))
	}

	if fi, ok := items[0].(fileItem); !ok || fi.name != ".." {
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

	items := m.leftList.Items()
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

	items := m.leftList.Items()
	if len(items) < 2 {
		t.Fatalf("loadDir produced too few items")
	}

	firstItem := items[0].(fileItem)
	if !firstItem.isDir {
		t.Errorf("first item is not a directory, got name=%q isDir=%v", firstItem.name, firstItem.isDir)
	}
}

func TestLoadDirFocusName(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "file2.txt"), []byte(""), 0o644)

	m := initialModel()
	m.loadDir(leftPane, dir, "file2.txt")

	selectedItem := m.leftList.SelectedItem()
	if selectedItem == nil {
		t.Fatalf("no selected item")
	}

	fi := selectedItem.(fileItem)
	if fi.name != "file2.txt" {
		t.Errorf("selected item = %q, want file2.txt", fi.name)
	}
}

func TestLoadDirFocusMiss(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(""), 0o644)

	m := initialModel()
	m.loadDir(leftPane, dir, "nonexistent.txt")

	selectedItem := m.leftList.SelectedItem()
	if selectedItem == nil {
		t.Fatalf("no selected item after focus miss")
	}

	fi := selectedItem.(fileItem)
	if fi.name == "nonexistent.txt" {
		t.Errorf("focus miss should not select the requested item")
	}
}

func TestLoadDirParentEntry(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte(""), 0o644)

	m := initialModel()
	m.loadDir(leftPane, dir, "")

	items := m.leftList.Items()
	if len(items) < 1 {
		t.Fatalf("loadDir produced no items")
	}

	firstItem := items[0].(fileItem)
	if firstItem.name != ".." {
		t.Errorf("first item should be '..' for non-root, got %q", firstItem.name)
	}
}

func TestLoadDirSetsTitle(t *testing.T) {
	dir := t.TempDir()

	m := initialModel()
	m.loadDir(leftPane, dir, "")

	if m.leftList.Title == "" {
		t.Errorf("loadDir did not set list title")
	}
}

func TestLoadDirRightPane(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte(""), 0o644)

	m := initialModel()
	m.loadDir(rightPane, dir, "")

	items := m.rightList.Items()
	if len(items) != 2 {
		t.Errorf("loadDir on rightPane produced %d items, want 2 (.. + file)", len(items))
	}

	if m.rightDir != dir {
		t.Errorf("loadDir on rightPane did not set rightDir")
	}
}
