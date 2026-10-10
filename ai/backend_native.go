//go:build !wasip1 && !js

package ai

import (
	"context"
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
	lmEngineDelete         func(engine uintptr)
	lmCfgCreate            func(engine uintptr) uintptr
	lmCfgDelete            func(config uintptr)
	lmConvCreate           func(engine, config uintptr) uintptr
	lmConvDelete           func(conv uintptr)
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

	lmCfgSetTools           func(config, toolsJson uintptr) int32
	lmCfgSetSystemMessage   func(config, systemMessageJson uintptr) int32
	lmCfgSetStreamToolCalls func(config uintptr, stream bool, channelName uintptr) int32
	lmCfgSetMessages        func(config, messagesJson uintptr) int32

	lmSettingsSetEnableSpeculativeDecoding    func(settings uintptr, enable bool)
	lmSettingsSetUseRingbuffersLocalAttention func(settings uintptr, enable bool)
	lmSettingsSetGpuDecodeStepsPerSync        func(settings uintptr, steps int32)
	lmSettingsSetNumThreads                   func(settings uintptr, threads int32)
	lmSettingsSetMaxNumTokens                 func(settings uintptr, maxNumTokens int32) int32

	lmLoadedFileCreate                        func(modelPath uintptr) uintptr
	lmLoadedFileDelete                        func(file uintptr)
	lmLoadedFileHasSpeculativeDecodingSupport func(file uintptr) bool
	lmLoadedFileMaxContextTokens              func(file uintptr) uint32
	lmLoadedFileIsDynamicContext              func(file uintptr) bool
	lmLoadedFileSupportsThinking              func(file uintptr) bool
	lmLoadedFileSupportsFunctionCalling       func(file uintptr) bool
	lmLoadedFileModalitySupportedBackends     func(file uintptr, modality int32, backends *int32, max_size int32, out_count *int32) int32

	lmEngineTryLoadVisionExecutor func(engine uintptr) bool
)

var (
	tokenCallbackMu       sync.Mutex
	tokenCallbackRegistry = make(map[uintptr]func(string))
	tokenDoneChannels     = make(map[uintptr]chan struct{})
	tokenCallbackNextID   uintptr
)

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
		tryRegisterLMFunc(&lmEngineDelete, lmLib, "litert_lm_engine_delete")
		purego.RegisterLibFunc(&lmCfgCreate, lmLib, "litert_lm_conversation_config_create")
		purego.RegisterLibFunc(&lmCfgDelete, lmLib, "litert_lm_conversation_config_delete")
		purego.RegisterLibFunc(&lmConvCreate, lmLib, "litert_lm_conversation_create")
		tryRegisterLMFunc(&lmConvDelete, lmLib, "litert_lm_conversation_delete")
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

		tryRegisterLMFunc(&lmCfgSetTools, lmLib, "litert_lm_conversation_config_set_tools")
		tryRegisterLMFunc(&lmCfgSetSystemMessage, lmLib, "litert_lm_conversation_config_set_system_message")
		tryRegisterLMFunc(&lmCfgSetStreamToolCalls, lmLib, "litert_lm_conversation_config_set_stream_tool_calls")
		tryRegisterLMFunc(&lmCfgSetMessages, lmLib, "litert_lm_conversation_config_set_messages")

		tryRegisterLMFunc(&lmSettingsSetEnableSpeculativeDecoding, lmLib, "litert_lm_engine_settings_set_enable_speculative_decoding")
		tryRegisterLMFunc(&lmSettingsSetUseRingbuffersLocalAttention, lmLib, "litert_lm_engine_settings_set_use_ringbuffers_local_attention")
		tryRegisterLMFunc(&lmSettingsSetGpuDecodeStepsPerSync, lmLib, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync")
		tryRegisterLMFunc(&lmSettingsSetNumThreads, lmLib, "litert_lm_engine_settings_set_num_threads")
		tryRegisterLMFunc(&lmSettingsSetMaxNumTokens, lmLib, "litert_lm_engine_settings_set_max_num_tokens")

		tryRegisterLMFunc(&lmLoadedFileCreate, lmLib, "litert_lm_loaded_file_create")
		tryRegisterLMFunc(&lmLoadedFileDelete, lmLib, "litert_lm_loaded_file_delete")
		tryRegisterLMFunc(&lmLoadedFileHasSpeculativeDecodingSupport, lmLib, "litert_lm_loaded_file_has_speculative_decoding_support")
		tryRegisterLMFunc(&lmLoadedFileMaxContextTokens, lmLib, "litert_lm_loaded_file_max_context_tokens")
		tryRegisterLMFunc(&lmLoadedFileIsDynamicContext, lmLib, "litert_lm_loaded_file_is_dynamic_context")
		tryRegisterLMFunc(&lmLoadedFileSupportsThinking, lmLib, "litert_lm_loaded_file_supports_thinking")
		tryRegisterLMFunc(&lmLoadedFileSupportsFunctionCalling, lmLib, "litert_lm_loaded_file_supports_function_calling")
		tryRegisterLMFunc(&lmLoadedFileModalitySupportedBackends, lmLib, "litert_lm_loaded_file_modality_supported_backends")

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

func checkLMSpeculativeDecoding(modelPtr uintptr) bool {
	if lmLoadedFileCreate == nil || lmLoadedFileHasSpeculativeDecodingSupport == nil {
		return false
	}
	file := lmLoadedFileCreate(modelPtr)
	if file == 0 {
		return false
	}
	defer func() {
		if lmLoadedFileDelete != nil {
			lmLoadedFileDelete(file)
		}
	}()
	return lmLoadedFileHasSpeculativeDecodingSupport(file)
}

func inspectModel(libPath, modelPath string) (*ModelInfo, error) {
	if err := ensureLMLibLoaded(libPath); err != nil {
		return nil, err
	}
	cModel, keepModel := goStringToCPtr(modelPath)
	_ = keepModel

	if lmLoadedFileCreate == nil {
		return nil, fmt.Errorf("loaded_file_create not exported by runtime")
	}
	file := lmLoadedFileCreate(cModel)
	if file == 0 {
		return nil, fmt.Errorf("failed to inspect model file %s", modelPath)
	}
	defer func() {
		if lmLoadedFileDelete != nil {
			lmLoadedFileDelete(file)
		}
	}()

	info := &ModelInfo{}
	if lmLoadedFileMaxContextTokens != nil {
		info.MaxContextTokens = int(lmLoadedFileMaxContextTokens(file))
	}
	if lmLoadedFileIsDynamicContext != nil {
		info.IsDynamicContext = lmLoadedFileIsDynamicContext(file)
	}
	if lmLoadedFileSupportsThinking != nil {
		info.SupportsThinking = lmLoadedFileSupportsThinking(file)
	}
	if lmLoadedFileSupportsFunctionCalling != nil {
		info.SupportsFunctionCalling = lmLoadedFileSupportsFunctionCalling(file)
	}
	if lmLoadedFileModalitySupportedBackends != nil {
		var backends [8]int32
		var count int32
		if lmLoadedFileModalitySupportedBackends(file, 0, &backends[0], int32(len(backends)), &count) == 0 {
			for i := int32(0); i < count && i < int32(len(backends)); i++ {
				switch backends[i] {
				case 1:
					info.SupportedBackends = append(info.SupportedBackends, "cpu")
				case 2:
					info.SupportedBackends = append(info.SupportedBackends, "gpu")
				case 3:
					info.SupportedBackends = append(info.SupportedBackends, "npu")
				}
			}
		}
	}
	return info, nil
}

type nativeEngineBackend struct {
	engine uintptr
}

func openBackend(libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	if err := ensureLMLibLoaded(libPath); err != nil {
		return nil, err
	}
	configureLMLogging()

	cModel, keepModel := goStringToCPtr(modelPath)
	_ = keepModel

	supportsSpec := checkLMSpeculativeDecoding(cModel)

	cBackend, keepBackend := goStringToCPtr(backend)
	_ = keepBackend

	settings := lmSettingsCreate(cModel, cBackend, cBackend, 0)
	if settings == 0 {
		return nil, fmt.Errorf("settings_create returned NULL for backend %s", backend)
	}
	if supportsSpec && lmSettingsSetEnableSpeculativeDecoding != nil {
		lmSettingsSetEnableSpeculativeDecoding(settings, true)
	}
	if lmSettingsSetUseRingbuffersLocalAttention != nil {
		lmSettingsSetUseRingbuffersLocalAttention(settings, true)
	}
	if backend == "gpu" && lmSettingsSetGpuDecodeStepsPerSync != nil {
		lmSettingsSetGpuDecodeStepsPerSync(settings, 8)
	}
	if backend == "cpu" && lmSettingsSetNumThreads != nil {
		lmSettingsSetNumThreads(settings, int32(runtime.NumCPU()))
	}
	if maxTokens > 0 && lmSettingsSetMaxNumTokens != nil {
		lmSettingsSetMaxNumTokens(settings, int32(maxTokens))
	}

	engine := lmEngineCreate(settings)
	lmSettingsDelete(settings)
	if engine == 0 {
		return nil, fmt.Errorf("engine_create returned NULL for backend %s", backend)
	}
	if lmEngineTryLoadVisionExecutor != nil {
		lmEngineTryLoadVisionExecutor(engine)
	}
	return &nativeEngineBackend{engine: engine}, nil
}

func (b *nativeEngineBackend) Close() error {
	if b.engine != 0 && lmEngineDelete != nil {
		lmEngineDelete(b.engine)
		b.engine = 0
	}
	return nil
}

type nativeConversationBackend struct {
	conv uintptr
}

func (b *nativeEngineBackend) NewConversation(systemJSON, toolsJSON, historyJSON string) (conversationBackend, error) {
	config := lmCfgCreate(b.engine)
	if config == 0 {
		return nil, fmt.Errorf("conversation_config_create returned NULL")
	}
	defer lmCfgDelete(config)

	if systemJSON != "" && lmCfgSetSystemMessage != nil {
		cSys, keepSys := goStringToCPtr(systemJSON)
		_ = keepSys
		lmCfgSetSystemMessage(config, cSys)
	}

	if toolsJSON != "" && lmCfgSetTools != nil {
		cTools, keepTools := goStringToCPtr(toolsJSON)
		_ = keepTools
		lmCfgSetTools(config, cTools)
	}

	if lmCfgSetStreamToolCalls != nil {
		cChan, keepChan := goStringToCPtr("tool_call")
		_ = keepChan
		lmCfgSetStreamToolCalls(config, true, cChan)
	}

	if historyJSON != "" && lmCfgSetMessages != nil {
		cMsgs, keepMsgs := goStringToCPtr(historyJSON)
		_ = keepMsgs
		lmCfgSetMessages(config, cMsgs)
	}

	conv := lmConvCreate(b.engine, config)
	if conv == 0 {
		return nil, fmt.Errorf("conversation_create returned NULL")
	}
	return &nativeConversationBackend{conv: conv}, nil
}

func (c *nativeConversationBackend) Close() error {
	if c.conv != 0 && lmConvDelete != nil {
		lmConvDelete(c.conv)
		c.conv = 0
	}
	return nil
}

func (c *nativeConversationBackend) SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error {
	_ = ctx
	tokenCallbackMu.Lock()
	tokenCallbackNextID++
	cbID := tokenCallbackNextID
	doneCh := make(chan struct{})
	tokenCallbackRegistry[cbID] = onChunk
	tokenDoneChannels[cbID] = doneCh
	tokenCallbackMu.Unlock()

	defer func() {
		tokenCallbackMu.Lock()
		delete(tokenCallbackRegistry, cbID)
		delete(tokenDoneChannels, cbID)
		tokenCallbackMu.Unlock()
	}()

	trampoline := purego.NewCallback(nativeTokenCallback)

	cMsg, keepMsg := goStringToCPtr(messageJSON)
	_ = keepMsg

	rc := lmConvSendStream(c.conv, cMsg, 0, 0, trampoline, cbID)
	if rc != 0 {
		return fmt.Errorf("send_message_stream failed: %d", rc)
	}
	<-doneCh

	if lmClearSinkLogger != nil {
		lmClearSinkLogger()
	}
	return nil
}
