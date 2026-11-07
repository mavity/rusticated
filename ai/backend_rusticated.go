//go:build wasip1 && !js

package ai

import (
	"dload"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/mavity/rusticated/platform"
	"math"
	"os"
	"strings"
	"sync"
	"unsafe"
)

const (
	i32 = 0x7F
	i64 = 0x7E
)

// ── Symbol handles (one per exported DLL symbol) ─────────────────────────

var (
	lmLibOnce sync.Once
	lmLibErr  error
	lmLib     uint64

	symLMGetAPIVersion       uint64
	symLMGetLastErrorMessage uint64

	symLMSettingsCreate         uint64
	symLMSettingsDelete         uint64
	symLMSpecConfigCreate       uint64
	symLMSpecConfigDelete       uint64
	symLMSpecConfigSetMode      uint64
	symLMSettingsSetSpecConfig  uint64
	symLMSettingsSetDispatchDir uint64
	symLMEngineCreate           uint64
	symLMEngineDelete           uint64
	symLMCfgCreate              uint64
	symLMCfgDelete              uint64
	symLMConvCreate             uint64
	symLMConvDelete             uint64
	symLMConvSendStream         uint64
	symLMConvCancelProcess      uint64
	symLMSetLogLevel            uint64
	symLMStreamChunkGetText     uint64
	symLMStreamChunkIsFinal     uint64
	symLMStreamChunkGetError    uint64

	symLMCfgSetTools                     uint64
	symLMCfgSetSystemMessage             uint64
	symLMCfgSetEnableConstrainedDecoding uint64
	symLMCfgSetStreamToolCalls           uint64
	symLMCfgSetMessages                  uint64

	symLMSettingsSetUseRingbuffersLocalAttention uint64
	symLMSettingsSetGpuDecodeStepsPerSync        uint64
	symLMSettingsSetNumThreads                   uint64
	symLMSettingsSetMaxNumTokens                 uint64

	symLMLoadedFileCreate                        uint64
	symLMLoadedFileDelete                        uint64
	symLMLoadedFileHasSpeculativeDecodingSupport uint64
	symLMLoadedFileMaxContextTokens              uint64
	symLMLoadedFileIsDynamicContext              uint64
	symLMLoadedFileSupportsThinking              uint64
	symLMLoadedFileSupportsFunctionCalling       uint64
	symLMLoadedFileModalitySupportedBackends     uint64

	symLMEmbeddingSettingsCreate             uint64
	symLMEmbeddingSettingsDelete             uint64
	symLMEmbeddingSettingsSetNumThreads      uint64
	symLMEmbeddingEngineCreate               uint64
	symLMEmbeddingEngineDelete               uint64
	symLMEmbeddingEngineComputeBatch         uint64
	symLMEmbeddingOptionsCreate              uint64
	symLMEmbeddingOptionsDelete              uint64
	symLMEmbeddingOptionsSetNormalize        uint64
	symLMEmbeddingOptionsSetOverflowStrategy uint64
	symLMInputDataCreate                     uint64
	symLMInputDataDelete                     uint64
	symLMEmbeddingEngineCompute              uint64
	symLMEmbeddingResponsesDelete            uint64
	symLMEmbeddingResponsesGetSize           uint64
	symLMEmbeddingResponsesGetAt             uint64
	symLMEmbeddingResponseDelete             uint64
	symLMEmbeddingResponseGetSize            uint64
	symLMEmbeddingResponseGetValues          uint64

	rusticatedDrv      rusticatedDriver
	rusticatedStatusMu sync.Mutex
)

// callDylib invokes a WASM dynamic library function with typed parameters.
func callDylib(sym uint64, paramTypes, resultTypes []byte, args ...uint64) (uint64, error) {
	return callDylibWithParent(sym, 0, paramTypes, resultTypes, args...)
}

func callDylibWithParent(sym uint64, parentInvID uint64, paramTypes, resultTypes []byte, args ...uint64) (uint64, error) {
	sig := make([]byte, 0, 3+len(paramTypes)+len(resultTypes))
	sig = append(sig, 0x60, byte(len(paramTypes)))
	sig = append(sig, paramTypes...)
	sig = append(sig, byte(len(resultTypes)))
	sig = append(sig, resultTypes...)
	res, err := dload.DylibCall(sym, sig, args, parentInvID)
	if err != nil {
		return 0, err
	}
	if len(res) > 0 {
		return res[0], nil
	}
	return 0, nil
}

// ── rusticatedDriver — implements LMDriver via callDylib ─────────────────────

type rusticatedDriver struct {
	parentInvID uint64
}

func (d *rusticatedDriver) ensureLoaded(libPath string) error {
	return ensureLMLibLoadedImpl(libPath)
}

func normalizeHostNativePath(s string) string {
	if len(s) >= 4 && (s[0] == '/' || s[0] == '\\') &&
		((s[1] >= 'A' && s[1] <= 'Z') || (s[1] >= 'a' && s[1] <= 'z')) &&
		s[2] == ':' && (s[3] == '/' || s[3] == '\\') {
		s = strings.ReplaceAll(s[1:], "/", `\`)
	}
	return s
}

func (d *rusticatedDriver) stringToCPtr(s string) (uint64, func(), error) {
	s = normalizeHostNativePath(s)
	ptr, free, err := dload.CString(lmLib, s)
	if err != nil {
		return 0, nil, fmt.Errorf("allocate LiteRT-LM string: %w", err)
	}
	return ptr, free, nil
}

func (d *rusticatedDriver) ptrToString(ptr uint64) (string, error) {
	if ptr == 0 {
		return "", nil
	}
	s, err := dload.GoString(lmLib, ptr)
	if err != nil {
		return "", fmt.Errorf("read LiteRT-LM string: %w", err)
	}
	return s, nil
}

func (d *rusticatedDriver) callStatus(operation string, sym uint64, paramTypes []byte, args ...uint64) error {
	if d.parentInvID == 0 {
		rusticatedStatusMu.Lock()
		defer rusticatedStatusMu.Unlock()
	}
	code, err := callDylibWithParent(sym, d.parentInvID, paramTypes, []byte{i32}, args...)
	if err != nil {
		return fmt.Errorf("call %s: %w", operation, err)
	}
	if int32(code) == liteRtStatusOK {
		return nil
	}
	messagePtr, getErr := callDylibWithParent(symLMGetLastErrorMessage, d.parentInvID, nil, []byte{i64})
	if getErr != nil {
		statusErr := newLiteRtStatusError(operation, int32(code), "")
		return fmt.Errorf("%w; retrieve LiteRT-LM last error: %v", statusErr, getErr)
	}
	message, messageErr := d.ptrToString(messagePtr)
	statusErr := newLiteRtStatusError(operation, int32(code), message)
	if messageErr != nil {
		return fmt.Errorf("%w; copy LiteRT-LM last error: %v", statusErr, messageErr)
	}
	return statusErr
}

func (d *rusticatedDriver) callStatusOut(operation string, sym uint64, paramTypes []byte, args []uint64, outSize uint64) (result uint64, err error) {
	out, err := dload.DylibAlloc(lmLib, outSize)
	if err != nil {
		return 0, fmt.Errorf("allocate %s output: %w", operation, err)
	}
	defer func() {
		if freeErr := dload.DylibFree(lmLib, out); freeErr != nil {
			err = errors.Join(err, fmt.Errorf("free %s output: %w", operation, freeErr))
		}
	}()
	args = append(args, out)
	if err = d.callStatus(operation, sym, append(append([]byte(nil), paramTypes...), i64), args...); err != nil {
		return 0, err
	}
	raw := make([]byte, outSize)
	if err = dload.DylibReadMem(lmLib, out, raw); err != nil {
		return 0, fmt.Errorf("read %s output: %w", operation, err)
	}
	switch outSize {
	case 1:
		return uint64(raw[0]), nil
	case 4:
		return uint64(binary.LittleEndian.Uint32(raw)), nil
	case 8:
		return binary.LittleEndian.Uint64(raw), nil
	default:
		return 0, fmt.Errorf("unsupported %s output width %d", operation, outSize)
	}
}

func reportRusticatedCleanupError(operation string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ai] failed to release %s: %v\n", operation, err)
	}
}

func (d *rusticatedDriver) settingsCreate(modelPath, backend, visionBackend, audioBackend uint64) (uint64, error) {
	return d.callStatusOut("litert_lm_engine_settings_create", symLMSettingsCreate, []byte{i64, i64, i64, i64},
		[]uint64{modelPath, backend, visionBackend, audioBackend}, 8)
}
func (d *rusticatedDriver) settingsDelete(settings uint64) {
	_, err := callDylib(symLMSettingsDelete, []byte{i64}, nil, settings)
	reportRusticatedCleanupError("LiteRT-LM engine settings", err)
}
func (d *rusticatedDriver) speculativeDecodingConfigCreate() (uint64, error) {
	return d.callStatusOut("litert_lm_speculative_decoding_config_create", symLMSpecConfigCreate, nil, nil, 8)
}
func (d *rusticatedDriver) speculativeDecodingConfigDelete(config uint64) {
	_, err := callDylib(symLMSpecConfigDelete, []byte{i64}, nil, config)
	reportRusticatedCleanupError("LiteRT-LM speculative-decoding config", err)
}
func (d *rusticatedDriver) speculativeDecodingConfigSetMode(config uint64, mode int32) error {
	return d.callStatus("litert_lm_speculative_decoding_config_set_mode", symLMSpecConfigSetMode,
		[]byte{i64, i32}, config, uint64(mode))
}
func (d *rusticatedDriver) settingsSetSpeculativeDecodingConfig(settings, config uint64) error {
	return d.callStatus("litert_lm_engine_settings_set_speculative_decoding_config", symLMSettingsSetSpecConfig,
		[]byte{i64, i64}, settings, config)
}
func (d *rusticatedDriver) settingsSetLitertDispatchLibDir(settings, libDir uint64) error {
	return d.callStatus("litert_lm_engine_settings_set_litert_dispatch_lib_dir", symLMSettingsSetDispatchDir,
		[]byte{i64, i64}, settings, libDir)
}
func (d *rusticatedDriver) settingsSetUseRingbuffersLocalAttention(settings uint64, enable bool) error {
	return d.callStatus("litert_lm_engine_settings_set_use_ringbuffers_local_attention", symLMSettingsSetUseRingbuffersLocalAttention,
		[]byte{i64, i32}, settings, boolArg(enable))
}
func (d *rusticatedDriver) settingsSetGpuDecodeStepsPerSync(settings uint64, steps int32) error {
	return d.callStatus("litert_lm_engine_settings_set_gpu_decode_steps_per_sync", symLMSettingsSetGpuDecodeStepsPerSync,
		[]byte{i64, i32}, settings, uint64(steps))
}
func (d *rusticatedDriver) settingsSetNumThreads(settings uint64, threads int32) error {
	return d.callStatus("litert_lm_engine_settings_set_num_threads", symLMSettingsSetNumThreads,
		[]byte{i64, i32}, settings, uint64(threads))
}
func (d *rusticatedDriver) settingsSetMaxNumTokens(settings uint64, maxTokens int32) error {
	return d.callStatus("litert_lm_engine_settings_set_max_num_tokens", symLMSettingsSetMaxNumTokens,
		[]byte{i64, i32}, settings, uint64(maxTokens))
}
func (d *rusticatedDriver) engineCreate(settings uint64) (uint64, error) {
	return d.callStatusOut("litert_lm_engine_create", symLMEngineCreate, []byte{i64}, []uint64{settings}, 8)
}
func (d *rusticatedDriver) engineDelete(engine uint64) {
	_, err := callDylib(symLMEngineDelete, []byte{i64}, nil, engine)
	reportRusticatedCleanupError("LiteRT-LM engine", err)
}
func (d *rusticatedDriver) setLogLevel(level int32) error {
	return d.callStatus("litert_lm_set_min_log_level", symLMSetLogLevel, []byte{i32}, uint64(level))
}
func (d *rusticatedDriver) cfgCreate() (uint64, error) {
	return d.callStatusOut("litert_lm_conversation_config_create", symLMCfgCreate, nil, nil, 8)
}
func (d *rusticatedDriver) cfgDelete(cfg uint64) {
	_, err := callDylib(symLMCfgDelete, []byte{i64}, nil, cfg)
	reportRusticatedCleanupError("LiteRT-LM conversation config", err)
}
func (d *rusticatedDriver) cfgSetTools(cfg, toolsJSON uint64) error {
	return d.callStatus("litert_lm_conversation_config_set_tools", symLMCfgSetTools, []byte{i64, i64}, cfg, toolsJSON)
}
func (d *rusticatedDriver) cfgSetSystemMessage(cfg, systemMsg uint64) error {
	return d.callStatus("litert_lm_conversation_config_set_system_message", symLMCfgSetSystemMessage, []byte{i64, i64}, cfg, systemMsg)
}
func (d *rusticatedDriver) cfgSetEnableConstrainedDecoding(cfg uint64, enable bool) error {
	return d.callStatus("litert_lm_conversation_config_set_enable_constrained_decoding", symLMCfgSetEnableConstrainedDecoding,
		[]byte{i64, i32}, cfg, boolArg(enable))
}
func (d *rusticatedDriver) cfgSetStreamToolCalls(cfg uint64, stream bool, channelName uint64) error {
	return d.callStatus("litert_lm_conversation_config_set_stream_tool_calls", symLMCfgSetStreamToolCalls,
		[]byte{i64, i32, i64}, cfg, boolArg(stream), channelName)
}
func (d *rusticatedDriver) cfgSetMessages(cfg, messagesJSON uint64) error {
	return d.callStatus("litert_lm_conversation_config_set_messages", symLMCfgSetMessages, []byte{i64, i64}, cfg, messagesJSON)
}
func (d *rusticatedDriver) convCreate(engine, cfg uint64) (uint64, error) {
	return d.callStatusOut("litert_lm_conversation_create", symLMConvCreate, []byte{i64, i64}, []uint64{engine, cfg}, 8)
}
func (d *rusticatedDriver) convDelete(conv uint64) {
	_, err := callDylib(symLMConvDelete, []byte{i64}, nil, conv)
	reportRusticatedCleanupError("LiteRT-LM conversation", err)
}
func (d *rusticatedDriver) convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) error {
	return d.callStatus("litert_lm_conversation_send_message_stream", symLMConvSendStream,
		[]byte{i64, i64, i64, i64, i64, i64}, conv, msg, extraCtx, optArgs, callback, userData)
}
func (d *rusticatedDriver) convCancelProcess(conv uint64) error {
	return d.callStatus("litert_lm_conversation_cancel_process", symLMConvCancelProcess, []byte{i64}, conv)
}
func (d *rusticatedDriver) loadedFileCreate(modelPath uint64) (uint64, error) {
	return d.callStatusOut("litert_lm_loaded_file_create", symLMLoadedFileCreate, []byte{i64}, []uint64{modelPath}, 8)
}
func (d *rusticatedDriver) loadedFileDelete(file uint64) {
	_, err := callDylib(symLMLoadedFileDelete, []byte{i64}, nil, file)
	reportRusticatedCleanupError("LiteRT-LM loaded model file", err)
}
func (d *rusticatedDriver) loadedFileHasSpeculativeDecodingSupport(file uint64) (bool, error) {
	v, err := d.callStatusOut("litert_lm_loaded_file_has_speculative_decoding_support", symLMLoadedFileHasSpeculativeDecodingSupport,
		[]byte{i64}, []uint64{file}, 1)
	return v != 0, err
}
func (d *rusticatedDriver) loadedFileMaxContextTokens(file uint64) (uint32, error) {
	v, err := d.callStatusOut("litert_lm_loaded_file_max_context_tokens", symLMLoadedFileMaxContextTokens,
		[]byte{i64}, []uint64{file}, 4)
	return uint32(v), err
}
func (d *rusticatedDriver) loadedFileIsDynamicContext(file uint64) (bool, error) {
	v, err := d.callStatusOut("litert_lm_loaded_file_is_dynamic_context", symLMLoadedFileIsDynamicContext,
		[]byte{i64}, []uint64{file}, 1)
	return v != 0, err
}
func (d *rusticatedDriver) loadedFileSupportsThinking(file uint64) (bool, error) {
	v, err := d.callStatusOut("litert_lm_loaded_file_supports_thinking", symLMLoadedFileSupportsThinking,
		[]byte{i64}, []uint64{file}, 1)
	return v != 0, err
}
func (d *rusticatedDriver) loadedFileSupportsFunctionCalling(file uint64) (bool, error) {
	v, err := d.callStatusOut("litert_lm_loaded_file_supports_function_calling", symLMLoadedFileSupportsFunctionCalling,
		[]byte{i64}, []uint64{file}, 1)
	return v != 0, err
}
func (d *rusticatedDriver) loadedFileModalitySupportedBackends(file uint64, modality int32) ([]uint32, error) {
	const maxBackendCount = uint64(8)
	backendsBuf, err := dload.DylibAlloc(lmLib, maxBackendCount*4)
	if err != nil {
		return nil, fmt.Errorf("allocate supported-backends buffer: %w", err)
	}
	defer func() {
		reportRusticatedCleanupError("supported-backends buffer", dload.DylibFree(lmLib, backendsBuf))
	}()
	countBuf, err := dload.DylibAlloc(lmLib, 8)
	if err != nil {
		return nil, fmt.Errorf("allocate supported-backends count: %w", err)
	}
	defer func() {
		reportRusticatedCleanupError("supported-backends count", dload.DylibFree(lmLib, countBuf))
	}()
	if err := d.callStatus("litert_lm_loaded_file_modality_supported_backends", symLMLoadedFileModalitySupportedBackends,
		[]byte{i64, i32, i64, i64, i64}, file, uint64(modality), backendsBuf, maxBackendCount, countBuf); err != nil {
		return nil, err
	}
	countBytes := make([]byte, 8)
	if err := dload.DylibReadMem(lmLib, countBuf, countBytes); err != nil {
		return nil, fmt.Errorf("read supported-backends count: %w", err)
	}
	count := binary.LittleEndian.Uint64(countBytes)
	if count > maxBackendCount {
		count = maxBackendCount
	}
	raw := make([]byte, int(count)*4)
	if err := dload.DylibReadMem(lmLib, backendsBuf, raw); err != nil {
		return nil, fmt.Errorf("read supported-backends buffer: %w", err)
	}
	out := make([]uint32, int(count))
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(raw[i*4 : (i+1)*4])
	}
	return out, nil
}
func (d *rusticatedDriver) streamChunkGetText(chunk uint64) (uint64, error) {
	return d.callStatusOut("litert_lm_stream_chunk_get_text", symLMStreamChunkGetText, []byte{i64}, []uint64{chunk}, 8)
}
func (d *rusticatedDriver) streamChunkIsFinal(chunk uint64) (bool, error) {
	v, err := d.callStatusOut("litert_lm_stream_chunk_is_final", symLMStreamChunkIsFinal, []byte{i64}, []uint64{chunk}, 1)
	return v != 0, err
}
func (d *rusticatedDriver) streamChunkGetError(chunk uint64) (uint64, error) {
	return d.callStatusOut("litert_lm_stream_chunk_get_error", symLMStreamChunkGetError, []byte{i64}, []uint64{chunk}, 8)
}

func (d *rusticatedDriver) embeddingSettingsCreate(modelPath, backend, visionBackend, audioBackend uint64) (uint64, error) {
	return d.callStatusOut("litert_lm_embedding_engine_settings_create", symLMEmbeddingSettingsCreate,
		[]byte{i64, i64, i64, i64}, []uint64{modelPath, backend, visionBackend, audioBackend}, 8)
}
func (d *rusticatedDriver) embeddingSettingsDelete(settings uint64) {
	_, err := callDylib(symLMEmbeddingSettingsDelete, []byte{i64}, nil, settings)
	reportRusticatedCleanupError("LiteRT-LM embedding settings", err)
}
func (d *rusticatedDriver) embeddingSettingsSetNumThreads(settings uint64, threads int32) error {
	return d.callStatus("litert_lm_embedding_engine_settings_set_num_threads", symLMEmbeddingSettingsSetNumThreads,
		[]byte{i64, i32}, settings, uint64(threads))
}
func (d *rusticatedDriver) embeddingEngineCreate(settings uint64) (uint64, error) {
	return d.callStatusOut("litert_lm_embedding_engine_create", symLMEmbeddingEngineCreate, []byte{i64}, []uint64{settings}, 8)
}
func (d *rusticatedDriver) embeddingEngineDelete(engine uint64) {
	_, err := callDylib(symLMEmbeddingEngineDelete, []byte{i64}, nil, engine)
	reportRusticatedCleanupError("LiteRT-LM embedding engine", err)
}
func (d *rusticatedDriver) embeddingOptionsCreate() (uint64, error) {
	return d.callStatusOut("litert_lm_embedding_options_create", symLMEmbeddingOptionsCreate, nil, nil, 8)
}
func (d *rusticatedDriver) embeddingOptionsDelete(options uint64) {
	_, err := callDylib(symLMEmbeddingOptionsDelete, []byte{i64}, nil, options)
	reportRusticatedCleanupError("LiteRT-LM embedding options", err)
}
func (d *rusticatedDriver) embeddingOptionsSetNormalize(options uint64, normalize bool) error {
	return d.callStatus("litert_lm_embedding_options_set_normalize", symLMEmbeddingOptionsSetNormalize,
		[]byte{i64, i32}, options, boolArg(normalize))
}
func (d *rusticatedDriver) embeddingOptionsSetInputOverflowStrategy(options uint64, strategy int32) error {
	return d.callStatus("litert_lm_embedding_options_set_input_overflow_strategy", symLMEmbeddingOptionsSetOverflowStrategy,
		[]byte{i64, i32}, options, uint64(strategy))
}
func (d *rusticatedDriver) inputDataCreate(dataType int32, data []byte) (uint64, error) {
	var dataBuf uint64
	if len(data) > 0 {
		var err error
		dataBuf, err = dload.DylibAlloc(lmLib, uint64(len(data)))
		if err != nil {
			return 0, fmt.Errorf("allocate embedding input: %w", err)
		}
		defer func() {
			reportRusticatedCleanupError("embedding input buffer", dload.DylibFree(lmLib, dataBuf))
		}()
		if err := dload.DylibWriteMem(lmLib, dataBuf, data); err != nil {
			return 0, fmt.Errorf("write embedding input: %w", err)
		}
	}
	return d.callStatusOut("litert_lm_input_data_create", symLMInputDataCreate,
		[]byte{i32, i64, i64}, []uint64{uint64(dataType), dataBuf, uint64(len(data))}, 8)
}
func (d *rusticatedDriver) inputDataDelete(inputData uint64) {
	_, err := callDylib(symLMInputDataDelete, []byte{i64}, nil, inputData)
	reportRusticatedCleanupError("LiteRT-LM embedding input", err)
}
func (d *rusticatedDriver) embeddingEngineCompute(engine uint64, inputs []uint64, options uint64) (out []float32, err error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no inputs provided")
	}
	const workerPointerSize = 8
	inputsBuf, err := dload.DylibAlloc(lmLib, uint64(len(inputs))*workerPointerSize)
	if err != nil {
		return nil, fmt.Errorf("allocate embedding input pointers: %w", err)
	}
	defer func() {
		if freeErr := dload.DylibFree(lmLib, inputsBuf); freeErr != nil {
			err = errors.Join(err, fmt.Errorf("free embedding input pointers: %w", freeErr))
		}
	}()
	rawPtrs := make([]byte, len(inputs)*workerPointerSize)
	for i, ptr := range inputs {
		binary.LittleEndian.PutUint64(rawPtrs[i*workerPointerSize:(i+1)*workerPointerSize], ptr)
	}
	if err := dload.DylibWriteMem(lmLib, inputsBuf, rawPtrs); err != nil {
		return nil, fmt.Errorf("write embedding input pointers: %w", err)
	}
	resp, err := d.callStatusOut("litert_lm_embedding_engine_compute_embedding", symLMEmbeddingEngineCompute,
		[]byte{i64, i64, i64, i64}, []uint64{engine, inputsBuf, uint64(len(inputs)), options}, 8)
	if err != nil {
		return nil, err
	}
	if resp == 0 {
		return nil, fmt.Errorf("embedding computation returned NULL without an error")
	}
	defer func() {
		_, deleteErr := callDylib(symLMEmbeddingResponseDelete, []byte{i64}, nil, resp)
		reportRusticatedCleanupError("LiteRT-LM embedding response", deleteErr)
	}()

	return d.embeddingResponseValues(resp)
}

func (d *rusticatedDriver) embeddingEngineComputeBatch(engine uint64, batches [][]uint64, options uint64) (vectors [][]float32, err error) {
	if len(batches) == 0 {
		return nil, fmt.Errorf("no embedding inputs provided")
	}

	const workerPointerSize = 8
	var allocated []uint64
	defer func() {
		for _, ptr := range allocated {
			if freeErr := dload.DylibFree(lmLib, ptr); freeErr != nil {
				err = errors.Join(err, fmt.Errorf("free embedding batch buffer: %w", freeErr))
			}
		}
	}()
	allocAndWrite := func(label string, data []byte) (uint64, error) {
		ptr, allocErr := dload.DylibAlloc(lmLib, uint64(len(data)))
		if allocErr != nil {
			return 0, fmt.Errorf("allocate %s: %w", label, allocErr)
		}
		allocated = append(allocated, ptr)
		if writeErr := dload.DylibWriteMem(lmLib, ptr, data); writeErr != nil {
			return 0, fmt.Errorf("write %s: %w", label, writeErr)
		}
		return ptr, nil
	}

	inputsBatchPtrs := make([]uint64, len(batches))
	inputCounts := make([]byte, len(batches)*workerPointerSize)
	for batchIndex, inputs := range batches {
		if len(inputs) == 0 {
			return nil, fmt.Errorf("embedding batch item %d has no inputs", batchIndex)
		}
		rawPtrs := make([]byte, len(inputs)*workerPointerSize)
		for i, ptr := range inputs {
			binary.LittleEndian.PutUint64(rawPtrs[i*workerPointerSize:(i+1)*workerPointerSize], ptr)
		}
		inputsBatchPtrs[batchIndex], err = allocAndWrite(
			fmt.Sprintf("embedding input pointers for batch item %d", batchIndex),
			rawPtrs,
		)
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint64(inputCounts[batchIndex*workerPointerSize:(batchIndex+1)*workerPointerSize], uint64(len(inputs)))
	}

	rawBatchPtrs := make([]byte, len(inputsBatchPtrs)*workerPointerSize)
	for i, ptr := range inputsBatchPtrs {
		binary.LittleEndian.PutUint64(rawBatchPtrs[i*workerPointerSize:(i+1)*workerPointerSize], ptr)
	}
	inputsBatchBuf, err := allocAndWrite("embedding batch pointers", rawBatchPtrs)
	if err != nil {
		return nil, err
	}
	inputCountsBuf, err := allocAndWrite("embedding batch input counts", inputCounts)
	if err != nil {
		return nil, err
	}

	responses, err := d.callStatusOut(
		"litert_lm_embedding_engine_compute_embedding_batch",
		symLMEmbeddingEngineComputeBatch,
		[]byte{i64, i64, i64, i64, i64},
		[]uint64{engine, inputsBatchBuf, inputCountsBuf, uint64(len(batches)), options},
		8,
	)
	if err != nil {
		return nil, err
	}
	if responses == 0 {
		return nil, fmt.Errorf("embedding batch computation returned NULL without an error")
	}
	defer func() {
		_, deleteErr := callDylib(symLMEmbeddingResponsesDelete, []byte{i64}, nil, responses)
		reportRusticatedCleanupError("LiteRT-LM embedding responses", deleteErr)
	}()

	responseCount, err := d.callStatusOut(
		"litert_lm_embedding_responses_get_size",
		symLMEmbeddingResponsesGetSize,
		[]byte{i64},
		[]uint64{responses},
		8,
	)
	if err != nil {
		return nil, err
	}
	if responseCount != uint64(len(batches)) {
		return nil, fmt.Errorf("embedding batch returned %d responses, want %d", responseCount, len(batches))
	}

	vectors = make([][]float32, len(batches))
	for i := range batches {
		response, getErr := d.callStatusOut(
			"litert_lm_embedding_responses_get_at",
			symLMEmbeddingResponsesGetAt,
			[]byte{i64, i64},
			[]uint64{responses, uint64(i)},
			8,
		)
		if getErr != nil {
			return nil, fmt.Errorf("get embedding batch response %d: %w", i, getErr)
		}
		if response == 0 {
			return nil, fmt.Errorf("embedding batch response %d is NULL", i)
		}
		vectors[i], err = d.embeddingResponseValues(response)
		if err != nil {
			return nil, fmt.Errorf("read embedding batch response %d: %w", i, err)
		}
	}
	return vectors, nil
}

func (d *rusticatedDriver) embeddingResponseValues(response uint64) ([]float32, error) {
	size, err := d.callStatusOut("litert_lm_embedding_response_get_size", symLMEmbeddingResponseGetSize, []byte{i64}, []uint64{response}, 8)
	if err != nil {
		return nil, err
	}
	valuesPtr, err := d.callStatusOut("litert_lm_embedding_response_get_values", symLMEmbeddingResponseGetValues, []byte{i64}, []uint64{response}, 8)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return []float32{}, nil
	}
	if valuesPtr == 0 {
		return nil, fmt.Errorf("invalid embedding response values pointer")
	}
	maxInt := uint64(^uint(0) >> 1)
	if size > maxInt/4 {
		return nil, fmt.Errorf("embedding response size %d exceeds Go slice capacity", size)
	}
	rawVals := make([]byte, int(size)*4)
	if err := dload.DylibReadMem(lmLib, valuesPtr, rawVals); err != nil {
		return nil, fmt.Errorf("read embedding response values: %w", err)
	}
	out := make([]float32, int(size))
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(rawVals[i*4 : (i+1)*4]))
	}
	return out, nil
}

func boolArg(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

var (
	rusticatedTrampoline uint64
	trampolineOv         [512]byte
	trampolineRegOnce    sync.Once
	trampolineRegErr     error
)

func (d *rusticatedDriver) createTrampoline() (uint64, error) {
	trampolineRegOnce.Do(func() {
		cbSig := []byte{0x60, 2, i64, i64, 0}
		rusticatedTrampoline, trampolineRegErr = dload.DylibRegisterCallbackWithContext(lmLib, cbSig, unsafe.Pointer(&trampolineOv[0]), 512,
			func(invID uint64, _ uint64, args []uint64) []uint64 {
				if len(args) < 2 {
					return nil
				}
				userData := args[0]
				chunkPtr := args[1]
				if chunkPtr == 0 {
					return nil
				}
				cbDrv := rusticatedDriver{parentInvID: invID}
				sharedTokenCallback(&cbDrv, userData, chunkPtr)
				return nil
			})
		if trampolineRegErr != nil {
			trampolineRegErr = fmt.Errorf("register LiteRT-LM stream callback trampoline: %w", trampolineRegErr)
		} else if rusticatedTrampoline == 0 {
			trampolineRegErr = fmt.Errorf("register LiteRT-LM stream callback trampoline returned NULL")
		}
	})
	return rusticatedTrampoline, trampolineRegErr
}

// ── Library loading ────────────────────────────────────────────────────────

func resolveRusticatedSymbol(target *uint64, name string) error {
	symbol, err := dload.DylibSym(lmLib, name)
	if err != nil {
		return fmt.Errorf("resolve LiteRT-LM symbol %q: %w", name, err)
	}
	*target = symbol
	return nil
}

func verifyRusticatedAPIVersion() error {
	versionBuf, err := dload.DylibAlloc(lmLib, 12)
	if err != nil {
		return fmt.Errorf("allocate LiteRT-LM API version outputs: %w", err)
	}
	defer func() {
		reportRusticatedCleanupError("LiteRT-LM API version outputs", dload.DylibFree(lmLib, versionBuf))
	}()
	if err := rusticatedDrv.callStatus("litert_lm_get_api_version", symLMGetAPIVersion,
		[]byte{i64, i64, i64}, versionBuf, versionBuf+4, versionBuf+8); err != nil {
		return fmt.Errorf("query LiteRT-LM C API version: %w", err)
	}
	raw := make([]byte, 12)
	if err := dload.DylibReadMem(lmLib, versionBuf, raw); err != nil {
		return fmt.Errorf("read LiteRT-LM API version: %w", err)
	}
	major := int32(binary.LittleEndian.Uint32(raw[0:4]))
	minor := int32(binary.LittleEndian.Uint32(raw[4:8]))
	patch := int32(binary.LittleEndian.Uint32(raw[8:12]))
	if major != 1 || minor < 0 {
		return fmt.Errorf("unsupported LiteRT-LM C API version %d.%d.%d; require 1.0 or newer within major version 1", major, minor, patch)
	}
	return nil
}

func ensureLMLibLoadedImpl(libPath string) error {
	lmLibOnce.Do(func() {
		var err error
		if Verbose {
			fmt.Fprintf(os.Stderr, "[ai] loading LiteRT-LM WASM library: %s\n", libPath)
		}
		lmLib, err = dload.DylibOpen(libPath, dload.RTLD_NOW|dload.RTLD_GLOBAL)
		if err != nil {
			lmLibErr = runtimeLibraryLoadError(libPath, platform.HostOS(), err)
			return
		}
		if err = resolveRusticatedSymbol(&symLMGetAPIVersion, "litert_lm_get_api_version"); err != nil {
			lmLibErr = err
			return
		}
		if err = resolveRusticatedSymbol(&symLMGetLastErrorMessage, "litert_lm_get_last_error_message"); err != nil {
			lmLibErr = err
			return
		}
		if err = verifyRusticatedAPIVersion(); err != nil {
			lmLibErr = err
			return
		}

		bindings := []struct {
			target *uint64
			name   string
		}{
			{&symLMSettingsCreate, "litert_lm_engine_settings_create"},
			{&symLMSettingsDelete, "litert_lm_engine_settings_delete"},
			{&symLMSpecConfigCreate, "litert_lm_speculative_decoding_config_create"},
			{&symLMSpecConfigDelete, "litert_lm_speculative_decoding_config_delete"},
			{&symLMSpecConfigSetMode, "litert_lm_speculative_decoding_config_set_mode"},
			{&symLMSettingsSetSpecConfig, "litert_lm_engine_settings_set_speculative_decoding_config"},
			{&symLMSettingsSetDispatchDir, "litert_lm_engine_settings_set_litert_dispatch_lib_dir"},
			{&symLMSettingsSetUseRingbuffersLocalAttention, "litert_lm_engine_settings_set_use_ringbuffers_local_attention"},
			{&symLMSettingsSetGpuDecodeStepsPerSync, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync"},
			{&symLMSettingsSetNumThreads, "litert_lm_engine_settings_set_num_threads"},
			{&symLMSettingsSetMaxNumTokens, "litert_lm_engine_settings_set_max_num_tokens"},
			{&symLMEngineCreate, "litert_lm_engine_create"},
			{&symLMEngineDelete, "litert_lm_engine_delete"},
			{&symLMSetLogLevel, "litert_lm_set_min_log_level"},
			{&symLMCfgCreate, "litert_lm_conversation_config_create"},
			{&symLMCfgDelete, "litert_lm_conversation_config_delete"},
			{&symLMCfgSetTools, "litert_lm_conversation_config_set_tools"},
			{&symLMCfgSetSystemMessage, "litert_lm_conversation_config_set_system_message"},
			{&symLMCfgSetEnableConstrainedDecoding, "litert_lm_conversation_config_set_enable_constrained_decoding"},
			{&symLMCfgSetStreamToolCalls, "litert_lm_conversation_config_set_stream_tool_calls"},
			{&symLMCfgSetMessages, "litert_lm_conversation_config_set_messages"},
			{&symLMConvCreate, "litert_lm_conversation_create"},
			{&symLMConvDelete, "litert_lm_conversation_delete"},
			{&symLMConvSendStream, "litert_lm_conversation_send_message_stream"},
			{&symLMConvCancelProcess, "litert_lm_conversation_cancel_process"},
			{&symLMStreamChunkGetText, "litert_lm_stream_chunk_get_text"},
			{&symLMStreamChunkIsFinal, "litert_lm_stream_chunk_is_final"},
			{&symLMStreamChunkGetError, "litert_lm_stream_chunk_get_error"},
			{&symLMLoadedFileCreate, "litert_lm_loaded_file_create"},
			{&symLMLoadedFileDelete, "litert_lm_loaded_file_delete"},
			{&symLMLoadedFileHasSpeculativeDecodingSupport, "litert_lm_loaded_file_has_speculative_decoding_support"},
			{&symLMLoadedFileMaxContextTokens, "litert_lm_loaded_file_max_context_tokens"},
			{&symLMLoadedFileIsDynamicContext, "litert_lm_loaded_file_is_dynamic_context"},
			{&symLMLoadedFileSupportsThinking, "litert_lm_loaded_file_supports_thinking"},
			{&symLMLoadedFileSupportsFunctionCalling, "litert_lm_loaded_file_supports_function_calling"},
			{&symLMLoadedFileModalitySupportedBackends, "litert_lm_loaded_file_modality_supported_backends"},
			{&symLMEmbeddingSettingsCreate, "litert_lm_embedding_engine_settings_create"},
			{&symLMEmbeddingSettingsDelete, "litert_lm_embedding_engine_settings_delete"},
			{&symLMEmbeddingSettingsSetNumThreads, "litert_lm_embedding_engine_settings_set_num_threads"},
			{&symLMEmbeddingEngineCreate, "litert_lm_embedding_engine_create"},
			{&symLMEmbeddingEngineDelete, "litert_lm_embedding_engine_delete"},
			{&symLMEmbeddingEngineComputeBatch, "litert_lm_embedding_engine_compute_embedding_batch"},
			{&symLMEmbeddingOptionsCreate, "litert_lm_embedding_options_create"},
			{&symLMEmbeddingOptionsDelete, "litert_lm_embedding_options_delete"},
			{&symLMEmbeddingOptionsSetNormalize, "litert_lm_embedding_options_set_normalize"},
			{&symLMEmbeddingOptionsSetOverflowStrategy, "litert_lm_embedding_options_set_input_overflow_strategy"},
			{&symLMInputDataCreate, "litert_lm_input_data_create"},
			{&symLMInputDataDelete, "litert_lm_input_data_delete"},
			{&symLMEmbeddingEngineCompute, "litert_lm_embedding_engine_compute_embedding"},
			{&symLMEmbeddingResponsesDelete, "litert_lm_embedding_responses_delete"},
			{&symLMEmbeddingResponsesGetSize, "litert_lm_embedding_responses_get_size"},
			{&symLMEmbeddingResponsesGetAt, "litert_lm_embedding_responses_get_at"},
			{&symLMEmbeddingResponseDelete, "litert_lm_embedding_response_delete"},
			{&symLMEmbeddingResponseGetSize, "litert_lm_embedding_response_get_size"},
			{&symLMEmbeddingResponseGetValues, "litert_lm_embedding_response_get_values"},
		}
		for _, binding := range bindings {
			if err = resolveRusticatedSymbol(binding.target, binding.name); err != nil {
				lmLibErr = err
				return
			}
		}
	})
	return lmLibErr
}

// ── Thin public wrappers ───────────────────────────────────────────────────

func openBackend(libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	if isBrowserHost() || libPath == "eval" {
		return openWebBackend(libPath, modelPath, backend, maxTokens)
	}
	return sharedOpenBackend(&rusticatedDrv, libPath, modelPath, backend, maxTokens)
}

func openEmbedderBackend(libPath, modelPath, backend string) (embedderBackend, error) {
	if isBrowserHost() {
		return openWebEmbedderBackend(libPath, modelPath, backend)
	}
	if libPath == "eval" {
		return nil, fmt.Errorf("web embedding engine not configured")
	}
	return sharedOpenEmbedder(&rusticatedDrv, libPath, modelPath, backend)
}

func inspectModel(libPath, modelPath string) (*ModelInfo, error) {
	if isBrowserHost() || libPath == "eval" {
		return &ModelInfo{
			MaxContextTokens:        8192,
			IsDynamicContext:        true,
			SupportsThinking:        true,
			SupportsFunctionCalling: true,
			SupportedBackends:       []string{"gpu"},
		}, nil
	}
	return sharedInspectModel(&rusticatedDrv, libPath, modelPath)
}
