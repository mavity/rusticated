package dload

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	RTLD_LAZY   = 0x1
	RTLD_NOW    = 0x2
	RTLD_GLOBAL = 0x100
	RTLD_LOCAL  = 0x0
)

const (
	WasmTypeI32 byte = 0x7F
	WasmTypeI64 byte = 0x7E
	WasmTypeF32 byte = 0x7D
	WasmTypeF64 byte = 0x7C
)

type handleEntry struct {
	client    *WorkerClient
	rawHandle uint64
}

var (
	handlesMu    sync.RWMutex
	handles      = make(map[uint64]handleEntry)
	handleSeq    uint64

	symsMu sync.RWMutex
	syms   = make(map[uint64]*WorkerClient)

	defaultClient     *WorkerClient
	defaultClientOnce sync.Once

	cbEntriesMu sync.Mutex
	cbEntries   = make(map[unsafe.Pointer]uint64)
)

func clientForSym(sym uint64) (*WorkerClient, error) {
	symsMu.RLock()
	client, ok := syms[sym]
	symsMu.RUnlock()
	if ok {
		return client, nil
	}
	return getDefaultClient()
}

func getDefaultClient() (*WorkerClient, error) {
	var err error
	defaultClientOnce.Do(func() {
		defaultClient, err = getWorkerForPath("")
	})
	if err != nil {
		return nil, err
	}
	if defaultClient == nil {
		return getWorkerForPath("")
	}
	return defaultClient, nil
}

func clientForHandle(libHandle uint64) (*WorkerClient, uint64, error) {
	handlesMu.RLock()
	entry, ok := handles[libHandle]
	handlesMu.RUnlock()
	if ok {
		return entry.client, entry.rawHandle, nil
	}
	client, err := getDefaultClient()
	if err != nil {
		return nil, 0, err
	}
	return client, libHandle, nil
}

func normalizeNativeLibraryPath(path string) string {
	if len(path) >= 4 && (path[0] == '/' || path[0] == '\\') &&
		((path[1] >= 'A' && path[1] <= 'Z') || (path[1] >= 'a' && path[1] <= 'z')) &&
		path[2] == ':' && (path[3] == '/' || path[3] == '\\') {
		out := make([]byte, len(path)-1)
		for i := 1; i < len(path); i++ {
			if path[i] == '/' {
				out[i-1] = '\\'
			} else {
				out[i-1] = path[i]
			}
		}
		return string(out)
	}
	return path
}

func DylibOpen(path string, flags int) (uint64, error) {
	path = normalizeNativeLibraryPath(path)
	client, err := getWorkerForPath(path)
	if err != nil {
		return 0, err
	}

	payload := make([]byte, 4+4+len(path))
	binary.LittleEndian.PutUint32(payload[0:4], uint32(flags))
	binary.LittleEndian.PutUint32(payload[4:8], uint32(len(path)))
	copy(payload[8:], path)

	resp, err := client.roundTrip(opOpenReq, payload)
	if err != nil {
		return 0, err
	}
	if len(resp.body) < 13 {
		return 0, errors.New("malformed OpenResp")
	}
	if resp.body[0] != 0 {
		errLen := binary.LittleEndian.Uint32(resp.body[9:13])
		return 0, errors.New(string(resp.body[13 : 13+errLen]))
	}
	rawHandle := binary.LittleEndian.Uint64(resp.body[1:9])
	hID := atomic.AddUint64(&handleSeq, 1)

	handlesMu.Lock()
	handles[hID] = handleEntry{client: client, rawHandle: rawHandle}
	handlesMu.Unlock()

	return hID, nil
}

func DylibSym(handle uint64, name string) (uint64, error) {
	client, rawH, err := clientForHandle(handle)
	if err != nil {
		return 0, err
	}

	payload := make([]byte, 8+4+len(name))
	binary.LittleEndian.PutUint64(payload[0:8], rawH)
	binary.LittleEndian.PutUint32(payload[8:12], uint32(len(name)))
	copy(payload[12:], name)

	resp, err := client.roundTrip(opSymReq, payload)
	if err != nil {
		return 0, err
	}
	if len(resp.body) < 13 {
		return 0, errors.New("malformed SymResp")
	}
	if resp.body[0] != 0 {
		errLen := binary.LittleEndian.Uint32(resp.body[9:13])
		return 0, errors.New(string(resp.body[13 : 13+errLen]))
	}
	sym := binary.LittleEndian.Uint64(resp.body[1:9])
	symsMu.Lock()
	syms[sym] = client
	symsMu.Unlock()
	return sym, nil
}

func DylibClose(handle uint64) error {
	handlesMu.Lock()
	entry, ok := handles[handle]
	if ok {
		delete(handles, handle)
	}
	handlesMu.Unlock()
	if !ok {
		return nil
	}

	payload := make([]byte, 8)
	binary.LittleEndian.PutUint64(payload[0:8], entry.rawHandle)
	resp, err := entry.client.roundTrip(opCloseReq, payload)
	if err != nil {
		return err
	}
	if len(resp.body) > 0 && resp.body[0] != 0 {
		if len(resp.body) >= 5 {
			errLen := binary.LittleEndian.Uint32(resp.body[1:5])
			return errors.New(string(resp.body[5 : 5+errLen]))
		}
		return errors.New("close failed")
	}
	return nil
}

func DylibCall(sym uint64, sig []byte, args []uint64, parentInvID uint64) ([]uint64, error) {
	client, err := clientForSym(sym)
	if err != nil {
		return nil, err
	}
	if parentInvID == 0 {
		// Serialize outer (non-reentrant) calls behind any in-flight Go callback
		// so opCbReturn is never starved behind an outer C call that waits on the
		// callback thread's C++ mutex.
		client.waitNoActiveCallback()
	}

	payload := make([]byte, 8+8+4+len(sig)+4+len(args)*8)
	binary.LittleEndian.PutUint64(payload[0:8], sym)
	binary.LittleEndian.PutUint64(payload[8:16], parentInvID)
	binary.LittleEndian.PutUint32(payload[16:20], uint32(len(sig)))
	copy(payload[20:20+len(sig)], sig)
	off := 20 + len(sig)
	binary.LittleEndian.PutUint32(payload[off:off+4], uint32(len(args)))
	off += 4
	for _, a := range args {
		binary.LittleEndian.PutUint64(payload[off:off+8], a)
		off += 8
	}

	resp, err := client.roundTrip(opCallReq, payload)
	if err != nil {
		return nil, err
	}
	if len(resp.body) < 5 {
		return nil, errors.New("malformed CallResp")
	}
	if resp.body[0] != 0 {
		return nil, errors.New("call failed")
	}
	resCount := int(binary.LittleEndian.Uint32(resp.body[1:5]))
	results := make([]uint64, resCount)
	for i := 0; i < resCount; i++ {
		rOff := 5 + i*8
		if rOff+8 <= len(resp.body) {
			results[i] = binary.LittleEndian.Uint64(resp.body[rOff : rOff+8])
		}
	}
	return results, nil
}

func DylibAlloc(libHandle uint64, size uint64) (uint64, error) {
	client, _, err := clientForHandle(libHandle)
	if err != nil {
		return 0, err
	}

	payload := make([]byte, 8+4)
	binary.LittleEndian.PutUint64(payload[0:8], size)
	binary.LittleEndian.PutUint32(payload[8:12], 16) // 16-byte align

	resp, err := client.roundTrip(opAllocReq, payload)
	if err != nil {
		return 0, err
	}
	if len(resp.body) < 9 {
		return 0, errors.New("malformed AllocResp")
	}
	if resp.body[0] != 0 {
		return 0, errors.New("alloc failed")
	}
	return binary.LittleEndian.Uint64(resp.body[1:9]), nil
}

func DylibFree(libHandle uint64, ptr uint64) error {
	client, _, err := clientForHandle(libHandle)
	if err != nil {
		return err
	}

	payload := make([]byte, 8)
	binary.LittleEndian.PutUint64(payload[0:8], ptr)

	_, err = client.roundTrip(opFreeReq, payload)
	return err
}

func DylibReadMem(libHandle uint64, ptr uint64, dst []byte) error {
	if len(dst) == 0 {
		return nil
	}
	client, _, err := clientForHandle(libHandle)
	if err != nil {
		return err
	}

	payload := make([]byte, 8+4)
	binary.LittleEndian.PutUint64(payload[0:8], ptr)
	binary.LittleEndian.PutUint32(payload[8:12], uint32(len(dst)))

	resp, err := client.roundTrip(opReadMemReq, payload)
	if err != nil {
		return err
	}
	if len(resp.body) < 5 {
		return errors.New("malformed ReadMemResp")
	}
	if resp.body[0] != 0 {
		return errors.New("read_mem failed")
	}
	dataLen := int(binary.LittleEndian.Uint32(resp.body[1:5]))
	if len(resp.body) < 5+dataLen {
		return errors.New("truncated ReadMemResp")
	}
	copy(dst, resp.body[5:5+dataLen])
	return nil
}

func DylibWriteMem(libHandle uint64, ptr uint64, src []byte) error {
	if len(src) == 0 {
		return nil
	}
	client, _, err := clientForHandle(libHandle)
	if err != nil {
		return err
	}

	payload := make([]byte, 8+4+len(src))
	binary.LittleEndian.PutUint64(payload[0:8], ptr)
	binary.LittleEndian.PutUint32(payload[8:12], uint32(len(src)))
	copy(payload[12:], src)

	resp, err := client.roundTrip(opWriteMemReq, payload)
	if err != nil {
		return err
	}
	if len(resp.body) > 0 && resp.body[0] != 0 {
		return errors.New("write_mem failed")
	}
	return nil
}

func DylibRegisterCallback(libHandle uint64, sig []byte, cbOvPtr unsafe.Pointer, _ uint32, fn func(args []uint64) []uint64) (uint64, error) {
	return DylibRegisterCallbackWithContext(libHandle, sig, cbOvPtr, 0, func(_ uint64, _ uint64, args []uint64) []uint64 {
		if fn == nil {
			return nil
		}
		return fn(args)
	})
}

func DylibRegisterCallbackWithContext(libHandle uint64, sig []byte, cbOvPtr unsafe.Pointer, _ uint32, fn CallbackHandler) (uint64, error) {
	client, _, err := clientForHandle(libHandle)
	if err != nil {
		return 0, err
	}

	cbID := client.registerCallback(fn)
	if cbOvPtr != nil {
		cbEntriesMu.Lock()
		cbEntries[cbOvPtr] = cbID
		cbEntriesMu.Unlock()
	}

	payload := make([]byte, 8+4+len(sig))
	binary.LittleEndian.PutUint64(payload[0:8], cbID)
	binary.LittleEndian.PutUint32(payload[8:12], uint32(len(sig)))
	copy(payload[12:], sig)

	resp, err := client.roundTrip(opCbRegReq, payload)
	if err != nil {
		client.unregisterCallback(cbID)
		return 0, err
	}
	if len(resp.body) < 9 || resp.body[0] != 0 {
		client.unregisterCallback(cbID)
		return 0, errors.New("register callback failed")
	}
	trampoline := binary.LittleEndian.Uint64(resp.body[1:9])
	symsMu.Lock()
	syms[trampoline] = client
	symsMu.Unlock()
	return trampoline, nil
}

func DylibUnregisterCallback(cbOvPtr unsafe.Pointer) {
	cbEntriesMu.Lock()
	cbID, ok := cbEntries[cbOvPtr]
	if ok {
		delete(cbEntries, cbOvPtr)
	}
	cbEntriesMu.Unlock()

	if ok {
		if c, err := getDefaultClient(); err == nil && c != nil {
			c.unregisterCallback(cbID)
		}
	}
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
