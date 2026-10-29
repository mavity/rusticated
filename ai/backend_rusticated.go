//go:build wasip1 && !js

package ai

import (
	"dload"
	"encoding/binary"
	"fmt"
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

	symLMSettingsCreate      uint64
	symLMSettingsDelete      uint64
	symLMEngineCreate        uint64
	symLMEngineDelete        uint64
	symLMCfgCreate           uint64
	symLMCfgDelete           uint64
	symLMConvCreate          uint64
	symLMConvDelete          uint64
	symLMConvSendStream      uint64
	symLMConvCancelProcess   uint64
	symLMSetLogLevel         uint64
	symLMStreamChunkGetText  uint64
	symLMStreamChunkIsFinal  uint64
	symLMStreamChunkGetError uint64

	symLMCfgSetTools                     uint64
	symLMCfgSetSystemMessage             uint64
	symLMCfgSetEnableConstrainedDecoding uint64
	symLMCfgSetStreamToolCalls           uint64
	symLMCfgSetMessages                  uint64

	symLMSettingsSetEnableSpeculativeDecoding    uint64
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
	symLMEmbeddingOptionsCreate              uint64
	symLMEmbeddingOptionsDelete              uint64
	symLMEmbeddingOptionsSetNormalize        uint64
	symLMEmbeddingOptionsSetOverflowStrategy uint64
	symLMInputDataCreate                     uint64
	symLMInputDataDelete                     uint64
	symLMEmbeddingEngineCompute              uint64
	symLMEmbeddingResponseDelete             uint64
	symLMEmbeddingResponseGetSize            uint64
	symLMEmbeddingResponseGetValues          uint64

	rusticatedDrv        rusticatedDriver
	rusticatedInputBufMu sync.Mutex
	rusticatedInputBufs  = map[uint64]uint64{}
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

func (d *rusticatedDriver) getSymbol(name string) (uint64, error) {
	return dload.DylibSym(lmLib, name)
}

func (d *rusticatedDriver) writeInt32(addr uint64, val int32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(val))
	_ = dload.DylibWriteMem(lmLib, addr, buf[:])
}

func (d *rusticatedDriver) writeBool(addr uint64, val bool) {
	b := byte(0)
	if val {
		b = 1
	}
	_ = dload.DylibWriteMem(lmLib, addr, []byte{b})
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

func (d *rusticatedDriver) settingsCreate(modelPath, backend, visionBackend, audioBackend uint64) uint64 {
	v, _ := callDylib(symLMSettingsCreate, []byte{i64, i64, i64, i64}, []byte{i64},
		modelPath, backend, visionBackend, audioBackend)
	return v
}
func (d *rusticatedDriver) settingsDelete(settings uint64) {
	_, _ = callDylib(symLMSettingsDelete, []byte{i64}, nil, settings)
}
func (d *rusticatedDriver) settingsSetEnableSpeculativeDecoding(settings uint64, enable bool) {
	en := byte(0)
	if enable {
		en = 1
	}
	_, _ = callDylib(symLMSettingsSetEnableSpeculativeDecoding, []byte{i64, i32}, nil, settings, uint64(en))
}
func (d *rusticatedDriver) settingsSetUseRingbuffersLocalAttention(settings uint64, enable bool) {
	en := byte(0)
	if enable {
		en = 1
	}
	_, _ = callDylib(symLMSettingsSetUseRingbuffersLocalAttention, []byte{i64, i32}, nil, settings, uint64(en))
}
func (d *rusticatedDriver) settingsSetGpuDecodeStepsPerSync(settings uint64, steps int32) {
	_, _ = callDylib(symLMSettingsSetGpuDecodeStepsPerSync, []byte{i64, i32}, nil, settings, uint64(steps))
}
func (d *rusticatedDriver) settingsSetNumThreads(settings uint64, threads int32) {
	_, _ = callDylib(symLMSettingsSetNumThreads, []byte{i64, i32}, nil, settings, uint64(threads))
}
func (d *rusticatedDriver) settingsSetMaxNumTokens(settings uint64, maxTokens int32) {
	_, _ = callDylib(symLMSettingsSetMaxNumTokens, []byte{i64, i32}, nil, settings, uint64(maxTokens))
}
func (d *rusticatedDriver) engineCreate(settings uint64) uint64 {
	v, _ := callDylib(symLMEngineCreate, []byte{i64}, []byte{i64}, settings)
	return v
}
func (d *rusticatedDriver) engineDelete(engine uint64) {
	_, _ = callDylib(symLMEngineDelete, []byte{i64}, nil, engine)
}
func (d *rusticatedDriver) setLogLevel(level uint64) {
	_, _ = callDylib(symLMSetLogLevel, []byte{i64}, nil, level)
}
func (d *rusticatedDriver) cfgCreate(engine uint64) uint64 {
	v, _ := callDylib(symLMCfgCreate, []byte{i64}, []byte{i64}, engine)
	return v
}
func (d *rusticatedDriver) cfgDelete(cfg uint64) {
	_, _ = callDylib(symLMCfgDelete, []byte{i64}, nil, cfg)
}
func (d *rusticatedDriver) cfgSetTools(cfg, toolsJSON uint64) {
	_, _ = callDylib(symLMCfgSetTools, []byte{i64, i64}, nil, cfg, toolsJSON)
}
func (d *rusticatedDriver) cfgSetSystemMessage(cfg, systemMsg uint64) {
	_, _ = callDylib(symLMCfgSetSystemMessage, []byte{i64, i64}, nil, cfg, systemMsg)
}
func (d *rusticatedDriver) cfgSetEnableConstrainedDecoding(cfg uint64, enable bool) error {
	enabled := uint64(0)
	if enable {
		enabled = 1
	}
	_, err := callDylib(
		symLMCfgSetEnableConstrainedDecoding,
		[]byte{i64, i32},
		nil,
		cfg,
		enabled,
	)
	if err != nil {
		return fmt.Errorf("call LiteRT-LM constrained-decoding setter: %w", err)
	}
	return nil
}
func (d *rusticatedDriver) cfgSetStreamToolCalls(cfg uint64, stream bool, channelName uint64) {
	s := byte(0)
	if stream {
		s = 1
	}
	_, _ = callDylib(symLMCfgSetStreamToolCalls, []byte{i64, i32, i64}, nil, cfg, uint64(s), channelName)
}
func (d *rusticatedDriver) cfgSetMessages(cfg, messagesJSON uint64) {
	_, _ = callDylib(symLMCfgSetMessages, []byte{i64, i64}, nil, cfg, messagesJSON)
}
func (d *rusticatedDriver) convCreate(engine, cfg uint64) uint64 {
	v, _ := callDylib(symLMConvCreate, []byte{i64, i64}, []byte{i64}, engine, cfg)
	return v
}
func (d *rusticatedDriver) convDelete(conv uint64) {
	_, _ = callDylib(symLMConvDelete, []byte{i64}, nil, conv)
}
func (d *rusticatedDriver) convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) int32 {
	v, err := callDylib(symLMConvSendStream, []byte{i64, i64, i64, i64, i64, i64}, []byte{i32},
		conv, msg, extraCtx, optArgs, callback, userData)
	if err != nil {
		return -1
	}
	return int32(v)
}
func (d *rusticatedDriver) convCancelProcess(conv uint64) {
	_, _ = callDylib(symLMConvCancelProcess, []byte{i64}, nil, conv)
}
func (d *rusticatedDriver) loadedFileCreate(modelPath uint64) uint64 {
	v, _ := callDylib(symLMLoadedFileCreate, []byte{i64}, []byte{i64}, modelPath)
	return v
}
func (d *rusticatedDriver) loadedFileDelete(file uint64) {
	_, _ = callDylib(symLMLoadedFileDelete, []byte{i64}, nil, file)
}
func (d *rusticatedDriver) loadedFileHasSpeculativeDecodingSupport(file uint64) bool {
	v, _ := callDylib(symLMLoadedFileHasSpeculativeDecodingSupport, []byte{i64}, []byte{i32}, file)
	return int32(v) != 0
}
func (d *rusticatedDriver) loadedFileMaxContextTokens(file uint64) (uint32, error) {
	v, err := callDylib(symLMLoadedFileMaxContextTokens, []byte{i64}, []byte{i32}, file)
	if err != nil {
		return 0, fmt.Errorf("call model max-context getter: %w", err)
	}
	return uint32(v), nil
}
func (d *rusticatedDriver) loadedFileIsDynamicContext(file uint64) (bool, error) {
	v, err := callDylib(symLMLoadedFileIsDynamicContext, []byte{i64}, []byte{i32}, file)
	if err != nil {
		return false, fmt.Errorf("call model dynamic-context getter: %w", err)
	}
	return int32(v) != 0, nil
}
func (d *rusticatedDriver) loadedFileSupportsThinking(file uint64) (bool, error) {
	v, err := callDylib(symLMLoadedFileSupportsThinking, []byte{i64}, []byte{i32}, file)
	if err != nil {
		return false, fmt.Errorf("call model thinking-capability getter: %w", err)
	}
	return int32(v) != 0, nil
}
func (d *rusticatedDriver) loadedFileSupportsFunctionCalling(file uint64) (bool, error) {
	v, err := callDylib(symLMLoadedFileSupportsFunctionCalling, []byte{i64}, []byte{i32}, file)
	if err != nil {
		return false, fmt.Errorf("call model function-calling getter: %w", err)
	}
	return int32(v) != 0, nil
}
func (d *rusticatedDriver) loadedFileModalitySupportedBackends(file uint64, modality int32) (out []uint32, err error) {
	backendsBuf, err := dload.DylibAlloc(lmLib, uint64(8*4))
	if err != nil {
		return nil, fmt.Errorf("allocate supported-backends buffer: %w", err)
	}
	defer func() {
		if freeErr := dload.DylibFree(lmLib, backendsBuf); freeErr != nil {
			freeErr = fmt.Errorf("free supported-backends buffer: %w", freeErr)
			if err == nil {
				err = freeErr
			} else {
				err = fmt.Errorf("%w; %v", err, freeErr)
			}
		}
	}()
	count, err := callDylib(symLMLoadedFileModalitySupportedBackends, []byte{i64, i32, i64, i32}, []byte{i32},
		file, uint64(modality), backendsBuf, 8)
	if err != nil {
		return nil, fmt.Errorf("call model supported-backends getter: %w", err)
	}
	cnt := int(int32(count))
	if cnt > 8 {
		cnt = 8
	}
	if cnt <= 0 {
		return []uint32{}, nil
	}
	bBytes := make([]byte, cnt*4)
	if err := dload.DylibReadMem(lmLib, backendsBuf, bBytes); err != nil {
		return nil, fmt.Errorf("read supported-backends buffer: %w", err)
	}
	out = make([]uint32, 0, cnt)
	for i := 0; i < cnt; i++ {
		val := uint32(binary.LittleEndian.Uint32(bBytes[i*4 : (i+1)*4]))
		out = append(out, val)
	}
	return out, nil
}
func (d *rusticatedDriver) streamChunkGetText(chunk uint64) (uint64, error) {
	v, err := callDylibWithParent(symLMStreamChunkGetText, d.parentInvID, []byte{i64}, []byte{i64}, chunk)
	if err != nil {
		return 0, fmt.Errorf("call stream-chunk text getter: %w", err)
	}
	return v, nil
}
func (d *rusticatedDriver) streamChunkIsFinal(chunk uint64) (bool, error) {
	v, err := callDylibWithParent(symLMStreamChunkIsFinal, d.parentInvID, []byte{i64}, []byte{i32}, chunk)
	if err != nil {
		return false, fmt.Errorf("call stream-chunk final-flag getter: %w", err)
	}
	return int32(v) != 0, nil
}
func (d *rusticatedDriver) streamChunkGetError(chunk uint64) (uint64, error) {
	v, err := callDylibWithParent(symLMStreamChunkGetError, d.parentInvID, []byte{i64}, []byte{i64}, chunk)
	if err != nil {
		return 0, fmt.Errorf("call stream-chunk error getter: %w", err)
	}
	return v, nil
}

func (d *rusticatedDriver) embeddingSettingsCreate(modelPath, backend, visionBackend, audioBackend uint64) uint64 {
	v, _ := callDylib(symLMEmbeddingSettingsCreate, []byte{i64, i64, i64, i64}, []byte{i64},
		modelPath, backend, visionBackend, audioBackend)
	return v
}
func (d *rusticatedDriver) embeddingSettingsDelete(settings uint64) {
	_, _ = callDylib(symLMEmbeddingSettingsDelete, []byte{i64}, nil, settings)
}
func (d *rusticatedDriver) embeddingSettingsSetNumThreads(settings uint64, threads int32) {
	_, _ = callDylib(symLMEmbeddingSettingsSetNumThreads, []byte{i64, i32}, nil, settings, uint64(threads))
}
func (d *rusticatedDriver) embeddingEngineCreate(settings uint64) uint64 {
	v, _ := callDylib(symLMEmbeddingEngineCreate, []byte{i64}, []byte{i64}, settings)
	return v
}
func (d *rusticatedDriver) embeddingEngineDelete(engine uint64) {
	_, _ = callDylib(symLMEmbeddingEngineDelete, []byte{i64}, nil, engine)
}
func (d *rusticatedDriver) embeddingOptionsCreate() uint64 {
	v, _ := callDylib(symLMEmbeddingOptionsCreate, nil, []byte{i64})
	return v
}
func (d *rusticatedDriver) embeddingOptionsDelete(options uint64) {
	_, _ = callDylib(symLMEmbeddingOptionsDelete, []byte{i64}, nil, options)
}
func (d *rusticatedDriver) embeddingOptionsSetNormalize(options uint64, normalize bool) {
	b := byte(0)
	if normalize {
		b = 1
	}
	_, _ = callDylib(symLMEmbeddingOptionsSetNormalize, []byte{i64, i32}, nil, options, uint64(b))
}
func (d *rusticatedDriver) embeddingOptionsSetInputOverflowStrategy(options uint64, strategy int32) {
	_, _ = callDylib(symLMEmbeddingOptionsSetOverflowStrategy, []byte{i64, i32}, nil, options, uint64(strategy))
}
func (d *rusticatedDriver) inputDataCreate(dataType int32, data []byte) uint64 {
	var buf uint64
	if len(data) > 0 {
		var err error
		buf, err = dload.DylibAlloc(lmLib, uint64(len(data)))
		if err != nil {
			return 0
		}
		if dload.DylibWriteMem(lmLib, buf, data) != nil {
			_ = dload.DylibFree(lmLib, buf)
			return 0
		}
	}
	v, err := callDylib(symLMInputDataCreate, []byte{i32, i64, i64}, []byte{i64},
		uint64(dataType), buf, uint64(len(data)))
	if err != nil || v == 0 {
		if buf != 0 {
			_ = dload.DylibFree(lmLib, buf)
		}
		return 0
	}
	if buf != 0 {
		rusticatedInputBufMu.Lock()
		rusticatedInputBufs[v] = buf
		rusticatedInputBufMu.Unlock()
	}
	return v
}
func (d *rusticatedDriver) inputDataDelete(inputData uint64) {
	_, _ = callDylib(symLMInputDataDelete, []byte{i64}, nil, inputData)
	rusticatedInputBufMu.Lock()
	buf, ok := rusticatedInputBufs[inputData]
	if ok {
		delete(rusticatedInputBufs, inputData)
	}
	rusticatedInputBufMu.Unlock()
	if ok && buf != 0 {
		_ = dload.DylibFree(lmLib, buf)
	}
}
func (d *rusticatedDriver) embeddingEngineCompute(engine uint64, inputs []uint64, options uint64) ([]float32, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no inputs provided")
	}
	inputsBuf, err := dload.DylibAlloc(lmLib, uint64(len(inputs)*8))
	if err != nil {
		return nil, fmt.Errorf("DylibAlloc failed: %w", err)
	}
	defer func() { _ = dload.DylibFree(lmLib, inputsBuf) }()

	rawPtrs := make([]byte, len(inputs)*8)
	for i, ptr := range inputs {
		binary.LittleEndian.PutUint64(rawPtrs[i*8:(i+1)*8], ptr)
	}
	if err := dload.DylibWriteMem(lmLib, inputsBuf, rawPtrs); err != nil {
		return nil, fmt.Errorf("DylibWriteMem failed: %w", err)
	}

	resp, err := callDylib(symLMEmbeddingEngineCompute, []byte{i64, i64, i64, i64}, []byte{i64},
		engine, inputsBuf, uint64(len(inputs)), options)
	if err != nil || resp == 0 {
		return nil, fmt.Errorf("litert_lm_embedding_engine_compute_embedding failed")
	}
	defer func() { _, _ = callDylib(symLMEmbeddingResponseDelete, []byte{i64}, nil, resp) }()

	size, _ := callDylib(symLMEmbeddingResponseGetSize, []byte{i64}, []byte{i64}, resp)
	valsPtr, _ := callDylib(symLMEmbeddingResponseGetValues, []byte{i64}, []byte{i64}, resp)
	if size == 0 {
		return []float32{}, nil
	}
	if valsPtr == 0 {
		return nil, fmt.Errorf("invalid embedding response values pointer")
	}
	rawVals := make([]byte, int(size)*4)
	if err := dload.DylibReadMem(lmLib, valsPtr, rawVals); err != nil {
		return nil, fmt.Errorf("DylibReadMem failed: %w", err)
	}
	out := make([]float32, int(size))
	for i := 0; i < int(size); i++ {
		bits := binary.LittleEndian.Uint32(rawVals[i*4 : (i+1)*4])
		out[i] = math.Float32frombits(bits)
	}
	return out, nil
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

func ensureLMLibLoadedImpl(libPath string) error {
	lmLibOnce.Do(func() {
		var err error
		if Verbose {
			fmt.Fprintf(os.Stderr, "[ai] loading LiteRT-LM WASM library: %s\n", libPath)
		}
		lmLib, err = dload.DylibOpen(libPath, dload.RTLD_NOW|dload.RTLD_GLOBAL)
		if err != nil {
			lmLibErr = fmt.Errorf("failed to load %s: %w", libPath, err)
			return
		}
		symLMSettingsCreate, _ = dload.DylibSym(lmLib, "litert_lm_engine_settings_create")
		symLMSettingsDelete, _ = dload.DylibSym(lmLib, "litert_lm_engine_settings_delete")
		symLMEngineCreate, _ = dload.DylibSym(lmLib, "litert_lm_engine_create")
		symLMEngineDelete, _ = dload.DylibSym(lmLib, "litert_lm_engine_delete")
		symLMCfgCreate, _ = dload.DylibSym(lmLib, "litert_lm_conversation_config_create")
		symLMCfgDelete, _ = dload.DylibSym(lmLib, "litert_lm_conversation_config_delete")
		symLMConvCreate, _ = dload.DylibSym(lmLib, "litert_lm_conversation_create")
		symLMConvDelete, _ = dload.DylibSym(lmLib, "litert_lm_conversation_delete")
		symLMConvSendStream, _ = dload.DylibSym(lmLib, "litert_lm_conversation_send_message_stream")
		symLMConvCancelProcess, _ = dload.DylibSym(lmLib, "litert_lm_conversation_cancel_process")
		symLMSetLogLevel, _ = dload.DylibSym(lmLib, "litert_lm_set_min_log_level")
		symLMStreamChunkGetText, _ = dload.DylibSym(lmLib, "litert_lm_stream_chunk_get_text")
		symLMStreamChunkIsFinal, _ = dload.DylibSym(lmLib, "litert_lm_stream_chunk_is_final")
		symLMStreamChunkGetError, _ = dload.DylibSym(lmLib, "litert_lm_stream_chunk_get_error")

		symLMCfgSetTools, _ = dload.DylibSym(lmLib, "litert_lm_conversation_config_set_tools")
		symLMCfgSetSystemMessage, _ = dload.DylibSym(lmLib, "litert_lm_conversation_config_set_system_message")
		symLMCfgSetEnableConstrainedDecoding, _ = dload.DylibSym(lmLib, "litert_lm_conversation_config_set_enable_constrained_decoding")
		symLMCfgSetStreamToolCalls, _ = dload.DylibSym(lmLib, "litert_lm_conversation_config_set_stream_tool_calls")
		symLMCfgSetMessages, _ = dload.DylibSym(lmLib, "litert_lm_conversation_config_set_messages")

		symLMSettingsSetEnableSpeculativeDecoding, _ = dload.DylibSym(lmLib, "litert_lm_engine_settings_set_enable_speculative_decoding")
		symLMSettingsSetUseRingbuffersLocalAttention, _ = dload.DylibSym(lmLib, "litert_lm_engine_settings_set_use_ringbuffers_local_attention")
		symLMSettingsSetGpuDecodeStepsPerSync, _ = dload.DylibSym(lmLib, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync")
		symLMSettingsSetNumThreads, _ = dload.DylibSym(lmLib, "litert_lm_engine_settings_set_num_threads")
		symLMSettingsSetMaxNumTokens, _ = dload.DylibSym(lmLib, "litert_lm_engine_settings_set_max_num_tokens")

		symLMLoadedFileCreate, _ = dload.DylibSym(lmLib, "litert_lm_loaded_file_create")
		symLMLoadedFileDelete, _ = dload.DylibSym(lmLib, "litert_lm_loaded_file_delete")
		symLMLoadedFileHasSpeculativeDecodingSupport, _ = dload.DylibSym(lmLib, "litert_lm_loaded_file_has_speculative_decoding_support")
		symLMLoadedFileMaxContextTokens, _ = dload.DylibSym(lmLib, "litert_lm_loaded_file_max_context_tokens")
		symLMLoadedFileIsDynamicContext, _ = dload.DylibSym(lmLib, "litert_lm_loaded_file_is_dynamic_context")
		symLMLoadedFileSupportsThinking, _ = dload.DylibSym(lmLib, "litert_lm_loaded_file_supports_thinking")
		symLMLoadedFileSupportsFunctionCalling, _ = dload.DylibSym(lmLib, "litert_lm_loaded_file_supports_function_calling")
		symLMLoadedFileModalitySupportedBackends, _ = dload.DylibSym(lmLib, "litert_lm_loaded_file_modality_supported_backends")

		symLMEmbeddingSettingsCreate, _ = dload.DylibSym(lmLib, "litert_lm_embedding_engine_settings_create")
		symLMEmbeddingSettingsDelete, _ = dload.DylibSym(lmLib, "litert_lm_embedding_engine_settings_delete")
		symLMEmbeddingSettingsSetNumThreads, _ = dload.DylibSym(lmLib, "litert_lm_embedding_engine_settings_set_num_threads")
		symLMEmbeddingEngineCreate, _ = dload.DylibSym(lmLib, "litert_lm_embedding_engine_create")
		symLMEmbeddingEngineDelete, _ = dload.DylibSym(lmLib, "litert_lm_embedding_engine_delete")
		symLMEmbeddingOptionsCreate, _ = dload.DylibSym(lmLib, "litert_lm_embedding_options_create")
		symLMEmbeddingOptionsDelete, _ = dload.DylibSym(lmLib, "litert_lm_embedding_options_delete")
		symLMEmbeddingOptionsSetNormalize, _ = dload.DylibSym(lmLib, "litert_lm_embedding_options_set_normalize")
		symLMEmbeddingOptionsSetOverflowStrategy, _ = dload.DylibSym(lmLib, "litert_lm_embedding_options_set_input_overflow_strategy")
		symLMInputDataCreate, _ = dload.DylibSym(lmLib, "litert_lm_input_data_create")
		symLMInputDataDelete, _ = dload.DylibSym(lmLib, "litert_lm_input_data_delete")
		symLMEmbeddingEngineCompute, _ = dload.DylibSym(lmLib, "litert_lm_embedding_engine_compute_embedding")
		symLMEmbeddingResponseDelete, _ = dload.DylibSym(lmLib, "litert_lm_embedding_response_delete")
		symLMEmbeddingResponseGetSize, _ = dload.DylibSym(lmLib, "litert_lm_embedding_response_get_size")
		symLMEmbeddingResponseGetValues, _ = dload.DylibSym(lmLib, "litert_lm_embedding_response_get_values")
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
	if isBrowserHost() || libPath == "eval" {
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
