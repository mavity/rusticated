//go:build !wasip1 && !js

package ai

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ── Purego C API bindings (one var per exported DLL symbol) ──────────────

var (
	lmSettingsCreate      func(modelPath, backend, visionBackend, audioBackend uintptr) uintptr
	lmSettingsDelete      func(settings uintptr)
	lmEngineCreate        func(settings uintptr) uintptr
	lmEngineDelete        func(engine uintptr)
	lmCfgCreate           func(engine uintptr) uintptr
	lmCfgDelete           func(config uintptr)
	lmConvCreate          func(engine, config uintptr) uintptr
	lmConvDelete          func(conv uintptr)
	lmConvSendStream      func(conv, message, extraCtx, optArgs, callback, userData uintptr) int32
	lmConvCancelProcess   func(conv uintptr)
	lmSetLogLevel         func(level uintptr)
	lmStreamChunkGetText  func(chunk uintptr) uintptr
	lmStreamChunkIsFinal  func(chunk uintptr) bool
	lmStreamChunkGetError func(chunk uintptr) uintptr

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

	lmEmbeddingSettingsCreate             func(modelPath, backend, visionBackend, audioBackend uintptr) uintptr
	lmEmbeddingSettingsDelete             func(settings uintptr)
	lmEmbeddingSettingsSetNumThreads      func(settings uintptr, threads int32)
	lmEmbeddingEngineCreate               func(settings uintptr) uintptr
	lmEmbeddingEngineDelete               func(engine uintptr)
	lmEmbeddingOptionsCreate              func() uintptr
	lmEmbeddingOptionsDelete              func(options uintptr)
	lmEmbeddingOptionsSetNormalize        func(options uintptr, normalize bool)
	lmEmbeddingOptionsSetOverflowStrategy func(options uintptr, strategy int32)
	lmInputDataCreate                     func(dataType int32, data uintptr, size uintptr) uintptr
	lmInputDataDelete                     func(inputData uintptr)
	lmEmbeddingEngineCompute              func(engine uintptr, inputs uintptr, numInputs uintptr, options uintptr) uintptr
	lmEmbeddingResponseDelete             func(resp uintptr)
	lmEmbeddingResponseGetSize            func(resp uintptr) uintptr
	lmEmbeddingResponseGetValues          func(resp uintptr) *float32
)

var (
	lmLibOnce sync.Once
	lmLibErr  error
	lmLib     uintptr

	nativeDrv        nativeDriver
	nativeInputPinMu sync.Mutex
	nativeInputPins  = map[uint64]pinnedInput{}
)

type pinnedInput struct {
	pinner *runtime.Pinner
	data   []byte
}

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
	b := append([]byte(s), 0)
	var p runtime.Pinner
	p.Pin(&b[0])
	return uint64(uintptr(unsafe.Pointer(&b[0]))), func() {
		p.Unpin()
		runtime.KeepAlive(b)
	}
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
func (d *nativeDriver) engineCreate(settings uint64) uint64 {
	return uint64(lmEngineCreate(uintptr(settings)))
}
func (d *nativeDriver) engineDelete(engine uint64)     { lmEngineDelete(uintptr(engine)) }
func (d *nativeDriver) setLogLevel(level uint64)       { lmSetLogLevel(uintptr(level)) }
func (d *nativeDriver) cfgCreate(engine uint64) uint64 { return uint64(lmCfgCreate(uintptr(engine))) }
func (d *nativeDriver) cfgDelete(cfg uint64)           { lmCfgDelete(uintptr(cfg)) }
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
func (d *nativeDriver) convCreate(engine, cfg uint64) uint64 {
	return uint64(lmConvCreate(uintptr(engine), uintptr(cfg)))
}
func (d *nativeDriver) convDelete(conv uint64) { lmConvDelete(uintptr(conv)) }
func (d *nativeDriver) convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) int32 {
	return lmConvSendStream(uintptr(conv), uintptr(msg), uintptr(extraCtx), uintptr(optArgs), uintptr(callback), uintptr(userData))
}
func (d *nativeDriver) convCancelProcess(conv uint64) { lmConvCancelProcess(uintptr(conv)) }
func (d *nativeDriver) loadedFileCreate(modelPath uint64) uint64 {
	return uint64(lmLoadedFileCreate(uintptr(modelPath)))
}
func (d *nativeDriver) loadedFileDelete(file uint64) { lmLoadedFileDelete(uintptr(file)) }
func (d *nativeDriver) loadedFileHasSpeculativeDecodingSupport(file uint64) bool {
	return lmLoadedFileHasSpeculativeDecodingSupport(uintptr(file))
}
func (d *nativeDriver) loadedFileMaxContextTokens(file uint64) uint32 {
	return lmLoadedFileMaxContextTokens(uintptr(file))
}
func (d *nativeDriver) loadedFileIsDynamicContext(file uint64) bool {
	return lmLoadedFileIsDynamicContext(uintptr(file))
}
func (d *nativeDriver) loadedFileSupportsThinking(file uint64) bool {
	return lmLoadedFileSupportsThinking(uintptr(file))
}
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
func (d *nativeDriver) streamChunkGetText(chunk uint64) uint64 {
	return uint64(lmStreamChunkGetText(uintptr(chunk)))
}
func (d *nativeDriver) streamChunkIsFinal(chunk uint64) bool {
	return lmStreamChunkIsFinal(uintptr(chunk))
}
func (d *nativeDriver) streamChunkGetError(chunk uint64) uint64 {
	return uint64(lmStreamChunkGetError(uintptr(chunk)))
}

func (d *nativeDriver) embeddingSettingsCreate(modelPath, backend, visionBackend, audioBackend uint64) uint64 {
	return uint64(lmEmbeddingSettingsCreate(uintptr(modelPath), uintptr(backend), uintptr(visionBackend), uintptr(audioBackend)))
}
func (d *nativeDriver) embeddingSettingsDelete(settings uint64) {
	lmEmbeddingSettingsDelete(uintptr(settings))
}
func (d *nativeDriver) embeddingSettingsSetNumThreads(settings uint64, threads int32) {
	lmEmbeddingSettingsSetNumThreads(uintptr(settings), threads)
}
func (d *nativeDriver) embeddingEngineCreate(settings uint64) uint64 {
	return uint64(lmEmbeddingEngineCreate(uintptr(settings)))
}
func (d *nativeDriver) embeddingEngineDelete(engine uint64) {
	lmEmbeddingEngineDelete(uintptr(engine))
}
func (d *nativeDriver) embeddingOptionsCreate() uint64 {
	return uint64(lmEmbeddingOptionsCreate())
}
func (d *nativeDriver) embeddingOptionsDelete(options uint64) {
	lmEmbeddingOptionsDelete(uintptr(options))
}
func (d *nativeDriver) embeddingOptionsSetNormalize(options uint64, normalize bool) {
	lmEmbeddingOptionsSetNormalize(uintptr(options), normalize)
}
func (d *nativeDriver) embeddingOptionsSetInputOverflowStrategy(options uint64, strategy int32) {
	lmEmbeddingOptionsSetOverflowStrategy(uintptr(options), strategy)
}
func (d *nativeDriver) inputDataCreate(dataType int32, data []byte) uint64 {
	var ptr uintptr
	var pinner *runtime.Pinner
	if len(data) > 0 {
		pinner = new(runtime.Pinner)
		pinner.Pin(&data[0])
		ptr = uintptr(unsafe.Pointer(&data[0]))
	}
	res := lmInputDataCreate(dataType, ptr, uintptr(len(data)))
	if res == 0 {
		if pinner != nil {
			pinner.Unpin()
		}
		return 0
	}
	if pinner != nil {
		nativeInputPinMu.Lock()
		nativeInputPins[uint64(res)] = pinnedInput{pinner: pinner, data: data}
		nativeInputPinMu.Unlock()
	}
	return uint64(res)
}
func (d *nativeDriver) inputDataDelete(inputData uint64) {
	lmInputDataDelete(uintptr(inputData))
	nativeInputPinMu.Lock()
	pi, ok := nativeInputPins[inputData]
	if ok {
		delete(nativeInputPins, inputData)
	}
	nativeInputPinMu.Unlock()
	if ok && pi.pinner != nil {
		pi.pinner.Unpin()
		runtime.KeepAlive(pi.data)
	}
}
func (d *nativeDriver) embeddingEngineCompute(engine uint64, inputs []uint64, options uint64) ([]float32, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no inputs provided")
	}
	cInputs := make([]uintptr, len(inputs))
	for i, in := range inputs {
		cInputs[i] = uintptr(in)
	}
	var pinner runtime.Pinner
	pinner.Pin(&cInputs[0])
	defer pinner.Unpin()

	resp := lmEmbeddingEngineCompute(uintptr(engine), uintptr(unsafe.Pointer(&cInputs[0])), uintptr(len(cInputs)), uintptr(options))
	runtime.KeepAlive(cInputs)
	if resp == 0 {
		return nil, fmt.Errorf("litert_lm_embedding_engine_compute_embedding failed")
	}
	defer lmEmbeddingResponseDelete(resp)

	size := lmEmbeddingResponseGetSize(resp)
	valsPtr := lmEmbeddingResponseGetValues(resp)
	if size == 0 {
		return []float32{}, nil
	}
	if valsPtr == nil {
		return nil, fmt.Errorf("invalid embedding response values pointer")
	}
	src := unsafe.Slice(valsPtr, int(size))
	out := make([]float32, int(size))
	copy(out, src)
	return out, nil
}

var callbackTrampoline uintptr

func (d *nativeDriver) createTrampoline() uint64 {
	if callbackTrampoline == 0 {
		callbackTrampoline = purego.NewCallback(nativeDriverCallback)
	}
	return uint64(callbackTrampoline)
}

// ── Public helpers (used by tflite_native.go and other platform code) ──────

func goStringToCPtr(s string) (uintptr, func()) {
	ptr, free := nativeDrv.stringToCPtr(s)
	return uintptr(ptr), free
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

		purego.RegisterLibFunc(&lmEmbeddingSettingsCreate, lmLib, "litert_lm_embedding_engine_settings_create")
		purego.RegisterLibFunc(&lmEmbeddingSettingsDelete, lmLib, "litert_lm_embedding_engine_settings_delete")
		purego.RegisterLibFunc(&lmEmbeddingSettingsSetNumThreads, lmLib, "litert_lm_embedding_engine_settings_set_num_threads")
		purego.RegisterLibFunc(&lmEmbeddingEngineCreate, lmLib, "litert_lm_embedding_engine_create")
		purego.RegisterLibFunc(&lmEmbeddingEngineDelete, lmLib, "litert_lm_embedding_engine_delete")
		purego.RegisterLibFunc(&lmEmbeddingOptionsCreate, lmLib, "litert_lm_embedding_options_create")
		purego.RegisterLibFunc(&lmEmbeddingOptionsDelete, lmLib, "litert_lm_embedding_options_delete")
		purego.RegisterLibFunc(&lmEmbeddingOptionsSetNormalize, lmLib, "litert_lm_embedding_options_set_normalize")
		purego.RegisterLibFunc(&lmEmbeddingOptionsSetOverflowStrategy, lmLib, "litert_lm_embedding_options_set_input_overflow_strategy")
		purego.RegisterLibFunc(&lmInputDataCreate, lmLib, "litert_lm_input_data_create")
		purego.RegisterLibFunc(&lmInputDataDelete, lmLib, "litert_lm_input_data_delete")
		purego.RegisterLibFunc(&lmEmbeddingEngineCompute, lmLib, "litert_lm_embedding_engine_compute_embedding")
		purego.RegisterLibFunc(&lmEmbeddingResponseDelete, lmLib, "litert_lm_embedding_response_delete")
		purego.RegisterLibFunc(&lmEmbeddingResponseGetSize, lmLib, "litert_lm_embedding_response_get_size")
		purego.RegisterLibFunc(&lmEmbeddingResponseGetValues, lmLib, "litert_lm_embedding_response_get_values")
	})
	return lmLibErr
}

// ── Thin public wrappers ───────────────────────────────────────────────────

func openBackend(libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	return sharedOpenBackend(&nativeDrv, libPath, modelPath, backend, maxTokens)
}

func openEmbedderBackend(libPath, modelPath, backend string) (embedderBackend, error) {
	return sharedOpenEmbedder(&nativeDrv, libPath, modelPath, backend)
}

func inspectModel(libPath, modelPath string) (*ModelInfo, error) {
	return sharedInspectModel(&nativeDrv, libPath, modelPath)
}

func isBrowserHost() bool {
	return false
}
