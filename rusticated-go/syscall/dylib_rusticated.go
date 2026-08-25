//go:build wasip1

package syscall

import (
	"encoding/binary"
	"unsafe"
)

// Dylib type tags for call descriptors and callback signatures.
const (
	DylibTagVoid byte = 0x00
	DylibTagI32  byte = 0x01
	DylibTagI64  byte = 0x02
	DylibTagU32  byte = 0x03
	DylibTagU64  byte = 0x04
	DylibTagF32  byte = 0x05
	DylibTagF64  byte = 0x06
	DylibTagPtr  byte = 0x07
	DylibTagBuf  byte = 0x08
	DylibTagCstr byte = 0x09
	DylibTagCb   byte = 0x0A
)

//go:wasmimport env dylib_open
//go:noescape
func rusticated_dylib_open(overlapped unsafe.Pointer, pathPtr *byte, pathLen uint32, flags uint32)

//go:wasmimport env dylib_sym
//go:noescape
func rusticated_dylib_sym(overlapped unsafe.Pointer, libHandle uint64, namePtr *byte, nameLen uint32)

//go:wasmimport env dylib_call
//go:noescape
func rusticated_dylib_call(overlapped unsafe.Pointer, symHandle uint64, descPtr *byte, descLen uint32)

//go:wasmimport env dylib_callback_create
//go:noescape
func rusticated_dylib_callback_create(overlapped unsafe.Pointer, libHandle uint64, sigPtr *byte, sigLen uint32, guestFnNamePtr *byte, guestFnNameLen uint32)

//go:wasmimport env dylib_callback_respond
//go:noescape
func rusticated_dylib_callback_respond(cbHandle uint64, invocationId uint32, retValue int64)

//go:wasmimport env dylib_close
//go:noescape
func rusticated_dylib_close(libHandle uint64)

//go:wasmimport env dylib_read_cstr
//go:noescape
func rusticated_dylib_read_cstr(hostPtr uint64, guestBufPtr *byte, maxLen uint32) uint32

//go:wasmimport env dylib_read_mem
//go:noescape
func rusticated_dylib_read_mem(hostPtr uint64, guestBufPtr *byte, length uint32) uint32

//go:wasmimport env dylib_callback_wait
//go:noescape
func rusticated_dylib_callback_wait(overlapped unsafe.Pointer, outPtr *byte, outLen uint32)

//go:wasmimport env dylib_callback_stop
func rusticated_dylib_callback_stop()

func DylibOpen(path string, flags uint32) (uint64, error) {
	pathBytes := []byte(path)
	var pathPtr *byte
	if len(pathBytes) > 0 {
		pathPtr = &pathBytes[0]
	}
	var ctx overlappedContext
	rusticated_dylib_open(unsafe.Pointer(&ctx.o), pathPtr, uint32(len(pathBytes)), flags)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return ctx.o.resultExt, nil
}

func DylibSym(libHandle uint64, name string) (uint64, error) {
	nameBytes := []byte(name)
	var namePtr *byte
	if len(nameBytes) > 0 {
		namePtr = &nameBytes[0]
	}
	var ctx overlappedContext
	rusticated_dylib_sym(unsafe.Pointer(&ctx.o), libHandle, namePtr, uint32(len(nameBytes)))
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return ctx.o.resultExt, nil
}

func DylibCall(symHandle uint64, desc []byte) (uint64, error) {
	var descPtr *byte
	if len(desc) > 0 {
		descPtr = &desc[0]
	}
	var ctx overlappedContext
	rusticated_dylib_call(unsafe.Pointer(&ctx.o), symHandle, descPtr, uint32(len(desc)))
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return ctx.o.resultExt, nil
}

func DylibCallbackCreate(libHandle uint64, sig []byte, guestFnName string) (uint64, error) {
	var sigPtr *byte
	if len(sig) > 0 {
		sigPtr = &sig[0]
	}
	guestFnBytes := []byte(guestFnName)
	var guestFnPtr *byte
	if len(guestFnBytes) > 0 {
		guestFnPtr = &guestFnBytes[0]
	}
	var ctx overlappedContext
	rusticated_dylib_callback_create(unsafe.Pointer(&ctx.o), libHandle, sigPtr, uint32(len(sig)), guestFnPtr, uint32(len(guestFnBytes)))
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return ctx.o.resultExt, nil
}

func DylibCallbackRespond(cbHandle uint64, invocationId uint32, retValue int64) {
	rusticated_dylib_callback_respond(cbHandle, invocationId, retValue)
}

// DylibCallbackWait blocks the calling goroutine until the host delivers a
// native callback invocation, returning its handle, invocation id and scalar
// arguments. ok is false when the wait is cancelled (DylibCallbackStop) or the
// host aborts it. The callback runs on the caller's own growable goroutine
// stack because the host delivers it as an ordinary async completion dispatched
// by the scheduler — never on the g0 stack.
func DylibCallbackWait() (cbHandle uint64, invocationId uint32, args []uint64, ok bool) {
	var buf [256]byte
	var ctx overlappedContext
	rusticated_dylib_callback_wait(unsafe.Pointer(&ctx.o), &buf[0], uint32(len(buf)))
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, 0, nil, false
	}
	cbHandle = ctx.o.resultExt
	invocationId = uint32(ctx.o.continued & 0xffffffff)
	argCount := int((ctx.o.continued >> 32) & 0xff)
	if argCount > len(buf)/8 {
		argCount = len(buf) / 8
	}
	args = make([]uint64, argCount)
	for i := 0; i < argCount; i++ {
		args[i] = binary.LittleEndian.Uint64(buf[i*8 : i*8+8])
	}
	return cbHandle, invocationId, args, true
}

// DylibCallbackStop cancels a pump goroutine parked in DylibCallbackWait so it
// can exit once the stream is complete.
func DylibCallbackStop() {
	rusticated_dylib_callback_stop()
}

func DylibClose(libHandle uint64) {
	rusticated_dylib_close(libHandle)
}

func DylibReadCstr(hostPtr uint64, buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	n := rusticated_dylib_read_cstr(hostPtr, &buf[0], uint32(len(buf)))
	return int(n)
}

func DylibReadMem(hostPtr uint64, buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	n := rusticated_dylib_read_mem(hostPtr, &buf[0], uint32(len(buf)))
	return int(n)
}
