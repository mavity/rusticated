//go:build wasip1

package main

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// tokenCallbackRegistry maps opaque IDs to Go callbacks for streaming tokens.
var wasmTokenCallbacks = make(map[uint64]func(string))

// wasmTokenDone maps opaque IDs to a channel signalled when the DLL delivers the
// final chunk of a stream. Completion is decided here in the guest (business
// logic), never in the marshalling layer.
var wasmTokenDone = make(map[uint64]chan struct{})
var wasmTokenNextID uint64

var wasmErrorCount int32

func reportFFIError(userData uint64, errMsg string) {
	count := atomic.AddInt32(&wasmErrorCount, 1)
	if count > 10 {
		return
	}
	if fn, ok := wasmTokenCallbacks[userData]; ok {
		if count == 10 {
			fn(errMsg + " \x1b[31m[Too many FFI errors. Silencing further reports.]\x1b[0m ")
		} else {
			fn(errMsg)
		}
	} else {
		println("FFI Error (no callback registered): " + errMsg)
	}
}

// wasmCallbackScratchBuf is a guest-side buffer used by the host to copy
// native C strings into guest linear memory before calling guest exports.
var wasmCallbackScratchBuf [65536]byte

//go:wasmexport wasmCallbackScratchAddr
func wasmCallbackScratchAddr() uint32 {
	return uint32(uintptr(unsafe.Pointer(&wasmCallbackScratchBuf[0])))
}

// wasmTokenCallback is invoked by the host for each streaming token.
// The C signature is:
//
//	void callback(void* userData, const char* text, bool isDone, const char* statsJSON)
//
// CStr arguments are pre-marshalled into guest scratch memory by marshalCallbackArgs,
// so `text` is a guest pointer to a NUL-terminated string — read it directly.
//
//go:wasmexport wasmTokenCallback
func wasmTokenCallback(userData, text, isDone, statsJSON uint64) uint64 {
	if text != 0 {
		// text is a guest pointer to a NUL-terminated string in scratch memory.
		ptr := uintptr(text)
		var buf [8192]byte
		n := 0
		for n < len(buf) {
			b := *(*byte)(unsafe.Pointer(ptr + uintptr(n)))
			if b == 0 {
				break
			}
			buf[n] = b
			n++
		}
		if n > 0 {
			if fn, ok := wasmTokenCallbacks[userData]; ok {
				fn(string(buf[:n]))
			}
		}
	}
	if isDone != 0 {
		if ch, ok := wasmTokenDone[userData]; ok {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	return 0
}

type callDescBuilder struct {
	buf []byte
}

func newCallDesc(retType byte) *callDescBuilder {
	return &callDescBuilder{
		buf: []byte{0, 0, retType, 0},
	}
}

func (c *callDescBuilder) pushPtr(val uint64) {
	c.buf[1]++
	c.buf = append(c.buf, syscall.DylibTagPtr)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], val)
	c.buf = append(c.buf, tmp[:]...)
}

func (c *callDescBuilder) pushCstr(ptr, len uint32) {
	c.buf[1]++
	c.buf = append(c.buf, syscall.DylibTagCstr)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], (uint64(len)<<32)|uint64(ptr))
	c.buf = append(c.buf, tmp[:]...)
}

func (c *callDescBuilder) pushCb(cbHandle uint64) {
	c.buf[1]++
	c.buf = append(c.buf, syscall.DylibTagCb)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], cbHandle)
	c.buf = append(c.buf, tmp[:]...)
}

func (c *callDescBuilder) bytes() []byte {
	return c.buf
}

func cstrArg(s string) (uint32, uint32) {
	ptr := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	return uint32(ptr), uint32(len(s))
}

// Global FFI dynamic library state
var (
	libOnce sync.Once
	libErr  error
	lib     uint64

	symSettingsCreate       uint64
	symSettingsDelete       uint64
	symEngineCreate         uint64
	symCfgCreate            uint64
	symCfgDelete            uint64
	symConvCreate           uint64
	symConvSend             uint64
	symSetLogLevel          uint64
	symGetDefaultLogger     uint64
	symSetMinLoggerSeverity uint64
	symUseSinkLogger        uint64
	symClearSinkLogger      uint64
)

// ensureLibLoaded opens the dylib and resolves symbols once.
func ensureLibLoaded(libPath string) error {
	libOnce.Do(func() {
		var err error
		lib, err = syscall.DylibOpen(libPath, 0)
		if err != nil {
			libErr = fmt.Errorf("failed to open %s: %w", libPath, err)
			return
		}

		resolve := func(name string) uint64 {
			if libErr != nil {
				return 0
			}
			sym, err := syscall.DylibSym(lib, name)
			if err != nil {
				libErr = fmt.Errorf("sym %s: %w", name, err)
				return 0
			}
			return sym
		}

		symSettingsCreate = resolve("litert_lm_engine_settings_create")
		symSettingsDelete = resolve("litert_lm_engine_settings_delete")
		symEngineCreate = resolve("litert_lm_engine_create")
		symCfgCreate = resolve("litert_lm_conversation_config_create")
		symCfgDelete = resolve("litert_lm_conversation_config_delete")
		symConvCreate = resolve("litert_lm_conversation_create")
		symConvSend = resolve("litert_lm_conversation_send_message_stream")

		// Optional logging symbols
		if sym, err := syscall.DylibSym(lib, "litert_lm_set_min_log_level"); err == nil {
			symSetLogLevel = sym
		}
		if sym, err := syscall.DylibSym(lib, "LiteRtGetDefaultLogger"); err == nil {
			symGetDefaultLogger = sym
		}
		if sym, err := syscall.DylibSym(lib, "LiteRtSetMinLoggerSeverity"); err == nil {
			symSetMinLoggerSeverity = sym
		}
		if sym, err := syscall.DylibSym(lib, "LiteRtUseSinkLogger"); err == nil {
			symUseSinkLogger = sym
		}
		if sym, err := syscall.DylibSym(lib, "LiteRtClearSinkLogger"); err == nil {
			symClearSinkLogger = sym
		}
	})
	return libErr
}

func configureLogging() {
	if symSetLogLevel != 0 {
		d := newCallDesc(syscall.DylibTagVoid)
		d.pushPtr(10) // Silent threshold
		_, _ = syscall.DylibCall(symSetLogLevel, d.bytes())
	}
	if symGetDefaultLogger != 0 && symSetMinLoggerSeverity != 0 {
		d1 := newCallDesc(syscall.DylibTagPtr)
		logger, err := syscall.DylibCall(symGetDefaultLogger, d1.bytes())
		if err == nil && logger != 0 {
			d2 := newCallDesc(syscall.DylibTagI32)
			d2.pushPtr(logger)
			d2.pushPtr(3) // FATAL severity
			_, _ = syscall.DylibCall(symSetMinLoggerSeverity, d2.bytes())
		}
	}
	if symUseSinkLogger != 0 {
		d := newCallDesc(syscall.DylibTagVoid)
		_, _ = syscall.DylibCall(symUseSinkLogger, d.bytes())
	}
}

// LMEngine wraps the backend model engine
type LMEngine struct {
	ptr uint64
}

func (e *LMEngine) RawEngine() uint64 { return e.ptr }

func NewLMEngine(libPath, modelPath, backend string) (*LMEngine, error) {
	if err := ensureLibLoaded(libPath); err != nil {
		return nil, err
	}

	configureLogging()

	// Create settings
	desc := newCallDesc(syscall.DylibTagPtr)
	mPtr, mLen := cstrArg(modelPath)
	desc.pushCstr(mPtr, mLen)
	bPtr, bLen := cstrArg(backend)
	desc.pushCstr(bPtr, bLen)
	desc.pushPtr(0)
	desc.pushPtr(0)
	settings, err := syscall.DylibCall(symSettingsCreate, desc.bytes())
	if err != nil {
		return nil, fmt.Errorf("settings_create: %w", err)
	}
	if settings == 0 {
		return nil, fmt.Errorf("litert_lm_engine_settings_create returned NULL")
	}
	defer func() {
		d := newCallDesc(syscall.DylibTagVoid)
		d.pushPtr(settings)
		_, _ = syscall.DylibCall(symSettingsDelete, d.bytes())
	}()

	// Create engine
	desc = newCallDesc(syscall.DylibTagPtr)
	desc.pushPtr(settings)
	engine, err := syscall.DylibCall(symEngineCreate, desc.bytes())
	if err != nil {
		return nil, fmt.Errorf("engine_create: %w", err)
	}
	if engine == 0 {
		return nil, fmt.Errorf("litert_lm_engine_create returned NULL")
	}

	return &LMEngine{ptr: engine}, nil
}

func (e *LMEngine) Close() error {
	return nil
}

// LMConversation wraps a session/conversation state
type LMConversation struct {
	ptr uint64
}

func (c *LMConversation) RawConv() uint64 { return c.ptr }

func NewLMConversationFromHandles(engine, conv uint64) *LMConversation {
	return &LMConversation{ptr: conv}
}

func (e *LMEngine) NewConversation() (*LMConversation, error) {
	// Create conversation config
	desc := newCallDesc(syscall.DylibTagPtr)
	desc.pushPtr(e.ptr)
	config, err := syscall.DylibCall(symCfgCreate, desc.bytes())
	if err != nil {
		return nil, fmt.Errorf("conv_config_create: %w", err)
	}
	if config == 0 {
		return nil, fmt.Errorf("litert_lm_conversation_config_create returned NULL")
	}
	defer func() {
		d := newCallDesc(syscall.DylibTagVoid)
		d.pushPtr(config)
		_, _ = syscall.DylibCall(symCfgDelete, d.bytes())
	}()

	// Create conversation
	desc = newCallDesc(syscall.DylibTagPtr)
	desc.pushPtr(e.ptr)
	desc.pushPtr(config)
	conv, err := syscall.DylibCall(symConvCreate, desc.bytes())
	if err != nil {
		return nil, fmt.Errorf("conv_create: %w", err)
	}
	if conv == 0 {
		return nil, fmt.Errorf("litert_lm_conversation_create returned NULL")
	}

	return &LMConversation{ptr: conv}, nil
}

func (c *LMConversation) Close() error {
	return nil
}

func (c *LMConversation) SendMessageStream(payloadJSON string, onToken func(string)) error {
	wasmTokenNextID++
	cbID := wasmTokenNextID
	wasmTokenCallbacks[cbID] = onToken
	defer delete(wasmTokenCallbacks, cbID)

	done := make(chan struct{}, 1)
	wasmTokenDone[cbID] = done
	defer delete(wasmTokenDone, cbID)

	// LiteRtLmStreamCallback: void(void* userData, const char* text, bool isDone, const char* statsJSON)
	cbSig := []byte{4, syscall.DylibTagVoid,
		syscall.DylibTagPtr, syscall.DylibTagCstr, syscall.DylibTagPtr, syscall.DylibTagCstr}
	cbHandle, err := syscall.DylibCallbackCreate(lib, cbSig, "wasmTokenCallback")
	if err != nil {
		return fmt.Errorf("callback_create: %w", err)
	}

	desc := newCallDesc(syscall.DylibTagI32)
	desc.pushPtr(c.ptr)
	mjPtr, mjLen := cstrArg(payloadJSON)
	desc.pushCstr(mjPtr, mjLen)
	cjPtr, cjLen := cstrArg("{}")
	desc.pushCstr(cjPtr, cjLen)
	desc.pushPtr(0) // optional_args = NULL
	desc.pushCb(cbHandle)
	desc.pushPtr(cbID)

	rc, err := syscall.DylibCall(symConvSend, desc.bytes())
	if err != nil {
		return fmt.Errorf("conv_send_message_stream: %w", err)
	}
	if int32(rc) != 0 {
		return fmt.Errorf("litert_lm_conversation_send_message_stream failed: %d", int32(rc))
	}

	// Wait for the native library to signal completion. The host delivers
	// callbacks via Poll → drainCallbacks → fn.Call("wasmTokenCallback"),
	// which runs on handleAsyncEvent's growable goroutine stack.
	<-done

	// Clear sink logger
	if symClearSinkLogger != 0 {
		d := newCallDesc(syscall.DylibTagVoid)
		_, _ = syscall.DylibCall(symClearSinkLogger, d.bytes())
	}

	return nil
}
