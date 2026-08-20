package main

import (
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type DylibRequest struct {
	ID        uint64
	Op        string
	Path      string
	Flags     int
	LibHandle uint64
	SymHandle uint64
	Name      string
	DescBuf   []byte
	BufParams [][]byte
	CbHandle  uint64
	InvocId   uint32
	CbRet     int64
	Ptr       uint64
	MaxLen    uint32

	// For callback initialization from Host to Satellite
	GuestFnName string
	ArgCount    uint8
	RetType     uint8
	ArgTypes    []uint8
}

type DylibResponse struct {
	ID        uint64
	ErrCode   uint32
	Handle    uint64
	Result    uint64
	BufParams [][]byte
	BytesRes  []byte

	IsCallback bool
	CbHandle   uint64
	CbArgs     []uint64
	CbInvocId  uint32
}

var (
	satMu        sync.Mutex
	satEncoder   *gob.Encoder
	satDecoder   *gob.Decoder
	satPending   = make(map[uint64]chan DylibResponse)
	satNextID    uint64
	satStarted   bool
	satGlobalEnv *HostEnv
)

func getSatellite(h *HostEnv) (*gob.Encoder, error) {
	satMu.Lock()
	defer satMu.Unlock()

	if satStarted {
		return satEncoder, nil
	}
	satGlobalEnv = h

	veg := os.Getenv("MOHABBAT_VEGETABLE_PATH")
	if veg == "" {
		veg = os.Args[0] // fallback to self
	}

	args := []string{}
	args = append(args, "--platform", "x64", "--dylib-satellite")

	cmd := exec.Command(veg, args...)
	cmd.Stderr = os.Stderr

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cr, cw, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	cmd.Stdin = pr
	cmd.Stdout = cw

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	satEncoder = gob.NewEncoder(pw)
	satDecoder = gob.NewDecoder(cr)
	satStarted = true

	go satelliteReader()

	return satEncoder, nil
}

func satelliteReader() {
	for {
		var resp DylibResponse
		err := satDecoder.Decode(&resp)
		if err != nil {
			if err == io.EOF {
				fmt.Fprintf(os.Stderr, "washmhost: satellite process died cleanly\n")
			} else {
				fmt.Fprintf(os.Stderr, "washmhost: satellite decode error: %v\n", err)
			}
			os.Exit(1)
		}

		if resp.IsCallback {
			go handleSatelliteCallback(resp)
			continue
		}

		satMu.Lock()
		ch, ok := satPending[resp.ID]
		if ok {
			delete(satPending, resp.ID)
		}
		satMu.Unlock()

		if ok {
			ch <- resp
		}
	}
}

func handleSatelliteCallback(resp DylibResponse) {
	// Call into the global HostEnv to invoke the Wasm callback
	if satGlobalEnv != nil {
		res := satGlobalEnv.invokeCallbackFromSatellite(resp.CbHandle, resp.CbArgs, resp.BufParams)

		satMu.Lock()
		enc := satEncoder
		satMu.Unlock()

		if enc != nil {
			req := DylibRequest{
				Op:       "CallbackRespond",
				CbHandle: resp.CbHandle,
				InvocId:  resp.CbInvocId,
				CbRet:    int64(res),
			}
			enc.Encode(&req)
		}
	}
}

func callSatellite(h *HostEnv, req DylibRequest) (DylibResponse, error) {
	enc, err := getSatellite(h)
	if err != nil {
		return DylibResponse{}, err
	}

	req.ID = atomic.AddUint64(&satNextID, 1)
	ch := make(chan DylibResponse, 1)

	satMu.Lock()
	satPending[req.ID] = ch
	err = enc.Encode(&req)
	satMu.Unlock()

	if err != nil {
		satMu.Lock()
		delete(satPending, req.ID)
		satMu.Unlock()
		return DylibResponse{}, err
	}

	resp := <-ch
	return resp, nil
}
