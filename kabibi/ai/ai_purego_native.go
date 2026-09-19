//go:build !wasip1

package main

import (
	"fmt"
	"os"
	"runtime"
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
	lmStreamChunkGetText   func(chunk uintptr) uintptr
	lmStreamChunkIsFinal   func(chunk uintptr) bool
	lmStreamChunkGetError  func(chunk uintptr) uintptr

	lmSettingsSetEnableSpeculativeDecoding     func(settings uintptr, enable bool)
	lmSettingsSetUseRingbuffersLocalAttention func(settings uintptr, enable bool)
	lmSettingsSetGpuDecodeStepsPerSync         func(settings uintptr, steps int32)
	lmSettingsSetNumThreads                    func(settings uintptr, threads int32)
	lmLoadedFileCreate                         func(modelPath uintptr) uintptr
	lmLoadedFileDelete                         func(file uintptr)
	lmLoadedFileHasSpeculativeDecodingSupport  func(file uintptr) bool
)

// tokenCallbackRegistry maps opaque IDs to Go callbacks for streaming tokens.
var (
	tokenCallbackMu       sync.Mutex
	tokenCallbackRegistry = make(map[uintptr]func(string))
	tokenDoneChannels     = make(map[uintptr]chan struct{})
	tokenCallbackNextID   uintptr
)

// nativeTokenCallback is the C-callable trampoline passed to
// litert_lm_conversation_send_message_stream.
//
// C signature: void callback(void* userData, void* chunkPtr)
func nativeTokenCallback(userData, chunkPtr uintptr) {
	if lmStreamChunkGetError != nil {
		errPtr := lmStreamChunkGetError(chunkPtr)
		if errPtr != 0 {
			errStr := ptrToGoString(errPtr)
			if errStr != "" {
				fmt.Fprintf(os.Stderr, "LiteRT stream error: %s\n", errStr)
			}
		}
	}
	if lmStreamChunkGetText != nil {
		textPtr := lmStreamChunkGetText(chunkPtr)
		if textPtr != 0 {
			token := ptrToGoString(textPtr)
			tokenCallbackMu.Lock()
			fn := tokenCallbackRegistry[userData]
			tokenCallbackMu.Unlock()
			if fn != nil {
				fn(token)
			}
		}
	}
	if lmStreamChunkIsFinal != nil {
		if isFinal := lmStreamChunkIsFinal(chunkPtr); isFinal {
			tokenCallbackMu.Lock()
			ch := tokenDoneChannels[userData]
			tokenCallbackMu.Unlock()
			if ch != nil {
				select {
				case <-ch:
				default:
					close(ch)
				}
			}
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
		purego.RegisterLibFunc(&lmStreamChunkGetText, lib, "litert_lm_stream_chunk_get_text")
		purego.RegisterLibFunc(&lmStreamChunkIsFinal, lib, "litert_lm_stream_chunk_is_final")
		purego.RegisterLibFunc(&lmStreamChunkGetError, lib, "litert_lm_stream_chunk_get_error")

		tryRegisterLibFunc(&lmSettingsSetEnableSpeculativeDecoding, lib, "litert_lm_engine_settings_set_enable_speculative_decoding")
		tryRegisterLibFunc(&lmSettingsSetUseRingbuffersLocalAttention, lib, "litert_lm_engine_settings_set_use_ringbuffers_local_attention")
		tryRegisterLibFunc(&lmSettingsSetGpuDecodeStepsPerSync, lib, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync")
		tryRegisterLibFunc(&lmSettingsSetNumThreads, lib, "litert_lm_engine_settings_set_num_threads")
		tryRegisterLibFunc(&lmLoadedFileCreate, lib, "litert_lm_loaded_file_create")
		tryRegisterLibFunc(&lmLoadedFileDelete, lib, "litert_lm_loaded_file_delete")
		tryRegisterLibFunc(&lmLoadedFileHasSpeculativeDecodingSupport, lib, "litert_lm_loaded_file_has_speculative_decoding_support")
	})
	return libErr
}

func tryRegisterLibFunc(target any, lib uintptr, name string) {
	if sym, err := dlsym(lib, name); err == nil && sym != 0 {
		purego.RegisterLibFunc(target, lib, name)
	}
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

func checkSpeculativeDecodingSupport(modelPtr uintptr) bool {
	if lmLoadedFileCreate == nil || lmLoadedFileHasSpeculativeDecodingSupport == nil {
		return false
	}
	file := lmLoadedFileCreate(modelPtr)
	if file == 0 {
		return false
	}
	hasSupport := lmLoadedFileHasSpeculativeDecodingSupport(file)
	if lmLoadedFileDelete != nil {
		lmLoadedFileDelete(file)
	}
	return hasSupport
}

func resolveBackendCandidates(backend string) []string {
	if backend != "" && backend != "auto" {
		if backend == "cpu" {
			return []string{"cpu"}
		}
		return []string{backend, "cpu"}
	}
	if env := os.Getenv("LITERTLM_BACKEND"); env != "" {
		if env == "cpu" {
			return []string{"cpu"}
		}
		return []string{env, "cpu"}
	}
	return []string{"gpu", "npu", "cpu"}
}

func NewLMEngine(libPath, modelPath, backend string) (*LMEngine, error) {
	if err := ensureLibLoaded(libPath); err != nil {
		return nil, err
	}

	configureLogging()

	cModel, keepModel := goStringToCPtr(modelPath)
	_ = keepModel

	supportsSpec := checkSpeculativeDecodingSupport(cModel)

	candidates := resolveBackendCandidates(backend)
	var lastErr error

	for _, cand := range candidates {
		cBackend, keepBackend := goStringToCPtr(cand)
		_ = keepBackend

		settings := lmSettingsCreate(cModel, cBackend, 0, 0)
		if settings == 0 {
			lastErr = fmt.Errorf("litert_lm_engine_settings_create returned NULL for backend %s", cand)
			continue
		}

		if supportsSpec && lmSettingsSetEnableSpeculativeDecoding != nil {
			lmSettingsSetEnableSpeculativeDecoding(settings, true)
		}
		if lmSettingsSetUseRingbuffersLocalAttention != nil {
			lmSettingsSetUseRingbuffersLocalAttention(settings, true)
		}
		if cand == "gpu" && lmSettingsSetGpuDecodeStepsPerSync != nil {
			lmSettingsSetGpuDecodeStepsPerSync(settings, 8)
		}
		if cand == "cpu" && lmSettingsSetNumThreads != nil {
			lmSettingsSetNumThreads(settings, int32(runtime.NumCPU()))
		}

		engine := lmEngineCreate(settings)
		lmSettingsDelete(settings)
		if engine != 0 {
			return &LMEngine{ptr: uint64(engine)}, nil
		}
		lastErr = fmt.Errorf("litert_lm_engine_create returned NULL for backend %s", cand)
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("failed to initialize LiteRT-LM engine on any backend")
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
	tokenCallbackMu.Lock()
	tokenCallbackNextID++
	cbID := tokenCallbackNextID
	doneCh := make(chan struct{})
	tokenCallbackRegistry[cbID] = onToken
	tokenDoneChannels[cbID] = doneCh
	tokenCallbackMu.Unlock()

	defer func() {
		tokenCallbackMu.Lock()
		delete(tokenCallbackRegistry, cbID)
		delete(tokenDoneChannels, cbID)
		tokenCallbackMu.Unlock()
	}()

	trampoline := purego.NewCallback(nativeTokenCallback)

	cMsg, keepMsg := goStringToCPtr(payloadJSON)
	_ = keepMsg

	rc := lmConvSendStream(uintptr(c.ptr), cMsg, 0, 0, trampoline, cbID)
	if rc != 0 {
		return fmt.Errorf("litert_lm_conversation_send_message_stream failed: %d", rc)
	}

	// Wait for stream completion from the background inference thread
	<-doneCh

	// Clean/clear the library's in-memory sink logger buffer to prevent RAM growth
	if lmClearSinkLogger != nil {
		lmClearSinkLogger()
	}

	return nil
}
