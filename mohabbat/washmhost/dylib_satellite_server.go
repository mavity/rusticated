package main

import (
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var (
	satSrvHandles          = make(map[uint64]interface{})
	satSrvNext      uint64 = 1
	satSrvMu        sync.Mutex
	satSrvEncoder   *gob.Encoder
	satSrvCallbacks        = make(map[uint32]chan int64)
	satSrvCbInvoc   uint32 = 1
)

type SatCbState struct {
	Handle     uint64
	Trampoline uintptr
	Sig        CallbackSig
}

func runSatellite() {
	decoder := gob.NewDecoder(os.Stdin)
	satSrvEncoder = gob.NewEncoder(os.Stdout)

	// Disable stdout/stderr so we don't corrupt the protocol
	os.Stdout = os.Stderr

	for {
		var req DylibRequest
		err := decoder.Decode(&req)
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "satellite read error: %v\n", err)
			}
			os.Exit(0)
		}

		go handleSatelliteRequest(req)
	}
}

func handleSatelliteRequest(req DylibRequest) {
	resp := DylibResponse{ID: req.ID}

	switch req.Op {
	case "Open":
		lib, err := dlopen(req.Path, 0x2|0x8)
		if err != nil {
			resp.ErrCode = mapErrno(err)
		} else {
			if sym, err := dlsym(lib, "FLAGS_minloglevel"); err == nil && sym != 0 {
				*(*int32)(unsafe.Pointer(sym)) = 2
			}
			satSrvMu.Lock()
			id := satSrvNext
			satSrvNext++
			satSrvHandles[id] = lib
			satSrvMu.Unlock()
			resp.Handle = id
		}

	case "Sym":
		satSrvMu.Lock()
		libAny, ok := satSrvHandles[req.LibHandle]
		satSrvMu.Unlock()
		if ok {
			sym, err := dlsym(libAny.(uintptr), req.Name)
			if err == nil && sym != 0 {
				satSrvMu.Lock()
				id := satSrvNext
				satSrvNext++
				satSrvHandles[id] = sym
				satSrvMu.Unlock()
				resp.Handle = id
			} else {
				resp.ErrCode = mapErrno(err)
			}
		} else {
			resp.ErrCode = 9 // EBADF
		}

	case "Call":
		satSrvMu.Lock()
		symAny, ok := satSrvHandles[req.SymHandle]
		satSrvMu.Unlock()
		if !ok {
			resp.ErrCode = 9
			break
		}

		descBuf := req.DescBuf
		_ = descBuf[0]
		argCount := descBuf[1]

		offset := 4
		var puregoArgs []uintptr
		var pinReferences []interface{}
		bufIdx := 0

		for i := byte(0); i < argCount; i++ {
			if offset >= len(descBuf) {
				resp.ErrCode = 22 // EINVAL
				break
			}
			tag := descBuf[offset]
			offset++

			switch tag {
			case 0x01: // i32
				val := int32(binary.LittleEndian.Uint32(descBuf[offset : offset+4]))
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 4
			case 0x02: // i64
				val := int64(binary.LittleEndian.Uint64(descBuf[offset : offset+8]))
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 8
			case 0x03: // u32
				val := binary.LittleEndian.Uint32(descBuf[offset : offset+4])
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 4
			case 0x04: // u64
				val := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 8
			case 0x05: // f32
				valBits := binary.LittleEndian.Uint32(descBuf[offset : offset+4])
				puregoArgs = append(puregoArgs, uintptr(valBits))
				offset += 4
			case 0x06: // f64
				valBits := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
				puregoArgs = append(puregoArgs, uintptr(valBits))
				offset += 8
			case 0x07: // ptr
				val := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 8
			case 0x08, 0x09: // buf, cstr
				_ = binary.LittleEndian.Uint32(descBuf[offset : offset+4])
				gLen := binary.LittleEndian.Uint32(descBuf[offset+4 : offset+8])
				offset += 8

				if bufIdx < len(req.BufParams) {
					hostBuf := req.BufParams[bufIdx]
					pinReferences = append(pinReferences, hostBuf)
					var addr uintptr
					if gLen > 0 {
						addr = uintptr(unsafe.Pointer(&hostBuf[0]))
					}
					puregoArgs = append(puregoArgs, addr)
					bufIdx++
				}
			case 0x0A: // cb
				cbHandle := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
				offset += 8

				satSrvMu.Lock()
				cbAny, cbOk := satSrvHandles[cbHandle]
				satSrvMu.Unlock()
				if cbOk {
					puregoArgs = append(puregoArgs, cbAny.(*SatCbState).Trampoline)
				}
			}
		}

		if resp.ErrCode == 0 {
			r1, _, _ := purego.SyscallN(symAny.(uintptr), puregoArgs...)
			_ = pinReferences

			resp.Result = uint64(r1)
			resp.BufParams = req.BufParams // Contains mutated buffers
		}

	case "ReadCstr":
		hostPtr := req.Ptr
		var buf []byte
		if hostPtr != 0 {
			for i := uintptr(0); ; i++ {
				b := *(*byte)(unsafe.Pointer(uintptr(hostPtr) + i))
				if b == 0 {
					break
				}
				buf = append(buf, b)
				if uint32(len(buf)) >= req.MaxLen {
					break
				}
			}
		}
		resp.BytesRes = buf

	case "CallbackCreate":
closure := makeSatCallbackClosure(req.CbHandle, req.ArgCount, req.RetType, req.ArgTypes, req.GuestFnName)
if closure != nil {
trampoline := purego.NewCallback(closure)
satSrvMu.Lock()
satSrvHandles[req.CbHandle] = &SatCbState{
Handle: req.CbHandle,
Trampoline: trampoline,
Sig: CallbackSig{
ArgCount: req.ArgCount,
RetType:  req.RetType,
ArgTypes: req.ArgTypes,
},
}
satSrvMu.Unlock()
resp.ErrCode = 0
} else {
resp.ErrCode = 1 // ENOSYS
}

case "CallbackRespond":
		satSrvMu.Lock()
		ch, ok := satSrvCallbacks[req.InvocId]
		if ok {
			delete(satSrvCallbacks, req.InvocId)
		}
		satSrvMu.Unlock()
		if ok {
			ch <- req.CbRet
		}
		return // We don't send a response for this
	}

	satSrvMu.Lock()
	satSrvEncoder.Encode(&resp)
	satSrvMu.Unlock()
}

func makeSatCallbackClosure(cbHandle uint64, argCount uint8, retType uint8, argTypes []uint8, guestFnName string) interface{} {
dispatch := func(args []uintptr) uintptr {
satSrvMu.Lock()
invocId := satSrvCbInvoc
satSrvCbInvoc++
ch := make(chan int64, 1)
satSrvCallbacks[invocId] = ch

var uintArgs []uint64
var bufs [][]byte
for i, a := range args {
uintArgs = append(uintArgs, uint64(a))
tag := uint8(0)
if i < len(argTypes) { tag = argTypes[i] }

if tag == 0x09 && a != 0 {
var buf []byte
for offset := uintptr(0); offset < 1048576; offset++ {
b := *(*byte)(unsafe.Pointer(a + offset))
if b == 0 { break }
buf = append(buf, b)
}
bufs = append(bufs, buf)
} else if tag == 0x0B && a != 0 {
strPtr := *(*uintptr)(unsafe.Pointer(a))
isFinal := *(*byte)(unsafe.Pointer(a + 8))
var buf []byte
if strPtr != 0 {
for offset := uintptr(0); offset < 1048576; offset++ {
b := *(*byte)(unsafe.Pointer(strPtr + offset))
if b == 0 { break }
buf = append(buf, b)
}
}
buf = append(buf, isFinal) // append isFinal explicitly as last byte
bufs = append(bufs, buf)
}
}

req := DylibResponse{
IsCallback: true,
CbHandle:   cbHandle,
CbArgs:     uintArgs,
CbInvocId:  invocId,
BufParams:  bufs,
}
satSrvEncoder.Encode(&req)
satSrvMu.Unlock()

ret := <-ch
return uintptr(ret)
}

switch argCount {
case 0: return func() uintptr { return dispatch([]uintptr{}) }
case 1: return func(a1 uintptr) uintptr { return dispatch([]uintptr{a1}) }
case 2: return func(a1, a2 uintptr) uintptr { return dispatch([]uintptr{a1, a2}) }
case 3: return func(a1, a2, a3 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3}) }
case 4: return func(a1, a2, a3, a4 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3, a4}) }
case 5: return func(a1, a2, a3, a4, a5 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3, a4, a5}) }
case 6: return func(a1, a2, a3, a4, a5, a6 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3, a4, a5, a6}) }
case 7: return func(a1, a2, a3, a4, a5, a6, a7 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3, a4, a5, a6, a7}) }
case 8: return func(a1, a2, a3, a4, a5, a6, a7, a8 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3, a4, a5, a6, a7, a8}) }
}
return nil
}