package main

import (
	"encoding/binary"
	"os"
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
			name: "strips setuid bit",
			fi: mockFileInfo{
				name:    "setuid",
				size:    1024,
				mode:    os.ModeSetuid | 0755,
				modTime: now,
			},
			wantKind: statKindFile,
			wantMode: 0755 | 0o100000, // Should only have perm and file prefix
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
			if int64(stat.ModifiedNs) != tt.wantTime {
				t.Errorf("ModifiedNs = %v, want %v", stat.ModifiedNs, tt.wantTime)
			}
			// Check symmetry of timestamps
			if stat.AccessedNs != stat.ModifiedNs || stat.CreatedNs != stat.ModifiedNs {
				t.Errorf("Timestamps are not symmetrical")
			}
		})
	}
}

func TestMarshalAbiStat(t *testing.T) {
	tests := []struct {
		name string
		stat AbiStat
	}{
		{
			name: "basic file",
			stat: AbiStat{Kind: statKindFile, Mode: 0644 | 0o100000, Size: 1234, ModifiedNs: 5678, AccessedNs: 5678, CreatedNs: 5678, Nlink: 1, Inode: 10},
		},
		{
			name: "basic dir",
			stat: AbiStat{Kind: statKindDir, Mode: 0755 | 0o040000, Size: 4096, ModifiedNs: 9999, AccessedNs: 9999, CreatedNs: 9999, Nlink: 2, Inode: 20},
		},
		{
			name: "basic symlink",
			stat: AbiStat{Kind: statKindSymlink, Mode: 0777 | 0o120000, Size: 20, ModifiedNs: 1111, AccessedNs: 1111, CreatedNs: 1111, Nlink: 1, Inode: 30},
		},
		{
			name: "large values",
			stat: AbiStat{Kind: 0xFFFFFFFF, Mode: 0xFFFFFFFF, Size: 0xFFFFFFFFFFFFFFFF, ModifiedNs: 0xFFFFFFFFFFFFFFFF, AccessedNs: 0xFFFFFFFFFFFFFFFF, CreatedNs: 0xFFFFFFFFFFFFFFFF, Nlink: 0xFFFFFFFFFFFFFFFF, Inode: 0xFFFFFFFFFFFFFFFF},
		},
		{
			name: "zero values",
			stat: AbiStat{Kind: 0, Mode: 0, Size: 0, ModifiedNs: 0, AccessedNs: 0, CreatedNs: 0, Nlink: 0, Inode: 0},
		},
		{
			name: "max size",
			stat: AbiStat{Kind: statKindFile, Mode: 0o100644, Size: 9223372036854775807, ModifiedNs: 123},
		},
		{
			name: "high inode",
			stat: AbiStat{Inode: 18446744073709551615},
		},
		{
			name: "many nlinks",
			stat: AbiStat{Nlink: 65535},
		},
		{
			name: "uid-gid (ignored but marshaled)",
			stat: AbiStat{Uid: 1000, Gid: 1000},
		},
		{
			name: "mix of values",
			stat: AbiStat{Kind: 1, Mode: 2, Uid: 3, Gid: 4, Size: 5, ModifiedNs: 6, AccessedNs: 7, CreatedNs: 8, Nlink: 9, Inode: 10},
		},
		{
			name: "file with permission 000",
			stat: AbiStat{Kind: statKindFile, Mode: 0o100000, Size: 0},
		},
		{
			name: "dir with permission 700",
			stat: AbiStat{Kind: statKindDir, Mode: 0o040700, Size: 4096},
		},
		{
			name: "symlink with permission 777",
			stat: AbiStat{Kind: statKindSymlink, Mode: 0o120777, Size: 12},
		},
		{
			name: "negative-like values (unsigned)",
			stat: AbiStat{Size: ^uint64(0)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := marshalAbiStat(tt.stat)
			if len(buf) != 64 {
				t.Fatalf("len(buf) = %v, want 64", len(buf))
			}

			// Verify each field using LittleEndian decoding
			if val := binary.LittleEndian.Uint32(buf[0:4]); val != tt.stat.Kind {
				t.Errorf("Kind: got %v, want %v", val, tt.stat.Kind)
			}
			if val := binary.LittleEndian.Uint32(buf[4:8]); val != tt.stat.Mode {
				t.Errorf("Mode: got %v, want %v", val, tt.stat.Mode)
			}
			if val := binary.LittleEndian.Uint32(buf[8:12]); val != tt.stat.Uid {
				t.Errorf("Uid: got %v, want %v", val, tt.stat.Uid)
			}
			if val := binary.LittleEndian.Uint32(buf[12:16]); val != tt.stat.Gid {
				t.Errorf("Gid: got %v, want %v", val, tt.stat.Gid)
			}
			if val := binary.LittleEndian.Uint64(buf[16:24]); val != tt.stat.Size {
				t.Errorf("Size: got %v, want %v", val, tt.stat.Size)
			}
			if val := binary.LittleEndian.Uint64(buf[24:32]); val != tt.stat.ModifiedNs {
				t.Errorf("ModifiedNs: got %v, want %v", val, tt.stat.ModifiedNs)
			}
			if val := binary.LittleEndian.Uint64(buf[32:40]); val != tt.stat.AccessedNs {
				t.Errorf("AccessedNs: got %v, want %v", val, tt.stat.AccessedNs)
			}
			if val := binary.LittleEndian.Uint64(buf[40:48]); val != tt.stat.CreatedNs {
				t.Errorf("CreatedNs: got %v, want %v", val, tt.stat.CreatedNs)
			}
			if val := binary.LittleEndian.Uint64(buf[48:56]); val != tt.stat.Nlink {
				t.Errorf("Nlink: got %v, want %v", val, tt.stat.Nlink)
			}
			if val := binary.LittleEndian.Uint64(buf[56:64]); val != tt.stat.Inode {
				t.Errorf("Inode: got %v, want %v", val, tt.stat.Inode)
			}
		})
	}
}
