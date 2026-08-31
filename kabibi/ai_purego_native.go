//go:build !wasip1

package main

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// LiterTLM C API function pointers, registered via purego.RegisterLibFunc.
var (
	lmSettingsCreate       func(modelPath, backend, visionBackend, audioBackend uintptr) uintptr
	lmSettingsDelete       func(settings uintptr)
	lmEngineCreate         func(settings uintptr) uintptr
	lmCfgCreate            func(engine uintptr) uintptr
	lmCfgDelete            func(config uintptr)
	lmConvCreate           func(engine, config uintptr) uintptr
	lmConvSendStream       func(conv, message, extraCtx, optArgs, callback, userData uintptr) int32
	lmSetLogLevel          func(level uintptr)
	lmGetDefaultLogger     func() uintptr
	lmSetMinLoggerSeverity func(logger uintptr, severity int32) int32
	lmGetSinkLoggerSize    func() uintptr
	lmGetSinkLoggerMessage func(index uintptr) uintptr
	lmClearSinkLogger      func()
)

// tokenCallbackRegistry maps opaque IDs to Go callbacks for streaming tokens.
var tokenCallbackRegistry = make(map[uintptr]func(string))
var tokenCallbackNextID uintptr

// nativeTokenCallback is the C-callable trampoline passed to
// litert_lm_conversation_send_message_stream.
//
// C signature: void callback(void* userData, const char* text, bool isDone, const char* statsJSON)
func nativeTokenCallback(userData, text uintptr, isDone bool, statsJSON uintptr) {
	if text != 0 {
		token := ptrToGoString(text)
		if fn, ok := tokenCallbackRegistry[userData]; ok {
			fn(token)
		}
	}
}

// ptrToGoString reads a NUL-terminated C string from a native pointer.
func ptrToGoString(ptr uintptr) string {
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

// goStringToCPtr returns a uintptr to a NUL-terminated copy of s.
func goStringToCPtr(s string) (uintptr, string) {
	c := s + "\x00"
	return uintptr(unsafe.Pointer(unsafe.StringData(c))), c
}

var (
	libOnce sync.Once
	libErr  error
	lib     uintptr
)

func ensureLibLoaded(libPath string) error {
	libOnce.Do(func() {
		var err error
		lib, err = dlopen(libPath, RTLD_NOW|RTLD_GLOBAL)
		if err != nil {
			libErr = fmt.Errorf("failed to load %s: %w", libPath, err)
			return
		}

		purego.RegisterLibFunc(&lmSettingsCreate, lib, "litert_lm_engine_settings_create")
		purego.RegisterLibFunc(&lmSettingsDelete, lib, "litert_lm_engine_settings_delete")
		purego.RegisterLibFunc(&lmEngineCreate, lib, "litert_lm_engine_create")
		purego.RegisterLibFunc(&lmCfgCreate, lib, "litert_lm_conversation_config_create")
		purego.RegisterLibFunc(&lmCfgDelete, lib, "litert_lm_conversation_config_delete")
		purego.RegisterLibFunc(&lmConvCreate, lib, "litert_lm_conversation_create")
		purego.RegisterLibFunc(&lmConvSendStream, lib, "litert_lm_conversation_send_message_stream")
		purego.RegisterLibFunc(&lmSetLogLevel, lib, "litert_lm_set_min_log_level")
		purego.RegisterLibFunc(&lmGetDefaultLogger, lib, "LiteRtGetDefaultLogger")
		purego.RegisterLibFunc(&lmSetMinLoggerSeverity, lib, "LiteRtSetMinLoggerSeverity")
		purego.RegisterLibFunc(&lmGetSinkLoggerSize, lib, "LiteRtGetSinkLoggerSize")
		purego.RegisterLibFunc(&lmGetSinkLoggerMessage, lib, "LiteRtGetSinkLoggerMessage")
		purego.RegisterLibFunc(&lmClearSinkLogger, lib, "LiteRtClearSinkLogger")
	})
	return libErr
}

func configureLogging() {
	// Mute any underlying glog/Abseil logging globally in the loaded library's memory space
	if sym, err := dlsym(lib, "FLAGS_minloglevel"); err == nil && sym != 0 {
		*(*int32)(unsafe.Pointer(sym)) = 2 // 2 = ERROR
	}
	if sym, err := dlsym(lib, "FLAGS_logtostderr"); err == nil && sym != 0 {
		*(*bool)(unsafe.Pointer(sym)) = false
	}
	if sym, err := dlsym(lib, "FLAGS_alsologtostderr"); err == nil && sym != 0 {
		*(*bool)(unsafe.Pointer(sym)) = false
	}

	if lmSetLogLevel != nil {
		lmSetLogLevel(10) // Set to highest silent threshold
	}

	if lmGetDefaultLogger != nil && lmSetMinLoggerSeverity != nil {
		logger := lmGetDefaultLogger()
		if logger != 0 {
			lmSetMinLoggerSeverity(logger, 3) // 3 = FATAL severity
		}
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

	// Build C strings.
	cModel, keepModel := goStringToCPtr(modelPath)
	cBackend, keepBackend := goStringToCPtr(backend)
	_ = keepModel
	_ = keepBackend

	settings := lmSettingsCreate(cModel, cBackend, 0, 0)
	if settings == 0 {
		return nil, fmt.Errorf("litert_lm_engine_settings_create returned NULL")
	}
	defer lmSettingsDelete(settings)

	engine := lmEngineCreate(settings)
	if engine == 0 {
		return nil, fmt.Errorf("litert_lm_engine_create returned NULL")
	}

	return &LMEngine{ptr: uint64(engine)}, nil
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
	config := lmCfgCreate(uintptr(e.ptr))
	if config == 0 {
		return nil, fmt.Errorf("litert_lm_conversation_config_create returned NULL")
	}
	defer lmCfgDelete(config)

	conv := lmConvCreate(uintptr(e.ptr), config)
	if conv == 0 {
		return nil, fmt.Errorf("litert_lm_conversation_create returned NULL")
	}

	return &LMConversation{ptr: uint64(conv)}, nil
}

func (c *LMConversation) Close() error {
	return nil
}

func (c *LMConversation) SendMessageStream(payloadJSON string, onToken func(string)) error {
	tokenCallbackNextID++
	cbID := tokenCallbackNextID
	tokenCallbackRegistry[cbID] = onToken
	defer delete(tokenCallbackRegistry, cbID)

	trampoline := purego.NewCallback(nativeTokenCallback)

	cMsg, keepMsg := goStringToCPtr(payloadJSON)
	_ = keepMsg

	rc := lmConvSendStream(uintptr(c.ptr), cMsg, 0, 0, trampoline, cbID)
	if rc != 0 {
		return fmt.Errorf("litert_lm_conversation_send_message_stream failed: %d", rc)
	}

	// Clean/clear the library's in-memory sink logger buffer to prevent RAM growth
	if lmClearSinkLogger != nil {
		lmClearSinkLogger()
	}

	return nil
}
