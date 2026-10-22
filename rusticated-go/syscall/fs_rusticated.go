//go:build wasip1

package syscall

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"structs"
	"unsafe"
	_ "unsafe" // for go:linkname
)

// ── Overlapped ────────────────────────────────────────────────────────────────
// Mirrors abi::Overlapped in the rusticated sysroot.

type Overlapped struct {
	_         structs.HostLayout
	flags     uint32
	hostError uint32
	continued uint64
	resultExt uint64
}

type overlappedContext struct {
	gp uintptr
	o  Overlapped
}

func (o *Overlapped) isComplete() bool { return o.flags&1 != 0 }

func awaitOverlapped(ctx *overlappedContext)
func cancelOverlapped(ctx *overlappedContext)

// ── Type definitions (from original syscall/fs_wasip1.go) ──────────────────

type uintptr32 = uint32
type size = uint32
type fdflags = uint32
type filesize = uint64
type lookupflags = uint32
type oflags = uint32
type rights = uint64
type timestamp = uint64
type filedelta = int64
type fstflags = uint32
type filetype = uint8

type iovec struct {
	_      structs.HostLayout
	buf    uintptr32
	bufLen size
}

// Stat_t is Rusticated's Go-level Unix stat projection. Its layout is not an ABI.
type Stat_t struct {
	Dev     uint64
	Ino     uint64
	Nlink   uint64
	Mode    uint32
	Uid     uint32
	Gid     uint32
	Rdev    uint64
	Size    int64
	Blksize int64
	Blocks  int64
	Atim    Timespec
	Mtim    Timespec
	Ctim    Timespec
	Btim    Timespec

	// BSD/macOS-compatible timestamp spellings.
	Atimespec     Timespec
	Mtimespec     Timespec
	Ctimespec     Timespec
	Birthtimespec Timespec

	// Go's wasip1 os overlay still reads these legacy fields.
	Filetype  uint8
	Atime     int64
	Mtime     int64
	Ctime     int64
	ValidMask uint64
}

const (
	STAT_VALID_UID       uint64 = 1 << 0
	STAT_VALID_GID       uint64 = 1 << 1
	STAT_VALID_ATIME     uint64 = 1 << 2
	STAT_VALID_MTIME     uint64 = 1 << 3
	STAT_VALID_CTIME     uint64 = 1 << 4
	STAT_VALID_BIRTHTIME uint64 = 1 << 5
	STAT_VALID_NLINK     uint64 = 1 << 6
	STAT_VALID_INODE     uint64 = 1 << 7
	STAT_VALID_DEV       uint64 = 1 << 8
	STAT_VALID_RDEV      uint64 = 1 << 9
	STAT_VALID_BLKSIZE   uint64 = 1 << 10
	STAT_VALID_BLOCKS    uint64 = 1 << 11
)

// ── Constants (from original syscall/fs_wasip1.go) ────────────────────────

const (
	LOOKUP_SYMLINK_FOLLOW = 0x00000001
)

const (
	OFLAG_CREATE    = 0x0001
	OFLAG_DIRECTORY = 0x0002
	OFLAG_EXCL      = 0x0004
	OFLAG_TRUNC     = 0x0008
)

const (
	FDFLAG_APPEND   = 0x0001
	FDFLAG_DSYNC    = 0x0002
	FDFLAG_NONBLOCK = 0x0004
	FDFLAG_RSYNC    = 0x0008
	FDFLAG_SYNC     = 0x0010
)

const (
	RIGHT_FD_DATASYNC = 1 << iota
	RIGHT_FD_READ
	RIGHT_FD_SEEK
	RIGHT_FDSTAT_SET_FLAGS
	RIGHT_FD_SYNC
	RIGHT_FD_TELL
	RIGHT_FD_WRITE
	RIGHT_FD_ADVISE
	RIGHT_FD_ALLOCATE
	RIGHT_PATH_CREATE_DIRECTORY
	RIGHT_PATH_CREATE_FILE
	RIGHT_PATH_LINK_SOURCE
	RIGHT_PATH_LINK_TARGET
	RIGHT_PATH_OPEN
	RIGHT_FD_READDIR
	RIGHT_PATH_READLINK
	RIGHT_PATH_RENAME_SOURCE
	RIGHT_PATH_RENAME_TARGET
	RIGHT_PATH_FILESTAT_GET
	RIGHT_PATH_FILESTAT_SET_SIZE
	RIGHT_PATH_FILESTAT_SET_TIMES
	RIGHT_FD_FILESTAT_GET
	RIGHT_FD_FILESTAT_SET_SIZE
	RIGHT_FD_FILESTAT_SET_TIMES
	RIGHT_PATH_SYMLINK
	RIGHT_PATH_REMOVE_DIRECTORY
	RIGHT_PATH_UNLINK_FILE
	RIGHT_POLL_FD_READWRITE
	RIGHT_SOCK_SHUTDOWN
	RIGHT_SOCK_ACCEPT
)

const (
	WHENCE_SET = 0
	WHENCE_CUR = 1
	WHENCE_END = 2
)

const (
	FILESTAT_SET_ATIM     = 0x0001
	FILESTAT_SET_ATIM_NOW = 0x0002
	FILESTAT_SET_MTIM     = 0x0004
	FILESTAT_SET_MTIM_NOW = 0x0008
)

const ImplementsGetwd = true

// ── Rusticated host ABI imports ────────────────────────────────────────────

//go:wasmimport env path_open
//go:noescape
func rusticated_path_open(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, flags uint32)

//go:wasmimport env read
//go:noescape
func rusticated_read(overlapped unsafe.Pointer, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env write
//go:noescape
func rusticated_write(overlapped unsafe.Pointer, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env handle_truncate
//go:noescape
func rusticated_handle_truncate(overlapped unsafe.Pointer, handle uint64, length int64)

//go:wasmimport env handle_close
func rusticated_handle_close(handle uint64)

//go:wasmimport env dir_read
//go:noescape
func rusticated_dir_read(overlapped unsafe.Pointer, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env path_stat
//go:noescape
func rusticated_path_stat(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, flags uint32, outPtr *byte, outLen uint32)

//go:wasmimport env path_chmod
//go:noescape
func rusticated_path_chmod(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, mode uint32)

//go:wasmimport env path_set_times
//go:noescape
func rusticated_path_set_times(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, atimeSec int64, atimeNsec uint32, mtimeSec int64, mtimeNsec uint32, flags uint32)

//go:wasmimport env seek
//go:noescape
func rusticated_seek(overlapped unsafe.Pointer, handle uint64, offset int64, whence uint32)

//go:wasmimport env get_random
func rusticated_random_get(buf *byte, bufLen uint32)

//go:wasmimport env fd_isatty
func rusticated_fd_isatty(fd int32) int32

//go:wasmimport env tty_get_size
func rusticated_tty_get_size(handle uint64) uint32

//go:wasmimport env tty_set_size
func rusticated_tty_set_size(handle uint64, cols uint32, rows uint32) uint32

//go:wasmimport env tty_set_mode
func rusticated_tty_set_mode(handle uint64, mode int32)

// Isatty reports whether the file descriptor fd refers to a terminal.
// The host returns 1 for a TTY, 0 otherwise.
func Isatty(fd uintptr) bool {
	return rusticated_fd_isatty(int32(fd)) != 0
}

//go:wasmimport env get_cwd
func rusticated_get_cwd(strPtr *byte, strLen uint32) uint64

//go:wasmimport env set_cwd
func rusticated_set_cwd(strPtr *byte, strLen uint32) uint32

//go:wasmimport env path_remove
//go:noescape
func rusticated_path_remove(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, flags uint32)

//go:wasmimport env path_mkdir
//go:noescape
func rusticated_path_mkdir(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, mode uint32)

//go:wasmimport env path_rename
//go:noescape
func rusticated_path_rename(overlapped unsafe.Pointer, oldPtr *byte, oldLen uint32, newPtr *byte, newLen uint32)

// ── fd <-> handle mapping ────────────────────────────────────────────────

type fdEntry struct {
	handle  uint64
	path    string
	pending []byte // buffered partial dirRead data
}

var (
	fdMap = map[int32]fdEntry{
		0: {handle: 0},
		1: {handle: 1},
		2: {handle: 2},
	}
	fdNext int32 = 3
)

func writeDirentEntry(dst []byte, next uint64, name []byte, kind byte) int {
	entryLen := 24 + len(name)
	if len(dst) < entryLen {
		return 0
	}
	binary.LittleEndian.PutUint64(dst[0:8], next)
	binary.LittleEndian.PutUint64(dst[8:16], 0) // inode unknown
	binary.LittleEndian.PutUint32(dst[16:20], uint32(len(name)))

	// Map our internal statKind to WASIP1 d_type
	var dType byte
	switch kind {
	case 1: // File
		dType = 4 // REG
	case 2: // Dir
		dType = 3 // DIR
	case 3: // Symlink
		dType = 7 // LNK
	default:
		dType = 0 // UNKNOWN
	}

	dst[20] = dType
	dst[21] = 0
	dst[22] = 0
	dst[23] = 0
	copy(dst[24:], name)
	return entryLen
}

func splitNullNames(data []byte) (names [][]byte, remainder []byte) {
	start := 0
	for i, b := range data {
		if b == 0 {
			names = append(names, append([]byte(nil), data[start:i]...))
			start = i + 1
		}
	}
	if start < len(data) {
		remainder = append([]byte(nil), data[start:]...)
	}
	return
}

func allocFD(handle uint64, path string) (int32, Errno) {
	fd := fdNext
	for {
		if fd > 1<<20 { // guard: over a million open FDs is a leak
			return -1, EMFILE
		}
		if _, exists := fdMap[fd]; !exists {
			break
		}
		fd++
	}
	fdNext = fd + 1
	fdMap[fd] = fdEntry{handle: handle, path: path}
	return fd, 0
}

func fdToHandle(fd int32) (uint64, Errno) {
	if entry, ok := fdMap[fd]; ok {
		return entry.handle, 0
	}
	return 0, EBADF
}

// ── Open / Close ────────────────────────────────────────────────────────

func isAbs(path string) bool {
	if len(path) > 0 && (path[0] == '/' || path[0] == '\\') {
		return true
	}
	if len(path) > 2 && path[1] == ':' && (path[2] == '/' || path[2] == '\\') {
		return true
	}
	return false
}

func joinPaths(base, rel string) string {
	if rel == "" {
		return base
	}
	if isAbs(rel) {
		return rel
	}

	if base == "" || base == "." {
		return rel
	}
	b := base
	if b[len(b)-1] != '/' && b[len(b)-1] != '\\' {
		b += "/"
	}
	return b + rel
}

func path_open_ext(path string, mode int, perm uint32) (uint64, error) {
	// Virtual device translation for cross-platform compatibility
	if path == "/dev/tty" {
		pi := GetPlatformInfo()
		if pi.OSKind == 1 { // Windows
			path = "CONIN$"
		}
	} else if path == "/dev/null" {
		pi := GetPlatformInfo()
		if pi.OSKind == 1 { // Windows
			path = "NUL"
		}
	}

	// Final absolute path check before calling host
	if isAbs(path) {
		// No-op, use as is
	} else {
		// Add relative prefix if needed
	}

	var ctx overlappedContext
	rusticated_path_open(unsafe.Pointer(&ctx.o), (*byte)(unsafe.StringData(path)), uint32(len(path)), uint32(mode))
	runtime.KeepAlive(path)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return ctx.o.resultExt, nil
}

func Open(path string, mode int, perm uint32) (int, error) {
	if path == "" {
		return -1, EINVAL
	}

	debugPrintln("DEBUG: Open path", path)
	h, err := path_open_ext(path, mode, perm)
	if err != nil {
		return -1, err
	}
	fd, errFD := allocFD(h, path)
	if errFD != 0 {
		rusticated_handle_close(h)
		return -1, errnoErr(errFD)
	}
	return int(fd), nil
}

// Openat resolves path relative to dirFd.
func Openat(dirFd int, path string, openmode int, perm uint32) (int, error) {
	debugPrintln("DEBUG: Openat dirFd", dirFd, "path", path)
	if isAbs(path) || dirFd == -100 { // -100 is AT_FDCWD
		return Open(path, openmode, perm)
	}
	if entry, ok := fdMap[int32(dirFd)]; ok && entry.path != "" {
		full := joinPaths(entry.path, path)
		debugPrintln("DEBUG: Openat joined", full)
		return Open(full, openmode, perm)
	}
	return -1, ENOSYS
}

func Close(fd int) error {
	if fd < 3 {
		return nil // Never close stdin/stdout/stderr handles
	}
	entry, ok := fdMap[int32(fd)]
	if !ok {
		return errnoErr(EBADF)
	}
	rusticated_handle_close(entry.handle)
	delete(fdMap, int32(fd))
	return nil
}

// ── Read / Write ──────────────────────────────────────────────────────────

func Read(fd int, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return 0, errnoErr(err)
	}
	var ctx overlappedContext
	rusticated_read(unsafe.Pointer(&ctx.o), handle, &p[0], uint32(len(p)))
	runtime.KeepAlive(p)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return int(ctx.o.resultExt), nil
}

func Write(fd int, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return 0, errnoErr(err)
	}
	var ctx overlappedContext
	rusticated_write(unsafe.Pointer(&ctx.o), handle, &p[0], uint32(len(p)))
	runtime.KeepAlive(p)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return int(ctx.o.resultExt), nil
}

// ── ReadDir ───────────────────────────────────────────────────────────────

func ReadDir(fd int, buf []byte, _ uint64) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	entry, ok := fdMap[int32(fd)]
	if !ok {
		return 0, errnoErr(EBADF)
	}
	var ctx overlappedContext
	hostBuf := make([]byte, 2048)
	rusticated_dir_read(unsafe.Pointer(&ctx.o), entry.handle, &hostBuf[0], uint32(len(hostBuf)))
	runtime.KeepAlive(hostBuf)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}

	pending := append(entry.pending, hostBuf[:int(ctx.o.resultExt)]...)
	written := 0

	for {
		if len(pending) == 0 {
			break
		}
		idx := bytes.IndexByte(pending, 0)
		if idx < 0 {
			break
		}

		kind := pending[0]
		name := pending[1:idx]
		required := 24 + len(name)
		if len(buf)-written < required {
			break
		}

		next := uint64(written + required)
		n := writeDirentEntry(buf[written:], next, name, kind)
		if n == 0 {
			break
		}
		written += n
		pending = pending[idx+1:]
	}

	entry.pending = pending
	fdMap[int32(fd)] = entry
	return written, nil
}

// ── Stat / Lstat / Fstat ──────────────────────────────────────────────────

const abiStatSize = 112

func abiKindToFiletype(kind uint32) uint8 {
	switch kind {
	case 1:
		return 4 // FILETYPE_REGULAR_FILE
	case 2:
		return 3 // FILETYPE_DIRECTORY
	case 3:
		return 7 // FILETYPE_SYMBOLIC_LINK
	case 4:
		return 2 // FILETYPE_CHARACTER_DEVICE
	case 5:
		return 1 // FILETYPE_BLOCK_DEVICE
	case 7:
		return 5 // FILETYPE_SOCKET_STREAM
	default:
		return 0 // FILETYPE_UNKNOWN
	}
}

func leU32fs(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func leU64fs(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

func leI64fs(b []byte) int64 {
	return int64(leU64fs(b))
}

func nsToTimespec(ns int64) Timespec {
	sec := ns / 1e9
	nsec := ns % 1e9
	if nsec < 0 {
		sec--
		nsec += 1e9
	}
	return Timespec{Sec: sec, Nsec: nsec}
}

func parseAbiStat(buf []byte, st *Stat_t) {
	if len(buf) < abiStatSize {
		return
	}
	st.Filetype = abiKindToFiletype(leU32fs(buf[0:4]))
	st.Uid = leU32fs(buf[8:12])
	st.Gid = leU32fs(buf[12:16])
	st.Size = int64(leU64fs(buf[16:24]))
	st.Mtime = leI64fs(buf[24:32])
	st.Atime = leI64fs(buf[32:40])
	st.Ctime = leI64fs(buf[40:48])
	st.Nlink = leU64fs(buf[56:64])
	st.Ino = leU64fs(buf[64:72])
	st.Dev = leU64fs(buf[72:80])
	st.Rdev = leU64fs(buf[80:88])
	st.Blksize = leI64fs(buf[88:96])
	st.Blocks = leI64fs(buf[96:104])
	st.ValidMask = leU64fs(buf[104:112])
	st.Mode = leU32fs(buf[4:8])
	st.Mtim = nsToTimespec(st.Mtime)
	st.Atim = nsToTimespec(st.Atime)
	st.Ctim = nsToTimespec(st.Ctime)
	st.Btim = nsToTimespec(leI64fs(buf[48:56]))
	st.Atimespec = st.Atim
	st.Mtimespec = st.Mtim
	st.Ctimespec = st.Ctim
	st.Birthtimespec = st.Btim
}

func statat_ext(dirFd int, path string, st *Stat_t, flags uint32) error {
	full := path
	if !isAbs(path) && dirFd != -100 {
		if entry, ok := fdMap[int32(dirFd)]; ok && entry.path != "" {
			full = joinPaths(entry.path, path)
		}
	}
	if full == "" {
		return EINVAL
	}

	var ctx overlappedContext
	buf := make([]byte, abiStatSize)
	rusticated_path_stat(
		unsafe.Pointer(&ctx.o),
		(*byte)(unsafe.StringData(full)), uint32(len(full)),
		flags, &buf[0], uint32(len(buf)),
	)
	runtime.KeepAlive(full)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	parseAbiStat(buf, st)
	runtime.KeepAlive(buf)
	return nil
}

func Stat(path string, st *Stat_t) error {
	return statat_ext(-100, path, st, 0)
}

func Lstat(path string, st *Stat_t) error {
	return statat_ext(-100, path, st, 1) // 1 = statFlagNoFollow
}

func Fstatat(dirFd int, path string, st *Stat_t, flags int) error {
	// Map flags to our ABI: AT_SYMLINK_NOFOLLOW = 0x100 in Go
	abiFlags := uint32(0)
	if (flags & 0x100) != 0 {
		abiFlags |= 1
	}
	return statat_ext(dirFd, path, st, abiFlags)
}

func Fstat(fd int, st *Stat_t) error {
	if entry, ok := fdMap[int32(fd)]; ok && entry.path != "" {
		return Stat(entry.path, st)
	}
	return ENOSYS
}

// ── Getwd / Chdir ─────────────────────────────────────────────────────────

func Getwd() (string, error) {
	packed := rusticated_get_cwd(nil, 0)
	errno := uint32(packed >> 32)
	if errno != 0 {
		return "", errnoErr(Errno(errno))
	}
	n := uint32(packed & 0xFFFFFFFF)
	buf := make([]byte, n)
	packed = rusticated_get_cwd(&buf[0], n)
	errno = uint32(packed >> 32)
	if errno != 0 {
		return "", errnoErr(Errno(errno))
	}
	runtime.KeepAlive(buf)
	return string(buf[:n]), nil
}

func Chdir(path string) error {
	if path == "" {
		return EINVAL
	}
	errno := rusticated_set_cwd((*byte)(unsafe.StringData(path)), uint32(len(path)))
	runtime.KeepAlive(path)
	if errno != 0 {
		return errnoErr(Errno(errno))
	}
	return nil
}

// ── Directory / File operations ───────────────────────────────────────────

func Mkdir(path string, perm uint32) error {
	var ctx overlappedContext
	rusticated_path_mkdir(unsafe.Pointer(&ctx.o), (*byte)(unsafe.StringData(path)), uint32(len(path)), perm)
	runtime.KeepAlive(path)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	return nil
}

func Unlink(path string) error {
	return pathRemove(path, 0)
}

const atRemoveDir = 0x200

func pathRemove(path string, flags uint32) error {
	var ctx overlappedContext
	rusticated_path_remove(unsafe.Pointer(&ctx.o), (*byte)(unsafe.StringData(path)), uint32(len(path)), flags)
	runtime.KeepAlive(path)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	return nil
}

func Rmdir(path string) error {
	return pathRemove(path, atRemoveDir)
}

// Unlinkat removes path relative to dirFd, matching the Rusticated host path
// operation's file-versus-directory behavior.
func Unlinkat(dirFd int, path string, flags int) error {
	if flags != 0 && flags != atRemoveDir {
		return EINVAL
	}
	if path == "" {
		return EINVAL
	}

	fullPath := path
	if !isAbs(fullPath) && dirFd != -100 {
		entry, ok := fdMap[int32(dirFd)]
		if !ok || entry.path == "" {
			return EBADF
		}
		fullPath = joinPaths(entry.path, fullPath)
	}

	return pathRemove(fullPath, uint32(flags))
}

func Rename(from, to string) error {
	var ctx overlappedContext
	rusticated_path_rename(unsafe.Pointer(&ctx.o), (*byte)(unsafe.StringData(from)), uint32(len(from)), (*byte)(unsafe.StringData(to)), uint32(len(to)))
	runtime.KeepAlive(from)
	runtime.KeepAlive(to)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	return nil
}
func Readlink(path string, buf []byte) (int, error) { return 0, ENOSYS }
func Link(path, link string) error                  { return ENOSYS }
func Symlink(path, link string) error               { return ENOSYS }
func Truncate(path string, length int64) error {
	if path == "" {
		return EINVAL
	}
	if length < 0 {
		return EINVAL
	}
	fd, err := Open(path, 1, 0)
	if err != nil {
		return err
	}
	truncateErr := Ftruncate(fd, length)
	closeErr := Close(fd)
	if truncateErr != nil {
		return truncateErr
	}
	return closeErr
}
func Ftruncate(fd int, length int64) error {
	if length < 0 {
		return EINVAL
	}
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return errnoErr(err)
	}
	var ctx overlappedContext
	rusticated_handle_truncate(unsafe.Pointer(&ctx.o), handle, length)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	return nil
}
func Fsync(fd int) error                            { return nil }
func Chmod(path string, mode uint32) error {
	if path == "" {
		return EINVAL
	}

	var ctx overlappedContext
	rusticated_path_chmod(unsafe.Pointer(&ctx.o), (*byte)(unsafe.StringData(path)), uint32(len(path)), mode)
	runtime.KeepAlive(path)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	return nil
}
func Fchmod(fd int, mode uint32) error       { return nil }
func Chown(path string, uid, gid int) error  { return ENOSYS }
func Fchown(fd int, uid, gid int) error      { return ENOSYS }
func Lchown(path string, uid, gid int) error { return ENOSYS }

const (
	atFDCWD              = -100
	atSymlinkNoFollow    = 0x100
	utimeNow             = 1<<30 - 1
	utimeOmit            = 1<<30 - 2
	pathSetAtime         = 1 << 0
	pathSetAtimeNow      = 1 << 1
	pathSetMtime         = 1 << 2
	pathSetMtimeNow      = 1 << 3
	pathSetTimesNoFollow = 1 << 4
)

func UtimesNano(path string, ts []Timespec) error {
	return UtimesNanoAt(atFDCWD, path, ts, 0)
}

func UtimesNanoAt(dirFd int, path string, ts []Timespec, flags int) error {
	if path == "" || flags&^atSymlinkNoFollow != 0 {
		return EINVAL
	}
	if ts != nil && len(ts) != 2 {
		return EINVAL
	}

	if !isAbs(path) && dirFd != atFDCWD {
		entry, ok := fdMap[int32(dirFd)]
		if !ok || entry.path == "" {
			return EBADF
		}
		path = joinPaths(entry.path, path)
	}

	var atimeSec, mtimeSec int64
	var atimeNsec, mtimeNsec uint32
	abiFlags := uint32(0)
	if ts == nil {
		abiFlags |= pathSetAtimeNow | pathSetMtimeNow
	} else {
		var err error
		atimeSec, atimeNsec, abiFlags, err = encodeTimespec(ts[0], pathSetAtime, pathSetAtimeNow, abiFlags)
		if err != nil {
			return err
		}
		mtimeSec, mtimeNsec, abiFlags, err = encodeTimespec(ts[1], pathSetMtime, pathSetMtimeNow, abiFlags)
		if err != nil {
			return err
		}
	}
	if flags&atSymlinkNoFollow != 0 {
		abiFlags |= pathSetTimesNoFollow
	}

	var ctx overlappedContext
	rusticated_path_set_times(
		unsafe.Pointer(&ctx.o),
		(*byte)(unsafe.StringData(path)), uint32(len(path)),
		atimeSec, atimeNsec, mtimeSec, mtimeNsec, abiFlags,
	)
	runtime.KeepAlive(path)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	return nil
}

func encodeTimespec(ts Timespec, setFlag, nowFlag, abiFlags uint32) (int64, uint32, uint32, error) {
	switch ts.Nsec {
	case utimeNow:
		return 0, 0, abiFlags | nowFlag, nil
	case utimeOmit:
		return 0, 0, abiFlags, nil
	}
	if ts.Nsec < 0 || ts.Nsec >= 1e9 {
		return 0, 0, abiFlags, EINVAL
	}
	return ts.Sec, uint32(ts.Nsec), abiFlags | setFlag, nil
}

// ── Seek / Pread / Pwrite / Dup / Pipe ───────────────────────────────────

func Seek(fd int, offset int64, whence int) (int64, error) {
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return 0, errnoErr(err)
	}
	var ctx overlappedContext
	rusticated_seek(unsafe.Pointer(&ctx.o), handle, offset, uint32(whence))
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return int64(ctx.o.resultExt), nil
}

func Pread(fd int, b []byte, offset int64) (int, error)  { return 0, ENOSYS }
func Pwrite(fd int, b []byte, offset int64) (int, error) { return 0, ENOSYS }

func Dup(oldfd int) (int, error) {
	entry, ok := fdMap[int32(oldfd)]
	if !ok {
		return -1, errnoErr(EBADF)
	}
	fd, errAlloc := allocFD(entry.handle, entry.path)
	if errAlloc != 0 {
		return -1, errnoErr(errAlloc)
	}
	return int(fd), nil
}

func Dup2(oldfd int, newfd int) error {
	entry, ok := fdMap[int32(oldfd)]
	if !ok {
		return errnoErr(EBADF)
	}
	if newfd < 0 {
		return EINVAL
	}
	// Note: standard Dup2 closes newfd if open (we just overwrite).
	fdMap[int32(newfd)] = fdEntry{handle: entry.handle, path: entry.path}
	return nil
}

func Pipe(fd []int) error {
	var ctx overlappedContext
	rusticated_process_pipe(unsafe.Pointer(&ctx.o))
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	// resultExt packs: (writeHandle << 32) | readHandle
	rh := uint64(uint32(ctx.o.resultExt))
	wh := uint64(uint32(ctx.o.resultExt >> 32))

	if len(fd) < 2 {
		return EINVAL
	}

	fd_r, err_r := allocFD(rh, "|0")
	if err_r != 0 {
		return errnoErr(err_r)
	}
	fd_w, err_w := allocFD(wh, "|1")
	if err_w != 0 {
		// Cleanup rh if wh fails? For now just return error.
		return errnoErr(err_w)
	}

	fd[0] = int(fd_r)
	fd[1] = int(fd_w)
	return nil
}

// ProcessSpawnWithPty spawns a child process with a PTY using the rusticated ABI.
// It returns (childHandle, ptyFd, err).
func ProcessSpawnWithPty(cfg []byte) (uint64, int, error) {
	if len(cfg) == 0 {
		return 0, -1, EINVAL
	}

	// 64-byte extended overlapped structure
	type spawnPtyContext struct {
		gp          uintptr
		o           Overlapped
		childHandle uint64
		ptyHandle   uint64
		inHandle    uint64
		outHandle   uint64
		errHandle   uint64
	}

	var ctx spawnPtyContext
	rusticated_process_spawn(unsafe.Pointer(&ctx.o), &cfg[0], uint32(len(cfg)))
	awaitOverlapped((*overlappedContext)(unsafe.Pointer(&ctx)))

	if ctx.o.hostError != 0 {
		return 0, -1, errnoErr(Errno(ctx.o.hostError))
	}

	childHandle := ctx.childHandle
	if childHandle == 0 {
		childHandle = uint64(uint32(ctx.o.resultExt))
	}

	ptyHandle := ctx.ptyHandle
	if ptyHandle == 0 {
		ptyHandle = uint64(uint32(ctx.o.resultExt >> 32))
	}

	fd, errAlloc := allocFD(ptyHandle, "/dev/ptmx")
	if errAlloc != 0 {
		return childHandle, -1, errnoErr(errAlloc)
	}

	return childHandle, int(fd), nil
}

// RawTtySetSize exposes direct rusticated_tty_set_size for guest packages.
func RawTtySetSize(fd int, cols, rows int) error {
	entry, ok := fdMap[int32(fd)]
	if !ok {
		return EBADF
	}
	errCode := rusticated_tty_set_size(entry.handle, uint32(cols), uint32(rows))
	if errCode != 0 {
		return errnoErr(Errno(errCode))
	}
	return nil
}
func SetNonblock(fd int, nonblocking bool) error { return nil }

// ── Randomness ────────────────────────────────────────────────────────────

func RandomGet(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	rusticated_random_get(&b[0], uint32(len(b)))
	return nil
}

// ── fd_fdstat stubs (linknamed from internal/syscall/unix and net) ─────────

// fd_fdstat_get_flags is accessed from internal/syscall/unix via go:linkname.
//
//go:linkname fd_fdstat_get_flags
func fd_fdstat_get_flags(fd int) (uint32, error) {
	return 0, nil
}

// fd_fdstat_get_type is accessed from the net package via go:linkname.
//
//go:linkname fd_fdstat_get_type
func fd_fdstat_get_type(fd int) (uint8, error) {
	if _, ok := fdMap[int32(fd)]; ok {
		return 4, nil // FILETYPE_REGULAR_FILE
	}
	return 0, EBADF
}

func CloseOnExec(fd int) {}
