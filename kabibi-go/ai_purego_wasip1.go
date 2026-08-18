//go:build wasip1

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

// tokenCallbackRegistry maps opaque IDs to Go callbacks for streaming tokens.
var wasmTokenCallbacks = make(map[uintptr]func(string))
var wasmTokenNextID uintptr

// wasmCallbackScratchBuf is a guest-side buffer used by the host to copy
// native C strings into guest linear memory before calling guest exports.
var wasmCallbackScratchBuf [65536]byte

//go:wasmexport wasmCallbackScratchAddr
func wasmCallbackScratchAddr() uint32 {
	return uint32(uintptr(unsafe.Pointer(&wasmCallbackScratchBuf[0])))
}

//go:wasmexport wasmTokenCallback
func wasmTokenCallback(userData, chunk, isFinal uint64) uint64 {
	if chunk != 0 {
		token := wasmPtrToGoString(uintptr(chunk))
		if fn, ok := wasmTokenCallbacks[uintptr(userData)]; ok {
			fn(token)
		}
	}
	return 0
}

// wasmPtrToGoString reads a NUL-terminated C string from WASM linear memory.
func wasmPtrToGoString(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	var buf []byte
	for i := uintptr(0); ; i++ {
		b := *(*byte)(unsafe.Pointer(ptr + i))
		if b == 0 {
			break
		}
		buf = append(buf, b)
	}
	return string(buf)
}

type callDescBuilder struct {
	buf []byte
}

func newCallDesc(retType byte) *callDescBuilder {
	// [cc=0, arg_count=0, ret_type, reserved=0]
	return &callDescBuilder{buf: []byte{0, 0, retType, 0}}
}

func (d *callDescBuilder) pushPtr(v uint64) {
	d.buf = append(d.buf, syscall.DylibTagPtr)
	d.buf = appendLE64(d.buf, v)
	d.buf[1]++
}

func (d *callDescBuilder) pushCstr(guestPtr uint32, length uint32) {
	d.buf = append(d.buf, syscall.DylibTagCstr)
	d.buf = appendLE32(d.buf, guestPtr)
	d.buf = appendLE32(d.buf, length)
	d.buf[1]++
}

func (d *callDescBuilder) pushCb(cbHandle uint64) {
	d.buf = append(d.buf, syscall.DylibTagCb)
	d.buf = appendLE64(d.buf, cbHandle)
	d.buf[1]++
}

func (d *callDescBuilder) bytes() []byte {
	return d.buf
}

func appendLE32(buf []byte, v uint32) []byte {
	return append(buf, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func appendLE64(buf []byte, v uint64) []byte {
	return append(buf,
		byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56),
	)
}

func runAIPrompt(userInput string, onToken func(string)) error {
	// Wrap the callback to parse LiterTLM JSON token chunks into plain text.
	origOnToken := onToken
	onToken = func(raw string) {
		origOnToken(extractTokenText(raw))
	}

	cacheDir, err := cacheDirPath()
	if err != nil {
		return err
	}

	modelPath := filepath.Join(cacheDir, defaultModelName)
	libDir := filepath.Join(cacheDir, "lib")

	libExt := ".so"
	switch HostOS() {
	case "windows":
		libExt = ".dll"
	case "darwin":
		libExt = ".dylib"
	}

	libPath := filepath.Join(libDir, "litert_lm_ext"+libExt)

	// Open the native library via the host dylib ABI.
	lib, err := syscall.DylibOpen(libPath, 0)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", libPath, err)
	}
	defer syscall.DylibClose(lib)

	// Resolve all LiterTLM symbols.
	symSettingsCreate, err := syscall.DylibSym(lib, "litert_lm_engine_settings_create")
	if err != nil {
		return fmt.Errorf("sym litert_lm_engine_settings_create: %w", err)
	}
	symSettingsDelete, err := syscall.DylibSym(lib, "litert_lm_engine_settings_delete")
	if err != nil {
		return fmt.Errorf("sym litert_lm_engine_settings_delete: %w", err)
	}
	symEngineCreate, err := syscall.DylibSym(lib, "litert_lm_engine_create")
	if err != nil {
		return fmt.Errorf("sym litert_lm_engine_create: %w", err)
	}
	symCfgCreate, err := syscall.DylibSym(lib, "litert_lm_conversation_config_create")
	if err != nil {
		return fmt.Errorf("sym litert_lm_conversation_config_create: %w", err)
	}
	symCfgDelete, err := syscall.DylibSym(lib, "litert_lm_conversation_config_delete")
	if err != nil {
		return fmt.Errorf("sym litert_lm_conversation_config_delete: %w", err)
	}
	symConvCreate, err := syscall.DylibSym(lib, "litert_lm_conversation_create")
	if err != nil {
		return fmt.Errorf("sym litert_lm_conversation_create: %w", err)
	}
	symConvSend, err := syscall.DylibSym(lib, "litert_lm_conversation_send_message_stream")
	if err != nil {
		return fmt.Errorf("sym litert_lm_conversation_send_message_stream: %w", err)
	}

	// Mute the library's internal glog and LiteRT logger outputs (warnings/errors/fatals only)
	if symSetLogLevel, err := syscall.DylibSym(lib, "litert_lm_set_min_log_level"); err == nil && symSetLogLevel != 0 {
		d := newCallDesc(syscall.DylibTagVoid)
		d.pushPtr(10) // Set to highest silent threshold
		_, _ = syscall.DylibCall(symSetLogLevel, d.bytes())
	}

	// Configure the default LiteRT logger to only output FATAL level messages
	symGetDefaultLogger, err1 := syscall.DylibSym(lib, "LiteRtGetDefaultLogger")
	symSetMinLoggerSeverity, err2 := syscall.DylibSym(lib, "LiteRtSetMinLoggerSeverity")
	if err1 == nil && err2 == nil && symGetDefaultLogger != 0 && symSetMinLoggerSeverity != 0 {
		d1 := newCallDesc(syscall.DylibTagPtr)
		logger, err := syscall.DylibCall(symGetDefaultLogger, d1.bytes())
		if err == nil && logger != 0 {
			d2 := newCallDesc(syscall.DylibTagI32)
			d2.pushPtr(logger)
			d2.pushPtr(3) // 3 = FATAL severity
			_, _ = syscall.DylibCall(symSetMinLoggerSeverity, d2.bytes())
		}
	}

	// Channel all LiteRT environment logs to the built-in, in-memory Sink Logger instead of stdout/stderr
	if symUseSinkLogger, err := syscall.DylibSym(lib, "LiteRtUseSinkLogger"); err == nil && symUseSinkLogger != 0 {
		d := newCallDesc(syscall.DylibTagVoid)
		_, _ = syscall.DylibCall(symUseSinkLogger, d.bytes())
	}

	cstrArg := func(s string) (uint32, uint32) {
		ptr := uintptr(unsafe.Pointer(unsafe.StringData(s)))
		return uint32(ptr), uint32(len(s))
	}

	desc := newCallDesc(syscall.DylibTagPtr)
	mPtr, mLen := cstrArg(modelPath)
	desc.pushCstr(mPtr, mLen)
	bPtr, bLen := cstrArg("cpu")
	desc.pushCstr(bPtr, bLen)
	desc.pushPtr(0)
	desc.pushPtr(0)
	settings, err := syscall.DylibCall(symSettingsCreate, desc.bytes())
	if err != nil {
		return fmt.Errorf("settings_create: %w", err)
	}

	if settings == 0 {
		return fmt.Errorf("litert_lm_engine_settings_create returned NULL")
	}

	defer func() {
		d := newCallDesc(syscall.DylibTagVoid)
		d.pushPtr(settings)
		syscall.DylibCall(symSettingsDelete, d.bytes())
	}()

	desc = newCallDesc(syscall.DylibTagPtr)
	desc.pushPtr(settings)
	engine, err := syscall.DylibCall(symEngineCreate, desc.bytes())
	if err != nil {
		return fmt.Errorf("engine_create: %w", err)
	}

	if engine == 0 {
		return fmt.Errorf("litert_lm_engine_create returned NULL")
	}

	desc = newCallDesc(syscall.DylibTagPtr)
	desc.pushPtr(engine)
	config, err := syscall.DylibCall(symCfgCreate, desc.bytes())
	if err != nil {
		return fmt.Errorf("conv_config_create: %w", err)
	}

	if config == 0 {
		return fmt.Errorf("litert_lm_conversation_config_create returned NULL")
	}

	defer func() {
		d := newCallDesc(syscall.DylibTagVoid)
		d.pushPtr(config)
		syscall.DylibCall(symCfgDelete, d.bytes())
	}()

	desc = newCallDesc(syscall.DylibTagPtr)
	desc.pushPtr(engine)
	desc.pushPtr(config)
	conv, err := syscall.DylibCall(symConvCreate, desc.bytes())
	if err != nil {
		return fmt.Errorf("conv_create: %w", err)
	}

	if conv == 0 {
		return fmt.Errorf("litert_lm_conversation_create returned NULL")
	}

	// Register Go callback and create a native trampoline via the host.
	wasmTokenNextID++
	cbID := wasmTokenNextID
	wasmTokenCallbacks[cbID] = onToken
	defer delete(wasmTokenCallbacks, cbID)

	// Callback signature blob: [argCount, retType, argType0..argTypeN]
	// Specify syscall.DylibTagCstr for string-type parameters to let the host
	// perform scratch buffer marshalling safely without guessing or address probing.
	var cbSig []byte
	// The C API signature is: void callback(void* user_data, const LiteRtLmStreamChunk* chunk)
	// 0x0B parses the LiteRtLmStreamChunk struct pointer explicitly in the host.
	cbSig = []byte{2, syscall.DylibTagPtr, syscall.DylibTagPtr, 0x0B}

	cbHandle, err := syscall.DylibCallbackCreate(lib, cbSig, "wasmTokenCallback")
	if err != nil {
		return fmt.Errorf("callback_create: %w", err)
	}

	// Securely serialize message structure to JSON to eliminate JSON injection risks.
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	msgPayload := Message{
		Role:    "user",
		Content: userInput,
	}

	payloadBytes, err := json.Marshal(msgPayload)
	if err != nil {
		return fmt.Errorf("failed to serialize message: %w", err)
	}

	desc = newCallDesc(syscall.DylibTagI32)
	desc.pushPtr(conv)
	iPtr, iLen := cstrArg(string(payloadBytes))
	desc.pushCstr(iPtr, iLen)
	desc.pushPtr(0)
	desc.pushPtr(0)
	desc.pushCb(cbHandle)
	desc.pushPtr(uint64(cbID))

	rc, err := syscall.DylibCall(symConvSend, desc.bytes())
	if err != nil {
		return fmt.Errorf("conv_send_message_stream: %w", err)
	}

	if int32(rc) != 0 {
		return fmt.Errorf("litert_lm_conversation_send_message_stream failed: %d", int32(rc))
	}

	// Clean/clear the library's in-memory sink logger buffer to prevent RAM growth
	if symClear, err := syscall.DylibSym(lib, "LiteRtClearSinkLogger"); err == nil && symClear != 0 {
		d := newCallDesc(syscall.DylibTagVoid)
		_, _ = syscall.DylibCall(symClear, d.bytes())
	}

	return nil
}
