package main

import (
	"encoding/binary"
	"fmt"
)

// WASM Functype constants
const (
	WasmTypeI32 byte = 0x7F
	WasmTypeI64 byte = 0x7E
	WasmTypeF32 byte = 0x7D
	WasmTypeF64 byte = 0x7C
)

// DecodeFuncType decodes a canonical WASM functype byte slice:
// [0x60, param_count, params..., result_count, results...]
func DecodeFuncType(data []byte) (params []byte, results []byte, err error) {
	if len(data) < 3 {
		return nil, nil, fmt.Errorf("functype too short: %d bytes", len(data))
	}
	if data[0] != 0x60 {
		return nil, nil, fmt.Errorf("invalid functype prefix: 0x%02x (expected 0x60)", data[0])
	}
	p := 1
	paramCount := int(data[p])
	p++
	if p+paramCount > len(data) {
		return nil, nil, fmt.Errorf("truncated params in functype")
	}
	params = make([]byte, paramCount)
	copy(params, data[p:p+paramCount])
	p += paramCount

	if p >= len(data) {
		return nil, nil, fmt.Errorf("missing result count in functype")
	}
	resultCount := int(data[p])
	p++
	if p+resultCount > len(data) {
		return nil, nil, fmt.Errorf("truncated results in functype")
	}
	results = make([]byte, resultCount)
	copy(results, data[p:p+resultCount])
	return params, results, nil
}

// EncodeFuncType encodes params and results into canonical WASM functype byte slice.
func EncodeFuncType(params []byte, results []byte) []byte {
	buf := make([]byte, 0, 3+len(params)+len(results))
	buf = append(buf, 0x60)
	buf = append(buf, byte(len(params)))
	buf = append(buf, params...)
	buf = append(buf, byte(len(results)))
	buf = append(buf, results...)
	return buf
}

// ── Overlapped Transport Layout For Callbacks ──────────────────────────────
//
// Layout (minimum 64 bytes, generous 512 bytes):
//   0..4:   flags (uint32) - bit 0 = completed
//   4..8:   hostError (uint32)
//   8..16:  continued (uint64)
//   16..24: resultExt (uint64)
//   24..32: callbackID (uint64)
//   32..40: invocationID (uint64)
//   40..44: argLen (uint32) - in bytes (len(params) * 8)
//   44..48: resultLen (uint32) - in bytes (len(results) * 8)
//   48..48+argLen: raw arguments ([]uint64)
//   48+argLen..48+argLen+resultLen: raw results ([]uint64)

const (
	CbOvHeaderSize = 48
	CbOvTotalSize  = 512
)

// ReadCallbackOverlappedHeader reads metadata from a guest callback overlapped.
func ReadCallbackOverlappedHeader(buf []byte) (flags, hostError uint32, cbID, invID uint64, argLen, resultLen uint32) {
	flags = binary.LittleEndian.Uint32(buf[0:4])
	hostError = binary.LittleEndian.Uint32(buf[4:8])
	cbID = binary.LittleEndian.Uint64(buf[24:32])
	invID = binary.LittleEndian.Uint64(buf[32:40])
	argLen = binary.LittleEndian.Uint32(buf[40:44])
	resultLen = binary.LittleEndian.Uint32(buf[44:48])
	return
}

// WriteCallbackOverlappedInvocation writes an incoming invocation into a guest callback overlapped.
func WriteCallbackOverlappedInvocation(buf []byte, cbID, invID uint64, args []uint64, resultLen uint32) {
	argLen := uint32(len(args) * 8)
	binary.LittleEndian.PutUint32(buf[0:4], 1) // Completed bit = 1 (new invocation ready)
	binary.LittleEndian.PutUint32(buf[4:8], 0) // hostError = 0
	binary.LittleEndian.PutUint64(buf[8:16], 0)
	binary.LittleEndian.PutUint64(buf[16:24], 0)
	binary.LittleEndian.PutUint64(buf[24:32], cbID)
	binary.LittleEndian.PutUint64(buf[32:40], invID)
	binary.LittleEndian.PutUint32(buf[40:44], argLen)
	binary.LittleEndian.PutUint32(buf[44:48], resultLen)

	off := CbOvHeaderSize
	for _, arg := range args {
		binary.LittleEndian.PutUint64(buf[off:off+8], arg)
		off += 8
	}
}

// ReadCallbackOverlappedResults reads the return values written by the guest into the callback overlapped.
func ReadCallbackOverlappedResults(buf []byte, resultCount int) []uint64 {
	argLen := binary.LittleEndian.Uint32(buf[40:44])
	off := int(CbOvHeaderSize + argLen)
	results := make([]uint64, resultCount)
	for i := 0; i < resultCount; i++ {
		if off+8 <= len(buf) {
			results[i] = binary.LittleEndian.Uint64(buf[off : off+8])
			off += 8
		}
	}
	return results
}

// ── Satellite IPC Messages ─────────────────────────────────────────────────

type DylibOpenReq struct {
	ReqID uint64
	Path  string
	Flags int
}

type DylibOpenResp struct {
	ReqID  uint64
	Handle uint64
	Error  string
}

type DylibSymReq struct {
	ReqID  uint64
	Handle uint64
	Name   string
}

type DylibSymResp struct {
	ReqID  uint64
	Symbol uint64
	Error  string
}

type DylibCloseReq struct {
	ReqID  uint64
	Handle uint64
}

type DylibCloseResp struct {
	ReqID uint64
	Error string
}

type DylibCallReq struct {
	ReqID              uint64
	Symbol             uint64
	SigBytes           []byte
	Args               []uint64
	ParentInvocationID uint64
}

type DylibCallResp struct {
	ReqID   uint64
	Results []uint64
	Error   string
}

type DylibAllocReq struct {
	ReqID uint64
	Size  uint64
	Align uint32
}

type DylibAllocResp struct {
	ReqID uint64
	Ptr   uint64
	Error string
}

type DylibFreeReq struct {
	ReqID uint64
	Ptr   uint64
}

type DylibFreeResp struct {
	ReqID uint64
	Error string
}

type DylibReadMemReq struct {
	ReqID uint64
	Ptr   uint64
	Len   uint32
}

type DylibReadMemResp struct {
	ReqID uint64
	Data  []byte
	Error string
}

type DylibWriteMemReq struct {
	ReqID uint64
	Ptr   uint64
	Data  []byte
}

type DylibWriteMemResp struct {
	ReqID uint64
	Error string
}

type DylibCbRegisterReq struct {
	ReqID    uint64
	CbID     uint64
	SigBytes []byte
}

type DylibCbRegisterResp struct {
	ReqID      uint64
	Trampoline uint64
	Error      string
}

// DylibCbInvokeEvent is sent by Satellite to Host when a native callback trampoline is invoked.
type DylibCbInvokeEvent struct {
	CbID     uint64
	InvID    uint64
	ThreadID uint64
	Args     []uint64
}

// DylibCbReturnReq is sent by Host to Satellite to deliver a completed callback's return payload.
type DylibCbReturnReq struct {
	InvID   uint64
	Results []uint64
	Error   string
}

// DylibCbReturnResp is the Satellite's acknowledgment of DylibCbReturnReq.
type DylibCbReturnResp struct {
	InvID uint64
	Error string
}

type DylibHandshake struct {
	IsHandshake bool
	HGOOS       string
	HGOARCH     string
}

// IPCEnvelope wraps all possible IPC messages for type-safe transmission.
type IPCEnvelope struct {
	Handshake      *DylibHandshake
	OpenReq        *DylibOpenReq
	OpenResp       *DylibOpenResp
	SymReq         *DylibSymReq
	SymResp        *DylibSymResp
	CloseReq       *DylibCloseReq
	CloseResp      *DylibCloseResp
	CallReq        *DylibCallReq
	CallResp       *DylibCallResp
	AllocReq       *DylibAllocReq
	AllocResp      *DylibAllocResp
	FreeReq        *DylibFreeReq
	FreeResp       *DylibFreeResp
	ReadMemReq     *DylibReadMemReq
	ReadMemResp    *DylibReadMemResp
	WriteMemReq    *DylibWriteMemReq
	WriteMemResp   *DylibWriteMemResp
	CbRegisterReq  *DylibCbRegisterReq
	CbRegisterResp *DylibCbRegisterResp
	CbInvokeEvent  *DylibCbInvokeEvent
	CbReturnReq    *DylibCbReturnReq
	CbReturnResp   *DylibCbReturnResp
}
