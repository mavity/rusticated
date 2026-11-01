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
	lmGetAPIVersion       func(outMajor, outMinor, outPatch uintptr) int32
	lmGetLastErrorMessage func() uintptr

	lmSettingsCreate         func(modelPath, backend, visionBackend, audioBackend, outSettings uintptr) int32
	lmSettingsDelete         func(settings uintptr)
	lmSpecConfigCreate       func(outConfig uintptr) int32
	lmSpecConfigDelete       func(config uintptr)
	lmSpecConfigSetMode      func(config uintptr, mode int32) int32
	lmSettingsSetSpecConfig  func(settings, config uintptr) int32
	lmSettingsSetDispatchDir func(settings, libDir uintptr) int32
	lmEngineCreate           func(settings, outEngine uintptr) int32
	lmEngineDelete           func(engine uintptr)
	lmCfgCreate              func(outConfig uintptr) int32
	lmCfgDelete              func(config uintptr)
	lmConvCreate             func(engine, config, outConversation uintptr) int32
	lmConvDelete             func(conv uintptr)
	lmConvSendStream         func(conv, message, extraCtx, optArgs, callback, userData uintptr) int32
	lmConvCancelProcess      func(conv uintptr) int32
	lmSetLogLevel            func(level int32) int32
	lmStreamChunkGetText     func(chunk, outText uintptr) int32
	lmStreamChunkIsFinal     func(chunk, outFinal uintptr) int32
	lmStreamChunkGetError    func(chunk, outError uintptr) int32

	lmCfgSetTools                     func(config, toolsJson uintptr) int32
	lmCfgSetSystemMessage             func(config, systemMessageJson uintptr) int32
	lmCfgSetEnableConstrainedDecoding func(config uintptr, enable bool) int32
	lmCfgSetStreamToolCalls           func(config uintptr, stream bool, channelName uintptr) int32
	lmCfgSetMessages                  func(config, messagesJson uintptr) int32

	lmSettingsSetUseRingbuffersLocalAttention func(settings uintptr, enable bool) int32
	lmSettingsSetGpuDecodeStepsPerSync        func(settings uintptr, steps int32) int32
	lmSettingsSetNumThreads                   func(settings uintptr, threads int32) int32
	lmSettingsSetMaxNumTokens                 func(settings uintptr, maxNumTokens int32) int32

	lmLoadedFileCreate                        func(modelPath, outFile uintptr) int32
	lmLoadedFileDelete                        func(file uintptr)
	lmLoadedFileHasSpeculativeDecodingSupport func(file, outSupported uintptr) int32
	lmLoadedFileMaxContextTokens              func(file, outMaxTokens uintptr) int32
	lmLoadedFileIsDynamicContext              func(file, outDynamic uintptr) int32
	lmLoadedFileSupportsThinking              func(file, outSupported uintptr) int32
	lmLoadedFileSupportsFunctionCalling       func(file, outSupported uintptr) int32
	lmLoadedFileModalitySupportedBackends     func(file uintptr, modality int32, backends uintptr, maxSize, outCount uintptr) int32

	lmEmbeddingSettingsCreate             func(modelPath, backend, visionBackend, audioBackend, outSettings uintptr) int32
	lmEmbeddingSettingsDelete             func(settings uintptr)
	lmEmbeddingSettingsSetNumThreads      func(settings uintptr, threads int32) int32
	lmEmbeddingEngineCreate               func(settings, outEngine uintptr) int32
	lmEmbeddingEngineDelete               func(engine uintptr)
	lmEmbeddingOptionsCreate              func(outOptions uintptr) int32
	lmEmbeddingOptionsDelete              func(options uintptr)
	lmEmbeddingOptionsSetNormalize        func(options uintptr, normalize bool) int32
	lmEmbeddingOptionsSetOverflowStrategy func(options uintptr, strategy int32) int32
	lmInputDataCreate                     func(dataType int32, data, size, outInput uintptr) int32
	lmInputDataDelete                     func(inputData uintptr)
	lmEmbeddingEngineCompute              func(engine, inputs, numInputs, options, outResponse uintptr) int32
	lmEmbeddingResponseDelete             func(resp uintptr)
	lmEmbeddingResponseGetSize            func(resp, outSize uintptr) int32
	lmEmbeddingResponseGetValues          func(resp, outValues uintptr) int32
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

func (d *nativeDriver) stringToCPtr(s string) (uint64, func(), error) {
	b := append([]byte(s), 0)
	var p runtime.Pinner
	p.Pin(&b[0])
	return uint64(uintptr(unsafe.Pointer(&b[0]))), func() {
		p.Unpin()
		runtime.KeepAlive(b)
	}, nil
}

func (d *nativeDriver) callStatus(operation string, call func() int32) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	code := call()
	if code == liteRtStatusOK {
		return nil
	}
	message := ""
	if lmGetLastErrorMessage != nil {
		message = ptrToGoString(lmGetLastErrorMessage())
	}
	return newLiteRtStatusError(operation, code, message)
}

func (d *nativeDriver) ptrToString(ptr uint64) (string, error) {
	return ptrToGoString(uintptr(ptr)), nil
}

func (d *nativeDriver) settingsCreate(modelPath, backend, visionBackend, audioBackend uint64) (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_engine_settings_create", func() int32 {
		return lmSettingsCreate(uintptr(modelPath), uintptr(backend), uintptr(visionBackend), uintptr(audioBackend), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) settingsDelete(settings uint64) { lmSettingsDelete(uintptr(settings)) }
func (d *nativeDriver) speculativeDecodingConfigCreate() (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_speculative_decoding_config_create", func() int32 {
		return lmSpecConfigCreate(uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) speculativeDecodingConfigDelete(config uint64) {
	lmSpecConfigDelete(uintptr(config))
}
func (d *nativeDriver) speculativeDecodingConfigSetMode(config uint64, mode int32) error {
	return d.callStatus("litert_lm_speculative_decoding_config_set_mode", func() int32 {
		return lmSpecConfigSetMode(uintptr(config), mode)
	})
}
func (d *nativeDriver) settingsSetSpeculativeDecodingConfig(settings, config uint64) error {
	return d.callStatus("litert_lm_engine_settings_set_speculative_decoding_config", func() int32 {
		return lmSettingsSetSpecConfig(uintptr(settings), uintptr(config))
	})
}
func (d *nativeDriver) settingsSetLitertDispatchLibDir(settings, libDir uint64) error {
	return d.callStatus("litert_lm_engine_settings_set_litert_dispatch_lib_dir", func() int32 {
		return lmSettingsSetDispatchDir(uintptr(settings), uintptr(libDir))
	})
}
func (d *nativeDriver) settingsSetUseRingbuffersLocalAttention(settings uint64, enable bool) error {
	return d.callStatus("litert_lm_engine_settings_set_use_ringbuffers_local_attention", func() int32 {
		return lmSettingsSetUseRingbuffersLocalAttention(uintptr(settings), enable)
	})
}
func (d *nativeDriver) settingsSetGpuDecodeStepsPerSync(settings uint64, steps int32) error {
	return d.callStatus("litert_lm_engine_settings_set_gpu_decode_steps_per_sync", func() int32 {
		return lmSettingsSetGpuDecodeStepsPerSync(uintptr(settings), steps)
	})
}
func (d *nativeDriver) settingsSetNumThreads(settings uint64, threads int32) error {
	return d.callStatus("litert_lm_engine_settings_set_num_threads", func() int32 {
		return lmSettingsSetNumThreads(uintptr(settings), threads)
	})
}
func (d *nativeDriver) settingsSetMaxNumTokens(settings uint64, maxTokens int32) error {
	return d.callStatus("litert_lm_engine_settings_set_max_num_tokens", func() int32 {
		return lmSettingsSetMaxNumTokens(uintptr(settings), maxTokens)
	})
}
func (d *nativeDriver) engineCreate(settings uint64) (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_engine_create", func() int32 {
		return lmEngineCreate(uintptr(settings), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) engineDelete(engine uint64) { lmEngineDelete(uintptr(engine)) }
func (d *nativeDriver) setLogLevel(level int32) error {
	return d.callStatus("litert_lm_set_min_log_level", func() int32 { return lmSetLogLevel(level) })
}
func (d *nativeDriver) cfgCreate() (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_conversation_config_create", func() int32 {
		return lmCfgCreate(uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) cfgDelete(cfg uint64) { lmCfgDelete(uintptr(cfg)) }
func (d *nativeDriver) cfgSetTools(cfg, toolsJSON uint64) error {
	return d.callStatus("litert_lm_conversation_config_set_tools", func() int32 {
		return lmCfgSetTools(uintptr(cfg), uintptr(toolsJSON))
	})
}
func (d *nativeDriver) cfgSetSystemMessage(cfg, systemMsg uint64) error {
	return d.callStatus("litert_lm_conversation_config_set_system_message", func() int32 {
		return lmCfgSetSystemMessage(uintptr(cfg), uintptr(systemMsg))
	})
}
func (d *nativeDriver) cfgSetEnableConstrainedDecoding(cfg uint64, enable bool) error {
	return d.callStatus("litert_lm_conversation_config_set_enable_constrained_decoding", func() int32 {
		return lmCfgSetEnableConstrainedDecoding(uintptr(cfg), enable)
	})
}
func (d *nativeDriver) cfgSetStreamToolCalls(cfg uint64, stream bool, channelName uint64) error {
	return d.callStatus("litert_lm_conversation_config_set_stream_tool_calls", func() int32 {
		return lmCfgSetStreamToolCalls(uintptr(cfg), stream, uintptr(channelName))
	})
}
func (d *nativeDriver) cfgSetMessages(cfg, messagesJSON uint64) error {
	return d.callStatus("litert_lm_conversation_config_set_messages", func() int32 {
		return lmCfgSetMessages(uintptr(cfg), uintptr(messagesJSON))
	})
}
func (d *nativeDriver) convCreate(engine, cfg uint64) (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_conversation_create", func() int32 {
		return lmConvCreate(uintptr(engine), uintptr(cfg), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) convDelete(conv uint64) { lmConvDelete(uintptr(conv)) }
func (d *nativeDriver) convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) error {
	return d.callStatus("litert_lm_conversation_send_message_stream", func() int32 {
		return lmConvSendStream(uintptr(conv), uintptr(msg), uintptr(extraCtx), uintptr(optArgs), uintptr(callback), uintptr(userData))
	})
}
func (d *nativeDriver) convCancelProcess(conv uint64) error {
	return d.callStatus("litert_lm_conversation_cancel_process", func() int32 {
		return lmConvCancelProcess(uintptr(conv))
	})
}
func (d *nativeDriver) loadedFileCreate(modelPath uint64) (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_loaded_file_create", func() int32 {
		return lmLoadedFileCreate(uintptr(modelPath), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) loadedFileDelete(file uint64) { lmLoadedFileDelete(uintptr(file)) }
func (d *nativeDriver) loadedFileHasSpeculativeDecodingSupport(file uint64) (bool, error) {
	var out uint8
	err := d.callStatus("litert_lm_loaded_file_has_speculative_decoding_support", func() int32 {
		return lmLoadedFileHasSpeculativeDecodingSupport(uintptr(file), uintptr(unsafe.Pointer(&out)))
	})
	return out != 0, err
}
func (d *nativeDriver) loadedFileMaxContextTokens(file uint64) (uint32, error) {
	var out uint32
	err := d.callStatus("litert_lm_loaded_file_max_context_tokens", func() int32 {
		return lmLoadedFileMaxContextTokens(uintptr(file), uintptr(unsafe.Pointer(&out)))
	})
	return out, err
}
func (d *nativeDriver) loadedFileIsDynamicContext(file uint64) (bool, error) {
	var out uint8
	err := d.callStatus("litert_lm_loaded_file_is_dynamic_context", func() int32 {
		return lmLoadedFileIsDynamicContext(uintptr(file), uintptr(unsafe.Pointer(&out)))
	})
	return out != 0, err
}
func (d *nativeDriver) loadedFileSupportsThinking(file uint64) (bool, error) {
	var out uint8
	err := d.callStatus("litert_lm_loaded_file_supports_thinking", func() int32 {
		return lmLoadedFileSupportsThinking(uintptr(file), uintptr(unsafe.Pointer(&out)))
	})
	return out != 0, err
}
func (d *nativeDriver) loadedFileSupportsFunctionCalling(file uint64) (bool, error) {
	var out uint8
	err := d.callStatus("litert_lm_loaded_file_supports_function_calling", func() int32 {
		return lmLoadedFileSupportsFunctionCalling(uintptr(file), uintptr(unsafe.Pointer(&out)))
	})
	return out != 0, err
}
func (d *nativeDriver) loadedFileModalitySupportedBackends(file uint64, modality int32) ([]uint32, error) {
	var backends [8]int32
	var count uintptr
	err := d.callStatus("litert_lm_loaded_file_modality_supported_backends", func() int32 {
		return lmLoadedFileModalitySupportedBackends(
			uintptr(file),
			modality,
			uintptr(unsafe.Pointer(&backends[0])),
			uintptr(len(backends)),
			uintptr(unsafe.Pointer(&count)),
		)
	})
	runtime.KeepAlive(&backends)
	runtime.KeepAlive(&count)
	if err != nil {
		return nil, err
	}
	if count > uintptr(len(backends)) {
		count = uintptr(len(backends))
	}
	out := make([]uint32, int(count))
	for i := range out {
		out[i] = uint32(backends[i])
	}
	return out, nil
}
func (d *nativeDriver) streamChunkGetText(chunk uint64) (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_stream_chunk_get_text", func() int32 {
		return lmStreamChunkGetText(uintptr(chunk), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) streamChunkIsFinal(chunk uint64) (bool, error) {
	var out uint8
	err := d.callStatus("litert_lm_stream_chunk_is_final", func() int32 {
		return lmStreamChunkIsFinal(uintptr(chunk), uintptr(unsafe.Pointer(&out)))
	})
	return out != 0, err
}
func (d *nativeDriver) streamChunkGetError(chunk uint64) (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_stream_chunk_get_error", func() int32 {
		return lmStreamChunkGetError(uintptr(chunk), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}

func (d *nativeDriver) embeddingSettingsCreate(modelPath, backend, visionBackend, audioBackend uint64) (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_embedding_engine_settings_create", func() int32 {
		return lmEmbeddingSettingsCreate(uintptr(modelPath), uintptr(backend), uintptr(visionBackend), uintptr(audioBackend), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) embeddingSettingsDelete(settings uint64) {
	lmEmbeddingSettingsDelete(uintptr(settings))
}
func (d *nativeDriver) embeddingSettingsSetNumThreads(settings uint64, threads int32) error {
	return d.callStatus("litert_lm_embedding_engine_settings_set_num_threads", func() int32 {
		return lmEmbeddingSettingsSetNumThreads(uintptr(settings), threads)
	})
}
func (d *nativeDriver) embeddingEngineCreate(settings uint64) (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_embedding_engine_create", func() int32 {
		return lmEmbeddingEngineCreate(uintptr(settings), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) embeddingEngineDelete(engine uint64) {
	lmEmbeddingEngineDelete(uintptr(engine))
}
func (d *nativeDriver) embeddingOptionsCreate() (uint64, error) {
	var out uintptr
	err := d.callStatus("litert_lm_embedding_options_create", func() int32 {
		return lmEmbeddingOptionsCreate(uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) embeddingOptionsDelete(options uint64) {
	lmEmbeddingOptionsDelete(uintptr(options))
}
func (d *nativeDriver) embeddingOptionsSetNormalize(options uint64, normalize bool) error {
	return d.callStatus("litert_lm_embedding_options_set_normalize", func() int32 {
		return lmEmbeddingOptionsSetNormalize(uintptr(options), normalize)
	})
}
func (d *nativeDriver) embeddingOptionsSetInputOverflowStrategy(options uint64, strategy int32) error {
	return d.callStatus("litert_lm_embedding_options_set_input_overflow_strategy", func() int32 {
		return lmEmbeddingOptionsSetOverflowStrategy(uintptr(options), strategy)
	})
}
func (d *nativeDriver) inputDataCreate(dataType int32, data []byte) (uint64, error) {
	var ptr uintptr
	var pinner runtime.Pinner
	if len(data) > 0 {
		pinner.Pin(&data[0])
		ptr = uintptr(unsafe.Pointer(&data[0]))
		defer pinner.Unpin()
	}
	var out uintptr
	err := d.callStatus("litert_lm_input_data_create", func() int32 {
		return lmInputDataCreate(dataType, ptr, uintptr(len(data)), uintptr(unsafe.Pointer(&out)))
	})
	runtime.KeepAlive(data)
	runtime.KeepAlive(&out)
	return uint64(out), err
}
func (d *nativeDriver) inputDataDelete(inputData uint64) { lmInputDataDelete(uintptr(inputData)) }
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

	var resp uintptr
	err := d.callStatus("litert_lm_embedding_engine_compute_embedding", func() int32 {
		return lmEmbeddingEngineCompute(uintptr(engine), uintptr(unsafe.Pointer(&cInputs[0])), uintptr(len(cInputs)), uintptr(options), uintptr(unsafe.Pointer(&resp)))
	})
	runtime.KeepAlive(cInputs)
	runtime.KeepAlive(&resp)
	if err != nil {
		return nil, err
	}
	if resp == 0 {
		return nil, fmt.Errorf("embedding computation returned NULL without an error")
	}
	defer lmEmbeddingResponseDelete(resp)

	var size uintptr
	if err := d.callStatus("litert_lm_embedding_response_get_size", func() int32 {
		return lmEmbeddingResponseGetSize(resp, uintptr(unsafe.Pointer(&size)))
	}); err != nil {
		return nil, err
	}
	var valsPtr uintptr
	if err := d.callStatus("litert_lm_embedding_response_get_values", func() int32 {
		return lmEmbeddingResponseGetValues(resp, uintptr(unsafe.Pointer(&valsPtr)))
	}); err != nil {
		return nil, err
	}
	if size == 0 {
		return []float32{}, nil
	}
	if valsPtr == 0 {
		return nil, fmt.Errorf("invalid embedding response values pointer")
	}
	maxInt := uintptr(^uint(0) >> 1)
	if size > maxInt {
		return nil, fmt.Errorf("embedding response size %d exceeds Go slice capacity", size)
	}
	src := unsafe.Slice((*float32)(unsafe.Pointer(valsPtr)), int(size))
	out := make([]float32, int(size))
	copy(out, src)
	return out, nil
}

var callbackTrampoline uintptr

func (d *nativeDriver) createTrampoline() (uint64, error) {
	if callbackTrampoline == 0 {
		callbackTrampoline = purego.NewCallback(nativeDriverCallback)
	}
	return uint64(callbackTrampoline), nil
}

// ── Public helpers (used by tflite_native.go and other platform code) ──────

func goStringToCPtr(s string) (uintptr, func(), error) {
	ptr, free, err := nativeDrv.stringToCPtr(s)
	return uintptr(ptr), free, err
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

func registerNativeLibFunc(fn any, name string) (err error) {
	if _, err := dlsym(lmLib, name); err != nil {
		return fmt.Errorf("resolve LiteRT-LM symbol %q: %w", name, err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("register LiteRT-LM symbol %q: %v", name, recovered)
		}
	}()
	purego.RegisterLibFunc(fn, lmLib, name)
	return nil
}

func verifyNativeAPIVersion() error {
	var major, minor, patch int32
	if err := nativeDrv.callStatus("litert_lm_get_api_version", func() int32 {
		return lmGetAPIVersion(
			uintptr(unsafe.Pointer(&major)),
			uintptr(unsafe.Pointer(&minor)),
			uintptr(unsafe.Pointer(&patch)),
		)
	}); err != nil {
		return fmt.Errorf("query LiteRT-LM C API version: %w", err)
	}
	runtime.KeepAlive(&major)
	runtime.KeepAlive(&minor)
	runtime.KeepAlive(&patch)
	if major != 1 || minor < 0 {
		return fmt.Errorf("unsupported LiteRT-LM C API version %d.%d.%d; require 1.0 or newer within major version 1", major, minor, patch)
	}
	return nil
}

func ensureLMLibLoadedImpl(libPath string) error {
	lmLibOnce.Do(func() {
		var err error
		if Verbose {
			fmt.Fprintf(os.Stderr, "[ai] loading LiteRT-LM native library: %s\n", libPath)
		}
		lmLib, err = dlopen(libPath, RTLD_NOW|RTLD_GLOBAL)
		if err != nil {
			lmLibErr = runtimeLibraryLoadError(libPath, runtime.GOOS, err)
			return
		}
		if err = registerNativeLibFunc(&lmGetAPIVersion, "litert_lm_get_api_version"); err != nil {
			lmLibErr = err
			return
		}
		if err = registerNativeLibFunc(&lmGetLastErrorMessage, "litert_lm_get_last_error_message"); err != nil {
			lmLibErr = err
			return
		}
		if err = verifyNativeAPIVersion(); err != nil {
			lmLibErr = err
			return
		}

		bindings := []struct {
			target any
			name   string
		}{
			{&lmSettingsCreate, "litert_lm_engine_settings_create"},
			{&lmSettingsDelete, "litert_lm_engine_settings_delete"},
			{&lmSpecConfigCreate, "litert_lm_speculative_decoding_config_create"},
			{&lmSpecConfigDelete, "litert_lm_speculative_decoding_config_delete"},
			{&lmSpecConfigSetMode, "litert_lm_speculative_decoding_config_set_mode"},
			{&lmSettingsSetSpecConfig, "litert_lm_engine_settings_set_speculative_decoding_config"},
			{&lmSettingsSetDispatchDir, "litert_lm_engine_settings_set_litert_dispatch_lib_dir"},
			{&lmSettingsSetUseRingbuffersLocalAttention, "litert_lm_engine_settings_set_use_ringbuffers_local_attention"},
			{&lmSettingsSetGpuDecodeStepsPerSync, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync"},
			{&lmSettingsSetNumThreads, "litert_lm_engine_settings_set_num_threads"},
			{&lmSettingsSetMaxNumTokens, "litert_lm_engine_settings_set_max_num_tokens"},
			{&lmEngineCreate, "litert_lm_engine_create"},
			{&lmEngineDelete, "litert_lm_engine_delete"},
			{&lmSetLogLevel, "litert_lm_set_min_log_level"},
			{&lmCfgCreate, "litert_lm_conversation_config_create"},
			{&lmCfgDelete, "litert_lm_conversation_config_delete"},
			{&lmCfgSetTools, "litert_lm_conversation_config_set_tools"},
			{&lmCfgSetSystemMessage, "litert_lm_conversation_config_set_system_message"},
			{&lmCfgSetEnableConstrainedDecoding, "litert_lm_conversation_config_set_enable_constrained_decoding"},
			{&lmCfgSetStreamToolCalls, "litert_lm_conversation_config_set_stream_tool_calls"},
			{&lmCfgSetMessages, "litert_lm_conversation_config_set_messages"},
			{&lmConvCreate, "litert_lm_conversation_create"},
			{&lmConvDelete, "litert_lm_conversation_delete"},
			{&lmConvSendStream, "litert_lm_conversation_send_message_stream"},
			{&lmConvCancelProcess, "litert_lm_conversation_cancel_process"},
			{&lmStreamChunkGetText, "litert_lm_stream_chunk_get_text"},
			{&lmStreamChunkIsFinal, "litert_lm_stream_chunk_is_final"},
			{&lmStreamChunkGetError, "litert_lm_stream_chunk_get_error"},
			{&lmLoadedFileCreate, "litert_lm_loaded_file_create"},
			{&lmLoadedFileDelete, "litert_lm_loaded_file_delete"},
			{&lmLoadedFileHasSpeculativeDecodingSupport, "litert_lm_loaded_file_has_speculative_decoding_support"},
			{&lmLoadedFileMaxContextTokens, "litert_lm_loaded_file_max_context_tokens"},
			{&lmLoadedFileIsDynamicContext, "litert_lm_loaded_file_is_dynamic_context"},
			{&lmLoadedFileSupportsThinking, "litert_lm_loaded_file_supports_thinking"},
			{&lmLoadedFileSupportsFunctionCalling, "litert_lm_loaded_file_supports_function_calling"},
			{&lmLoadedFileModalitySupportedBackends, "litert_lm_loaded_file_modality_supported_backends"},
			{&lmEmbeddingSettingsCreate, "litert_lm_embedding_engine_settings_create"},
			{&lmEmbeddingSettingsDelete, "litert_lm_embedding_engine_settings_delete"},
			{&lmEmbeddingSettingsSetNumThreads, "litert_lm_embedding_engine_settings_set_num_threads"},
			{&lmEmbeddingEngineCreate, "litert_lm_embedding_engine_create"},
			{&lmEmbeddingEngineDelete, "litert_lm_embedding_engine_delete"},
			{&lmEmbeddingOptionsCreate, "litert_lm_embedding_options_create"},
			{&lmEmbeddingOptionsDelete, "litert_lm_embedding_options_delete"},
			{&lmEmbeddingOptionsSetNormalize, "litert_lm_embedding_options_set_normalize"},
			{&lmEmbeddingOptionsSetOverflowStrategy, "litert_lm_embedding_options_set_input_overflow_strategy"},
			{&lmInputDataCreate, "litert_lm_input_data_create"},
			{&lmInputDataDelete, "litert_lm_input_data_delete"},
			{&lmEmbeddingEngineCompute, "litert_lm_embedding_engine_compute_embedding"},
			{&lmEmbeddingResponseDelete, "litert_lm_embedding_response_delete"},
			{&lmEmbeddingResponseGetSize, "litert_lm_embedding_response_get_size"},
			{&lmEmbeddingResponseGetValues, "litert_lm_embedding_response_get_values"},
		}
		for _, binding := range bindings {
			if err = registerNativeLibFunc(binding.target, binding.name); err != nil {
				lmLibErr = err
				return
			}
		}
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
