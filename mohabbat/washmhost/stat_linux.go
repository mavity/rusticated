//go:build linux

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
	stat.Mode = st.Mode
	stat.Kind = statKindFromUnixMode(st.Mode)
	stat.Uid = st.Uid
	stat.Gid = st.Gid
	stat.Nlink = uint64(st.Nlink)
	stat.Inode = st.Ino
	stat.Dev = st.Dev
	stat.Rdev = st.Rdev
	stat.Blksize = int64(st.Blksize)
	stat.Blocks = st.Blocks
	stat.AccessedNs = int64(st.Atim.Sec)*1e9 + int64(st.Atim.Nsec)
	stat.ModifiedNs = int64(st.Mtim.Sec)*1e9 + int64(st.Mtim.Nsec)
	stat.ChangedNs = int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec)
	stat.ValidMask |= statValidUID | statValidGID | statValidAtime | statValidMtime |
		statValidCtime | statValidNlink | statValidInode | statValidDev |
		statValidRdev | statValidBlksize | statValidBlocks
}
