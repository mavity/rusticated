//go:build wasip1

package app

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRusticatedIndexEntryIncludesAvailableStatMetadata(t *testing.T) {
	root := t.TempDir()
	repo := initAppTestRepository(t, root)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open Git worktree: %v", err)
	}

	const name = "metadata.txt"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("Rusticated metadata\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat test file: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		t.Fatalf("FileInfo.Sys() = %T, want *syscall.Stat_t", info.Sys())
	}

	if _, err := worktree.Add(name); err != nil {
		t.Fatalf("stage test file: %v", err)
	}
	idx, err := repo.Storer.Index()
	if err != nil {
		t.Fatalf("read Git index: %v", err)
	}
	entry, err := idx.Entry(name)
	if err != nil {
		t.Fatalf("read Git index entry: %v", err)
	}

	var wantCreatedAt time.Time
	switch {
	case stat.ValidMask&syscall.STAT_VALID_CTIME != 0:
		wantCreatedAt = time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)
	case stat.ValidMask&syscall.STAT_VALID_BIRTHTIME != 0:
		wantCreatedAt = time.Unix(stat.Btim.Sec, stat.Btim.Nsec)
	case stat.ValidMask&syscall.STAT_VALID_MTIME != 0:
		wantCreatedAt = time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
	}
	if !entry.CreatedAt.Equal(wantCreatedAt) {
		t.Fatalf("index CreatedAt = %v, want %v from valid stat timestamps", entry.CreatedAt, wantCreatedAt)
	}

	tests := []struct {
		name  string
		valid bool
		got   uint32
		want  uint32
	}{
		{"Dev", stat.ValidMask&syscall.STAT_VALID_DEV != 0, entry.Dev, uint32(stat.Dev)},
		{"Inode", stat.ValidMask&syscall.STAT_VALID_INODE != 0, entry.Inode, uint32(stat.Ino)},
		{"UID", stat.ValidMask&syscall.STAT_VALID_UID != 0, entry.UID, stat.Uid},
		{"GID", stat.ValidMask&syscall.STAT_VALID_GID != 0, entry.GID, stat.Gid},
	}
	for _, test := range tests {
		want := uint32(0)
		if test.valid {
			want = test.want
		}
		if test.got != want {
			t.Errorf("index %s = %d, want %d (valid=%t)", test.name, test.got, want, test.valid)
		}
	}
}
