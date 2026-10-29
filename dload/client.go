package dload

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	headerSize = 13

	opHandshake    = 0x01
	opOpenReq      = 0x10
	opOpenResp     = 0x11
	opSymReq       = 0x12
	opSymResp      = 0x13
	opCloseReq     = 0x14
	opCloseResp    = 0x15
	opCallReq      = 0x20
	opCallResp     = 0x21
	opAllocReq     = 0x30
	opAllocResp    = 0x31
	opFreeReq      = 0x32
	opFreeResp     = 0x33
	opReadMemReq   = 0x34
	opReadMemResp  = 0x35
	opWriteMemReq  = 0x36
	opWriteMemResp = 0x37
	opCbRegReq     = 0x40
	opCbRegResp    = 0x41
	opCbInvoke     = 0x42
	opCbReturn     = 0x43
	opCbUnregReq   = 0x44
	opCbUnregResp  = 0x45
)

type frame struct {
	len    uint32
	opcode byte
	id     uint64
	body   []byte
}

type CallbackHandler func(invID uint64, threadID uint64, args []uint64) []uint64

type WorkerClient struct {
	mu              sync.Mutex
	writeMu         sync.Mutex
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	stdout          io.Reader
	reqSeq          uint64
	pending         map[uint64]chan frame
	cbMu            sync.RWMutex
	callbacks       map[uint64]CallbackHandler
	cbSeq           uint64
	activeCbMu      sync.Mutex
	activeBgCbCount int
	activeBgCbIdle  chan struct{}
}

var (
	workersMu sync.Mutex
	workers   = make(map[string]*WorkerClient)
)

func getWorkerForPath(libPath string) (*WorkerClient, error) {
	flavour, err := detectFlavour(libPath)
	if err != nil {
		return nil, err
	}
	workersMu.Lock()
	defer workersMu.Unlock()
	if w, ok := workers[flavour]; ok {
		return w, nil
	}
	w, err := launchWorker(flavour)
	if err != nil {
		return nil, err
	}
	workers[flavour] = w
	return w, nil
}

func ensureWorkerExtracted(flavour string) (string, error) {
	ext := ""
	if strings.HasPrefix(flavour, "windows") {
		ext = ".exe"
	}
	embeddedName := fmt.Sprintf("embedded/dload-%s%s", flavour, ext)
	data, err := embeddedWorkers.ReadFile(embeddedName)
	if err != nil {
		return "", fmt.Errorf("read embedded worker %s: %w", embeddedName, err)
	}

	h := sha256.Sum256(data)
	hashStr := hex.EncodeToString(h[:8])
	targetPath := filepath.Join(os.TempDir(), fmt.Sprintf("dload-%s-%s%s", flavour, hashStr, ext))

	if info, err := os.Stat(targetPath); err == nil && info.Size() == int64(len(data)) {
		if err := os.Chmod(targetPath, 0o755); err != nil {
			return "", fmt.Errorf("set worker executable mode %s: %w", targetPath, err)
		}
		return targetPath, nil
	}

	tmpPath := targetPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o755); err != nil {
		return "", fmt.Errorf("extract worker %s: %w", targetPath, err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("set worker executable mode %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		if _, statErr := os.Stat(targetPath); statErr != nil {
			return "", fmt.Errorf("rename worker %s: %w", targetPath, err)
		}
		if chmodErr := os.Chmod(targetPath, 0o755); chmodErr != nil {
			return "", fmt.Errorf("set worker executable mode %s: %w", targetPath, chmodErr)
		}
	}
	return targetPath, nil
}

func launchWorker(flavour string) (*WorkerClient, error) {
	binPath, err := ensureWorkerExtracted(flavour)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(binPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("worker stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("worker stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start worker %s: %w", binPath, err)
	}

	client := &WorkerClient{
		cmd:            cmd,
		stdin:          stdin,
		stdout:         stdout,
		pending:        make(map[uint64]chan frame),
		callbacks:      make(map[uint64]CallbackHandler),
		activeBgCbIdle: make(chan struct{}),
	}
	close(client.activeBgCbIdle)

	// Read handshake frame
	hsHeader := make([]byte, headerSize)
	if _, err := io.ReadFull(stdout, hsHeader); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("read worker handshake header: %w", err)
	}
	hsLen := binary.LittleEndian.Uint32(hsHeader[0:4])
	if hsHeader[4] != opHandshake {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("unexpected worker handshake opcode: 0x%02x", hsHeader[4])
	}
	hsBody := make([]byte, hsLen)
	if _, err := io.ReadFull(stdout, hsBody); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("read worker handshake body: %w", err)
	}

	go client.readLoop()
	return client, nil
}

func (c *WorkerClient) readLoop() {
	headerBuf := make([]byte, headerSize)
	for {
		if _, err := io.ReadFull(c.stdout, headerBuf); err != nil {
			c.abortPending(err)
			return
		}
		pLen := binary.LittleEndian.Uint32(headerBuf[0:4])
		opcode := headerBuf[4]
		id := binary.LittleEndian.Uint64(headerBuf[5:13])

		body := make([]byte, pLen)
		if pLen > 0 {
			if _, err := io.ReadFull(c.stdout, body); err != nil {
				c.abortPending(err)
				return
			}
		}

		if opcode == opCbInvoke {
			go c.handleCbInvoke(id, body)
			continue
		}

		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()

		if ok && ch != nil {
			ch <- frame{len: pLen, opcode: opcode, id: id, body: body}
		}
	}
}

func (c *WorkerClient) abortPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
}

func (c *WorkerClient) send(opcode byte, id uint64, payload []byte) error {
	hdr := make([]byte, headerSize)
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(payload)))
	hdr[4] = opcode
	binary.LittleEndian.PutUint64(hdr[5:13], id)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.stdin.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *WorkerClient) roundTrip(opcode byte, payload []byte) (frame, error) {
	reqID := atomic.AddUint64(&c.reqSeq, 1)
	ch := make(chan frame, 1)

	c.mu.Lock()
	c.pending[reqID] = ch
	c.mu.Unlock()

	if err := c.send(opcode, reqID, payload); err != nil {
		c.mu.Lock()
		delete(c.pending, reqID)
		c.mu.Unlock()
		return frame{}, err
	}

	resp, ok := <-ch
	if !ok {
		return frame{}, errors.New("dload worker terminated unexpectedly")
	}
	return resp, nil
}

func (c *WorkerClient) beginCallback() {
	c.activeCbMu.Lock()
	if c.activeBgCbCount == 0 {
		c.activeBgCbIdle = make(chan struct{})
	}
	c.activeBgCbCount++
	c.activeCbMu.Unlock()
}

func (c *WorkerClient) endCallback() {
	c.activeCbMu.Lock()
	c.activeBgCbCount--
	if c.activeBgCbCount == 0 && c.activeBgCbIdle != nil {
		close(c.activeBgCbIdle)
	}
	c.activeCbMu.Unlock()
}

func (c *WorkerClient) waitNoActiveCallback() {
	c.activeCbMu.Lock()
	ch := c.activeBgCbIdle
	c.activeCbMu.Unlock()
	if ch != nil {
		<-ch
	}
}

func (c *WorkerClient) handleCbInvoke(invID uint64, body []byte) {
	if len(body) < 20 {
		return
	}
	cbID := binary.LittleEndian.Uint64(body[0:8])
	threadID := binary.LittleEndian.Uint64(body[8:16])
	argc := int(binary.LittleEndian.Uint32(body[16:20]))
	args := make([]uint64, argc)
	for i := 0; i < argc; i++ {
		off := 20 + i*8
		if off+8 <= len(body) {
			args[i] = binary.LittleEndian.Uint64(body[off : off+8])
		}
	}

	c.cbMu.RLock()
	fn := c.callbacks[cbID]
	c.cbMu.RUnlock()

	c.beginCallback()
	var results []uint64
	if fn != nil {
		results = fn(invID, threadID, args)
	}

	resPayload := make([]byte, 1+4+len(results)*8)
	resPayload[0] = 0 // ok
	binary.LittleEndian.PutUint32(resPayload[1:5], uint32(len(results)))
	for i, r := range results {
		binary.LittleEndian.PutUint64(resPayload[5+i*8:5+(i+1)*8], r)
	}
	_ = c.send(opCbReturn, invID, resPayload)
	c.endCallback()
}

func (c *WorkerClient) registerCallback(fn CallbackHandler) uint64 {
	c.cbMu.Lock()
	defer c.cbMu.Unlock()
	c.cbSeq++
	id := c.cbSeq
	c.callbacks[id] = fn
	return id
}

func (c *WorkerClient) unregisterCallback(cbID uint64) {
	c.cbMu.Lock()
	defer c.cbMu.Unlock()
	delete(c.callbacks, cbID)
}
