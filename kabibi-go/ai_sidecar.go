package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type SidecarClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	mu     sync.Mutex

	callbacks sync.Map // map[uint64]chan SidecarResponse
	streams   sync.Map // map[uint64]func(string)
	completes sync.Map // map[uint64]chan string
	nextID    uint64
}

type SidecarRequest struct {
	Action       string      `json:"action"`
	ModelPath    string      `json:"model_path,omitempty"`
	Config       interface{} `json:"config,omitempty"`
	Engine       string      `json:"engine,omitempty"`
	Conversation string      `json:"conversation,omitempty"`
	Message      string      `json:"message,omitempty"`
	Callback     uint64      `json:"callback,omitempty"`
}

type SidecarResponse struct {
	Status       string `json:"status,omitempty"`
	Event        string `json:"event,omitempty"`
	Error        string `json:"error,omitempty"`
	Engine       string `json:"engine,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Callback     uint64 `json:"callback,omitempty"`
	Token        string `json:"token,omitempty"`
}

func NewSidecarClient(proxyPath string, libPath string) (*SidecarClient, error) {
	cmd := exec.Command(proxyPath)
	cmd.Env = append(os.Environ(), "LITERTLM_LIB_PATH="+libPath)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sc := &SidecarClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewScanner(stdout),
	}

	go sc.readLoop()

	return sc, nil
}

func (sc *SidecarClient) readLoop() {
	for sc.stdout.Scan() {
		var resp SidecarResponse
		if err := json.Unmarshal(sc.stdout.Bytes(), &resp); err != nil {
			continue
		}

		if resp.Event == "token" {
			if fn, ok := sc.streams.Load(resp.Callback); ok {
				fn.(func(string))(resp.Token)
			}
		} else if resp.Event == "complete" {
			if ch, ok := sc.completes.Load(resp.Callback); ok {
				ch.(chan string) <- resp.Status
			}
		} else if resp.Callback != 0 {
			if ch, ok := sc.callbacks.Load(resp.Callback); ok {
				ch.(chan SidecarResponse) <- resp
			}
		}
	}
}

func (sc *SidecarClient) call(req SidecarRequest) (SidecarResponse, error) {
	id := atomic.AddUint64(&sc.nextID, 1)
	req.Callback = id

	ch := make(chan SidecarResponse, 1)
	sc.callbacks.Store(id, ch)
	defer sc.callbacks.Delete(id)

	data, _ := json.Marshal(req)
	sc.mu.Lock()
	_, err := fmt.Fprintln(sc.stdin, string(data))
	sc.mu.Unlock()

	if err != nil {
		return SidecarResponse{}, err
	}

	resp := <-ch
	if resp.Status == "error" {
		return resp, fmt.Errorf(resp.Error)
	}
	return resp, nil
}

func (sc *SidecarClient) EngineCreate(modelPath string, backend string) (string, error) {
	resp, err := sc.call(SidecarRequest{
		Action:    "engine_create",
		ModelPath: modelPath,
		Config:    map[string]interface{}{"backend": backend},
	})
	if err != nil {
		return "", err
	}
	return resp.Engine, nil
}

func (sc *SidecarClient) ConversationCreate(engine string) (string, error) {
	resp, err := sc.call(SidecarRequest{
		Action: "conversation_create",
		Engine: engine,
	})
	if err != nil {
		return "", err
	}
	return resp.Conversation, nil
}

func (sc *SidecarClient) ConversationSend(conv string, message string, onToken func(string)) error {
	id := atomic.AddUint64(&sc.nextID, 1)

	done := make(chan string, 1)
	sc.streams.Store(id, onToken)
	sc.completes.Store(id, done)
	defer sc.streams.Delete(id)
	defer sc.completes.Delete(id)

	data, _ := json.Marshal(SidecarRequest{
		Action:       "conversation_send",
		Conversation: conv,
		Message:      message,
		Callback:     id,
	})

	sc.mu.Lock()
	fmt.Fprintln(sc.stdin, string(data))
	sc.mu.Unlock()

	status := <-done
	if status != "success" {
		return fmt.Errorf("conversation yield failed with status: %s", status)
	}
	return nil
}

func (sc *SidecarClient) Close() {
	sc.stdin.Close()
	sc.cmd.Wait()
}
