//go:build wasip1

package syscall

import (
	"encoding/binary"
	"io"
	"sync"
	"unsafe"
	_ "unsafe"
)

const (
	RTLD_LAZY   = 0x1
	RTLD_NOW    = 0x2
	RTLD_GLOBAL = 0x100
	RTLD_LOCAL  = 0x0
)

//go:wasmimport env dylib_open
func rusticated_dylib_open(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, flags uint32)

//go:wasmimport env dylib_sym
func rusticated_dylib_sym(overlapped unsafe.Pointer, libHandle uint64, namePtr *byte, nameLen uint32)

//go:wasmimport env dylib_close
func rusticated_dylib_close(overlapped unsafe.Pointer, libHandle uint64)

//go:wasmimport env dylib_call
func rusticated_dylib_call(overlapped unsafe.Pointer, symHandle uint64, sigPtr *byte, sigLen uint32, argsPtr *byte, argsCount uint32, parentInvID uint64, resultsPtr *byte, resultsCap uint32)

//go:wasmimport env dylib_alloc
func rusticated_dylib_alloc(overlapped unsafe.Pointer, libHandle uint64, size uint64, align uint32)

//go:wasmimport env dylib_free
func rusticated_dylib_free(overlapped unsafe.Pointer, libHandle uint64, ptr uint64)

//go:wasmimport env dylib_read_mem
func rusticated_dylib_read_mem(overlapped unsafe.Pointer, libHandle uint64, hostPtr uint64, guestBufPtr *byte, length uint32)

//go:wasmimport env dylib_write_mem
func rusticated_dylib_write_mem(overlapped unsafe.Pointer, libHandle uint64, hostPtr uint64, guestBufPtr *byte, length uint32)

//go:wasmimport env dylib_callback_register
func rusticated_dylib_callback_register(overlapped unsafe.Pointer, libHandle uint64, sigPtr *byte, sigLen uint32, cbOvPtr unsafe.Pointer, cbOvLen uint32)

func DylibOpen(path string, flags int) (uint64, error) {
	var ctx overlappedContext
	pathBytes := []byte(path)
	var pathPtr *byte
	if len(pathBytes) > 0 {
		pathPtr = &pathBytes[0]
	}

	rusticated_dylib_open(unsafe.Pointer(&ctx.o), pathPtr, uint32(len(pathBytes)), uint32(flags))
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return 0, Errno(ctx.o.hostError)
	}
	return ctx.o.resultExt, nil
}

func DylibSym(handle uint64, name string) (uint64, error) {
	var ctx overlappedContext
	nameBytes := []byte(name)
	var namePtr *byte
	if len(nameBytes) > 0 {
		namePtr = &nameBytes[0]
	}

	rusticated_dylib_sym(unsafe.Pointer(&ctx.o), handle, namePtr, uint32(len(nameBytes)))
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return 0, Errno(ctx.o.hostError)
	}
	return ctx.o.resultExt, nil
}

func DylibClose(handle uint64) error {
	var ctx overlappedContext
	rusticated_dylib_close(unsafe.Pointer(&ctx.o), handle)
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return Errno(ctx.o.hostError)
	}
	return nil
}

func DylibAlloc(libHandle uint64, size uint64) (uint64, error) {
	var ctx overlappedContext
	rusticated_dylib_alloc(unsafe.Pointer(&ctx.o), libHandle, size, 16)
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return 0, Errno(ctx.o.hostError)
	}
	return ctx.o.resultExt, nil
}

func DylibFree(libHandle uint64, ptr uint64) error {
	var ctx overlappedContext
	rusticated_dylib_free(unsafe.Pointer(&ctx.o), libHandle, ptr)
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return Errno(ctx.o.hostError)
	}
	return nil
}

func DylibReadMem(libHandle uint64, ptr uint64, dst []byte) error {
	if len(dst) == 0 {
		return nil
	}
	var ctx overlappedContext
	rusticated_dylib_read_mem(unsafe.Pointer(&ctx.o), libHandle, ptr, &dst[0], uint32(len(dst)))
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return Errno(ctx.o.hostError)
	}
	return nil
}

func DylibWriteMem(libHandle uint64, ptr uint64, src []byte) error {
	if len(src) == 0 {
		return nil
	}
	var ctx overlappedContext
	rusticated_dylib_write_mem(unsafe.Pointer(&ctx.o), libHandle, ptr, &src[0], uint32(len(src)))
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return Errno(ctx.o.hostError)
	}
	return nil
}

// ── Satellite Memory Helpers ───────────────────────────────────────────────

func Alloc(libHandle uint64, size uint64) (uint64, error) {
	return DylibAlloc(libHandle, size)
}

func Free(libHandle uint64, ptr uint64) error {
	return DylibFree(libHandle, ptr)
}

func ReadMem(libHandle uint64, ptr uint64, dst []byte) error {
	return DylibReadMem(libHandle, ptr, dst)
}

func WriteMem(libHandle uint64, ptr uint64, src []byte) error {
	return DylibWriteMem(libHandle, ptr, src)
}

func CString(libHandle uint64, s string) (uint64, func(), error) {
	data := append([]byte(s), 0)
	ptr, err := DylibAlloc(libHandle, uint64(len(data)))
	if err != nil {
		return 0, nil, err
	}
	if err := DylibWriteMem(libHandle, ptr, data); err != nil {
		_ = DylibFree(libHandle, ptr)
		return 0, nil, err
	}
	free := func() {
		_ = DylibFree(libHandle, ptr)
	}
	return ptr, free, nil
}

func DylibCString(libHandle uint64, s string) (uint64, func(), error) {
	return CString(libHandle, s)
}

func GoString(libHandle uint64, ptr uint64) (string, error) {
	if ptr == 0 {
		return "", nil
	}
	var buf []byte
	chunkSize := 64
	chunk := make([]byte, chunkSize)
	curr := ptr

	for {
		if err := DylibReadMem(libHandle, curr, chunk); err != nil {
			return "", err
		}
		for i, b := range chunk {
			if b == 0 {
				return string(buf), nil
			}
			buf = append(buf, b)
			if i == len(chunk)-1 {
				curr += uint64(chunkSize)
			}
		}
	}
}

func DylibGoString(libHandle uint64, ptr uint64) (string, error) {
	return GoString(libHandle, ptr)
}

func GoStringN(libHandle uint64, ptr uint64, length int) (string, error) {
	data, err := GoBytes(libHandle, ptr, length)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func DylibGoStringN(libHandle uint64, ptr uint64, length int) (string, error) {
	return GoStringN(libHandle, ptr, length)
}

func GoBytes(libHandle uint64, ptr uint64, length int) ([]byte, error) {
	if ptr == 0 || length == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, length)
	if err := DylibReadMem(libHandle, ptr, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func DylibGoBytes(libHandle uint64, ptr uint64, length int) ([]byte, error) {
	return GoBytes(libHandle, ptr, length)
}

// RemoteReader implements io.ReaderAt over satellite memory.
type RemoteReader struct {
	libHandle uint64
	base      uint64
	size      int64
}

func NewRemoteReader(libHandle uint64, ptr uint64, size int64) *RemoteReader {
	return &RemoteReader{libHandle: libHandle, base: ptr, size: size}
}

func (r *RemoteReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off < 0 || off >= r.size {
		return 0, io.EOF
	}
	toRead := int64(len(p))
	if off+toRead > r.size {
		toRead = r.size - off
	}
	targetPtr := r.base + uint64(off)
	if err := DylibReadMem(r.libHandle, targetPtr, p[:toRead]); err != nil {
		return 0, err
	}
	if toRead < int64(len(p)) {
		return int(toRead), io.EOF
	}
	return int(toRead), nil
}

// RemoteWriter implements io.WriterAt over satellite memory.
type RemoteWriter struct {
	libHandle uint64
	base      uint64
	size      int64
}

func NewRemoteWriter(libHandle uint64, ptr uint64, size int64) *RemoteWriter {
	return &RemoteWriter{libHandle: libHandle, base: ptr, size: size}
}

func (w *RemoteWriter) WriteAt(p []byte, off int64) (n int, err error) {
	if off < 0 || off >= w.size {
		return 0, io.ErrShortWrite
	}
	toWrite := int64(len(p))
	if off+toWrite > w.size {
		toWrite = w.size - off
	}
	targetPtr := w.base + uint64(off)
	if err := DylibWriteMem(w.libHandle, targetPtr, p[:toWrite]); err != nil {
		return 0, err
	}
	return int(toWrite), nil
}

var (
	goroutineInvMu sync.Mutex
	goroutineInvs  = make(map[uintptr]uint64)
)

func SetGoroutineParentInvID(invID uint64) {
	g := currentG()
	goroutineInvMu.Lock()
	if invID != 0 {
		goroutineInvs[g] = invID
	} else {
		delete(goroutineInvs, g)
	}
	goroutineInvMu.Unlock()
}

func CurrentParentInvID() uint64 {
	g := currentG()
	goroutineInvMu.Lock()
	id := goroutineInvs[g]
	goroutineInvMu.Unlock()
	return id
}

func DylibCall(sym uint64, sig []byte, args []uint64, parentInvID uint64) ([]uint64, error) {
	if parentInvID == 0 {
		parentInvID = CurrentParentInvID()
	}

	var ctx overlappedContext
	var sigPtr *byte
	if len(sig) > 0 {
		sigPtr = &sig[0]
	}

	var argsBuf []byte
	var argsPtr *byte
	if len(args) > 0 {
		argsBuf = make([]byte, len(args)*8)
		for i, a := range args {
			binary.LittleEndian.PutUint64(argsBuf[i*8:(i+1)*8], a)
		}
		argsPtr = &argsBuf[0]
	}

	// Parse result count from functype [0x60, param_count, params..., result_count, results...]
	resultCount := 1
	if len(sig) >= 3 && sig[0] == 0x60 {
		paramCount := int(sig[1])
		if len(sig) > 2+paramCount {
			resultCount = int(sig[2+paramCount])
		}
	}

	var resultsBuf []byte
	var resultsPtr *byte
	if resultCount > 0 {
		resultsBuf = make([]byte, resultCount*8)
		resultsPtr = &resultsBuf[0]
	}

	rusticated_dylib_call(unsafe.Pointer(&ctx.o), sym, sigPtr, uint32(len(sig)), argsPtr, uint32(len(args)), parentInvID, resultsPtr, uint32(resultCount))
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return nil, Errno(ctx.o.hostError)
	}

	results := make([]uint64, resultCount)
	if resultCount == 1 {
		results[0] = ctx.o.resultExt
	} else if resultCount > 1 {
		for i := 0; i < resultCount; i++ {
			results[i] = binary.LittleEndian.Uint64(resultsBuf[i*8 : (i+1)*8])
		}
	}
	return results, nil
}

func DylibCallbackRegister(libHandle uint64, sig []byte, cbOvPtr unsafe.Pointer, cbOvLen uint32) (uint64, error) {
	var ctx overlappedContext
	var sigPtr *byte
	if len(sig) > 0 {
		sigPtr = &sig[0]
	}

	rusticated_dylib_callback_register(unsafe.Pointer(&ctx.o), libHandle, sigPtr, uint32(len(sig)), cbOvPtr, cbOvLen)
	awaitOverlapped(&ctx)

	if ctx.o.hostError != 0 {
		return 0, Errno(ctx.o.hostError)
	}
	return ctx.o.resultExt, nil
}

func currentG() uintptr
func registerCallbackRuntime(ovPtr unsafe.Pointer, fn func(args []uint64, invID, cbID uint64)) bool
func unregisterCallbackRuntime(ovPtr unsafe.Pointer)
func pauseToHost()

// DylibRegisterCallback registers a callback overlapped with the host and hooks into runtime continuation.
// fn receives raw arguments and returns return values (or nil for void callbacks).
// Completion signaling, result writing, and yielding to the host are handled automatically when fn returns.
func DylibRegisterCallback(libHandle uint64, sig []byte, cbOvPtr unsafe.Pointer, cbOvLen uint32, fn func(args []uint64) []uint64) (uint64, error) {
	trampoline, err := DylibCallbackRegister(libHandle, sig, cbOvPtr, cbOvLen)
	if err != nil {
		return 0, err
	}
	if !registerCallbackRuntime(cbOvPtr, func(args []uint64, invID, cbID uint64) {
		SetGoroutineParentInvID(invID)
		defer SetGoroutineParentInvID(0)

		var results []uint64
		if fn != nil {
			results = fn(args)
		}

		argLen := *(*uint32)(unsafe.Pointer(uintptr(cbOvPtr) + 40))
		resultOff := uintptr(48 + argLen)
		for i, r := range results {
			*(*uint64)(unsafe.Pointer(uintptr(cbOvPtr) + resultOff + uintptr(i*8))) = r
		}
		*(*uint64)(unsafe.Pointer(uintptr(cbOvPtr) + 24)) = cbID
		*(*uint64)(unsafe.Pointer(uintptr(cbOvPtr) + 32)) = invID
		// Set completion flag to 1
		*(*uint32)(cbOvPtr) = 1
		// Yield immediately back to host so it drains the result before next re-entry
		pauseToHost()
	}) {
		return 0, Errno(ENOMEM)
	}
	return trampoline, nil
}

// DylibUnregisterCallback unhooks the callback from the runtime.
func DylibUnregisterCallback(cbOvPtr unsafe.Pointer) {
	unregisterCallbackRuntime(cbOvPtr)
}
