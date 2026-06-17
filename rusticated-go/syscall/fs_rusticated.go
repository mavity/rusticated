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

//go:wasmimport env path_remove
//go:noescape
func rusticated_path_remove(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32)

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

func fixJoinedPath(path string) string {
	// Detect "doubled" paths like C:\cwd\C:/sdk/...
	// This happens because the Go wasip1 runtime doesn't recognize
	// Windows-style absolute paths and joins them with the current directory.
	for i := 1; i < len(path)-1; i++ {
		if path[i] == ':' && (path[i+1] == '/' || path[i+1] == '\\') {
			if i > 1 {
				// Found a drive letter (e.g., C:/) in the middle of a path.
				// Ensure it's preceded by a letter to avoid false positives.
				c := path[i-1]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					return path[i-1:]
				}
			}
		}
	}
	return path
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
	path = fixJoinedPath(path)
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

	// println("DEBUG: Open path", path)
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
	// println("DEBUG: Openat dirFd", dirFd, "path", path)
	if isAbs(path) || dirFd == -100 { // -100 is AT_FDCWD
		return Open(path, openmode, perm)
	}
	if entry, ok := fdMap[int32(dirFd)]; ok && entry.path != "" {
		full := joinPaths(entry.path, path)
		// println("DEBUG: Openat joined", full)
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

func statat_ext(dirFd int, path string, st *Stat_t, flags uint32) error {
	path = fixJoinedPath(path)
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
	runtime.KeepAlive(buf)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	parseAbiStat(buf, st)
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
	if n == 0 {
		pi := GetPlatformInfo()
		if pi.PathSeparator == '\\' {
			return "C:\\", nil
		}
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
	var ctx overlappedContext
	rusticated_path_remove(unsafe.Pointer(&ctx.o), (*byte)(unsafe.StringData(path)), uint32(len(path)))
	runtime.KeepAlive(path)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return errnoErr(Errno(ctx.o.hostError))
	}
	return nil
}

func Rmdir(path string) error {
	// host os.Remove handles both files and empty directories
	return Unlink(path)
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
