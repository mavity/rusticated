// Package backend defines the platform-agnostic LiteRT-LM orchestration layer.
// It provides an LMDriver interface for low-level FFI operations and implements
// the high-level engine/conversation workflows on top of it.
// Platform files (backend_native.go, backend_wasip1.go) implement LMDriver.
package ai

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
)

// Verbose enables verbose logging of library loading and other internals.
var Verbose bool

// ── Public interfaces (consumed by engine.go / ai.go) ───────────────────────

// engineBackend abstracts the platform-specific dynamic library execution for
// a LiteRT-LM engine. All cross-boundary payloads are passed as plain Go JSON strings.
type engineBackend interface {
	NewConversation(systemJSON, toolsJSON, historyJSON string) (conversationBackend, error)
	Close() error
}

// conversationBackend abstracts the platform-specific execution of an active chat turn.
type conversationBackend interface {
	SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error
	Close() error
}

// ModelInfo is declared in ai.go — this file provides only shared orchestration.

// ── LMDriver — minimal FFI abstraction each platform must implement ─────────

// LMDriver exposes every C API function used by the shared orchestration.
// Each platform (native purego / wasip1 callDylib) implements these methods.
// All C-level pointers and handles are represented as uint64.
type LMDriver interface {
	// ── Library loading ──────────────────────────────────────────────────
	ensureLoaded(libPath string) error

	// ── Symbol / global variable access (FLAGS_* etc.) ───────────────────
	getSymbol(name string) (uint64, error)
	writeInt32(addr uint64, val int32)
	writeBool(addr uint64, val bool)

	// ── String marshaling ────────────────────────────────────────────────
	stringToCPtr(s string) (uint64, func())
	ptrToString(ptr uint64) string

	// ── Engine settings ──────────────────────────────────────────────────
	settingsCreate(modelPath, backend, visionBackend, audioBackend uint64) uint64
	settingsDelete(settings uint64)
	settingsSetEnableSpeculativeDecoding(settings uint64, enable bool)
	settingsSetUseRingbuffersLocalAttention(settings uint64, enable bool)
	settingsSetGpuDecodeStepsPerSync(settings uint64, steps int32)
	settingsSetNumThreads(settings uint64, threads int32)
	settingsSetMaxNumTokens(settings uint64, maxTokens int32)

	// ── Engine ───────────────────────────────────────────────────────────
	engineCreate(settings uint64) uint64
	engineDelete(engine uint64)
	setLogLevel(level uint64)

	// ── Conversation config ──────────────────────────────────────────────
	cfgCreate(engine uint64) uint64
	cfgDelete(cfg uint64)
	cfgSetTools(cfg, toolsJSON uint64)
	cfgSetSystemMessage(cfg, systemMsg uint64)
	cfgSetStreamToolCalls(cfg uint64, stream bool, channelName uint64)
	cfgSetMessages(cfg, messagesJSON uint64)

	// ── Conversation ─────────────────────────────────────────────────────
	convCreate(engine, cfg uint64) uint64
	convDelete(conv uint64)
	convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) int32

	// ── Loaded file inspection ───────────────────────────────────────────
	loadedFileCreate(modelPath uint64) uint64
	loadedFileDelete(file uint64)
	loadedFileHasSpeculativeDecodingSupport(file uint64) bool
	loadedFileMaxContextTokens(file uint64) uint32
	loadedFileIsDynamicContext(file uint64) bool
	loadedFileSupportsThinking(file uint64) bool
	loadedFileSupportsFunctionCalling(file uint64) bool
	loadedFileModalitySupportedBackends(file uint64, modality int32) []uint32

	// ── Stream chunk inspection ──────────────────────────────────────────
	streamChunkGetText(chunk uint64) uint64
	streamChunkIsFinal(chunk uint64) bool
	streamChunkGetError(chunk uint64) uint64

	// ── Callback trampoline creation ─────────────────────────────────────
	createTrampoline(callbackID uint64) uint64
}

// ── Shared callback state ─────────────────────────────────────────────────

var (
	tokenCallbackMu       sync.Mutex
	tokenCallbackRegistry = make(map[uint64]func(string))
	tokenDoneChannels     = make(map[uint64]chan struct{})
	tokenCallbackNextID   uint64
)

// sharedTokenCallback is invoked by the platform-specific trampoline when the
// C runtime emits a stream chunk.  It extracts text and completion status via
// the driver and dispatches to the registered Go callback and done-channel.
func sharedTokenCallback(drv LMDriver, userData, chunkPtr uint64) {
	if errPtr := drv.streamChunkGetError(chunkPtr); errPtr != 0 {
		if s := drv.ptrToString(errPtr); s != "" {
			fmt.Fprintf(os.Stderr, "LiteRT stream error: %s\n", s)
		}
	}

	if textPtr := drv.streamChunkGetText(chunkPtr); textPtr != 0 {
		token := drv.ptrToString(textPtr)
		tokenCallbackMu.Lock()
		fn := tokenCallbackRegistry[userData]
		tokenCallbackMu.Unlock()
		if fn != nil {
			fn(token)
		}
	}

	if drv.streamChunkIsFinal(chunkPtr) {
		tokenCallbackMu.Lock()
		ch := tokenDoneChannels[userData]
		tokenCallbackMu.Unlock()
		if Verbose {
			fmt.Fprintf(os.Stderr, "[ai] stream final chunk for cb %d\n", userData)
		}
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// ── Shared orchestration ──────────────────────────────────────────────────

func configureLMLogging(drv LMDriver) {
	if addr, err := drv.getSymbol("FLAGS_minloglevel"); err == nil && addr != 0 {
		drv.writeInt32(addr, 2)
	}
	if addr, err := drv.getSymbol("FLAGS_logtostderr"); err == nil && addr != 0 {
		drv.writeBool(addr, false)
	}
	if addr, err := drv.getSymbol("FLAGS_alsologtostderr"); err == nil && addr != 0 {
		drv.writeBool(addr, false)
	}
	drv.setLogLevel(10)
}

func checkSpeculativeDecoding(drv LMDriver, modelPtr uint64) bool {
	file := drv.loadedFileCreate(modelPtr)
	if file == 0 {
		return false
	}
	defer drv.loadedFileDelete(file)
	return drv.loadedFileHasSpeculativeDecodingSupport(file)
}

func sharedInspectModel(drv LMDriver, libPath, modelPath string) (*ModelInfo, error) {
	if err := drv.ensureLoaded(libPath); err != nil {
		return nil, err
	}
	cModel, freeModel := drv.stringToCPtr(modelPath)
	defer freeModel()

	file := drv.loadedFileCreate(cModel)
	if file == 0 {
		return nil, fmt.Errorf("failed to inspect model file %s", modelPath)
	}
	defer drv.loadedFileDelete(file)

	info := &ModelInfo{}
	if maxTok := drv.loadedFileMaxContextTokens(file); maxTok > 0 {
		info.MaxContextTokens = int(maxTok)
	}
	info.IsDynamicContext = drv.loadedFileIsDynamicContext(file)
	info.SupportsThinking = drv.loadedFileSupportsThinking(file)
	info.SupportsFunctionCalling = drv.loadedFileSupportsFunctionCalling(file)
	backendIDs := drv.loadedFileModalitySupportedBackends(file, 0)
	for _, id := range backendIDs {
		switch id {
		case 1:
			info.SupportedBackends = append(info.SupportedBackends, "cpu")
		case 2:
			info.SupportedBackends = append(info.SupportedBackends, "gpu")
		case 3:
			info.SupportedBackends = append(info.SupportedBackends, "npu")
		}
	}
	return info, nil
}

// ── Shared backend structs ─────────────────────────────────────────────────

type drvConversationBackend struct {
	drv  LMDriver
	conv uint64
}

type drvEngineBackend struct {
	drv    LMDriver
	engine uint64
}

func sharedOpenBackend(drv LMDriver, libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	if err := drv.ensureLoaded(libPath); err != nil {
		return nil, err
	}
	configureLMLogging(drv)

	cModel, freeModel := drv.stringToCPtr(modelPath)
	defer freeModel()

	supportsSpec := checkSpeculativeDecoding(drv, cModel)

	cBackend, freeBackend := drv.stringToCPtr(backend)
	settings := drv.settingsCreate(cModel, cBackend, cBackend, 0)
	freeBackend()
	if settings == 0 {
		return nil, fmt.Errorf("settings_create returned NULL for backend %s", backend)
	}
	if supportsSpec {
		drv.settingsSetEnableSpeculativeDecoding(settings, true)
	}
	drv.settingsSetUseRingbuffersLocalAttention(settings, true)
	if backend == "gpu" {
		drv.settingsSetGpuDecodeStepsPerSync(settings, 8)
	}
	drv.settingsSetNumThreads(settings, int32(runtime.NumCPU()))
	if maxTokens > 0 {
		drv.settingsSetMaxNumTokens(settings, int32(maxTokens))
	}

	engine := drv.engineCreate(settings)
	drv.settingsDelete(settings)
	if engine == 0 {
		return nil, fmt.Errorf("engine_create returned NULL for backend %s", backend)
	}
	return &drvEngineBackend{drv: drv, engine: engine}, nil
}

func (b *drvEngineBackend) Close() error {
	if b.engine != 0 {
		b.drv.engineDelete(b.engine)
		b.engine = 0
	}
	return nil
}

func (b *drvEngineBackend) NewConversation(systemJSON, toolsJSON, historyJSON string) (conversationBackend, error) {
	config := b.drv.cfgCreate(b.engine)
	if config == 0 {
		return nil, fmt.Errorf("conversation_config_create returned NULL")
	}
	defer b.drv.cfgDelete(config)

	if systemJSON != "" {
		cSys, freeSys := b.drv.stringToCPtr(systemJSON)
		b.drv.cfgSetSystemMessage(config, cSys)
		freeSys()
	}
	if toolsJSON != "" {
		cTools, freeTools := b.drv.stringToCPtr(toolsJSON)
		b.drv.cfgSetTools(config, cTools)
		freeTools()
	}
	{
		cChan, freeChan := b.drv.stringToCPtr("tool_call")
		b.drv.cfgSetStreamToolCalls(config, true, cChan)
		freeChan()
	}
	if historyJSON != "" {
		cMsgs, freeMsgs := b.drv.stringToCPtr(historyJSON)
		b.drv.cfgSetMessages(config, cMsgs)
		freeMsgs()
	}

	conv := b.drv.convCreate(b.engine, config)
	if conv == 0 {
		return nil, fmt.Errorf("conversation_create returned NULL")
	}
	return &drvConversationBackend{drv: b.drv, conv: conv}, nil
}

func (c *drvConversationBackend) Close() error {
	if c.conv != 0 {
		c.drv.convDelete(c.conv)
		c.conv = 0
	}
	return nil
}

// diagTokenIdleTimeout is a TEMPORARY diagnostic: if no stream activity arrives
// for this long, SendStream fails instead of hanging. It also keeps a pending
// timer alive so Go's deadlock detector does not fire while native work runs.
const diagTokenIdleTimeout = time.Minute

func (c *drvConversationBackend) SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error {
	actCh := make(chan struct{}, 1)
	doneCh := make(chan struct{}, 1)
	notifyActivity := func(token string) {
		onChunk(token)
		select {
		case actCh <- struct{}{}:
		default:
		}
	}
	tokenCallbackMu.Lock()
	tokenCallbackNextID++
	cbID := tokenCallbackNextID
	tokenCallbackRegistry[cbID] = notifyActivity
	tokenDoneChannels[cbID] = doneCh
	tokenCallbackMu.Unlock()

	defer func() {
		tokenCallbackMu.Lock()
		delete(tokenCallbackRegistry, cbID)
		delete(tokenDoneChannels, cbID)
		tokenCallbackMu.Unlock()
	}()

	trampoline := c.drv.createTrampoline(cbID)

	cMsg, freeMsg := c.drv.stringToCPtr(messageJSON)
	defer freeMsg()

	if Verbose {
		fmt.Fprintf(os.Stderr, "[ai] convSendStream cb %d: calling\n", cbID)
	}
	rc := c.drv.convSendStream(c.conv, cMsg, 0, 0, trampoline, cbID)
	if Verbose {
		fmt.Fprintf(os.Stderr, "[ai] convSendStream cb %d: returned rc=%d\n", cbID, rc)
	}
	if rc != 0 {
		return fmt.Errorf("send_message_stream failed: %d", rc)
	}

	idle := time.NewTimer(diagTokenIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case <-doneCh:
			return nil
		case <-actCh:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(diagTokenIdleTimeout)
		case <-idle.C:
			return fmt.Errorf("[diag] no stream completion for cb %d within %v (native callback never reported is_final)", cbID, diagTokenIdleTimeout)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
