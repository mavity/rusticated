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
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned by SendStream when the conversation was closed, or
// invalidated by cancellation, before or during the request.
var ErrClosed = errors.New("ai: conversation is closed")

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
	cfgSetEnableConstrainedDecoding(cfg uint64, enable bool) error
	cfgSetStreamToolCalls(cfg uint64, stream bool, channelName uint64)
	cfgSetMessages(cfg, messagesJSON uint64)

	// ── Conversation ─────────────────────────────────────────────────────
	convCreate(engine, cfg uint64) uint64
	convDelete(conv uint64)
	convSendStream(conv, msg, extraCtx, optArgs, callback, userData uint64) int32
	convCancelProcess(conv uint64)

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

	// ── Embedding Engine ─────────────────────────────────────────────────
	embeddingSettingsCreate(modelPath, backend, visionBackend, audioBackend uint64) uint64
	embeddingSettingsDelete(settings uint64)
	embeddingSettingsSetNumThreads(settings uint64, threads int32)
	embeddingEngineCreate(settings uint64) uint64
	embeddingEngineDelete(engine uint64)
	embeddingOptionsCreate() uint64
	embeddingOptionsDelete(options uint64)
	embeddingOptionsSetNormalize(options uint64, normalize bool)
	embeddingOptionsSetInputOverflowStrategy(options uint64, strategy int32)
	inputDataCreate(dataType int32, data []byte) uint64
	inputDataDelete(inputData uint64)
	embeddingEngineCompute(engine uint64, inputs []uint64, options uint64) ([]float32, error)

	// ── Callback trampoline creation ─────────────────────────────────────
	// The trampoline is created once per process; the request is identified
	// by the userData passed to convSendStream.
	createTrampoline() uint64
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
	// doneCh is 1-buffered: the terminal chunk delivers nil on clean
	// completion or the native error, without ever blocking the C callback.
	doneCh chan error
	// closeCh is closed to ask the SendStream goroutine to abandon the stream.
	closeCh  chan struct{}
	closeReq atomic.Bool
}

func newStreamState(onChunk func(string)) *streamState {
	return &streamState{
		onChunk:  onChunk,
		bodyDone: make(chan struct{}),
		doneCh:   make(chan error, 1),
		closeCh:  make(chan struct{}),
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
	if entered {
		if textPtr := drv.streamChunkGetText(chunkPtr); textPtr != 0 {
			if token := drv.ptrToString(textPtr); token != "" {
				st.onChunk(token)
			}
		}
	}

	isFinal := drv.streamChunkIsFinal(chunkPtr)
	var streamErr error
	if isFinal {
		if errPtr := drv.streamChunkGetError(chunkPtr); errPtr != 0 {
			// The chunk is only valid during this call, so copy the message now.
			msg := drv.ptrToString(errPtr)
			if msg == "" {
				msg = "native stream reported an error without a message"
			}
			streamErr = fmt.Errorf("LiteRT stream error: %s", msg)
		}
		if Verbose {
			fmt.Fprintf(os.Stderr, "[ai] stream final chunk (err: %v)\n", streamErr)
		}
	}

	if entered {
		st.endCallback()
	}

	if isFinal {
		select {
		case st.doneCh <- streamErr:
		default:
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

func (b *drvEngineBackend) NewConversation(cfg ConversationConfig) (conversationBackend, error) {
	systemJSON, toolsJSON, historyJSON, err := buildLiteRTConversationConfig(cfg)
	if err != nil {
		return nil, err
	}
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
	if err := b.drv.cfgSetEnableConstrainedDecoding(config, len(cfg.Tools) > 0); err != nil {
		return nil, fmt.Errorf("configure constrained decoding: %w", err)
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

	streamID := registerStreamState(st)

	cMsg, freeMsg := c.drv.stringToCPtr(messageJSON)
	defer freeMsg()

	if Verbose {
		fmt.Fprintf(os.Stderr, "[ai] convSendStream: calling\n")
	}
	rc := c.drv.convSendStream(c.conv, cMsg, 0, 0, c.drv.createTrampoline(), streamID)
	if Verbose {
		fmt.Fprintf(os.Stderr, "[ai] convSendStream: returned rc=%d\n", rc)
	}
	if rc != 0 {
		unregisterStreamState(streamID)
		c.active.Store(nil)
		c.releaseConv()
		return fmt.Errorf("send_message_stream failed to start: %d", rc)
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
		case <-ctx.Done():
			c.abort(st, streamID)
			return ctx.Err()
		case <-st.closeCh:
			c.abort(st, streamID)
			return ErrClosed
		case <-keepAlive.C:
		}
	}
}

// abort cancels the in-flight stream and invalidates the conversation. When it
// returns, onChunk is neither running nor will run again.
func (c *drvConversationBackend) abort(st *streamState, streamID uint64) {
	c.closed.Store(true)
	st.stop()
	c.drv.convCancelProcess(c.conv)

	// The terminal chunk is delivered on the native callback thread, which is
	// also what runs the conversation's destructor if it is deleted there, so
	// deletion has to be done from a goroutine of our own.
	go func() {
		<-st.doneCh
		unregisterStreamState(streamID)
		c.active.Store(nil)
		c.releaseConv()
	}()
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
	configureLMLogging(drv)

	cModel, freeModel := drv.stringToCPtr(modelPath)
	defer freeModel()

	cBackend, freeBackend := drv.stringToCPtr(backend)
	settings := drv.embeddingSettingsCreate(cModel, cBackend, 0, 0)
	freeBackend()
	if settings == 0 {
		return nil, fmt.Errorf("embedding_engine_settings_create returned NULL for backend %s", backend)
	}
	drv.embeddingSettingsSetNumThreads(settings, int32(runtime.NumCPU()))

	engine := drv.embeddingEngineCreate(settings)
	drv.embeddingSettingsDelete(settings)
	if engine == 0 {
		return nil, fmt.Errorf("embedding_engine_create returned NULL for backend %s", backend)
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

	opts := b.drv.embeddingOptionsCreate()
	if opts == 0 {
		return nil, fmt.Errorf("embedding_options_create returned NULL")
	}
	defer b.drv.embeddingOptionsDelete(opts)

	b.drv.embeddingOptionsSetNormalize(opts, true)
	b.drv.embeddingOptionsSetInputOverflowStrategy(opts, overflowStrategyTruncate)

	createdPtrs := make([]uint64, 0, len(inputs))
	defer func() {
		for _, ptr := range createdPtrs {
			b.drv.inputDataDelete(ptr)
		}
	}()

	for _, in := range inputs {
		ptr := b.drv.inputDataCreate(in.dataType, in.data)
		if ptr == 0 {
			return nil, fmt.Errorf("litert_lm_input_data_create failed")
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
