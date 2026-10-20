package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type mockFileInfo struct {
	os.FileInfo
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m mockFileInfo) ModTime() time.Time { return m.modTime }
func (m mockFileInfo) IsDir() bool        { return m.mode.IsDir() }
func (m mockFileInfo) Sys() any           { return nil }

func TestCreateAbiStat(t *testing.T) {
	now := time.Now()
	epoch := time.Unix(0, 0)
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		fi       mockFileInfo
		wantKind uint32
		wantMode uint32
		wantSize uint64
		wantTime int64
	}{
		{
			name: "regular file",
			fi: mockFileInfo{
				name:    "test.txt",
				size:    1234,
				mode:    0644,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0644 | 0o100000,
			wantSize: 1234,
			wantTime: now.UnixNano(),
		},
		{
			name: "directory",
			fi: mockFileInfo{
				name:    "docs",
				size:    4096,
				mode:    os.ModeDir | 0755,
				modTime: now,
			},
			wantKind: statKindDir,
			wantMode: 0755 | 0o040000,
			wantSize: 4096,
			wantTime: now.UnixNano(),
		},
		{
			name: "symlink",
			fi: mockFileInfo{
				name:    "link",
				size:    10,
				mode:    os.ModeSymlink | 0777,
				modTime: now,
			},
			wantKind: statKindSymlink,
			wantMode: 0777 | 0o120000,
			wantSize: 10,
			wantTime: now.UnixNano(),
		},
		{
			name: "empty file",
			fi: mockFileInfo{
				name:    "empty",
				size:    0,
				mode:    0600,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0600 | 0o100000,
			wantSize: 0,
			wantTime: now.UnixNano(),
		},
		{
			name: "large file",
			fi: mockFileInfo{
				name:    "big",
				size:    1 << 40, // 1TB
				mode:    0644,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0644 | 0o100000,
			wantSize: 1 << 40,
			wantTime: now.UnixNano(),
		},
		{
			name: "no permissions",
			fi: mockFileInfo{
				name:    "locked",
				size:    1,
				mode:    0000,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0000 | 0o100000,
			wantSize: 1,
			wantTime: now.UnixNano(),
		},
		{
			name: "full permissions",
			fi: mockFileInfo{
				name:    "open",
				size:    1,
				mode:    0777,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0777 | 0o100000,
			wantSize: 1,
			wantTime: now.UnixNano(),
		},
		{
			name: "read only",
			fi: mockFileInfo{
				name:    "readonly",
				size:    1,
				mode:    0444,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0444 | 0o100000,
			wantSize: 1,
			wantTime: now.UnixNano(),
		},
		{
			name: "execute only",
			fi: mockFileInfo{
				name:    "run",
				size:    1,
				mode:    0111,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0111 | 0o100000,
			wantSize: 1,
			wantTime: now.UnixNano(),
		},
		{
			name: "epoch time",
			fi: mockFileInfo{
				name:    "old",
				size:    1,
				mode:    0644,
				modTime: epoch,
			},
			wantKind: statKindFile,
			wantMode: 0644 | 0o100000,
			wantSize: 1,
			wantTime: 0,
		},
		{
			name: "future time",
			fi: mockFileInfo{
				name:    "future",
				size:    1,
				mode:    0644,
				modTime: future,
			},
			wantKind: statKindFile,
			wantMode: 0644 | 0o100000,
			wantSize: 1,
			wantTime: future.UnixNano(),
		},
		{
			name: "dir with perm 700",
			fi: mockFileInfo{
				name:    "private",
				size:    4096,
				mode:    os.ModeDir | 0700,
				modTime: now,
			},
			wantKind: statKindDir,
			wantMode: 0700 | 0o040000,
			wantSize: 4096,
			wantTime: now.UnixNano(),
		},
		{
			name: "preserves setuid bit",
			fi: mockFileInfo{
				name:    "setuid",
				size:    1024,
				mode:    os.ModeSetuid | 0755,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0o100000 | 0o4000 | 0755,
			wantSize: 1024,
			wantTime: now.UnixNano(),
		},
		{
			name: "max file size",
			fi: mockFileInfo{
				name:    "max",
				size:    9223372036854775807,
				mode:    0644,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0644 | 0o100000,
			wantSize: 9223372036854775807,
			wantTime: now.UnixNano(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stat := createAbiStat(tt.fi)
			if stat.Kind != tt.wantKind {
				t.Errorf("Kind = %v, want %v", stat.Kind, tt.wantKind)
			}
			if stat.Mode != tt.wantMode {
				t.Errorf("Mode = %o, want %o", stat.Mode, tt.wantMode)
			}
			if stat.Size != tt.wantSize {
				t.Errorf("Size = %v, want %v", stat.Size, tt.wantSize)
			}
			if stat.ModifiedNs != tt.wantTime {
				t.Errorf("ModifiedNs = %v, want %v", stat.ModifiedNs, tt.wantTime)
			}
			if stat.ValidMask&statValidMtime == 0 {
				t.Errorf("modification time should be marked valid")
			}
			if stat.ValidMask&statValidAtime != 0 || stat.ValidMask&statValidBirthtime != 0 {
				t.Errorf("unavailable timestamps should not be marked valid")
			}
		})
	}
}

func TestCreateAbiStatUsesNativeMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native-stat")
	if err := os.WriteFile(path, []byte("file metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := createAbiStatAt(fi, path, false)
	if stat.ValidMask&statValidMtime == 0 || stat.ModifiedNs != fi.ModTime().UnixNano() {
		t.Fatalf("mtime missing or incorrect: %+v", stat)
	}

	switch runtime.GOOS {
	case "linux", "darwin":
		required := statValidUID | statValidGID | statValidAtime | statValidCtime |
			statValidNlink | statValidInode | statValidDev | statValidRdev |
			statValidBlksize | statValidBlocks
		if stat.ValidMask&required != required {
			t.Fatalf("Unix host omitted native stat fields: valid mask %#x, want %#x", stat.ValidMask, required)
		}
	case "windows":
		required := statValidAtime | statValidBirthtime | statValidNlink |
			statValidInode | statValidDev
		if stat.ValidMask&required != required {
			t.Fatalf("Windows host omitted available stat fields: valid mask %#x, want %#x", stat.ValidMask, required)
		}
	}
}

func TestStatModeAndKindPreservesUnixTypeAndSpecialBits(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode os.FileMode
		want uint32
		kind uint32
	}{
		{"setuid file", os.ModeSetuid | 0o755, 0o104755, statKindFile},
		{"setgid dir", os.ModeDir | os.ModeSetgid | 0o750, 0o42750, statKindDir},
		{"sticky dir", os.ModeDir | os.ModeSticky | 0o777, 0o041777, statKindDir},
		{"symlink", os.ModeSymlink | 0o777, 0o120777, statKindSymlink},
		{"socket", os.ModeSocket | 0o600, 0o140600, statKindSocket},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mode, kind := statModeAndKind(tt.mode)
			if mode != tt.want || kind != tt.kind {
				t.Fatalf("statModeAndKind() = (%#o, %d), want (%#o, %d)", mode, kind, tt.want, tt.kind)
			}
		})
	}
}

func TestMarshalAbiStat(t *testing.T) {
	stat := AbiStat{
		Kind: statKindFile, Mode: 0o104755, Uid: 12, Gid: 34, Size: 1234,
		ModifiedNs: -1_000_000_002, AccessedNs: 2, ChangedNs: 3,
		CreatedNs: 4, Nlink: 5, Inode: 6, Dev: 7, Rdev: 8,
		Blksize: 4096, Blocks: 9, ValidMask: 0xabc,
	}
	buf := marshalAbiStat(stat)
	if len(buf) != 112 {
		t.Fatalf("len(buf) = %v, want 112", len(buf))
	}
	checks := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"kind", uint64(binary.LittleEndian.Uint32(buf[0:4])), uint64(stat.Kind)},
		{"mode", uint64(binary.LittleEndian.Uint32(buf[4:8])), uint64(stat.Mode)},
		{"uid", uint64(binary.LittleEndian.Uint32(buf[8:12])), uint64(stat.Uid)},
		{"gid", uint64(binary.LittleEndian.Uint32(buf[12:16])), uint64(stat.Gid)},
		{"size", binary.LittleEndian.Uint64(buf[16:24]), stat.Size},
		{"mtime", binary.LittleEndian.Uint64(buf[24:32]), uint64(stat.ModifiedNs)},
		{"atime", binary.LittleEndian.Uint64(buf[32:40]), uint64(stat.AccessedNs)},
		{"ctime", binary.LittleEndian.Uint64(buf[40:48]), uint64(stat.ChangedNs)},
		{"birthtime", binary.LittleEndian.Uint64(buf[48:56]), uint64(stat.CreatedNs)},
		{"nlink", binary.LittleEndian.Uint64(buf[56:64]), stat.Nlink},
		{"inode", binary.LittleEndian.Uint64(buf[64:72]), stat.Inode},
		{"dev", binary.LittleEndian.Uint64(buf[72:80]), stat.Dev},
		{"rdev", binary.LittleEndian.Uint64(buf[80:88]), stat.Rdev},
		{"blksize", binary.LittleEndian.Uint64(buf[88:96]), uint64(stat.Blksize)},
		{"blocks", binary.LittleEndian.Uint64(buf[96:104]), uint64(stat.Blocks)},
		{"valid_mask", binary.LittleEndian.Uint64(buf[104:112]), stat.ValidMask},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %d, want %d", check.name, check.got, check.want)
		}
	}
}
