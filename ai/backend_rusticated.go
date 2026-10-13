//go:build wasip1 && !js

package ai

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"syscall"
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

	symLMCfgSetTools           uint64
	symLMCfgSetSystemMessage   uint64
	symLMCfgSetStreamToolCalls uint64
	symLMCfgSetMessages        uint64

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

	rusticatedDrv rusticatedDriver
)

// callDylib invokes a WASM dynamic library function with typed parameters.
func callDylib(sym uint64, paramTypes, resultTypes []byte, args ...uint64) (uint64, error) {
	sig := make([]byte, 0, 3+len(paramTypes)+len(resultTypes))
	sig = append(sig, 0x60, byte(len(paramTypes)))
	sig = append(sig, paramTypes...)
	sig = append(sig, byte(len(resultTypes)))
	sig = append(sig, resultTypes...)
	res, err := syscall.DylibCall(sym, sig, args, 0)
	if err != nil {
		return 0, err
	}
	if len(res) > 0 {
		return res[0], nil
	}
	return 0, nil
}

// ── rusticatedDriver — implements LMDriver via callDylib ─────────────────────

type rusticatedDriver struct{}

func (d *rusticatedDriver) ensureLoaded(libPath string) error {
	return ensureLMLibLoadedImpl(libPath)
}

func (d *rusticatedDriver) getSymbol(name string) (uint64, error) {
	return syscall.DylibSym(lmLib, name)
}

func (d *rusticatedDriver) writeInt32(addr uint64, val int32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(val))
	_ = syscall.DylibWriteMem(lmLib, addr, buf[:])
}

func (d *rusticatedDriver) writeBool(addr uint64, val bool) {
	b := byte(0)
	if val {
		b = 1
	}
	_ = syscall.DylibWriteMem(lmLib, addr, []byte{b})
}

func (d *rusticatedDriver) stringToCPtr(s string) (uint64, func()) {
	ptr, free, err := syscall.CString(lmLib, s)
	if err != nil {
		return 0, func() {}
	}
	return ptr, free
}

func (d *rusticatedDriver) ptrToString(ptr uint64) string {
	if ptr == 0 {
		return ""
	}
	s, _ := syscall.GoString(lmLib, ptr)
	return s
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
func (d *rusticatedDriver) loadedFileMaxContextTokens(file uint64) uint32 {
	v, _ := callDylib(symLMLoadedFileMaxContextTokens, []byte{i64}, []byte{i32}, file)
	return uint32(v)
}
func (d *rusticatedDriver) loadedFileIsDynamicContext(file uint64) bool {
	v, _ := callDylib(symLMLoadedFileIsDynamicContext, []byte{i64}, []byte{i32}, file)
	return int32(v) != 0
}
func (d *rusticatedDriver) loadedFileSupportsThinking(file uint64) bool {
	v, _ := callDylib(symLMLoadedFileSupportsThinking, []byte{i64}, []byte{i32}, file)
	return int32(v) != 0
}
func (d *rusticatedDriver) loadedFileSupportsFunctionCalling(file uint64) bool {
	v, _ := callDylib(symLMLoadedFileSupportsFunctionCalling, []byte{i64}, []byte{i32}, file)
	return int32(v) != 0
}
func (d *rusticatedDriver) loadedFileModalitySupportedBackends(file uint64, modality int32) []uint32 {
	backendsBuf, err := syscall.DylibAlloc(lmLib, uint64(8*4))
	if err != nil {
		return []uint32{}
	}
	defer func() { _ = syscall.DylibFree(lmLib, backendsBuf) }()
	count, err := callDylib(symLMLoadedFileModalitySupportedBackends, []byte{i64, i32, i64, i32}, []byte{i32},
		file, uint64(modality), backendsBuf, 8)
	if err != nil {
		return []uint32{}
	}
	cnt := int(int32(count))
	if cnt > 8 {
		cnt = 8
	}
	if cnt <= 0 {
		return []uint32{}
	}
	bBytes := make([]byte, cnt*4)
	if syscall.DylibReadMem(lmLib, backendsBuf, bBytes) != nil {
		return []uint32{}
	}
	var out []uint32
	for i := 0; i < cnt; i++ {
		val := uint32(binary.LittleEndian.Uint32(bBytes[i*4 : (i+1)*4]))
		out = append(out, val)
	}
	return out
}
func (d *rusticatedDriver) streamChunkGetText(chunk uint64) uint64 {
	v, _ := callDylib(symLMStreamChunkGetText, []byte{i64}, []byte{i64}, chunk)
	return v
}
func (d *rusticatedDriver) streamChunkIsFinal(chunk uint64) bool {
	v, _ := callDylib(symLMStreamChunkIsFinal, []byte{i64}, []byte{i32}, chunk)
	return int32(v) != 0
}
func (d *rusticatedDriver) streamChunkGetError(chunk uint64) uint64 {
	v, _ := callDylib(symLMStreamChunkGetError, []byte{i64}, []byte{i64}, chunk)
	return v
}

var (
	rusticatedTrampoline uint64
	trampolineOv         [512]byte
	trampolineRegOnce    sync.Once
)

func (d *rusticatedDriver) createTrampoline() uint64 {
	trampolineRegOnce.Do(func() {
		cbSig := []byte{0x60, 2, i64, i64, 0}
		rusticatedTrampoline, _ = syscall.DylibRegisterCallback(lmLib, cbSig, unsafe.Pointer(&trampolineOv[0]), 512,
			func(args []uint64) []uint64 {
				if len(args) < 2 {
					return nil
				}
				userData := args[0]
				chunkPtr := args[1]
				if chunkPtr == 0 {
					return nil
				}
				sharedTokenCallback(&rusticatedDrv, userData, chunkPtr)
				return nil
			})
	})
	return rusticatedTrampoline
}

// ── Library loading ────────────────────────────────────────────────────────

func ensureLMLibLoadedImpl(libPath string) error {
	lmLibOnce.Do(func() {
		var err error
		if Verbose {
			fmt.Fprintf(os.Stderr, "[ai] loading LiteRT-LM WASM library: %s\n", libPath)
		}
		lmLib, err = syscall.DylibOpen(libPath, syscall.RTLD_NOW|syscall.RTLD_GLOBAL)
		if err != nil {
			lmLibErr = fmt.Errorf("failed to load %s: %w", libPath, err)
			return
		}
		symLMSettingsCreate, _ = syscall.DylibSym(lmLib, "litert_lm_engine_settings_create")
		symLMSettingsDelete, _ = syscall.DylibSym(lmLib, "litert_lm_engine_settings_delete")
		symLMEngineCreate, _ = syscall.DylibSym(lmLib, "litert_lm_engine_create")
		symLMEngineDelete, _ = syscall.DylibSym(lmLib, "litert_lm_engine_delete")
		symLMCfgCreate, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_config_create")
		symLMCfgDelete, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_config_delete")
		symLMConvCreate, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_create")
		symLMConvDelete, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_delete")
		symLMConvSendStream, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_send_message_stream")
		symLMConvCancelProcess, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_cancel_process")
		symLMSetLogLevel, _ = syscall.DylibSym(lmLib, "litert_lm_set_min_log_level")
		symLMStreamChunkGetText, _ = syscall.DylibSym(lmLib, "litert_lm_stream_chunk_get_text")
		symLMStreamChunkIsFinal, _ = syscall.DylibSym(lmLib, "litert_lm_stream_chunk_is_final")
		symLMStreamChunkGetError, _ = syscall.DylibSym(lmLib, "litert_lm_stream_chunk_get_error")

		symLMCfgSetTools, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_config_set_tools")
		symLMCfgSetSystemMessage, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_config_set_system_message")
		symLMCfgSetStreamToolCalls, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_config_set_stream_tool_calls")
		symLMCfgSetMessages, _ = syscall.DylibSym(lmLib, "litert_lm_conversation_config_set_messages")

		symLMSettingsSetEnableSpeculativeDecoding, _ = syscall.DylibSym(lmLib, "litert_lm_engine_settings_set_enable_speculative_decoding")
		symLMSettingsSetUseRingbuffersLocalAttention, _ = syscall.DylibSym(lmLib, "litert_lm_engine_settings_set_use_ringbuffers_local_attention")
		symLMSettingsSetGpuDecodeStepsPerSync, _ = syscall.DylibSym(lmLib, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync")
		symLMSettingsSetNumThreads, _ = syscall.DylibSym(lmLib, "litert_lm_engine_settings_set_num_threads")
		symLMSettingsSetMaxNumTokens, _ = syscall.DylibSym(lmLib, "litert_lm_engine_settings_set_max_num_tokens")

		symLMLoadedFileCreate, _ = syscall.DylibSym(lmLib, "litert_lm_loaded_file_create")
		symLMLoadedFileDelete, _ = syscall.DylibSym(lmLib, "litert_lm_loaded_file_delete")
		symLMLoadedFileHasSpeculativeDecodingSupport, _ = syscall.DylibSym(lmLib, "litert_lm_loaded_file_has_speculative_decoding_support")
		symLMLoadedFileMaxContextTokens, _ = syscall.DylibSym(lmLib, "litert_lm_loaded_file_max_context_tokens")
		symLMLoadedFileIsDynamicContext, _ = syscall.DylibSym(lmLib, "litert_lm_loaded_file_is_dynamic_context")
		symLMLoadedFileSupportsThinking, _ = syscall.DylibSym(lmLib, "litert_lm_loaded_file_supports_thinking")
		symLMLoadedFileSupportsFunctionCalling, _ = syscall.DylibSym(lmLib, "litert_lm_loaded_file_supports_function_calling")
		symLMLoadedFileModalitySupportedBackends, _ = syscall.DylibSym(lmLib, "litert_lm_loaded_file_modality_supported_backends")
	})
	return lmLibErr
}

// ── Thin public wrappers ───────────────────────────────────────────────────

func openBackend(libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	return sharedOpenBackend(&rusticatedDrv, libPath, modelPath, backend, maxTokens)
}

func inspectModel(libPath, modelPath string) (*ModelInfo, error) {
	return sharedInspectModel(&rusticatedDrv, libPath, modelPath)
}
