//go:build wasip1 && !js

package ai

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const (
	i32 = 0x7F
	i64 = 0x7E
)

var (
	lmLibOnce sync.Once
	lmLibErr  error
	lmLib     uint64

	symLMSettingsCreate       uint64
	symLMSettingsDelete       uint64
	symLMEngineCreate         uint64
	symLMEngineDelete         uint64
	symLMCfgCreate            uint64
	symLMCfgDelete            uint64
	symLMConvCreate           uint64
	symLMConvDelete           uint64
	symLMConvSendStream       uint64
	symLMSetLogLevel          uint64
	symLMGetDefaultLogger     uint64
	symLMSetMinLoggerSeverity uint64
	symLMGetSinkLoggerSize    uint64
	symLMGetSinkLoggerMessage uint64
	symLMClearSinkLogger      uint64
	symLMStreamChunkGetText   uint64
	symLMStreamChunkIsFinal   uint64
	symLMStreamChunkGetError  uint64

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

	symLMEngineTryLoadVisionExecutor uint64
)

var (
	tokenCallbackMu       sync.Mutex
	tokenCallbackRegistry = make(map[uint64]func(string))
	tokenDoneChannels     = make(map[uint64]chan struct{})
	tokenCallbackNextID   uint64
	tokenCbOv             [512]byte
	tokenTrampoline       uint64
	tokenRegOnce          sync.Once
	tokenRegErr           error
)

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

func goStringToCPtr(s string) (uint64, func()) {
	ptr, free, err := syscall.CString(lmLib, s)
	if err != nil {
		return 0, func() {}
	}
	return ptr, free
}

func ptrToGoString(ptr uint64) string {
	if ptr == 0 {
		return ""
	}
	s, _ := syscall.GoString(lmLib, ptr)
	return s
}

func ensureLMLibLoaded(libPath string) error {
	lmLibOnce.Do(func() {
		var err error
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
		symLMSetLogLevel, _ = syscall.DylibSym(lmLib, "litert_lm_set_min_log_level")
		symLMGetDefaultLogger, _ = syscall.DylibSym(lmLib, "LiteRtGetDefaultLogger")
		symLMSetMinLoggerSeverity, _ = syscall.DylibSym(lmLib, "LiteRtSetMinLoggerSeverity")
		symLMGetSinkLoggerSize, _ = syscall.DylibSym(lmLib, "LiteRtGetSinkLoggerSize")
		symLMGetSinkLoggerMessage, _ = syscall.DylibSym(lmLib, "LiteRtGetSinkLoggerMessage")
		symLMClearSinkLogger, _ = syscall.DylibSym(lmLib, "LiteRtClearSinkLogger")
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

		symLMEngineTryLoadVisionExecutor, _ = syscall.DylibSym(lmLib, "litert_lm_engine_try_loading_vision_executor")
	})
	return lmLibErr
}

func configureLMLogging() {
	if sym, err := syscall.DylibSym(lmLib, "FLAGS_minloglevel"); err == nil && sym != 0 {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], 2)
		_ = syscall.DylibWriteMem(lmLib, sym, buf[:])
	}
	if sym, err := syscall.DylibSym(lmLib, "FLAGS_logtostderr"); err == nil && sym != 0 {
		_ = syscall.DylibWriteMem(lmLib, sym, []byte{0})
	}
	if sym, err := syscall.DylibSym(lmLib, "FLAGS_alsologtostderr"); err == nil && sym != 0 {
		_ = syscall.DylibWriteMem(lmLib, sym, []byte{0})
	}
	if symLMSetLogLevel != 0 {
		_, _ = callDylib(symLMSetLogLevel, []byte{i64}, nil, 10)
	}
	if symLMGetDefaultLogger != 0 && symLMSetMinLoggerSeverity != 0 {
		if logger, err := callDylib(symLMGetDefaultLogger, nil, []byte{i64}); err == nil && logger != 0 {
			_, _ = callDylib(symLMSetMinLoggerSeverity, []byte{i64, i32}, []byte{i32}, logger, 3)
		}
	}
}

func ensureTokenCallbackRegistered() error {
	tokenRegOnce.Do(func() {
		cbSig := []byte{0x60, 2, i64, i64, 0}
		var err error
		tokenTrampoline, err = syscall.DylibRegisterCallback(lmLib, cbSig, unsafe.Pointer(&tokenCbOv[0]), 512, func(args []uint64) []uint64 {
			userData := args[0]
			chunkPtr := args[1]
			if chunkPtr == 0 {
				return nil
			}
			if symLMStreamChunkGetError != 0 {
				if errPtr, _ := callDylib(symLMStreamChunkGetError, []byte{i64}, []byte{i64}, chunkPtr); errPtr != 0 {
					if s := ptrToGoString(errPtr); s != "" {
						fmt.Fprintf(os.Stderr, "LiteRT stream error: %s\n", s)
					}
				}
			}
			if symLMStreamChunkGetText != 0 {
				if textPtr, _ := callDylib(symLMStreamChunkGetText, []byte{i64}, []byte{i64}, chunkPtr); textPtr != 0 {
					if token := ptrToGoString(textPtr); token != "" {
						tokenCallbackMu.Lock()
						fn := tokenCallbackRegistry[userData]
						tokenCallbackMu.Unlock()
						if fn != nil {
							fn(token)
						}
					}
				}
			}
			if symLMStreamChunkIsFinal != 0 {
				if v, _ := callDylib(symLMStreamChunkIsFinal, []byte{i64}, []byte{i32}, chunkPtr); int32(v) != 0 {
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
			return nil
		})
		if err != nil {
			tokenRegErr = fmt.Errorf("failed to register token callback: %w", err)
		}
	})
	return tokenRegErr
}

func checkLMSpeculativeDecoding(modelPtr uint64) bool {
	if symLMLoadedFileCreate == 0 || symLMLoadedFileHasSpeculativeDecodingSupport == 0 {
		return false
	}
	file, err := callDylib(symLMLoadedFileCreate, []byte{i64}, []byte{i64}, modelPtr)
	if err != nil || file == 0 {
		return false
	}
	defer func() {
		if symLMLoadedFileDelete != 0 {
			_, _ = callDylib(symLMLoadedFileDelete, []byte{i64}, nil, file)
		}
	}()
	ok, err := callDylib(symLMLoadedFileHasSpeculativeDecodingSupport, []byte{i64}, []byte{i32}, file)
	return err == nil && int32(ok) != 0
}

func inspectModel(libPath, modelPath string) (*ModelInfo, error) {
	if err := ensureLMLibLoaded(libPath); err != nil {
		return nil, err
	}
	cModel, freeModel := goStringToCPtr(modelPath)
	defer freeModel()

	if symLMLoadedFileCreate == 0 {
		return nil, fmt.Errorf("loaded_file_create not exported by runtime")
	}
	file, err := callDylib(symLMLoadedFileCreate, []byte{i64}, []byte{i64}, cModel)
	if err != nil || file == 0 {
		return nil, fmt.Errorf("failed to inspect model file %s", modelPath)
	}
	defer func() {
		if symLMLoadedFileDelete != 0 {
			_, _ = callDylib(symLMLoadedFileDelete, []byte{i64}, nil, file)
		}
	}()

	info := &ModelInfo{}
	if symLMLoadedFileMaxContextTokens != 0 {
		if tokens, err := callDylib(symLMLoadedFileMaxContextTokens, []byte{i64}, []byte{i32}, file); err == nil {
			info.MaxContextTokens = int(uint32(tokens))
		}
	}
	if symLMLoadedFileIsDynamicContext != 0 {
		if dyn, err := callDylib(symLMLoadedFileIsDynamicContext, []byte{i64}, []byte{i32}, file); err == nil {
			info.IsDynamicContext = int32(dyn) != 0
		}
	}
	if symLMLoadedFileSupportsThinking != 0 {
		if think, err := callDylib(symLMLoadedFileSupportsThinking, []byte{i64}, []byte{i32}, file); err == nil {
			info.SupportsThinking = int32(think) != 0
		}
	}
	if symLMLoadedFileSupportsFunctionCalling != 0 {
		if fc, err := callDylib(symLMLoadedFileSupportsFunctionCalling, []byte{i64}, []byte{i32}, file); err == nil {
			info.SupportsFunctionCalling = int32(fc) != 0
		}
	}
	if symLMLoadedFileModalitySupportedBackends != 0 {
		maxSize := 8
		backendsBuf, err1 := syscall.DylibAlloc(lmLib, uint64(maxSize*4))
		countBuf, err2 := syscall.DylibAlloc(lmLib, 4)
		if err1 == nil && err2 == nil {
			rc, err := callDylib(symLMLoadedFileModalitySupportedBackends, []byte{i64, i32, i64, i32, i64}, []byte{i32},
				file, 0, backendsBuf, uint64(maxSize), countBuf)
			if err == nil && int32(rc) == 0 {
				var countBytes [4]byte
				if syscall.DylibReadMem(lmLib, countBuf, countBytes[:]) == nil {
					count := int(binary.LittleEndian.Uint32(countBytes[:]))
					if count > maxSize {
						count = maxSize
					}
					bBytes := make([]byte, count*4)
					if syscall.DylibReadMem(lmLib, backendsBuf, bBytes) == nil {
						for i := 0; i < count; i++ {
							val := binary.LittleEndian.Uint32(bBytes[i*4 : (i+1)*4])
							switch val {
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
			}
		}
		if backendsBuf != 0 {
			_ = syscall.DylibFree(lmLib, backendsBuf)
		}
		if countBuf != 0 {
			_ = syscall.DylibFree(lmLib, countBuf)
		}
	}
	return info, nil
}

type wasip1EngineBackend struct {
	engine uint64
}

func openBackend(libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	if err := ensureLMLibLoaded(libPath); err != nil {
		return nil, err
	}
	configureLMLogging()

	cModel, freeModel := goStringToCPtr(modelPath)
	defer freeModel()

	supportsSpec := checkLMSpeculativeDecoding(cModel)

	cBackend, freeBackend := goStringToCPtr(backend)
	settings, err := callDylib(symLMSettingsCreate, []byte{i64, i64, i64, i64}, []byte{i64}, cModel, cBackend, cBackend, 0)
	freeBackend()
	if err != nil || settings == 0 {
		return nil, fmt.Errorf("settings_create failed for backend %s: %v", backend, err)
	}
	if supportsSpec && symLMSettingsSetEnableSpeculativeDecoding != 0 {
		_, _ = callDylib(symLMSettingsSetEnableSpeculativeDecoding, []byte{i64, i32}, nil, settings, 1)
	}
	if symLMSettingsSetUseRingbuffersLocalAttention != 0 {
		_, _ = callDylib(symLMSettingsSetUseRingbuffersLocalAttention, []byte{i64, i32}, nil, settings, 1)
	}
	if backend == "gpu" && symLMSettingsSetGpuDecodeStepsPerSync != 0 {
		_, _ = callDylib(symLMSettingsSetGpuDecodeStepsPerSync, []byte{i64, i32}, nil, settings, 8)
	}
	if backend == "cpu" && symLMSettingsSetNumThreads != 0 {
		_, _ = callDylib(symLMSettingsSetNumThreads, []byte{i64, i32}, nil, settings, uint64(runtime.NumCPU()))
	}
	if maxTokens > 0 && symLMSettingsSetMaxNumTokens != 0 {
		_, _ = callDylib(symLMSettingsSetMaxNumTokens, []byte{i64, i32}, []byte{i32}, settings, uint64(maxTokens))
	}

	engine, err := callDylib(symLMEngineCreate, []byte{i64}, []byte{i64}, settings)
	if symLMSettingsDelete != 0 {
		_, _ = callDylib(symLMSettingsDelete, []byte{i64}, nil, settings)
	}
	if err != nil || engine == 0 {
		return nil, fmt.Errorf("engine_create failed for backend %s: %v", backend, err)
	}
	if symLMEngineTryLoadVisionExecutor != 0 {
		_, _ = callDylib(symLMEngineTryLoadVisionExecutor, []byte{i64}, []byte{i32}, engine)
	}
	return &wasip1EngineBackend{engine: engine}, nil
}

func (b *wasip1EngineBackend) Close() error {
	if b.engine != 0 && symLMEngineDelete != 0 {
		_, _ = callDylib(symLMEngineDelete, []byte{i64}, nil, b.engine)
		b.engine = 0
	}
	return nil
}

type wasip1ConversationBackend struct {
	conv uint64
}

func (b *wasip1EngineBackend) NewConversation(systemJSON, toolsJSON, historyJSON string) (conversationBackend, error) {
	config, err := callDylib(symLMCfgCreate, []byte{i64}, []byte{i64}, b.engine)
	if err != nil || config == 0 {
		return nil, fmt.Errorf("conversation_config_create failed: %v", err)
	}
	defer func() { _, _ = callDylib(symLMCfgDelete, []byte{i64}, nil, config) }()

	if systemJSON != "" && symLMCfgSetSystemMessage != 0 {
		cSys, freeSys := goStringToCPtr(systemJSON)
		_, _ = callDylib(symLMCfgSetSystemMessage, []byte{i64, i64}, []byte{i32}, config, cSys)
		freeSys()
	}

	if toolsJSON != "" && symLMCfgSetTools != 0 {
		cTools, freeTools := goStringToCPtr(toolsJSON)
		_, _ = callDylib(symLMCfgSetTools, []byte{i64, i64}, []byte{i32}, config, cTools)
		freeTools()
	}

	if symLMCfgSetStreamToolCalls != 0 {
		cChan, freeChan := goStringToCPtr("tool_call")
		_, _ = callDylib(symLMCfgSetStreamToolCalls, []byte{i64, i32, i64}, []byte{i32}, config, 1, cChan)
		freeChan()
	}

	if historyJSON != "" && symLMCfgSetMessages != 0 {
		cMsgs, freeMsgs := goStringToCPtr(historyJSON)
		_, _ = callDylib(symLMCfgSetMessages, []byte{i64, i64}, []byte{i32}, config, cMsgs)
		freeMsgs()
	}

	conv, err := callDylib(symLMConvCreate, []byte{i64, i64}, []byte{i64}, b.engine, config)
	if err != nil || conv == 0 {
		return nil, fmt.Errorf("conversation_create failed: %v", err)
	}
	return &wasip1ConversationBackend{conv: conv}, nil
}

func (c *wasip1ConversationBackend) Close() error {
	if c.conv != 0 && symLMConvDelete != 0 {
		_, _ = callDylib(symLMConvDelete, []byte{i64}, nil, c.conv)
		c.conv = 0
	}
	return nil
}

func (c *wasip1ConversationBackend) SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error {
	_ = ctx
	if err := ensureTokenCallbackRegistered(); err != nil {
		return err
	}

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

	cMsg, freeMsg := goStringToCPtr(messageJSON)
	defer freeMsg()

	rc, err := callDylib(symLMConvSendStream, []byte{i64, i64, i64, i64, i64, i64}, []byte{i32},
		c.conv, cMsg, 0, 0, tokenTrampoline, cbID)
	if err != nil {
		return fmt.Errorf("send_message_stream failed: %w", err)
	}
	if int32(rc) != 0 {
		return fmt.Errorf("send_message_stream returned error code: %d", int32(rc))
	}
	<-doneCh

	if symLMClearSinkLogger != 0 {
		_, _ = callDylib(symLMClearSinkLogger, nil, nil)
	}
	return nil
}
