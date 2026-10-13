//go:build !wasip1 && !js

package ai

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ── Purego C API bindings (one var per exported DLL symbol) ──────────────

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
	lmConvCancelProcess    func(conv uintptr)
	lmSetLogLevel          func(level uintptr)
	lmStreamChunkGetText   func(chunk uintptr) uintptr
	lmStreamChunkIsFinal   func(chunk uintptr) bool
	lmStreamChunkGetError  func(chunk uintptr) uintptr

	lmCfgSetTools           func(config, toolsJson uintptr)
	lmCfgSetSystemMessage   func(config, systemMessageJson uintptr)
	lmCfgSetStreamToolCalls func(config uintptr, stream bool, channelName uintptr)
	lmCfgSetMessages        func(config, messagesJson uintptr)

	lmSettingsSetEnableSpeculativeDecoding    func(settings uintptr, enable bool)
	lmSettingsSetUseRingbuffersLocalAttention func(settings uintptr, enable bool)
	lmSettingsSetGpuDecodeStepsPerSync        func(settings uintptr, steps int32)
	lmSettingsSetNumThreads                   func(settings uintptr, threads int32)
	lmSettingsSetMaxNumTokens                 func(settings uintptr, maxNumTokens int32)

	lmLoadedFileCreate                        func(modelPath uintptr) uintptr
	lmLoadedFileDelete                        func(file uintptr)
	lmLoadedFileHasSpeculativeDecodingSupport func(file uintptr) bool
	lmLoadedFileMaxContextTokens              func(file uintptr) uint32
	lmLoadedFileIsDynamicContext              func(file uintptr) bool
	lmLoadedFileSupportsThinking              func(file uintptr) bool
	lmLoadedFileSupportsFunctionCalling       func(file uintptr) bool
	lmLoadedFileModalitySupportedBackends     func(file uintptr, modality int32, backends *int32, max_size int32) int32
)

var (
	lmLibOnce sync.Once
	lmLibErr  error
	lmLib     uintptr

	nativeDrv nativeDriver
)

// ── nativeDriver — implements LMDriver via purego ─────────────────────────

type nativeDriver struct{}

func (d *nativeDriver) ensureLoaded(libPath string) error {
	return ensureLMLibLoadedImpl(libPath)
}

func (d *nativeDriver) getSymbol(name string) (uint64, error) {
	ptr, err := dlsym(lmLib, name)
	return uint64(ptr), err
}

func (d *nativeDriver) writeInt32(addr uint64, val int32) {
	*(*int32)(unsafe.Pointer(uintptr(addr))) = val
}

func (d *nativeDriver) writeBool(addr uint64, val bool) {
	*(*bool)(unsafe.Pointer(uintptr(addr))) = val
}

func (d *nativeDriver) stringToCPtr(s string) (uint64, func()) {
	c := s + "\x00"
	return uint64(uintptr(unsafe.Pointer(unsafe.StringData(c)))), func() {}
}

func (d *nativeDriver) ptrToString(ptr uint64) string {
	p := uintptr(ptr)
	if p == 0 {
		return ""
	}
	var buf []byte
	for i := uintptr(0); ; i++ {
		b := *(*byte)(unsafe.Pointer(p + i))
		if b == 0 {
			break
		}
		buf = append(buf, b)
	}
	return string(buf)
}

func (d *nativeDriver) settingsCreate(modelPath, backend, visionBackend, audioBackend uint64) uint64 {
	return uint64(lmSettingsCreate(uintptr(modelPath), uintptr(backend), uintptr(visionBackend), uintptr(audioBackend)))
}
func (d *nativeDriver) settingsDelete(settings uint64) { lmSettingsDelete(uintptr(settings)) }
func (d *nativeDriver) settingsSetEnableSpeculativeDecoding(settings uint64, enable bool) {
	lmSettingsSetEnableSpeculativeDecoding(uintptr(settings), enable)
}
func (d *nativeDriver) settingsSetUseRingbuffersLocalAttention(settings uint64, enable bool) {
	lmSettingsSetUseRingbuffersLocalAttention(uintptr(settings), enable)
}
func (d *nativeDriver) settingsSetGpuDecodeStepsPerSync(settings uint64, steps int32) {
	lmSettingsSetGpuDecodeStepsPerSync(uintptr(settings), steps)
}
func (d *nativeDriver) settingsSetNumThreads(settings uint64, threads int32) {
	lmSettingsSetNumThreads(uintptr(settings), threads)
}
func (d *nativeDriver) settingsSetMaxNumTokens(settings uint64, maxTokens int32) {
	lmSettingsSetMaxNumTokens(uintptr(settings), maxTokens)
}
func (d *nativeDriver) engineCreate(settings uint64) uint64   { return uint64(lmEngineCreate(uintptr(settings))) }
func (d *nativeDriver) engineDelete(engine uint64)            { lmEngineDelete(uintptr(engine)) }
func (d *nativeDriver) setLogLevel(level uint64)              { lmSetLogLevel(uintptr(level)) }
func (d *nativeDriver) cfgCreate(engine uint64) uint64        { return uint64(lmCfgCreate(uintptr(engine))) }
func (d *nativeDriver) cfgDelete(cfg uint64)                  { lmCfgDelete(uintptr(cfg)) }
func (d *nativeDriver) cfgSetTools(cfg, toolsJSON uint64) {
	lmCfgSetTools(uintptr(cfg), uintptr(toolsJSON))
}
func (d *nativeDriver) cfgSetSystemMessage(cfg, systemMsg uint64) {
	lmCfgSetSystemMessage(uintptr(cfg), uintptr(systemMsg))
}
func (d *nativeDriver) cfgSetStreamToolCalls(cfg uint64, stream bool, channelName uint64) {
	lmCfgSetStreamToolCalls(uintptr(cfg), stream, uintptr(channelName))
}
func (d *nativeDriver) cfgSetMessages(cfg, messagesJSON uint64) {
	lmCfgSetMessages(uintptr(cfg), uintptr(messagesJSON))
}
func (d *nativeDriver) convCreate(engine, cfg uint64) uint64  { return uint64(lmConvCreate(uintptr(engine), uintptr(cfg))) }
func (d *nativeDriver) convDelete(conv uint64)                { lmConvDelete(uintptr(conv)) }
func (d *nativeDriver) convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) int32 {
	return lmConvSendStream(uintptr(conv), uintptr(msg), uintptr(extraCtx), uintptr(optArgs), uintptr(callback), uintptr(userData))
}
func (d *nativeDriver) convCancelProcess(conv uint64) { lmConvCancelProcess(uintptr(conv)) }
func (d *nativeDriver) loadedFileCreate(modelPath uint64) uint64 { return uint64(lmLoadedFileCreate(uintptr(modelPath))) }
func (d *nativeDriver) loadedFileDelete(file uint64)              { lmLoadedFileDelete(uintptr(file)) }
func (d *nativeDriver) loadedFileHasSpeculativeDecodingSupport(file uint64) bool {
	return lmLoadedFileHasSpeculativeDecodingSupport(uintptr(file))
}
func (d *nativeDriver) loadedFileMaxContextTokens(file uint64) uint32 { return lmLoadedFileMaxContextTokens(uintptr(file)) }
func (d *nativeDriver) loadedFileIsDynamicContext(file uint64) bool   { return lmLoadedFileIsDynamicContext(uintptr(file)) }
func (d *nativeDriver) loadedFileSupportsThinking(file uint64) bool   { return lmLoadedFileSupportsThinking(uintptr(file)) }
func (d *nativeDriver) loadedFileSupportsFunctionCalling(file uint64) bool {
	return lmLoadedFileSupportsFunctionCalling(uintptr(file))
}
func (d *nativeDriver) loadedFileModalitySupportedBackends(file uint64, modality int32) []uint32 {
	var backends [8]int32
	count := lmLoadedFileModalitySupportedBackends(uintptr(file), modality, &backends[0], int32(len(backends)))
	if count > 0 && count <= int32(len(backends)) {
		var out []uint32
		for i := int32(0); i < count; i++ {
			out = append(out, uint32(backends[i]))
		}
		return out
	}
	return []uint32{}
}
func (d *nativeDriver) streamChunkGetText(chunk uint64) uint64  { return uint64(lmStreamChunkGetText(uintptr(chunk))) }
func (d *nativeDriver) streamChunkIsFinal(chunk uint64) bool    { return lmStreamChunkIsFinal(uintptr(chunk)) }
func (d *nativeDriver) streamChunkGetError(chunk uint64) uint64 { return uint64(lmStreamChunkGetError(uintptr(chunk))) }

var callbackTrampoline uintptr

func (d *nativeDriver) createTrampoline() uint64 {
	if callbackTrampoline == 0 {
		callbackTrampoline = purego.NewCallback(nativeDriverCallback)
	}
	return uint64(callbackTrampoline)
}

// ── Public helpers (used by tflite_native.go and other platform code) ──────

func goStringToCPtr(s string) (uintptr, string) {
	c := s + "\x00"
	return uintptr(unsafe.Pointer(unsafe.StringData(c))), c
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

// nativeDriverCallback is the C-callable trampoline registered via
// purego.NewCallback. It receives (userData, chunkPtr) from C and dispatches
// to the shared callback logic.
func nativeDriverCallback(userData, chunkPtr uintptr) uintptr {
	sharedTokenCallback(&nativeDrv, uint64(userData), uint64(chunkPtr))
	return 0
}

// ── Library loading ────────────────────────────────────────────────────────

func ensureLMLibLoadedImpl(libPath string) error {
	lmLibOnce.Do(func() {
		var err error
		if Verbose {
			fmt.Fprintf(os.Stderr, "[ai] loading LiteRT-LM native library: %s\n", libPath)
		}
		lmLib, err = dlopen(libPath, RTLD_NOW|RTLD_GLOBAL)
		if err != nil {
			lmLibErr = fmt.Errorf("failed to load %s: %w", libPath, err)
			return
		}
		purego.RegisterLibFunc(&lmSettingsCreate, lmLib, "litert_lm_engine_settings_create")
		purego.RegisterLibFunc(&lmSettingsDelete, lmLib, "litert_lm_engine_settings_delete")
		purego.RegisterLibFunc(&lmEngineCreate, lmLib, "litert_lm_engine_create")
		purego.RegisterLibFunc(&lmEngineDelete, lmLib, "litert_lm_engine_delete")
		purego.RegisterLibFunc(&lmCfgCreate, lmLib, "litert_lm_conversation_config_create")
		purego.RegisterLibFunc(&lmCfgDelete, lmLib, "litert_lm_conversation_config_delete")
		purego.RegisterLibFunc(&lmConvCreate, lmLib, "litert_lm_conversation_create")
		purego.RegisterLibFunc(&lmConvDelete, lmLib, "litert_lm_conversation_delete")
		purego.RegisterLibFunc(&lmConvSendStream, lmLib, "litert_lm_conversation_send_message_stream")
		purego.RegisterLibFunc(&lmConvCancelProcess, lmLib, "litert_lm_conversation_cancel_process")
		purego.RegisterLibFunc(&lmSetLogLevel, lmLib, "litert_lm_set_min_log_level")
		purego.RegisterLibFunc(&lmStreamChunkGetText, lmLib, "litert_lm_stream_chunk_get_text")
		purego.RegisterLibFunc(&lmStreamChunkIsFinal, lmLib, "litert_lm_stream_chunk_is_final")
		purego.RegisterLibFunc(&lmStreamChunkGetError, lmLib, "litert_lm_stream_chunk_get_error")

		purego.RegisterLibFunc(&lmCfgSetTools, lmLib, "litert_lm_conversation_config_set_tools")
		purego.RegisterLibFunc(&lmCfgSetSystemMessage, lmLib, "litert_lm_conversation_config_set_system_message")
		purego.RegisterLibFunc(&lmCfgSetStreamToolCalls, lmLib, "litert_lm_conversation_config_set_stream_tool_calls")
		purego.RegisterLibFunc(&lmCfgSetMessages, lmLib, "litert_lm_conversation_config_set_messages")

		purego.RegisterLibFunc(&lmSettingsSetEnableSpeculativeDecoding, lmLib, "litert_lm_engine_settings_set_enable_speculative_decoding")
		purego.RegisterLibFunc(&lmSettingsSetUseRingbuffersLocalAttention, lmLib, "litert_lm_engine_settings_set_use_ringbuffers_local_attention")
		purego.RegisterLibFunc(&lmSettingsSetGpuDecodeStepsPerSync, lmLib, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync")
		purego.RegisterLibFunc(&lmSettingsSetNumThreads, lmLib, "litert_lm_engine_settings_set_num_threads")
		purego.RegisterLibFunc(&lmSettingsSetMaxNumTokens, lmLib, "litert_lm_engine_settings_set_max_num_tokens")

		purego.RegisterLibFunc(&lmLoadedFileCreate, lmLib, "litert_lm_loaded_file_create")
		purego.RegisterLibFunc(&lmLoadedFileDelete, lmLib, "litert_lm_loaded_file_delete")
		purego.RegisterLibFunc(&lmLoadedFileHasSpeculativeDecodingSupport, lmLib, "litert_lm_loaded_file_has_speculative_decoding_support")
		purego.RegisterLibFunc(&lmLoadedFileMaxContextTokens, lmLib, "litert_lm_loaded_file_max_context_tokens")
		purego.RegisterLibFunc(&lmLoadedFileIsDynamicContext, lmLib, "litert_lm_loaded_file_is_dynamic_context")
		purego.RegisterLibFunc(&lmLoadedFileSupportsThinking, lmLib, "litert_lm_loaded_file_supports_thinking")
		purego.RegisterLibFunc(&lmLoadedFileSupportsFunctionCalling, lmLib, "litert_lm_loaded_file_supports_function_calling")
		purego.RegisterLibFunc(&lmLoadedFileModalitySupportedBackends, lmLib, "litert_lm_loaded_file_modality_supported_backends")
	})
	return lmLibErr
}

// ── Thin public wrappers ───────────────────────────────────────────────────

func openBackend(libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	return sharedOpenBackend(&nativeDrv, libPath, modelPath, backend, maxTokens)
}

func inspectModel(libPath, modelPath string) (*ModelInfo, error) {
	return sharedInspectModel(&nativeDrv, libPath, modelPath)
}