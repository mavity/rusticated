//go:build windows

package main

import (
	"os"
	"syscall"
)

const fileReadAttributes = 0x0080
const shareReadWriteDelete = 0x00000001 | 0x00000002 | 0x00000004

func populatePlatformAbiStat(fi os.FileInfo, hostPath string, noFollow bool, stat *AbiStat) {
	data, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok || data == nil {
		return
	}
	stat.AccessedNs = data.LastAccessTime.Nanoseconds()
	stat.ModifiedNs = data.LastWriteTime.Nanoseconds()
	stat.CreatedNs = data.CreationTime.Nanoseconds()
	stat.ValidMask |= statValidAtime | statValidMtime | statValidBirthtime

	if hostPath == "" {
		return
	}
	pathPtr, err := syscall.UTF16PtrFromString(hostPath)
	if err != nil {
		return
	}
	attrs := uint32(syscall.FILE_FLAG_BACKUP_SEMANTICS)
	if noFollow {
		attrs |= syscall.FILE_FLAG_OPEN_REPARSE_POINT
	}
	handle, err := syscall.CreateFile(pathPtr, fileReadAttributes, shareReadWriteDelete, nil, syscall.OPEN_EXISTING, attrs, 0)
	if err != nil {
		return
	}
	defer syscall.CloseHandle(handle)

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		return
	}
	stat.Nlink = uint64(info.NumberOfLinks)
	stat.Inode = uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	stat.Dev = uint64(info.VolumeSerialNumber)
	stat.ValidMask |= statValidNlink | statValidInode | statValidDev
}
