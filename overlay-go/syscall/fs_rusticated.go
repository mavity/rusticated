//go:build wasip1

package syscall

import (
	"internal/runtime/sys"
	"runtime"
	"structs"
	"unsafe"
)

type uintptr32 = uint32
type size = uint32

type Overlapped struct {
	_         structs.HostLayout
	flags     uint32
	error     uint32
	continued uint64
	resultExt uint64
}

func (o *Overlapped) isComplete() bool { return o.flags&1 != 0 }

//go:linkname pause runtime.pause
func pause(newsp uintptr)

func awaitOverlapped(o *Overlapped) {
	for !o.isComplete() {
		pause(sys.GetCallerSP() - 16)
	}
}

//go:wasmimport env path_open
func rusticated_path_open(overlapped uintptr, pathPtr *byte, pathLen uint32, flags uint32)

//go:wasmimport env read
func rusticated_read(overlapped uintptr, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env write
func rusticated_write(overlapped uintptr, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env handle_close
func rusticated_handle_close(handle uint64)

//go:wasmimport env dir_read
func rusticated_dir_read(overlapped uintptr, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env get_random
func rusticated_random_get(buf *byte, bufLen uint32)

var (
	fdTable [1024]uint64
	fdInUse [1024]bool
)

func init() {
	fdTable[0] = 0
	fdTable[1] = 1
	fdTable[2] = 2
	fdInUse[0] = true
	fdInUse[1] = true
	fdInUse[2] = true
}

func allocFD(handle uint64) (int32, Errno) {
	for i := 3; i < len(fdTable); i++ {
		if !fdInUse[i] {
			fdTable[i] = handle
			fdInUse[i] = true
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

func Open(path string, mode int, perm uint32) (int, error) {
	if path == "" {
		return -1, EINVAL
	}
	var ov Overlapped
	rusticated_path_open(uintptr(unsafe.Pointer(&ov)), (*byte)(unsafe.StringData(path)), uint32(len(path)), uint32(mode))
	runtime.KeepAlive(path)
	awaitOverlapped(&ov)
	if ov.error != 0 {
		return -1, errnoErr(Errno(ov.error))
	}
	fd, err := allocFD(ov.resultExt)
	if err != 0 {
		rusticated_handle_close(ov.resultExt)
		return -1, errnoErr(err)
	}
	return int(fd), nil
}

func Close(fd int) error {
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return errnoErr(err)
	}
	rusticated_handle_close(handle)
	fdInUse[fd] = false
	return nil
}

func Read(fd int, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return 0, errnoErr(err)
	}
	var ov Overlapped
	rusticated_read(uintptr(unsafe.Pointer(&ov)), handle, &p[0], uint32(len(p)))
	runtime.KeepAlive(p)
	awaitOverlapped(&ov)
	if ov.error != 0 {
		return 0, errnoErr(Errno(ov.error))
	}
	return int(ov.resultExt), nil
}

func Write(fd int, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return 0, errnoErr(err)
	}
	var ov Overlapped
	rusticated_write(uintptr(unsafe.Pointer(&ov)), handle, &p[0], uint32(len(p)))
	runtime.KeepAlive(p)
	awaitOverlapped(&ov)
	if ov.error != 0 {
		return 0, errnoErr(Errno(ov.error))
	}
	return int(ov.resultExt), nil
}

func ReadDir(fd int, buf []byte, cookie Dircookie) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	handle, err := fdToHandle(int32(fd))
	if err != 0 {
		return 0, errnoErr(err)
	}
	var ov Overlapped
	rusticated_dir_read(uintptr(unsafe.Pointer(&ov)), handle, &buf[0], uint32(len(buf)))
	runtime.KeepAlive(buf)
	awaitOverlapped(&ov)
	if ov.error != 0 {
		return 0, errnoErr(Errno(ov.error))
	}
	return int(ov.resultExt), nil
}

func RandomGet(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	rusticated_random_get(&b[0], uint32(len(b)))
	return nil
}

func Seek(fd int, offset int64, whence int) (int64, error) { return 0, ENOSYS }
func Pread(fd int, b []byte, offset int64) (int, error) { return 0, ENOSYS }
func Pwrite(fd int, b []byte, offset int64) (int, error) { return 0, ENOSYS }
func Dup(fd int) (int, error) { return 0, ENOSYS }
func Dup2(fd int, newfd int) error { return ENOSYS }
func Pipe(fd []int) error { return ENOSYS }
func SetNonblock(fd int, nonblocking bool) error { return nil }

func init() {}
