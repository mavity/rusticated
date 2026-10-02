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

	lmSettingsSetEnableSpeculativeDecoding    func(settings uintptr, enable bool)
	lmSettingsSetUseRingbuffersLocalAttention func(settings uintptr, enable bool)
	lmSettingsSetGpuDecodeStepsPerSync        func(settings uintptr, steps int32)
	lmSettingsSetNumThreads                   func(settings uintptr, threads int32)
	lmLoadedFileCreate                        func(modelPath uintptr) uintptr
	lmLoadedFileDelete                        func(file uintptr)
	lmLoadedFileHasSpeculativeDecodingSupport func(file uintptr) bool
	lmEngineTryLoadVisionExecutor             func(engine uintptr) bool
)

var (
	tokenCallbackMu       sync.Mutex
	tokenCallbackRegistry = make(map[uintptr]func(string))
	tokenDoneChannels     = make(map[uintptr]chan struct{})
	tokenCallbackNextID   uintptr
)

// nativeTokenCallback is the C-callable trampoline for streaming tokens.
func nativeTokenCallback(userData, chunkPtr uintptr) {
	if lmStreamChunkGetError != nil {
		if errPtr := lmStreamChunkGetError(chunkPtr); errPtr != 0 {
			if s := ptrToGoString(errPtr); s != "" {
				fmt.Fprintf(os.Stderr, "LiteRT stream error: %s\n", s)
			}
		}
	}
	if lmStreamChunkGetText != nil {
		if textPtr := lmStreamChunkGetText(chunkPtr); textPtr != 0 {
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
		if lmStreamChunkIsFinal(chunkPtr) {
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

func goStringToCPtr(s string) (uintptr, string) {
	c := s + "\x00"
	return uintptr(unsafe.Pointer(unsafe.StringData(c))), c
}

var (
	lmLibOnce sync.Once
	lmLibErr  error
	lmLib     uintptr
)

func ensureLMLibLoaded(libPath string) error {
	lmLibOnce.Do(func() {
		var err error
		lmLib, err = dlopen(libPath, RTLD_NOW|RTLD_GLOBAL)
		if err != nil {
			lmLibErr = fmt.Errorf("failed to load %s: %w", libPath, err)
			return
		}

		purego.RegisterLibFunc(&lmSettingsCreate, lmLib, "litert_lm_engine_settings_create")
		purego.RegisterLibFunc(&lmSettingsDelete, lmLib, "litert_lm_engine_settings_delete")
		purego.RegisterLibFunc(&lmEngineCreate, lmLib, "litert_lm_engine_create")
		purego.RegisterLibFunc(&lmCfgCreate, lmLib, "litert_lm_conversation_config_create")
		purego.RegisterLibFunc(&lmCfgDelete, lmLib, "litert_lm_conversation_config_delete")
		purego.RegisterLibFunc(&lmConvCreate, lmLib, "litert_lm_conversation_create")
		purego.RegisterLibFunc(&lmConvSendStream, lmLib, "litert_lm_conversation_send_message_stream")
		purego.RegisterLibFunc(&lmSetLogLevel, lmLib, "litert_lm_set_min_log_level")
		purego.RegisterLibFunc(&lmGetDefaultLogger, lmLib, "LiteRtGetDefaultLogger")
		purego.RegisterLibFunc(&lmSetMinLoggerSeverity, lmLib, "LiteRtSetMinLoggerSeverity")
		purego.RegisterLibFunc(&lmGetSinkLoggerSize, lmLib, "LiteRtGetSinkLoggerSize")
		purego.RegisterLibFunc(&lmGetSinkLoggerMessage, lmLib, "LiteRtGetSinkLoggerMessage")
		purego.RegisterLibFunc(&lmClearSinkLogger, lmLib, "LiteRtClearSinkLogger")
		purego.RegisterLibFunc(&lmStreamChunkGetText, lmLib, "litert_lm_stream_chunk_get_text")
		purego.RegisterLibFunc(&lmStreamChunkIsFinal, lmLib, "litert_lm_stream_chunk_is_final")
		purego.RegisterLibFunc(&lmStreamChunkGetError, lmLib, "litert_lm_stream_chunk_get_error")

		tryRegisterLMFunc(&lmSettingsSetEnableSpeculativeDecoding, lmLib, "litert_lm_engine_settings_set_enable_speculative_decoding")
		tryRegisterLMFunc(&lmSettingsSetUseRingbuffersLocalAttention, lmLib, "litert_lm_engine_settings_set_use_ringbuffers_local_attention")
		tryRegisterLMFunc(&lmSettingsSetGpuDecodeStepsPerSync, lmLib, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync")
		tryRegisterLMFunc(&lmSettingsSetNumThreads, lmLib, "litert_lm_engine_settings_set_num_threads")
		tryRegisterLMFunc(&lmLoadedFileCreate, lmLib, "litert_lm_loaded_file_create")
		tryRegisterLMFunc(&lmLoadedFileDelete, lmLib, "litert_lm_loaded_file_delete")
		tryRegisterLMFunc(&lmLoadedFileHasSpeculativeDecodingSupport, lmLib, "litert_lm_loaded_file_has_speculative_decoding_support")
		tryRegisterLMFunc(&lmEngineTryLoadVisionExecutor, lmLib, "litert_lm_engine_try_loading_vision_executor")
	})
	return lmLibErr
}

func tryRegisterLMFunc(target any, lib uintptr, name string) {
	if sym, err := dlsym(lib, name); err == nil && sym != 0 {
		purego.RegisterLibFunc(target, lib, name)
	}
}

func configureLMLogging() {
	if sym, err := dlsym(lmLib, "FLAGS_minloglevel"); err == nil && sym != 0 {
		*(*int32)(unsafe.Pointer(sym)) = 2
	}
	if sym, err := dlsym(lmLib, "FLAGS_logtostderr"); err == nil && sym != 0 {
		*(*bool)(unsafe.Pointer(sym)) = false
	}
	if sym, err := dlsym(lmLib, "FLAGS_alsologtostderr"); err == nil && sym != 0 {
		*(*bool)(unsafe.Pointer(sym)) = false
	}
	if lmSetLogLevel != nil {
		lmSetLogLevel(10)
	}
	if lmGetDefaultLogger != nil && lmSetMinLoggerSeverity != nil {
		if logger := lmGetDefaultLogger(); logger != 0 {
			lmSetMinLoggerSeverity(logger, 3)
		}
	}
}

type LMEngine struct{ ptr uint64 }

func checkLMSpeculativeDecoding(modelPtr uintptr) bool {
	if lmLoadedFileCreate == nil || lmLoadedFileHasSpeculativeDecodingSupport == nil {
		return false
	}
	file := lmLoadedFileCreate(modelPtr)
	if file == 0 {
		return false
	}
	ok := lmLoadedFileHasSpeculativeDecodingSupport(file)
	if lmLoadedFileDelete != nil {
		lmLoadedFileDelete(file)
	}
	return ok
}

func resolveLMBackendCandidates(backend string) []string {
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
	if err := ensureLMLibLoaded(libPath); err != nil {
		return nil, err
	}
	configureLMLogging()

	cModel, keepModel := goStringToCPtr(modelPath)
	_ = keepModel

	supportsSpec := checkLMSpeculativeDecoding(cModel)
	var lastErr error

	for _, cand := range resolveLMBackendCandidates(backend) {
		cBackend, keepBackend := goStringToCPtr(cand)
		_ = keepBackend

		settings := lmSettingsCreate(cModel, cBackend, cBackend, 0)
		if settings == 0 {
			lastErr = fmt.Errorf("settings_create returned NULL for backend %s", cand)
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
			if lmEngineTryLoadVisionExecutor != nil {
				lmEngineTryLoadVisionExecutor(engine)
			}
			return &LMEngine{ptr: uint64(engine)}, nil
		}
		lastErr = fmt.Errorf("engine_create returned NULL for backend %s", cand)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("failed to initialize LiteRT-LM engine")
}

func (e *LMEngine) Close() {}

type LMConversation struct{ ptr uint64 }

func (e *LMEngine) NewConversation() (*LMConversation, error) {
	config := lmCfgCreate(uintptr(e.ptr))
	if config == 0 {
		return nil, fmt.Errorf("conversation_config_create returned NULL")
	}
	defer lmCfgDelete(config)
	conv := lmConvCreate(uintptr(e.ptr), config)
	if conv == 0 {
		return nil, fmt.Errorf("conversation_create returned NULL")
	}
	return &LMConversation{ptr: uint64(conv)}, nil
}

func (c *LMConversation) Close() {}

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
		return fmt.Errorf("send_message_stream failed: %d", rc)
	}
	<-doneCh

	if lmClearSinkLogger != nil {
		lmClearSinkLogger()
	}
	return nil
}
