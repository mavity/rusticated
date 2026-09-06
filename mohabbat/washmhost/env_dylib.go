package main

import (
	"context"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/tetratelabs/wazero/api"
)

type FrameState int

const (
	FrameStatePending FrameState = iota
	FrameStateProjected
	FrameStateGuestAsync
	FrameStateCompleted
	FrameStateReturned
)

type CallbackFrame struct {
	cbID     uint64
	invID    uint64
	threadID uint64
	state    FrameState
	args     []uint64
	results  []uint64
	reg      *HostCallbackReg
}

type HostCallbackReg struct {
	cbID               uint64
	cbOvPtr            uint32
	cbOvLen            uint32
	sigBytes           []byte
	params             []byte
	results            []byte
	trampoline         uint64
	activeInvID        uint64
	pendingInvocations []*CallbackFrame
}

const callbackOverlappedHeaderSize = 48

func minCallbackOverlappedSize(paramCount, resultCount int) uint32 {
	return callbackOverlappedHeaderSize + uint32(paramCount+resultCount)*8
}

func writeCallbackInvocationCell(buf []byte, cbID, invID uint64, args []uint64, resultCount int) bool {
	need := minCallbackOverlappedSize(len(args), resultCount)
	if uint32(len(buf)) < need {
		return false
	}
	binary.LittleEndian.PutUint32(buf[0:4], 1)
	binary.LittleEndian.PutUint64(buf[24:32], cbID)
	binary.LittleEndian.PutUint64(buf[32:40], invID)
	binary.LittleEndian.PutUint32(buf[40:44], uint32(len(args)*8))
	binary.LittleEndian.PutUint32(buf[44:48], uint32(resultCount*8))
	for i, arg := range args {
		off := callbackOverlappedHeaderSize + i*8
		binary.LittleEndian.PutUint64(buf[off:off+8], arg)
	}
	return true
}

func readCallbackCompletionCell(buf []byte, expectedResultCount int) (uint64, []uint64, bool) {
	if len(buf) < callbackOverlappedHeaderSize {
		return 0, nil, false
	}
	invID := binary.LittleEndian.Uint64(buf[32:40])
	argLen := int(binary.LittleEndian.Uint32(buf[40:44]))
	resultLen := int(binary.LittleEndian.Uint32(buf[44:48]))
	resultOff := callbackOverlappedHeaderSize + argLen
	if argLen < 0 || resultLen < 0 || resultOff < callbackOverlappedHeaderSize || resultOff > len(buf) {
		return 0, nil, false
	}
	availableResults := resultLen / 8
	if expectedResultCount > availableResults {
		expectedResultCount = availableResults
	}
	if expectedResultCount < 0 {
		expectedResultCount = 0
	}
	end := resultOff + expectedResultCount*8
	if end > len(buf) {
		return 0, nil, false
	}
	results := make([]uint64, expectedResultCount)
	for i := 0; i < expectedResultCount; i++ {
		off := resultOff + i*8
		results[i] = binary.LittleEndian.Uint64(buf[off : off+8])
	}
	return invID, results, true
}

type dylibPendingOp struct {
	reqID      uint64
	ovPtr      uint32
	handle     uint64
	kind       dylibPendingOpKind
	manager    *DylibHostManager
	target     dylibTargetKey
	resultExt  uint64
	bufPtr     uint32
	bufLen     uint32
	resultsPtr uint32
	resultsCap uint32
}

type dylibPendingOpKind int

const (
	dylibPendingOpGeneric dylibPendingOpKind = iota
	dylibPendingOpOpenLibrary
	dylibPendingOpResolveSymbol
)

type DylibHostManager struct {
	mu                  sync.Mutex
	hEnv                *HostEnv
	target              dylibTargetKey
	cmd                 *exec.Cmd
	satEnc              *gob.Encoder
	satDec              *gob.Decoder
	satCloser           io.Closer
	started             bool
	closed              bool
	nextReqID           uint64
	nextCbID            uint64
	pendingOps          map[uint64]*dylibPendingOp
	callbacks           map[uint64]*HostCallbackReg
	callbacksByOv       map[uint32]*HostCallbackReg
	callbackList        []*HostCallbackReg
	threadStacks        map[uint64][]*CallbackFrame
	allFrames           map[uint64]*CallbackFrame
	incomingInvocations chan *DylibCbInvokeEvent
}

func NewDylibHostManager(hEnv *HostEnv) *DylibHostManager {
	return &DylibHostManager{
		hEnv:                hEnv,
		pendingOps:          make(map[uint64]*dylibPendingOp),
		callbacks:           make(map[uint64]*HostCallbackReg),
		callbacksByOv:       make(map[uint32]*HostCallbackReg),
		threadStacks:        make(map[uint64][]*CallbackFrame),
		allFrames:           make(map[uint64]*CallbackFrame),
		incomingInvocations: make(chan *DylibCbInvokeEvent, 128),
	}
}

// SetSatelliteStream allows tests to provide an in-process satellite communication pipe.
func (m *DylibHostManager) SetSatelliteStream(r io.Reader, w io.Writer, closer io.Closer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.satEnc = gob.NewEncoder(w)
	m.satDec = gob.NewDecoder(r)
	m.satCloser = closer
	m.started = true
	if m.hEnv != nil && m.hEnv.dylibRegistry != nil {
		m.hEnv.dylibRegistry.SetReadyManager(dylibTargetKey{goos: runtime.GOOS, goarch: runtime.GOARCH}, m)
	}
	go m.readSatelliteLoop()
}

func (r *DylibSatelliteRegistry) managerForTarget(target dylibTargetKey) (*DylibHostManager, error) {
	for {
		r.mu.Lock()
		entry, ok := r.entries[target]
		if !ok {
			entry = &satelliteEntry{
				target:  target,
				manager: NewDylibHostManager(r.hEnv),
				state:   satelliteEntryStarting,
				readyCh: make(chan struct{}),
			}
			entry.manager.target = target
			r.entries[target] = entry
			go r.startEntry(entry)
		}
		if entry.state == satelliteEntryReady && entry.manager != nil {
			manager := entry.manager
			r.mu.Unlock()
			return manager, nil
		}
		if entry.state == satelliteEntryFailed {
			delete(r.entries, target)
			r.mu.Unlock()
			continue
		}
		readyCh := entry.readyCh
		r.mu.Unlock()

		<-readyCh

		r.mu.Lock()
		state := entry.state
		manager := entry.manager
		startErr := entry.startErr
		if state == satelliteEntryFailed {
			if current, ok := r.entries[target]; ok && current == entry {
				delete(r.entries, target)
			}
			r.mu.Unlock()
			if startErr == nil {
				startErr = fmt.Errorf("satellite start failed for %s/%s", target.goos, target.goarch)
			}
			return nil, startErr
		}
		r.mu.Unlock()
		if state == satelliteEntryReady && manager != nil {
			return manager, nil
		}
	}
}

func (r *DylibSatelliteRegistry) startEntry(entry *satelliteEntry) {
	err := entry.manager.startSatelliteForTarget(entry.target.goos, entry.target.goarch)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		entry.startErr = err
		entry.state = satelliteEntryFailed
		close(entry.readyCh)
		return
	}
	entry.state = satelliteEntryReady
	close(entry.readyCh)
	go entry.manager.readSatelliteLoop()
}

func (r *DylibSatelliteRegistry) EvictManager(manager *DylibHostManager) {
	if manager == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[manager.target]
	if ok && entry != nil && entry.manager == manager {
		delete(r.entries, manager.target)
	}
}

func (h *HostEnv) resolveDylibTarget(path string) dylibTargetKey {
	tgt, err := detectDylibTarget(path)
	if err != nil {
		return dylibTargetKey{goos: runtime.GOOS, goarch: runtime.GOARCH}
	}
	return dylibTargetKey{goos: tgt.goos, goarch: tgt.goarch}
}

func (m *DylibHostManager) EnsureSatellite() error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	tgtGOOS, tgtGOARCH := m.target.goos, m.target.goarch
	if tgtGOOS == "" {
		tgtGOOS, tgtGOARCH = runtime.GOOS, runtime.GOARCH
	}
	if err := m.startSatelliteForTarget(tgtGOOS, tgtGOARCH); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	go m.readSatelliteLoop()
	return nil
}

func formatPlatformTarget(goos, goarch string) string {
	return goos + "-" + goarch
}

func (m *DylibHostManager) startSatelliteForTarget(goos, goarch string) error {
	outPath, err := buildWashmhostForPlatform(goos, goarch)
	if err != nil {
		return err
	}
	cmd := exec.Command(outPath, "--dylib-satellite")
	inPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("satellite stdin pipe: %w", err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = inPipe.Close()
		return fmt.Errorf("satellite stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = inPipe.Close()
		_ = outPipe.Close()
		return fmt.Errorf("satellite process start: %w", err)
	}

	var hs IPCEnvelope
	dec := gob.NewDecoder(outPipe)
	if err := dec.Decode(&hs); err != nil {
		_ = cmd.Process.Kill()
		_ = inPipe.Close()
		_ = outPipe.Close()
		return fmt.Errorf("satellite handshake failed: %w", err)
	}
	if hs.Handshake == nil || !hs.Handshake.IsHandshake {
		_ = cmd.Process.Kill()
		_ = inPipe.Close()
		_ = outPipe.Close()
		return fmt.Errorf("satellite protocol error: expected handshake")
	}
	if hs.Handshake.HGOOS != goos || hs.Handshake.HGOARCH != goarch {
		_ = cmd.Process.Kill()
		_ = inPipe.Close()
		_ = outPipe.Close()
		return fmt.Errorf("satellite arch mismatch: need %s/%s, got %s/%s", goos, goarch, hs.Handshake.HGOOS, hs.Handshake.HGOARCH)
	}

	m.cmd = cmd
	m.satEnc = gob.NewEncoder(inPipe)
	m.satDec = dec
	m.satCloser = inPipe
	m.started = true
	return nil
}

func (m *DylibHostManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	if m.satCloser != nil {
		_ = m.satCloser.Close()
	}
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	m.mu.Unlock()
}

func (m *DylibHostManager) send(env *IPCEnvelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sendLocked(env)
}

func (m *DylibHostManager) sendLocked(env *IPCEnvelope) error {
	if m.satEnc == nil {
		return fmt.Errorf("satellite not connected")
	}
	return m.satEnc.Encode(env)
}

func (m *DylibHostManager) readSatelliteLoop() {
	for {
		var env IPCEnvelope
		err := m.satDec.Decode(&env)
		if err != nil {
			// Satellite died or pipe closed: abort pending ops with EFAULT / EIO
			fmt.Fprintf(os.Stderr, "washmhost satellite disconnect for %s/%s: %v\n", m.target.goos, m.target.goarch, err)
			m.handleSatelliteDisconnect()
			return
		}

		if env.OpenResp != nil {
			m.finishOp(env.OpenResp.ReqID, env.OpenResp.Error, env.OpenResp.Handle, nil)
		} else if env.SymResp != nil {
			m.finishOp(env.SymResp.ReqID, env.SymResp.Error, env.SymResp.Symbol, nil)
		} else if env.CloseResp != nil {
			m.finishOp(env.CloseResp.ReqID, env.CloseResp.Error, 0, nil)
		} else if env.CallResp != nil {
			var ext uint64
			if len(env.CallResp.Results) > 0 {
				ext = env.CallResp.Results[0]
			}
			m.finishOp(env.CallResp.ReqID, env.CallResp.Error, ext, env.CallResp.Results)
		} else if env.AllocResp != nil {
			m.finishOp(env.AllocResp.ReqID, env.AllocResp.Error, env.AllocResp.Ptr, nil)
		} else if env.FreeResp != nil {
			m.finishOp(env.FreeResp.ReqID, env.FreeResp.Error, 0, nil)
		} else if env.ReadMemResp != nil {
			m.finishReadMemOp(env.ReadMemResp.ReqID, env.ReadMemResp.Error, env.ReadMemResp.Data)
		} else if env.WriteMemResp != nil {
			m.finishOp(env.WriteMemResp.ReqID, env.WriteMemResp.Error, 0, nil)
		} else if env.CbRegisterResp != nil {
			m.finishOp(env.CbRegisterResp.ReqID, env.CbRegisterResp.Error, env.CbRegisterResp.Trampoline, nil)
		} else if env.CbInvokeEvent != nil {
			m.incomingInvocations <- env.CbInvokeEvent
			m.hEnv.fileOpsQueue <- func() {} // Wake up Poll()
		}
	}
}

func (m *DylibHostManager) handleSatelliteDisconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for reqID, op := range m.pendingOps {
		delete(m.pendingOps, reqID)
		ovPtr := op.ovPtr
		m.hEnv.fileOpsQueue <- func() {
			if m.hEnv.mod != nil {
				_ = writeOverlapped(m.hEnv.mod, ovPtr, wasiEFAULT, 0, 0)
			}
			m.hEnv.DecOps()
		}
	}
	if m.hEnv != nil {
		m.hEnv.deleteDylibResourcesForManager(m)
		if m.hEnv.dylibRegistry != nil {
			m.hEnv.dylibRegistry.EvictManager(m)
		}
	}
}

func (m *DylibHostManager) finishOp(reqID uint64, errMsg string, resultExt uint64, extraResults []uint64) {
	m.mu.Lock()
	op, ok := m.pendingOps[reqID]
	if ok {
		delete(m.pendingOps, reqID)
	}
	m.mu.Unlock()

	if !ok {
		return
	}

	m.hEnv.fileOpsQueue <- func() {
		mod := m.hEnv.mod
		if mod == nil {
			m.hEnv.DecOps()
			return
		}
		var errCode uint32 = 0
		if errMsg != "" {
			errCode = wasiEIO
		} else {
			switch op.kind {
			case dylibPendingOpOpenLibrary:
				resultExt = m.hEnv.registerDylibLibrary(op.manager, resultExt, op.target)
			case dylibPendingOpResolveSymbol:
				resultExt = m.hEnv.registerDylibSymbol(op.manager, op.handle, resultExt)
			}
			if op.resultsPtr != 0 && len(extraResults) > 0 {
				toWrite := len(extraResults)
				if int(op.resultsCap) < toWrite {
					toWrite = int(op.resultsCap)
				}
				buf := make([]byte, toWrite*8)
				for i := 0; i < toWrite; i++ {
					binary.LittleEndian.PutUint64(buf[i*8:(i+1)*8], extraResults[i])
				}
				mod.Memory().Write(op.resultsPtr, buf)
			}
		}

		_ = writeOverlapped(mod, op.ovPtr, errCode, 0, resultExt)
		m.hEnv.DecOps()
	}
}

func (m *DylibHostManager) finishReadMemOp(reqID uint64, errMsg string, data []byte) {
	m.mu.Lock()
	op, ok := m.pendingOps[reqID]
	if ok {
		delete(m.pendingOps, reqID)
	}
	m.mu.Unlock()

	if !ok {
		return
	}

	m.hEnv.fileOpsQueue <- func() {
		mod := m.hEnv.mod
		if mod == nil {
			m.hEnv.DecOps()
			return
		}
		var errCode uint32 = 0
		if errMsg != "" {
			errCode = wasiEIO
		} else if len(data) > 0 && op.bufPtr != 0 {
			mod.Memory().Write(op.bufPtr, data)
		}

		_ = writeOverlapped(mod, op.ovPtr, errCode, 0, uint64(len(data)))
		m.hEnv.DecOps()
	}
}

func (m *DylibHostManager) HasActiveWork() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pendingOps) > 0 {
		return true
	}
	if len(m.incomingInvocations) > 0 {
		return true
	}
	if len(m.callbackList) > 0 {
		return true
	}
	for _, stack := range m.threadStacks {
		if len(stack) > 0 {
			return true
		}
	}
	return false
}

// BeforeRun processes incoming callback invocations and projects ready ones into guest memory.
func (m *DylibHostManager) BeforeRun(ctx context.Context, mod api.Module) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Drain incoming invocation events into thread stacks and deterministic FIFO per-callback queues
	for {
		select {
		case ev := <-m.incomingInvocations:
			reg := m.callbacks[ev.CbID]
			frame := &CallbackFrame{
				cbID:     ev.CbID,
				invID:    ev.InvID,
				threadID: ev.ThreadID,
				state:    FrameStatePending,
				args:     ev.Args,
				reg:      reg,
			}
			m.threadStacks[ev.ThreadID] = append(m.threadStacks[ev.ThreadID], frame)
			m.allFrames[ev.InvID] = frame
			if reg != nil {
				reg.pendingInvocations = append(reg.pendingInvocations, frame)
			}
		default:
			goto project
		}
	}

project:
	// 2. Project pending frames into free guest callback overlappeds gated strictly by cell availability (flags & 1 == 0)
	mem := mod.Memory()
	if mem == nil {
		return
	}

	for _, reg := range m.callbackList {
		if len(reg.pendingInvocations) == 0 {
			continue
		}
		flagBuf, ok := mem.Read(reg.cbOvPtr, 4)
		if !ok {
			continue
		}
		flags := binary.LittleEndian.Uint32(flagBuf)
		if flags&1 != 0 {
			// Cell is currently completed or occupied
			continue
		}

		// Pop the head of the deterministic FIFO queue for this callback
		frame := reg.pendingInvocations[0]
		reg.pendingInvocations = reg.pendingInvocations[1:]

		// Project into the registration-sized guest callback overlapped.
		buf := make([]byte, reg.cbOvLen)
		if !writeCallbackInvocationCell(buf, frame.cbID, frame.invID, frame.args, len(reg.results)) {
			reg.pendingInvocations = append([]*CallbackFrame{frame}, reg.pendingInvocations...)
			continue
		}
		if !mem.Write(reg.cbOvPtr, buf) {
			reg.pendingInvocations = append([]*CallbackFrame{frame}, reg.pendingInvocations...)
			continue
		}
		frame.state = FrameStateProjected
		reg.activeInvID = frame.invID
	}
}

// AfterRun inspects guest callback overlappeds at exit from run(), captures results,
// marks states, and unwinds completed frames in LIFO order.
func (m *DylibHostManager) AfterRun(ctx context.Context, mod api.Module) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mem := mod.Memory()
	if mem == nil {
		return
	}

	for _, reg := range m.callbackList {
		flagBuf, ok := mem.Read(reg.cbOvPtr, 4)
		if !ok {
			continue
		}
		flags := binary.LittleEndian.Uint32(flagBuf)

		if flags&1 == 0 {
			// flags & 1 == 0: Do not read any additional bytes. Update host-side state using reg.activeInvID stored in host memory.
			if reg.activeInvID != 0 {
				if frame, exists := m.allFrames[reg.activeInvID]; exists && frame.state == FrameStateProjected {
					frame.state = FrameStateGuestAsync
				}
			}
		} else {
			// flags & 1 != 0: always attribute completion by the invocation id written into the cell itself.
			buf, ok := mem.Read(reg.cbOvPtr, reg.cbOvLen)
			if ok {
				invID, results, parsed := readCallbackCompletionCell(buf, len(reg.results))
				if parsed {
					if frame, exists := m.allFrames[invID]; exists && frame.state != FrameStateCompleted && frame.state != FrameStateReturned {
						frame.results = results
						frame.state = FrameStateCompleted
					}
				}
			}
			mem.Write(reg.cbOvPtr, []byte{0, 0, 0, 0})
			reg.activeInvID = 0
			select {
			case m.hEnv.fileOpsQueue <- func() {}:
			default:
			}
		}
	}

	// Unwind top completed frames per thread stack in strict LIFO order
	for threadID, stack := range m.threadStacks {
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			if top.state == FrameStateCompleted {
				// Send return message to satellite!
				_ = m.sendLocked(&IPCEnvelope{
					CbReturnReq: &DylibCbReturnReq{
						InvID:   top.invID,
						Results: top.results,
					},
				})
				top.state = FrameStateReturned
				delete(m.allFrames, top.invID)
				stack = stack[:len(stack)-1]
				m.threadStacks[threadID] = stack
			} else {
				// Top frame is not yet complete (still Pending, Projected, or GuestAsync).
				// We must park any completed frames below it until top unwinds!
				break
			}
		}
	}
}

// ── Host ABI Functions ─────────────────────────────────────────────────────

func (h *HostEnv) sys_dylib_open(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	flags := int(stack[3])

	pathBytes, ok := m.Memory().Read(pathPtr, pathLen)
	if !ok {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	path := string(pathBytes)
	target := h.resolveDylibTarget(path)
	manager := h.dylibMgr
	if h.dylibRegistry != nil {
		var err error
		manager, err = h.dylibRegistry.managerForTarget(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "washmhost satellite start error for %s/%s: %v\n", target.goos, target.goarch, err)
			_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
			return
		}
	} else if err := h.dylibMgr.EnsureSatellite(); err != nil {
		fmt.Fprintf(os.Stderr, "washmhost satellite ensure error for %s/%s: %v\n", target.goos, target.goarch, err)
		_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
		return
	}

	reqID := atomic.AddUint64(&manager.nextReqID, 1)
	manager.mu.Lock()
	manager.pendingOps[reqID] = &dylibPendingOp{
		reqID:   reqID,
		ovPtr:   ovPtr,
		kind:    dylibPendingOpOpenLibrary,
		manager: manager,
		target:  target,
	}
	manager.mu.Unlock()
	h.IncOps()

	_ = manager.send(&IPCEnvelope{
		OpenReq: &DylibOpenReq{
			ReqID: reqID,
			Path:  string(pathBytes),
			Flags: flags,
		},
	})
}

func (h *HostEnv) sys_dylib_sym(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	namePtr := uint32(stack[2])
	nameLen := uint32(stack[3])

	libRef, ok := h.lookupDylibLibrary(libHandle)
	if !ok {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	if libRef.manager == nil {
		_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
		return
	}
	if err := libRef.manager.EnsureSatellite(); err != nil {
		_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
		return
	}

	nameBytes, ok := m.Memory().Read(namePtr, nameLen)
	if !ok {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	reqID := atomic.AddUint64(&libRef.manager.nextReqID, 1)
	libRef.manager.mu.Lock()
	libRef.manager.pendingOps[reqID] = &dylibPendingOp{
		reqID:   reqID,
		ovPtr:   ovPtr,
		handle:  libHandle,
		kind:    dylibPendingOpResolveSymbol,
		manager: libRef.manager,
	}
	libRef.manager.mu.Unlock()
	h.IncOps()

	_ = libRef.manager.send(&IPCEnvelope{
		SymReq: &DylibSymReq{
			ReqID:  reqID,
			Handle: libRef.remote,
			Name:   string(nameBytes),
		},
	})
}

func (h *HostEnv) sys_dylib_close(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	libRef, ok := h.lookupDylibLibrary(libHandle)
	if !ok || libRef.manager == nil {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	reqID := atomic.AddUint64(&libRef.manager.nextReqID, 1)
	libRef.manager.mu.Lock()
	libRef.manager.pendingOps[reqID] = &dylibPendingOp{
		reqID: reqID,
		ovPtr: ovPtr,
	}
	libRef.manager.mu.Unlock()
	h.IncOps()

	h.deleteDylibLibrary(libHandle)
	_ = libRef.manager.send(&IPCEnvelope{
		CloseReq: &DylibCloseReq{
			ReqID:  reqID,
			Handle: libRef.remote,
		},
	})
}

func (h *HostEnv) sys_dylib_call(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	symHandle := stack[1]
	sigPtr := uint32(stack[2])
	sigLen := uint32(stack[3])
	argsPtr := uint32(stack[4])
	argsCount := uint32(stack[5])
	parentInvID := stack[6]
	var resultsPtr uint32
	var resultsCap uint32
	if len(stack) > 8 {
		resultsPtr = uint32(stack[7])
		resultsCap = uint32(stack[8])
	}

	symRef, ok := h.lookupDylibSymbol(symHandle)
	if !ok || symRef.manager == nil || symRef.remote == 0 {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	sigBytes, ok := m.Memory().Read(sigPtr, sigLen)
	if !ok {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	args := make([]uint64, argsCount)
	if argsCount > 0 {
		argsRaw, ok := m.Memory().Read(argsPtr, argsCount*8)
		if !ok {
			_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
			return
		}
		for i := uint32(0); i < argsCount; i++ {
			args[i] = binary.LittleEndian.Uint64(argsRaw[i*8 : (i+1)*8])
		}
	}

	reqID := atomic.AddUint64(&symRef.manager.nextReqID, 1)
	symRef.manager.mu.Lock()
	symRef.manager.pendingOps[reqID] = &dylibPendingOp{
		reqID:      reqID,
		ovPtr:      ovPtr,
		handle:     symHandle,
		resultsPtr: resultsPtr,
		resultsCap: resultsCap,
	}
	symRef.manager.mu.Unlock()
	h.IncOps()

	_ = symRef.manager.send(&IPCEnvelope{
		CallReq: &DylibCallReq{
			ReqID:              reqID,
			Symbol:             symRef.remote,
			SigBytes:           sigBytes,
			Args:               args,
			ParentInvocationID: parentInvID,
		},
	})
}

func (h *HostEnv) sys_dylib_alloc(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	size := stack[2]
	align := uint32(stack[3])
	libRef, ok := h.lookupDylibLibrary(libHandle)
	if !ok || libRef.manager == nil {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	if err := libRef.manager.EnsureSatellite(); err != nil {
		_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
		return
	}

	reqID := atomic.AddUint64(&libRef.manager.nextReqID, 1)
	libRef.manager.mu.Lock()
	libRef.manager.pendingOps[reqID] = &dylibPendingOp{
		reqID: reqID,
		ovPtr: ovPtr,
	}
	libRef.manager.mu.Unlock()
	h.IncOps()

	_ = libRef.manager.send(&IPCEnvelope{
		AllocReq: &DylibAllocReq{
			ReqID: reqID,
			Size:  size,
			Align: align,
		},
	})
}

func (h *HostEnv) sys_dylib_free(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	ptr := stack[2]
	libRef, ok := h.lookupDylibLibrary(libHandle)
	if !ok || libRef.manager == nil {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	if err := libRef.manager.EnsureSatellite(); err != nil {
		_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
		return
	}

	reqID := atomic.AddUint64(&libRef.manager.nextReqID, 1)
	libRef.manager.mu.Lock()
	libRef.manager.pendingOps[reqID] = &dylibPendingOp{
		reqID: reqID,
		ovPtr: ovPtr,
	}
	libRef.manager.mu.Unlock()
	h.IncOps()

	_ = libRef.manager.send(&IPCEnvelope{
		FreeReq: &DylibFreeReq{
			ReqID: reqID,
			Ptr:   ptr,
		},
	})
}

func (h *HostEnv) sys_dylib_read_mem(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	hostPtr := stack[2]
	guestBufPtr := uint32(stack[3])
	length := uint32(stack[4])
	libRef, ok := h.lookupDylibLibrary(libHandle)
	if !ok || libRef.manager == nil {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	if err := libRef.manager.EnsureSatellite(); err != nil {
		_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
		return
	}

	reqID := atomic.AddUint64(&libRef.manager.nextReqID, 1)
	libRef.manager.mu.Lock()
	libRef.manager.pendingOps[reqID] = &dylibPendingOp{
		reqID:  reqID,
		ovPtr:  ovPtr,
		bufPtr: guestBufPtr,
		bufLen: length,
	}
	libRef.manager.mu.Unlock()
	h.IncOps()

	_ = libRef.manager.send(&IPCEnvelope{
		ReadMemReq: &DylibReadMemReq{
			ReqID: reqID,
			Ptr:   hostPtr,
			Len:   length,
		},
	})
}

func (h *HostEnv) sys_dylib_write_mem(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	hostPtr := stack[2]
	guestBufPtr := uint32(stack[3])
	length := uint32(stack[4])
	libRef, ok := h.lookupDylibLibrary(libHandle)
	if !ok || libRef.manager == nil {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	if err := libRef.manager.EnsureSatellite(); err != nil {
		_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
		return
	}

	data, ok := m.Memory().Read(guestBufPtr, length)
	if !ok {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	reqID := atomic.AddUint64(&libRef.manager.nextReqID, 1)
	libRef.manager.mu.Lock()
	libRef.manager.pendingOps[reqID] = &dylibPendingOp{
		reqID: reqID,
		ovPtr: ovPtr,
	}
	libRef.manager.mu.Unlock()
	h.IncOps()

	_ = libRef.manager.send(&IPCEnvelope{
		WriteMemReq: &DylibWriteMemReq{
			ReqID: reqID,
			Ptr:   hostPtr,
			Data:  data,
		},
	})
}

func (h *HostEnv) sys_dylib_callback_register(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	sigPtr := uint32(stack[2])
	sigLen := uint32(stack[3])
	cbOvPtr := uint32(stack[4])
	cbOvLen := uint32(stack[5])
	libRef, ok := h.lookupDylibLibrary(libHandle)
	if !ok || libRef.manager == nil {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	if err := libRef.manager.EnsureSatellite(); err != nil {
		_ = writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
		return
	}

	sigBytes, ok := m.Memory().Read(sigPtr, sigLen)
	if !ok {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	params, results, err := DecodeFuncType(sigBytes)
	if err != nil {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	if cbOvLen < minCallbackOverlappedSize(len(params), len(results)) {
		_ = writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	cbID := atomic.AddUint64(&libRef.manager.nextCbID, 1)
	reg := &HostCallbackReg{
		cbID:     cbID,
		cbOvPtr:  cbOvPtr,
		cbOvLen:  cbOvLen,
		sigBytes: sigBytes,
		params:   params,
		results:  results,
	}

	libRef.manager.mu.Lock()
	libRef.manager.callbacks[cbID] = reg
	libRef.manager.callbacksByOv[cbOvPtr] = reg
	libRef.manager.callbackList = append(libRef.manager.callbackList, reg)
	reqID := atomic.AddUint64(&libRef.manager.nextReqID, 1)
	libRef.manager.pendingOps[reqID] = &dylibPendingOp{
		reqID: reqID,
		ovPtr: ovPtr,
	}
	libRef.manager.mu.Unlock()
	h.IncOps()

	_ = libRef.manager.send(&IPCEnvelope{
		CbRegisterReq: &DylibCbRegisterReq{
			ReqID:    reqID,
			CbID:     cbID,
			SigBytes: sigBytes,
		},
	})
}
