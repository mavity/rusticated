package main

import (
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

	IsHandshake bool
	HGOOS       string
	HGOARCH     string
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

var (
	devWorkspaceRoot string
	startupGOROOT    string
)

// captureDevContext records the workspace root and Go toolchain location at host
// startup, so a cross-arch dylib satellite can be launched via `go run` later,
// before the guest has a chance to change the working directory.
func captureDevContext() {
	startupGOROOT = os.Getenv("GOROOT")
	devWorkspaceRoot = findWorkspaceRoot()
}

func findWorkspaceRoot() string {
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	for _, dir := range starts {
		d := dir
		for i := 0; i < 6; i++ {
			if fileExists(filepath.Join(d, "sysroot.toml")) {
				return d
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func getSatellite(h *HostEnv) (*gob.Encoder, error) {
	satMu.Lock()
	defer satMu.Unlock()

	if satStarted {
		return satEncoder, nil
	}
	satGlobalEnv = h

	// The required satellite arch is derived from the DLL at first open; if no
	// open has recorded one, default to the host arch.
	h.mu.Lock()
	tgtGOOS, tgtGOARCH := h.satTargetGOOS, h.satTargetGOARCH
	h.mu.Unlock()
	if tgtGOOS == "" {
		tgtGOOS, tgtGOARCH = runtime.GOOS, runtime.GOARCH
	}

	cmd, err := buildSatelliteCommand(tgtGOOS, tgtGOARCH)
	if err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("spawn satellite: %w", err)
	}
	// The child owns its ends now; closing ours lets the child observe EOF when we
	// exit (and lets us observe EOF when the child dies).
	pr.Close()
	cw.Close()

	satEncoder = gob.NewEncoder(pw)
	satDecoder = gob.NewDecoder(cr)

	// The satellite reports its arch as the first framed message. A build/launch
	// failure surfaces on stderr and closes stdout, so Decode returns an error.
	var hs DylibResponse
	if err := satDecoder.Decode(&hs); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("satellite handshake failed (build or launch error): %w", err)
	}
	if !hs.IsHandshake {
		cmd.Process.Kill()
		return nil, fmt.Errorf("satellite protocol error: expected handshake, got response")
	}
	if hs.HGOOS != tgtGOOS || hs.HGOARCH != tgtGOARCH {
		cmd.Process.Kill()
		return nil, fmt.Errorf("satellite arch mismatch: need %s/%s, got %s/%s", tgtGOOS, tgtGOARCH, hs.HGOOS, hs.HGOARCH)
	}

	satStarted = true
	go satelliteReader()

	return satEncoder, nil
}

// buildSatelliteCommand selects how to launch the satellite: from the packaged
// vegetable launcher when running inside one, otherwise via `go run` for the
// target arch during a dev run.
func buildSatelliteCommand(goos, goarch string) (*exec.Cmd, error) {
	veg := os.Getenv("MOHABBAT_VEGETABLE_PATH")
	if veg != "" && fileExists(veg) {
		return exec.Command(veg, "--platform", archToken(goarch), "--dylib-satellite"), nil
	}
	if devWorkspaceRoot != "" {
		cmd := exec.Command(resolveGoBinary(), "run", ".", "--dylib-satellite")
		cmd.Dir = filepath.Join(devWorkspaceRoot, "mohabbat", "washmhost")
		cmd.Env = sanitizedGoEnv(goos, goarch)
		return cmd, nil
	}
	return nil, fmt.Errorf("cannot launch %s/%s satellite: not running in a vegetable and workspace root not found (dev runs must start from the workspace)", goos, goarch)
}

func archToken(goarch string) string {
	switch goarch {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return goarch
	}
}

func resolveGoBinary() string {
	exe := "go"
	if runtime.GOOS == "windows" {
		exe = "go.exe"
	}
	if startupGOROOT != "" {
		if cand := filepath.Join(startupGOROOT, "bin", exe); fileExists(cand) {
			return cand
		}
	}
	if ver := washmhostGoModVersion(); ver != "" {
		if home, err := os.UserHomeDir(); err == nil {
			if cand := filepath.Join(home, "sdk", "go"+ver, "bin", exe); fileExists(cand) {
				return cand
			}
		}
	}
	return exe
}

func washmhostGoModVersion() string {
	if devWorkspaceRoot == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(devWorkspaceRoot, "mohabbat", "washmhost", "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "go ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	return ""
}

// sanitizedGoEnv builds a minimal allowlisted environment for the satellite build,
// avoiding inherited control vars (e.g. stray GOOS/GOARCH/MOHABBAT_*) that could
// mis-target or poison the nested `go run`.
func sanitizedGoEnv(goos, goarch string) []string {
	allow := []string{
		"PATH", "SystemRoot", "SystemDrive", "windir", "TEMP", "TMP",
		"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "HOME",
		"LOCALAPPDATA", "APPDATA", "GOCACHE", "GOMODCACHE", "GOPATH",
	}
	var env []string
	for _, k := range allow {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if startupGOROOT != "" {
		env = append(env, "GOROOT="+startupGOROOT)
	}
	return env
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
