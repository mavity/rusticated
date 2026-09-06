//go:build wasip1

package main

import (
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
	libOnce sync.Once
	libErr  error
	lib     uint64

	symSettingsCreate       uint64
	symSettingsDelete       uint64
	symEngineCreate         uint64
	symCfgCreate            uint64
	symCfgDelete            uint64
	symConvCreate           uint64
	symConvSendStream       uint64
	symSetLogLevel          uint64
	symGetDefaultLogger     uint64
	symSetMinLoggerSeverity uint64
	symGetSinkLoggerSize    uint64
	symGetSinkLoggerMessage uint64
	symClearSinkLogger      uint64
	symStreamChunkGetText   uint64
	symStreamChunkIsFinal   uint64
	symStreamChunkGetError  uint64

	symSettingsSetEnableSpeculativeDecoding    uint64
	symSettingsSetUseRingbuffersLocalAttention uint64
	symSettingsSetGpuDecodeStepsPerSync        uint64
	symSettingsSetNumThreads                   uint64
	symLoadedFileCreate                        uint64
	symLoadedFileDelete                        uint64
	symLoadedFileHasSpeculativeDecodingSupport uint64
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
	ptr, free, err := syscall.CString(lib, s)
	if err != nil {
		return 0, func() {}
	}
	return ptr, free
}

func ptrToGoString(ptr uint64) string {
	if ptr == 0 {
		return ""
	}
	s, _ := syscall.GoString(lib, ptr)
	return s
}

func ensureLibLoaded(libPath string) error {
	libOnce.Do(func() {
		var err error
		lib, err = syscall.DylibOpen(libPath, syscall.RTLD_NOW|syscall.RTLD_GLOBAL)
		if err != nil {
			libErr = fmt.Errorf("failed to load %s: %w", libPath, err)
			return
		}

		symSettingsCreate, _ = syscall.DylibSym(lib, "litert_lm_engine_settings_create")
		symSettingsDelete, _ = syscall.DylibSym(lib, "litert_lm_engine_settings_delete")
		symEngineCreate, _ = syscall.DylibSym(lib, "litert_lm_engine_create")
		symCfgCreate, _ = syscall.DylibSym(lib, "litert_lm_conversation_config_create")
		symCfgDelete, _ = syscall.DylibSym(lib, "litert_lm_conversation_config_delete")
		symConvCreate, _ = syscall.DylibSym(lib, "litert_lm_conversation_create")
		symConvSendStream, _ = syscall.DylibSym(lib, "litert_lm_conversation_send_message_stream")
		symSetLogLevel, _ = syscall.DylibSym(lib, "litert_lm_set_min_log_level")
		symGetDefaultLogger, _ = syscall.DylibSym(lib, "LiteRtGetDefaultLogger")
		symSetMinLoggerSeverity, _ = syscall.DylibSym(lib, "LiteRtSetMinLoggerSeverity")
		symGetSinkLoggerSize, _ = syscall.DylibSym(lib, "LiteRtGetSinkLoggerSize")
		symGetSinkLoggerMessage, _ = syscall.DylibSym(lib, "LiteRtGetSinkLoggerMessage")
		symClearSinkLogger, _ = syscall.DylibSym(lib, "LiteRtClearSinkLogger")
		symStreamChunkGetText, _ = syscall.DylibSym(lib, "litert_lm_stream_chunk_get_text")
		symStreamChunkIsFinal, _ = syscall.DylibSym(lib, "litert_lm_stream_chunk_is_final")
		symStreamChunkGetError, _ = syscall.DylibSym(lib, "litert_lm_stream_chunk_get_error")

		symSettingsSetEnableSpeculativeDecoding, _ = syscall.DylibSym(lib, "litert_lm_engine_settings_set_enable_speculative_decoding")
		symSettingsSetUseRingbuffersLocalAttention, _ = syscall.DylibSym(lib, "litert_lm_engine_settings_set_use_ringbuffers_local_attention")
		symSettingsSetGpuDecodeStepsPerSync, _ = syscall.DylibSym(lib, "litert_lm_engine_settings_set_gpu_decode_steps_per_sync")
		symSettingsSetNumThreads, _ = syscall.DylibSym(lib, "litert_lm_engine_settings_set_num_threads")
		symLoadedFileCreate, _ = syscall.DylibSym(lib, "litert_lm_loaded_file_create")
		symLoadedFileDelete, _ = syscall.DylibSym(lib, "litert_lm_loaded_file_delete")
		symLoadedFileHasSpeculativeDecodingSupport, _ = syscall.DylibSym(lib, "litert_lm_loaded_file_has_speculative_decoding_support")
	})
	return libErr
}

func configureLogging() {
	if sym, err := syscall.DylibSym(lib, "FLAGS_minloglevel"); err == nil && sym != 0 {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], 2) // 2 = ERROR
		_ = syscall.DylibWriteMem(lib, sym, buf[:])
	}
	if sym, err := syscall.DylibSym(lib, "FLAGS_logtostderr"); err == nil && sym != 0 {
		_ = syscall.DylibWriteMem(lib, sym, []byte{0})
	}
	if sym, err := syscall.DylibSym(lib, "FLAGS_alsologtostderr"); err == nil && sym != 0 {
		_ = syscall.DylibWriteMem(lib, sym, []byte{0})
	}

	if symSetLogLevel != 0 {
		_, _ = callDylib(symSetLogLevel, []byte{i64}, nil, 10)
	}

	if symGetDefaultLogger != 0 && symSetMinLoggerSeverity != 0 {
		logger, err := callDylib(symGetDefaultLogger, nil, []byte{i64})
		if err == nil && logger != 0 {
			_, _ = callDylib(symSetMinLoggerSeverity, []byte{i64, i32}, []byte{i32}, logger, 3)
		}
	}
}

func ensureCallbackRegistered() error {
	tokenRegOnce.Do(func() {
		// Callback signature: void callback(void* userData, void* chunkPtr)
		// WASM functype: [i64, i64] -> []
		cbSig := []byte{0x60, 2, i64, i64, 0}
		var err error
		tokenTrampoline, err = syscall.DylibRegisterCallback(lib, cbSig, unsafe.Pointer(&tokenCbOv[0]), 512, func(args []uint64) []uint64 {
			userData := args[0]
			chunkPtr := args[1]

			if chunkPtr != 0 {
				if symStreamChunkGetError != 0 {
					errPtr, _ := callDylib(symStreamChunkGetError, []byte{i64}, []byte{i64}, chunkPtr)
					if errPtr != 0 {
						errStr := ptrToGoString(errPtr)
						if errStr != "" {
							fmt.Fprintf(os.Stderr, "LiteRT stream error: %s\n", errStr)
						}
					}
				}

				if symStreamChunkGetText != 0 {
					textPtr, _ := callDylib(symStreamChunkGetText, []byte{i64}, []byte{i64}, chunkPtr)
					if textPtr != 0 {
						token := ptrToGoString(textPtr)
						if token != "" {
							tokenCallbackMu.Lock()
							fn := tokenCallbackRegistry[userData]
							tokenCallbackMu.Unlock()
							if fn != nil {
								fn(token)
							}
						}
					}
				}

				if symStreamChunkIsFinal != 0 {
					isFinalVal, _ := callDylib(symStreamChunkIsFinal, []byte{i64}, []byte{i32}, chunkPtr)
					if int32(isFinalVal) != 0 {
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

			return nil
		})
		if err != nil {
			tokenRegErr = fmt.Errorf("failed to register callback: %w", err)
		}
	})
	return tokenRegErr
}

// LMEngine wraps the backend model engine
type LMEngine struct {
	ptr uint64
}

func (e *LMEngine) RawEngine() uint64 { return e.ptr }

func checkSpeculativeDecodingSupport(modelPtr uint64) bool {
	if symLoadedFileCreate == 0 || symLoadedFileHasSpeculativeDecodingSupport == 0 {
		return false
	}
	file, err := callDylib(symLoadedFileCreate, []byte{i64}, []byte{i64}, modelPtr)
	if err != nil || file == 0 {
		return false
	}
	hasSupport, err := callDylib(symLoadedFileHasSpeculativeDecodingSupport, []byte{i64}, []byte{i32}, file)
	if symLoadedFileDelete != 0 {
		_, _ = callDylib(symLoadedFileDelete, []byte{i64}, nil, file)
	}
	return err == nil && int32(hasSupport) != 0
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

	cModel, freeModel := goStringToCPtr(modelPath)
	defer freeModel()

	supportsSpec := checkSpeculativeDecodingSupport(cModel)

	candidates := resolveBackendCandidates(backend)
	var lastErr error

	for _, cand := range candidates {
		cBackend, freeBackend := goStringToCPtr(cand)

		settings, err := callDylib(symSettingsCreate, []byte{i64, i64, i64, i64}, []byte{i64}, cModel, cBackend, 0, 0)
		freeBackend()
		if err != nil || settings == 0 {
			lastErr = fmt.Errorf("litert_lm_engine_settings_create failed for backend %s: %v", cand, err)
			continue
		}

		if supportsSpec && symSettingsSetEnableSpeculativeDecoding != 0 {
			_, _ = callDylib(symSettingsSetEnableSpeculativeDecoding, []byte{i64, i32}, nil, settings, 1)
		}
		if symSettingsSetUseRingbuffersLocalAttention != 0 {
			_, _ = callDylib(symSettingsSetUseRingbuffersLocalAttention, []byte{i64, i32}, nil, settings, 1)
		}
		if cand == "gpu" && symSettingsSetGpuDecodeStepsPerSync != 0 {
			_, _ = callDylib(symSettingsSetGpuDecodeStepsPerSync, []byte{i64, i32}, nil, settings, 8)
		}
		if cand == "cpu" && symSettingsSetNumThreads != 0 {
			_, _ = callDylib(symSettingsSetNumThreads, []byte{i64, i32}, nil, settings, uint64(runtime.NumCPU()))
		}

		engine, err := callDylib(symEngineCreate, []byte{i64}, []byte{i64}, settings)
		if symSettingsDelete != 0 {
			_, _ = callDylib(symSettingsDelete, []byte{i64}, nil, settings)
		}
		if err == nil && engine != 0 {
			return &LMEngine{ptr: engine}, nil
		}
		lastErr = fmt.Errorf("litert_lm_engine_create failed for backend %s: %v", cand, err)
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
	config, err := callDylib(symCfgCreate, []byte{i64}, []byte{i64}, e.ptr)
	if err != nil || config == 0 {
		return nil, fmt.Errorf("litert_lm_conversation_config_create failed: %v", err)
	}
	defer func() {
		_, _ = callDylib(symCfgDelete, []byte{i64}, nil, config)
	}()

	conv, err := callDylib(symConvCreate, []byte{i64, i64}, []byte{i64}, e.ptr, config)
	if err != nil || conv == 0 {
		return nil, fmt.Errorf("litert_lm_conversation_create failed: %v", err)
	}

	return &LMConversation{ptr: conv}, nil
}

func (c *LMConversation) Close() error {
	return nil
}

func (c *LMConversation) SendMessageStream(payloadJSON string, onToken func(string)) error {
	if err := ensureCallbackRegistered(); err != nil {
		return err
	}

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

	cMsg, freeMsg := goStringToCPtr(payloadJSON)
	defer freeMsg()

	rc, err := callDylib(symConvSendStream, []byte{i64, i64, i64, i64, i64, i64}, []byte{i32},
		c.ptr, cMsg, 0, 0, tokenTrampoline, cbID)
	if err != nil {
		return fmt.Errorf("litert_lm_conversation_send_message_stream failed: %w", err)
	}
	if int32(rc) != 0 {
		return fmt.Errorf("litert_lm_conversation_send_message_stream returned error: %d", int32(rc))
	}

	// Wait for stream completion from the background inference thread
	<-doneCh

	if symClearSinkLogger != 0 {
		_, _ = callDylib(symClearSinkLogger, nil, nil)
	}

	return nil
}
