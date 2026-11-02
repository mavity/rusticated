// Package backend defines the platform-agnostic LiteRT-LM orchestration layer.
// It provides an LMDriver interface for low-level FFI operations and implements
// the high-level engine/conversation workflows on top of it.
// Platform files (backend_native.go, backend_wasip1.go) implement LMDriver.
package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned by SendStream when the conversation was closed, or
// invalidated by cancellation, before or during the request.
var ErrClosed = errors.New("ai: conversation is closed")

const (
	liteRtStatusOK        int32 = 0
	liteRtStatusNotFound  int32 = 5
	liteRtStatusCancelled int32 = 1

	liteRtLmLogSeverityVerbose int32 = 0
	liteRtLmLogSeveritySilent  int32 = 1000
)

var errLiteRtStatusNotFound = errors.New("LiteRT-LM status not found")

type liteRtStatusError struct {
	operation string
	code      int32
	message   string
}

func (e *liteRtStatusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("LiteRT-LM %s returned LiteRtLmStatusCode %d", e.operation, e.code)
	}
	return fmt.Sprintf("LiteRT-LM %s returned LiteRtLmStatusCode %d: %s", e.operation, e.code, e.message)
}

func (e *liteRtStatusError) Is(target error) bool {
	return target == errLiteRtStatusNotFound && e.code == liteRtStatusNotFound
}

func newLiteRtStatusError(operation string, code int32, message string) error {
	if code == liteRtStatusOK {
		return nil
	}
	return &liteRtStatusError{operation: operation, code: code, message: message}
}

func runtimeLibraryLoadError(libPath, hostOS string, err error) error {
	if hostOS == "linux" {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "glibc") || strings.Contains(message, "libc.so.6") {
			return fmt.Errorf(
				"failed to load %s: %w; LiteRT-LM %s Linux wheels require glibc 2.27 or newer and are not compatible with musl",
				libPath,
				err,
				"0.19.0",
			)
		}
	}
	return fmt.Errorf("failed to load %s: %w", libPath, err)
}

// Verbose enables verbose logging of library loading and other internals.
var Verbose bool

// ── Public interfaces (consumed by engine.go / ai.go) ───────────────────────

// engineBackend abstracts the engine implementation. Wire-format serialization
// belongs in the concrete backend, not in the conversation orchestration layer.
type engineBackend interface {
	NewConversation(cfg ConversationConfig) (conversationBackend, error)
	Close() error
}

// conversationBackend abstracts the execution of an active chat turn.
type conversationBackend interface {
	SendStream(ctx context.Context, parts []PromptPart, onEvent func(ModelEvent)) error
	Close() error
}

// embedderBackend abstracts the platform-specific execution of a LiteRT-LM EmbeddingEngine.
type embedderBackend interface {
	ComputeEmbedding(inputs []embedInput) ([]float32, error)
	Close() error
}

type embedInput struct {
	dataType int32 // 0 = TEXT, 1 = IMAGE
	data     []byte
}

// ModelInfo is declared in ai.go — this file provides only shared orchestration.

// ── LMDriver — minimal FFI abstraction each platform must implement ─────────

// LMDriver exposes every C API function used by the shared orchestration.
// Each platform (native purego / wasip1 callDylib) implements these methods.
// All C-level pointers and handles are represented as uint64.
type LMDriver interface {
	// ── Library loading ──────────────────────────────────────────────────
	ensureLoaded(libPath string) error

	// ── String marshaling ────────────────────────────────────────────────
	stringToCPtr(s string) (uint64, func(), error)
	ptrToString(ptr uint64) (string, error)

	// ── Engine settings ──────────────────────────────────────────────────
	settingsCreate(modelPath, backend, visionBackend, audioBackend uint64) (uint64, error)
	settingsDelete(settings uint64)
	speculativeDecodingConfigCreate() (uint64, error)
	speculativeDecodingConfigDelete(config uint64)
	speculativeDecodingConfigSetMode(config uint64, mode int32) error
	settingsSetSpeculativeDecodingConfig(settings, config uint64) error
	settingsSetUseRingbuffersLocalAttention(settings uint64, enable bool) error
	settingsSetGpuDecodeStepsPerSync(settings uint64, steps int32) error
	settingsSetNumThreads(settings uint64, threads int32) error
	settingsSetMaxNumTokens(settings uint64, maxTokens int32) error
	settingsSetLitertDispatchLibDir(settings, libDir uint64) error

	// ── Engine ───────────────────────────────────────────────────────────
	engineCreate(settings uint64) (uint64, error)
	engineDelete(engine uint64)
	setLogLevel(level int32) error

	// ── Conversation config ──────────────────────────────────────────────
	cfgCreate() (uint64, error)
	cfgDelete(cfg uint64)
	cfgSetTools(cfg, toolsJSON uint64) error
	cfgSetSystemMessage(cfg, systemMsg uint64) error
	cfgSetEnableConstrainedDecoding(cfg uint64, enable bool) error
	cfgSetStreamToolCalls(cfg uint64, stream bool, channelName uint64) error
	cfgSetMessages(cfg, messagesJSON uint64) error

	// ── Conversation ─────────────────────────────────────────────────────
	convCreate(engine, cfg uint64) (uint64, error)
	convDelete(conv uint64)
	convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) error
	convCancelProcess(conv uint64) error

	// ── Loaded file inspection ───────────────────────────────────────────
	loadedFileCreate(modelPath uint64) (uint64, error)
	loadedFileDelete(file uint64)
	loadedFileHasSpeculativeDecodingSupport(file uint64) (bool, error)
	loadedFileMaxContextTokens(file uint64) (uint32, error)
	loadedFileIsDynamicContext(file uint64) (bool, error)
	loadedFileSupportsThinking(file uint64) (bool, error)
	loadedFileSupportsFunctionCalling(file uint64) (bool, error)
	loadedFileModalitySupportedBackends(file uint64, modality int32) ([]uint32, error)

	// ── Stream chunk inspection ──────────────────────────────────────────
	streamChunkGetText(chunk uint64) (uint64, error)
	streamChunkIsFinal(chunk uint64) (bool, error)
	streamChunkGetError(chunk uint64) (uint64, error)

	// ── Embedding Engine ─────────────────────────────────────────────────
	embeddingSettingsCreate(modelPath, backend, visionBackend, audioBackend uint64) (uint64, error)
	embeddingSettingsDelete(settings uint64)
	embeddingSettingsSetNumThreads(settings uint64, threads int32) error
	embeddingEngineCreate(settings uint64) (uint64, error)
	embeddingEngineDelete(engine uint64)
	embeddingOptionsCreate() (uint64, error)
	embeddingOptionsDelete(options uint64)
	embeddingOptionsSetNormalize(options uint64, normalize bool) error
	embeddingOptionsSetInputOverflowStrategy(options uint64, strategy int32) error
	inputDataCreate(dataType int32, data []byte) (uint64, error)
	inputDataDelete(inputData uint64)
	embeddingEngineCompute(engine uint64, inputs []uint64, options uint64) ([]float32, error)

	// ── Callback trampoline creation ─────────────────────────────────────
	// The trampoline is created once per process; the request is identified
	// by the userData passed to convSendStream.
	createTrampoline() (uint64, error)
}

// ── Shared callback state ─────────────────────────────────────────────────

// Delivery states of a stream, driving streamState.deliver and stop.
const (
	deliveryIdle          int32 = iota
	deliveryActive              // onChunk is running on the native callback thread
	deliveryStopRequested       // stop arrived while onChunk was running
	deliveryStopped             // no further chunk reaches onChunk
)

// streamState is the state of one in-flight SendStream request. It is found by
// the native callback through its userData: a pointer, kept alive and unmoved
// by a runtime.Pinner for as long as native code may still call back.
type streamState struct {
	onChunk  func(string)
	delivery atomic.Int32
	// bodyDone is closed by the callback thread when it finishes the onChunk
	// that was running when stop was requested.
	bodyDone chan struct{}
	// doneCh is 1-buffered: the terminal callback delivers completion or an
	// error, without ever blocking the C callback.
	doneCh chan error
	// callbackErrCh reports a bridge failure before a terminal callback arrives.
	callbackErrCh chan error
	// closeCh is closed to ask the SendStream goroutine to abandon the stream.
	closeCh  chan struct{}
	closeReq atomic.Bool
}

func newStreamState(onChunk func(string)) *streamState {
	return &streamState{
		onChunk:       onChunk,
		bodyDone:      make(chan struct{}),
		doneCh:        make(chan error, 1),
		callbackErrCh: make(chan error, 1),
		closeCh:       make(chan struct{}),
	}
}

func (st *streamState) requestClose() {
	if st.closeReq.CompareAndSwap(false, true) {
		close(st.closeCh)
	}
}

// beginCallback enters the callback critical section unless the stream has
// already been stopped.
func (st *streamState) beginCallback() bool {
	return st.delivery.CompareAndSwap(deliveryIdle, deliveryActive)
}

// endCallback exits the callback critical section, closing bodyDone if stop()
// was requested while this callback was in progress.
func (st *streamState) endCallback() {
	if !st.delivery.CompareAndSwap(deliveryActive, deliveryIdle) {
		st.delivery.Store(deliveryStopped)
		close(st.bodyDone)
	}
}

// deliver runs onChunk on the calling (native callback) thread unless the
// stream has been stopped.
func (st *streamState) deliver(token string) {
	if st.delivery.Load() == deliveryActive {
		st.onChunk(token)
		return
	}
	if !st.beginCallback() {
		return
	}
	defer st.endCallback()
	st.onChunk(token)
}

// stop guarantees that sharedTokenCallback/onChunk is not running, and will
// not run again, once it returns. It waits for a callback that is already in progress.
func (st *streamState) stop() {
	for {
		switch st.delivery.Load() {
		case deliveryIdle:
			if st.delivery.CompareAndSwap(deliveryIdle, deliveryStopped) {
				return
			}
		case deliveryActive:
			if st.delivery.CompareAndSwap(deliveryActive, deliveryStopRequested) {
				<-st.bodyDone
				return
			}
		default:
			return
		}
	}
}

var (
	streamRegistryMu  sync.RWMutex
	streamRegistry    = make(map[uint64]*streamState)
	streamRegistrySeq uint64
)

func registerStreamState(st *streamState) uint64 {
	id := atomic.AddUint64(&streamRegistrySeq, 1)
	streamRegistryMu.Lock()
	streamRegistry[id] = st
	streamRegistryMu.Unlock()
	return id
}

func unregisterStreamState(id uint64) {
	streamRegistryMu.Lock()
	delete(streamRegistry, id)
	streamRegistryMu.Unlock()
}

func streamStateFromUserData(userData uint64) *streamState {
	streamRegistryMu.RLock()
	st := streamRegistry[userData]
	streamRegistryMu.RUnlock()
	return st
}

// sharedTokenCallback is invoked by the platform-specific trampoline when the
// C runtime emits a stream chunk. Text chunks go to the request's onChunk. The
// terminal chunk (is_final) carries no text, and also carries the error if the
// stream failed, so it is what completes the request. The request state must
// not be touched after the terminal chunk has been signalled.
func sharedTokenCallback(drv LMDriver, userData, chunkPtr uint64) {
	st := streamStateFromUserData(userData)
	if st == nil {
		return
	}

	entered := st.beginCallback()
	var callbackErr error
	if entered {
		textPtr, err := drv.streamChunkGetText(chunkPtr)
		if err != nil {
			callbackErr = errors.Join(callbackErr, fmt.Errorf("read LiteRT stream chunk text: %w", err))
		} else if textPtr != 0 {
			token, err := drv.ptrToString(textPtr)
			if err != nil {
				callbackErr = errors.Join(callbackErr, fmt.Errorf("read LiteRT stream chunk text: %w", err))
			} else if token != "" {
				st.onChunk(token)
			}
		}
	}

	isFinal, finalErr := drv.streamChunkIsFinal(chunkPtr)
	if finalErr != nil {
		callbackErr = errors.Join(callbackErr, fmt.Errorf("read LiteRT stream final flag: %w", finalErr))
	}
	var streamErr error
	if finalErr == nil && isFinal {
		errPtr, err := drv.streamChunkGetError(chunkPtr)
		if err != nil {
			streamErr = fmt.Errorf("read LiteRT stream error: %w", err)
		} else if errPtr != 0 {
			// The chunk is only valid during this call, so copy the message now.
			msg, err := drv.ptrToString(errPtr)
			if err != nil {
				streamErr = fmt.Errorf("read LiteRT stream error message: %w", err)
			} else if msg == "" {
				msg = "native stream reported an error without a message"
			}
			if err == nil {
				streamErr = fmt.Errorf("LiteRT stream error: %s", msg)
			}
		}
		callbackErr = errors.Join(callbackErr, streamErr)
	}

	if entered {
		st.endCallback()
	}

	if finalErr == nil && isFinal {
		if Verbose {
			fmt.Fprintf(os.Stderr, "[ai] stream final chunk (err: %v)\n", callbackErr)
		}
		select {
		case st.doneCh <- callbackErr:
		default:
		}
	} else if callbackErr != nil {
		select {
		case st.callbackErrCh <- callbackErr:
		default:
		}
	}
}

// ── Shared orchestration ──────────────────────────────────────────────────

func configureLMLogging(drv LMDriver) error {
	level := liteRtLmLogSeveritySilent
	if Verbose {
		level = liteRtLmLogSeverityVerbose
	}
	if err := drv.setLogLevel(level); err != nil {
		return fmt.Errorf("configure LiteRT-LM logging: %w", err)
	}
	return nil
}

func checkSpeculativeDecoding(drv LMDriver, modelPtr uint64) (bool, error) {
	file, err := drv.loadedFileCreate(modelPtr)
	if err != nil {
		return false, fmt.Errorf("load model metadata for speculative decoding: %w", err)
	}
	if file == 0 {
		return false, fmt.Errorf("loaded_file_create returned NULL without an error")
	}
	defer drv.loadedFileDelete(file)
	supported, err := drv.loadedFileHasSpeculativeDecodingSupport(file)
	if err != nil {
		return false, fmt.Errorf("inspect speculative-decoding support: %w", err)
	}
	return supported, nil
}

func sharedInspectModel(drv LMDriver, libPath, modelPath string) (*ModelInfo, error) {
	if err := drv.ensureLoaded(libPath); err != nil {
		return nil, err
	}
	if err := configureLMLogging(drv); err != nil {
		return nil, err
	}
	cModel, freeModel, err := drv.stringToCPtr(modelPath)
	if err != nil {
		return nil, fmt.Errorf("marshal model path for inspection: %w", err)
	}
	defer freeModel()

	file, err := drv.loadedFileCreate(cModel)
	if err != nil {
		return nil, fmt.Errorf("load model file %s: %w", modelPath, err)
	}
	if file == 0 {
		return nil, fmt.Errorf("loaded_file_create returned NULL for %s without an error", modelPath)
	}
	defer drv.loadedFileDelete(file)

	info := &ModelInfo{}
	maxTok, err := drv.loadedFileMaxContextTokens(file)
	if errors.Is(err, errLiteRtStatusNotFound) {
		maxTok = 0
	} else if err != nil {
		return nil, fmt.Errorf("read model context-token limit: %w", err)
	}
	if maxTok > 0 {
		info.MaxContextTokens = int(maxTok)
	}
	info.IsDynamicContext, err = drv.loadedFileIsDynamicContext(file)
	if err != nil {
		return nil, fmt.Errorf("read model dynamic-context capability: %w", err)
	}
	info.SupportsThinking, err = drv.loadedFileSupportsThinking(file)
	if err != nil {
		return nil, fmt.Errorf("read model thinking capability: %w", err)
	}
	info.SupportsFunctionCalling, err = drv.loadedFileSupportsFunctionCalling(file)
	if err != nil {
		return nil, fmt.Errorf("read model function-calling capability: %w", err)
	}
	backendIDs, err := drv.loadedFileModalitySupportedBackends(file, 0)
	if err != nil {
		return nil, fmt.Errorf("read model supported backends: %w", err)
	}
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
	// closed is set once, by Close or by cancellation, and is never cleared.
	closed atomic.Bool
	// deleted guards convDelete so it runs exactly once.
	deleted atomic.Bool
	// active is the stream the native runtime may still call back into.
	active atomic.Pointer[streamState]
}

type drvEngineBackend struct {
	drv    LMDriver
	engine uint64
}

func sharedOpenBackend(drv LMDriver, libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	if err := drv.ensureLoaded(libPath); err != nil {
		return nil, err
	}
	if err := configureLMLogging(drv); err != nil {
		return nil, err
	}

	cModel, freeModel, err := drv.stringToCPtr(modelPath)
	if err != nil {
		return nil, fmt.Errorf("marshal model path: %w", err)
	}
	defer freeModel()

	supportsSpec, err := checkSpeculativeDecoding(drv, cModel)
	if err != nil {
		return nil, err
	}

	cBackend, freeBackend, err := drv.stringToCPtr(backend)
	if err != nil {
		return nil, fmt.Errorf("marshal backend name: %w", err)
	}
	settings, err := drv.settingsCreate(cModel, cBackend, 0, 0)
	freeBackend()
	if err != nil {
		return nil, fmt.Errorf("create engine settings for backend %s: %w", backend, err)
	}
	if settings == 0 {
		return nil, fmt.Errorf("engine settings creation returned NULL for backend %s without an error", backend)
	}
	defer drv.settingsDelete(settings)
	if supportsSpec {
		specConfig, err := drv.speculativeDecodingConfigCreate()
		if err != nil {
			return nil, fmt.Errorf("create speculative-decoding config: %w", err)
		}
		if specConfig == 0 {
			return nil, fmt.Errorf("speculative-decoding config creation returned NULL without an error")
		}
		defer drv.speculativeDecodingConfigDelete(specConfig)
		if err := drv.speculativeDecodingConfigSetMode(specConfig, 1); err != nil {
			return nil, fmt.Errorf("enable speculative decoding: %w", err)
		}
		if err := drv.settingsSetSpeculativeDecodingConfig(settings, specConfig); err != nil {
			return nil, fmt.Errorf("configure speculative decoding: %w", err)
		}
	}
	if backend == "gpu_artisan" {
		if err := drv.settingsSetUseRingbuffersLocalAttention(settings, true); err != nil {
			return nil, fmt.Errorf("configure local-attention ring buffers: %w", err)
		}
	}
	if backend == "gpu" {
		if err := drv.settingsSetGpuDecodeStepsPerSync(settings, 8); err != nil {
			return nil, fmt.Errorf("configure GPU decode synchronization: %w", err)
		}
	}
	if backend == "npu" {
		normalizedLibPath := strings.ReplaceAll(libPath, `\`, "/")
		if strings.EqualFold(path.Ext(normalizedLibPath), ".dll") {
			dispatchDir := path.Join(path.Dir(normalizedLibPath), "vendors", "intel_openvino", "dispatch")
			cDispatch, freeDispatch, err := drv.stringToCPtr(dispatchDir)
			if err != nil {
				return nil, fmt.Errorf("marshal NPU dispatch directory: %w", err)
			}
			if err := drv.settingsSetLitertDispatchLibDir(settings, cDispatch); err != nil {
				freeDispatch()
				return nil, fmt.Errorf("configure NPU dispatch directory %s: %w", dispatchDir, err)
			}
			freeDispatch()
		}
	}
	if backend == "cpu" {
		if err := drv.settingsSetNumThreads(settings, int32(runtime.NumCPU())); err != nil {
			return nil, fmt.Errorf("configure engine thread count: %w", err)
		}
	}
	if maxTokens > 0 {
		if err := drv.settingsSetMaxNumTokens(settings, int32(maxTokens)); err != nil {
			return nil, fmt.Errorf("configure maximum token count: %w", err)
		}
	}

	engine, err := drv.engineCreate(settings)
	if err != nil {
		return nil, fmt.Errorf("create engine for backend %s: %w", backend, err)
	}
	if engine == 0 {
		return nil, fmt.Errorf("engine creation returned NULL for backend %s without an error", backend)
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

func (b *drvEngineBackend) NewConversation(cfg ConversationConfig) (conversationBackend, error) {
	systemJSON, toolsJSON, historyJSON, err := buildLiteRTConversationConfig(cfg)
	if err != nil {
		return nil, err
	}
	config, err := b.drv.cfgCreate()
	if err != nil {
		return nil, fmt.Errorf("create conversation config: %w", err)
	}
	if config == 0 {
		return nil, fmt.Errorf("conversation config creation returned NULL without an error")
	}
	defer b.drv.cfgDelete(config)

	if systemJSON != "" {
		cSys, freeSys, err := b.drv.stringToCPtr(systemJSON)
		if err != nil {
			return nil, fmt.Errorf("marshal system message: %w", err)
		}
		if err := b.drv.cfgSetSystemMessage(config, cSys); err != nil {
			freeSys()
			return nil, fmt.Errorf("configure system message: %w", err)
		}
		freeSys()
	}
	if toolsJSON != "" {
		cTools, freeTools, err := b.drv.stringToCPtr(toolsJSON)
		if err != nil {
			return nil, fmt.Errorf("marshal tools configuration: %w", err)
		}
		if err := b.drv.cfgSetTools(config, cTools); err != nil {
			freeTools()
			return nil, fmt.Errorf("configure tools: %w", err)
		}
		freeTools()
	}
	if err := b.drv.cfgSetEnableConstrainedDecoding(config, len(cfg.Tools) > 0); err != nil {
		return nil, fmt.Errorf("configure constrained decoding: %w", err)
	}
	{
		cChan, freeChan, err := b.drv.stringToCPtr("tool_call")
		if err != nil {
			return nil, fmt.Errorf("marshal tool-call channel: %w", err)
		}
		if err := b.drv.cfgSetStreamToolCalls(config, true, cChan); err != nil {
			freeChan()
			return nil, fmt.Errorf("configure tool-call streaming: %w", err)
		}
		freeChan()
	}
	if historyJSON != "" {
		cMsgs, freeMsgs, err := b.drv.stringToCPtr(historyJSON)
		if err != nil {
			return nil, fmt.Errorf("marshal conversation history: %w", err)
		}
		if err := b.drv.cfgSetMessages(config, cMsgs); err != nil {
			freeMsgs()
			return nil, fmt.Errorf("configure conversation history: %w", err)
		}
		freeMsgs()
	}

	conv, err := b.drv.convCreate(b.engine, config)
	if err != nil {
		return nil, fmt.Errorf("create conversation: %w", err)
	}
	if conv == 0 {
		return nil, fmt.Errorf("conversation creation returned NULL without an error")
	}
	return &drvConversationBackend{drv: b.drv, conv: conv}, nil
}

// Close invalidates the conversation. Without a stream in flight it releases
// the native conversation at once; with one, the stream's SendStream is asked
// to abandon it, and the native conversation is released once the runtime has
// delivered the stream's terminal chunk. Close never waits.
func (c *drvConversationBackend) Close() error {
	c.closed.Store(true)
	if st := c.active.Load(); st != nil {
		st.requestClose()
	}
	c.releaseConv()
	return nil
}

// releaseConv deletes the native conversation once it is both closed and no
// longer referenced by a stream. It is called after each of those two events,
// so whichever happens last performs the deletion, exactly once.
func (c *drvConversationBackend) releaseConv() {
	if c.closed.Load() && c.active.Load() == nil && c.deleted.CompareAndSwap(false, true) {
		c.drv.convDelete(c.conv)
	}
}

var errStreamActive = errors.New("ai: conversation already has a stream in flight")

// SendStream sends one message and delivers the response through onChunk,
// directly on the native callback thread, until the terminal chunk arrives.
//
// If ctx is cancelled or the conversation is closed in the meantime, the
// native runtime is asked to cancel (litert_lm_conversation_cancel_process),
// the conversation is invalidated for good (LiteRT-LM leaves a cancelled
// conversation unusable), and SendStream returns at once with ctx.Err() or
// ErrClosed. The native runtime keeps unwinding on its own threads; a
// goroutine waits for its terminal chunk, then deletes the conversation.
// Chunks arriving after the return never reach onChunk.
func (c *drvConversationBackend) SendStream(ctx context.Context, parts []PromptPart, onEvent func(ModelEvent)) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	messageJSON, err := buildMessageJSON(parts)
	if err != nil {
		return err
	}
	cMsg, freeMsg, err := c.drv.stringToCPtr(messageJSON)
	if err != nil {
		return fmt.Errorf("marshal stream message: %w", err)
	}
	defer freeMsg()

	onChunk := func(raw string) {
		for _, event := range parseChunkEvents(raw) {
			onEvent(event)
		}
	}
	st := newStreamState(onChunk)
	if !c.active.CompareAndSwap(nil, st) {
		return errStreamActive
	}
	// Close either saw the stream just registered (and will signal it) or
	// ran first, in which case it is seen here.
	if c.closed.Load() {
		c.active.Store(nil)
		c.releaseConv()
		return ErrClosed
	}

	callback, err := c.drv.createTrampoline()
	if err != nil {
		c.active.Store(nil)
		c.releaseConv()
		return fmt.Errorf("register LiteRT stream callback: %w", err)
	}
	if callback == 0 {
		c.active.Store(nil)
		c.releaseConv()
		return fmt.Errorf("register LiteRT stream callback: returned NULL")
	}

	streamID := registerStreamState(st)

	if Verbose {
		fmt.Fprintf(os.Stderr, "[ai] convSendStream: calling\n")
	}
	sendErr := c.drv.convSendStream(c.conv, cMsg, 0, 0, callback, streamID)
	if Verbose {
		fmt.Fprintf(os.Stderr, "[ai] convSendStream: returned err=%v\n", sendErr)
	}
	if sendErr != nil {
		unregisterStreamState(streamID)
		c.active.Store(nil)
		c.releaseConv()
		return fmt.Errorf("send_message_stream failed to start: %w", sendErr)
	}

	// Inference runs on native threads the Go scheduler knows nothing about. On
	// a platform without cgo the runtime only learns of a native callback
	// thread once the first callback arrives, so until then a goroutine parked
	// here with no timer registered is reported as "all goroutines are asleep".
	// This ticker keeps a timer pending; it is not a stream timeout.
	keepAlive := time.NewTicker(time.Minute)
	defer keepAlive.Stop()
	for {
		select {
		case err := <-st.doneCh:
			unregisterStreamState(streamID)
			c.active.Store(nil)
			c.releaseConv()
			if err != nil && c.closed.Load() {
				return ErrClosed
			}
			return err
		case err := <-st.callbackErrCh:
			return errors.Join(err, c.abort(st, streamID))
		case <-ctx.Done():
			return errors.Join(ctx.Err(), c.abort(st, streamID))
		case <-st.closeCh:
			return errors.Join(ErrClosed, c.abort(st, streamID))
		case <-keepAlive.C:
		}
	}
}

// abort cancels the in-flight stream and invalidates the conversation. When it
// returns, onChunk is neither running nor will run again.
func (c *drvConversationBackend) abort(st *streamState, streamID uint64) error {
	c.closed.Store(true)
	st.stop()
	cancelErr := c.drv.convCancelProcess(c.conv)

	// The terminal chunk is delivered on the native callback thread, which is
	// also what runs the conversation's destructor if it is deleted there, so
	// deletion has to be done from a goroutine of our own.
	go func() {
		<-st.doneCh
		unregisterStreamState(streamID)
		c.active.Store(nil)
		c.releaseConv()
	}()
	if cancelErr != nil {
		return fmt.Errorf("cancel LiteRT-LM stream: %w", cancelErr)
	}
	return nil
}

// ── Shared Embedder backend ────────────────────────────────────────────────

const (
	inputDataTypeText  int32 = 0
	inputDataTypeImage int32 = 1

	overflowStrategyTruncate int32 = 1
)

type drvEmbedderBackend struct {
	drv    LMDriver
	engine uint64
}

func sharedOpenEmbedder(drv LMDriver, libPath, modelPath, backend string) (embedderBackend, error) {
	if err := drv.ensureLoaded(libPath); err != nil {
		return nil, err
	}
	if err := configureLMLogging(drv); err != nil {
		return nil, err
	}

	cModel, freeModel, err := drv.stringToCPtr(modelPath)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding model path: %w", err)
	}
	defer freeModel()

	cBackend, freeBackend, err := drv.stringToCPtr(backend)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding backend name: %w", err)
	}
	settings, err := drv.embeddingSettingsCreate(cModel, cBackend, 0, 0)
	freeBackend()
	if err != nil {
		return nil, fmt.Errorf("create embedding engine settings for backend %s: %w", backend, err)
	}
	if settings == 0 {
		return nil, fmt.Errorf("embedding engine settings creation returned NULL for backend %s without an error", backend)
	}
	defer drv.embeddingSettingsDelete(settings)
	if err := drv.embeddingSettingsSetNumThreads(settings, int32(runtime.NumCPU())); err != nil {
		return nil, fmt.Errorf("configure embedding engine thread count: %w", err)
	}

	engine, err := drv.embeddingEngineCreate(settings)
	if err != nil {
		return nil, fmt.Errorf("create embedding engine: %w", err)
	}
	if engine == 0 {
		return nil, fmt.Errorf("embedding engine creation returned NULL for backend %s without an error", backend)
	}
	return &drvEmbedderBackend{drv: drv, engine: engine}, nil
}

func (b *drvEmbedderBackend) ComputeEmbedding(inputs []embedInput) ([]float32, error) {
	if b == nil || b.engine == 0 {
		return nil, ErrClosed
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("ai: cannot compute embedding for empty input")
	}

	opts, err := b.drv.embeddingOptionsCreate()
	if err != nil {
		return nil, fmt.Errorf("create embedding options: %w", err)
	}
	if opts == 0 {
		return nil, fmt.Errorf("embedding options creation returned NULL without an error")
	}
	defer b.drv.embeddingOptionsDelete(opts)

	if err := b.drv.embeddingOptionsSetNormalize(opts, true); err != nil {
		return nil, fmt.Errorf("configure embedding normalization: %w", err)
	}
	if err := b.drv.embeddingOptionsSetInputOverflowStrategy(opts, overflowStrategyTruncate); err != nil {
		return nil, fmt.Errorf("configure embedding input overflow strategy: %w", err)
	}

	createdPtrs := make([]uint64, 0, len(inputs))
	defer func() {
		for _, ptr := range createdPtrs {
			b.drv.inputDataDelete(ptr)
		}
	}()

	for _, in := range inputs {
		ptr, err := b.drv.inputDataCreate(in.dataType, in.data)
		if err != nil {
			return nil, fmt.Errorf("create embedding input data: %w", err)
		}
		if ptr == 0 {
			return nil, fmt.Errorf("input data creation returned NULL without an error")
		}
		createdPtrs = append(createdPtrs, ptr)
	}

	return b.drv.embeddingEngineCompute(b.engine, createdPtrs, opts)
}

func (b *drvEmbedderBackend) Close() error {
	if b != nil && b.engine != 0 {
		b.drv.embeddingEngineDelete(b.engine)
		b.engine = 0
	}
	return nil
}
