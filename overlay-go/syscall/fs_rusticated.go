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

// Stat_t mirrors the WASI filestat structure (was in syscall/fs_wasip1.go).
type Stat_t struct {
	Dev      uint64
	Ino      uint64
	Filetype uint8
	Nlink    uint64
	Size     uint64
	Atime    uint64
	Mtime    uint64
	Ctime    uint64
	Mode     int
	Uid      uint32
	Gid      uint32
}

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

//go:wasmimport env handle_close
func rusticated_handle_close(handle uint64)

//go:wasmimport env dir_read
//go:noescape
func rusticated_dir_read(overlapped unsafe.Pointer, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env path_stat
//go:noescape
func rusticated_path_stat(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, flags uint32, outPtr *byte, outLen uint32)

//go:wasmimport env get_random
func rusticated_random_get(buf *byte, bufLen uint32)

//go:wasmimport env get_cwd
func rusticated_get_cwd(strPtr *byte, strLen uint32) uint64

//go:wasmimport env set_cwd
func rusticated_set_cwd(strPtr *byte, strLen uint32) uint32

// ── fd <-> handle mapping ────────────────────────────────────────────────

var (
	fdTable        [1024]uint64
	fdInUse        [1024]bool
	fdPaths        [1024]string // absolute path used to open this fd (for Fstat)
	dirReadPending [1024][]byte
)

func init() {
	fdTable[0] = 0
	fdTable[1] = 1
	fdTable[2] = 2
	fdInUse[0] = true
	fdInUse[1] = true
	fdInUse[2] = true
}

func writeDirentEntry(dst []byte, next uint64, name []byte) int {
	entryLen := 24 + len(name)
	if len(dst) < entryLen {
		return 0
	}
	binary.LittleEndian.PutUint64(dst[0:8], next)
	binary.LittleEndian.PutUint64(dst[8:16], 0) // inode unknown
	binary.LittleEndian.PutUint32(dst[16:20], uint32(len(name)))
	dst[20] = 0
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
	for i := 3; i < len(fdTable); i++ {
		if !fdInUse[i] {
			fdTable[i] = handle
			fdInUse[i] = true
			fdPaths[i] = path
			return int32(i), 0
		}
	}
	return -1, EMFILE
}

func fdToHandle(fd int32) (uint64, Errno) {
	if fd < 0 || int(fd) >= len(fdTable) || !fdInUse[fd] {
		return 0, EBADF
	}
	return fdTable[fd], 0
}

// ── Open / Close ────────────────────────────────────────────────────────

func Open(path string, mode int, perm uint32) (int, error) {
	if path == "" {
		return -1, EINVAL
	}
	var ctx overlappedContext
	rusticated_path_open(unsafe.Pointer(&ctx.o), (*byte)(unsafe.StringData(path)), uint32(len(path)), uint32(mode))
	runtime.KeepAlive(path)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return -1, errnoErr(Errno(ctx.o.hostError))
	}
	fd, err := allocFD(ctx.o.resultExt, path)
	if err != 0 {
		rusticated_handle_close(ctx.o.resultExt)
		return -1, errnoErr(err)
	}
	return int(fd), nil
}

// Openat resolves path relative to dirFd (ignored; we pass absolute paths to the host).
func Openat(dirFd int, path string, openmode int, perm uint32) (int, error) {
	return Open(path, openmode, perm)
}

func Close(fd int) error {
	if fd < 3 {
		return nil // Never close stdin/stdout/stderr handles
	}
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return errnoErr(err)
	}
	rusticated_handle_close(handle)
	fdInUse[fd] = false
	fdPaths[fd] = ""
	dirReadPending[fd] = nil
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
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return 0, errnoErr(err)
	}
	var ctx overlappedContext
	hostBuf := make([]byte, 2048)
	rusticated_dir_read(unsafe.Pointer(&ctx.o), handle, &hostBuf[0], uint32(len(hostBuf)))
	runtime.KeepAlive(hostBuf)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}

	pending := append(dirReadPending[fd], hostBuf[:int(ctx.o.resultExt)]...)
	written := 0

	for {
		if len(pending) == 0 {
			break
		}
		idx := bytes.IndexByte(pending, 0)
		if idx < 0 {
			break
		}

		name := pending[:idx]
		required := 24 + len(name)
		if len(buf)-written < required {
			break
		}

		next := uint64(written + required)
		n := writeDirentEntry(buf[written:], next, name)
		if n == 0 {
			break
		}
		written += n
		pending = pending[idx+1:]
	}

	dirReadPending[fd] = pending
	return written, nil
}

// ── Stat / Lstat / Fstat ──────────────────────────────────────────────────

const abiStatSize = 64

func abiKindToFiletype(kind uint32) uint8 {
	switch kind {
	case 1:
		return 4 // FILETYPE_REGULAR_FILE
	case 2:
		return 3 // FILETYPE_DIRECTORY
	case 3:
		return 7 // FILETYPE_SYMBOLIC_LINK
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

func setDefaultMode(st *Stat_t) {
	if st.Filetype == 3 {
		st.Mode = 0700
	} else {
		st.Mode = 0600
	}
}

func parseAbiStat(buf []byte, st *Stat_t) {
	if len(buf) < abiStatSize {
		return
	}
	st.Filetype = abiKindToFiletype(leU32fs(buf[0:4]))
	st.Uid = leU32fs(buf[8:12])
	st.Gid = leU32fs(buf[12:16])
	st.Size = leU64fs(buf[16:24])
	st.Mtime = leU64fs(buf[24:32])
	st.Atime = leU64fs(buf[32:40])
	st.Ctime = leU64fs(buf[40:48])
	st.Nlink = leU64fs(buf[48:56])
	st.Ino = leU64fs(buf[56:64])
	st.Mode = int(leU32fs(buf[4:8]))
	st.Dev = 1
}

func Stat(path string, st *Stat_t) error {
	println("DEBUG: guest Stat", path)
	if path == "" {
		return EINVAL
	}
	var ctx overlappedContext
	buf := make([]byte, abiStatSize)
	rusticated_path_stat(
		unsafe.Pointer(&ctx.o),
		(*byte)(unsafe.StringData(path)), uint32(len(path)),
		0, &buf[0], uint32(len(buf)),
	)
	runtime.KeepAlive(path)
	runtime.KeepAlive(buf)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	parseAbiStat(buf, st)
	return nil
}

func Lstat(path string, st *Stat_t) error {
	println("DEBUG: guest Lstat", path)
	if path == "" {
		return EINVAL
	}
	var ctx overlappedContext
	buf := make([]byte, abiStatSize)
	rusticated_path_stat(
		unsafe.Pointer(&ctx.o),
		(*byte)(unsafe.StringData(path)), uint32(len(path)),
		1, // no-follow (statFlagNoFollow)
		&buf[0], uint32(len(buf)),
	)
	runtime.KeepAlive(path)
	runtime.KeepAlive(buf)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	parseAbiStat(buf, st)
	return nil
}

func Fstat(fd int, st *Stat_t) error {
	if fd >= 0 && fd < len(fdPaths) && fdPaths[fd] != "" {
		return Stat(fdPaths[fd], st)
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
	if n == 0 {
		return "/", nil
	}
	buf := make([]byte, n)
	rusticated_get_cwd(&buf[0], n)
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

func Mkdir(path string, perm uint32) error          { return ENOSYS }
func Unlink(path string) error                      { return ENOSYS }
func Rmdir(path string) error                       { return ENOSYS }
func Rename(from, to string) error                  { return ENOSYS }
func Readlink(path string, buf []byte) (int, error) { return 0, ENOSYS }
func Link(path, link string) error                  { return ENOSYS }
func Symlink(path, link string) error               { return ENOSYS }
func Truncate(path string, length int64) error      { return ENOSYS }
func Ftruncate(fd int, length int64) error          { return ENOSYS }
func Fsync(fd int) error                            { return nil }
func Chmod(path string, mode uint32) error          { return nil }
func Fchmod(fd int, mode uint32) error              { return nil }
func Chown(path string, uid, gid int) error         { return ENOSYS }
func Fchown(fd int, uid, gid int) error             { return ENOSYS }
func Lchown(path string, uid, gid int) error        { return ENOSYS }
func UtimesNano(path string, ts []Timespec) error   { return ENOSYS }

// ── Seek / Pread / Pwrite / Dup / Pipe ───────────────────────────────────

func Seek(fd int, offset int64, whence int) (int64, error) { return 0, ENOSYS }
func Pread(fd int, b []byte, offset int64) (int, error)    { return 0, ENOSYS }
func Pwrite(fd int, b []byte, offset int64) (int, error)   { return 0, ENOSYS }
func Dup(fd int) (int, error)                              { return 0, ENOSYS }
func Dup2(fd int, newfd int) error                         { return ENOSYS }
func Pipe(fd []int) error                                  { return ENOSYS }
func SetNonblock(fd int, nonblocking bool) error           { return nil }

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
	if fd >= 0 && fd < len(fdTable) && fdInUse[fd] {
		return 4, nil // FILETYPE_REGULAR_FILE
	}
	return 0, EBADF
}

func CloseOnExec(fd int) {}
