//go:build darwin

package main

import (
	"os"
	"syscall"
)

func populatePlatformAbiStat(fi os.FileInfo, _ string, _ bool, stat *AbiStat) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return
	}
	stat.Mode = uint32(st.Mode)
	stat.Kind = statKindFromUnixMode(stat.Mode)
	stat.Uid = st.Uid
	stat.Gid = st.Gid
	stat.Nlink = uint64(st.Nlink)
	stat.Inode = st.Ino
	stat.Dev = uint64(uint32(st.Dev))
	stat.Rdev = uint64(uint32(st.Rdev))
	stat.Blksize = int64(st.Blksize)
	stat.Blocks = st.Blocks
	stat.AccessedNs = st.Atimespec.Sec*1e9 + st.Atimespec.Nsec
	stat.ModifiedNs = st.Mtimespec.Sec*1e9 + st.Mtimespec.Nsec
	stat.ChangedNs = st.Ctimespec.Sec*1e9 + st.Ctimespec.Nsec
	stat.CreatedNs = st.Birthtimespec.Sec*1e9 + st.Birthtimespec.Nsec
	stat.ValidMask |= statValidUID | statValidGID | statValidAtime | statValidMtime |
		statValidCtime | statValidBirthtime | statValidNlink | statValidInode |
		statValidDev | statValidRdev | statValidBlksize | statValidBlocks
}
