//go:build windows

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
	"pty"
)

const schemaVersion = 1

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type config struct {
	executable     string
	appArgs        stringList
	workingDir     string
	outputDir      string
	cols           int
	rows           int
	startupTimeout time.Duration
	idleDuration   time.Duration
	slowText       string
	slowInterval   time.Duration
	typingSeed     int64
	fastText       string
	fastInterval   time.Duration
	editText       string
	sampleInterval         time.Duration
	threadInterval         time.Duration
	drainDuration          time.Duration
	cooperativeExit        bool
	cooperativeExitTimeout time.Duration
}

type event struct {
	SchemaVersion int            `json:"schema_version"`
	RunID         string         `json:"run_id"`
	Sequence      uint64         `json:"sequence"`
	Kind          string         `json:"kind"`
	AtUTC         time.Time      `json:"at_utc"`
	ElapsedNS     int64          `json:"elapsed_ns"`
	Phase         string         `json:"phase"`
	Source        string         `json:"source"`
	SourceAPI     string         `json:"source_api"`
	ProcessID     uint32         `json:"process_id,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
}

type eventLog struct {
	mu       sync.Mutex
	encoder  *json.Encoder
	writer   *bufio.Writer
	file     *os.File
	start    time.Time
	runID    string
	phase    string
	sequence uint64
	phaseIO  map[string]*phaseIO
}

type phaseIO struct {
	InputChunks  uint64 `json:"input_chunks"`
	InputBytes   uint64 `json:"input_bytes"`
	OutputChunks uint64 `json:"output_chunks"`
	OutputBytes  uint64 `json:"output_bytes"`
}

func openEventLog(path, runID string, start time.Time) (*eventLog, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create events file: %w", err)
	}
	writer := bufio.NewWriterSize(file, 256*1024)
	return &eventLog{
		encoder: json.NewEncoder(writer),
		writer:  writer,
		file:    file,
		start:   start,
		runID:   runID,
		phase:   "startup",
		phaseIO: make(map[string]*phaseIO),
	}, nil
}

func (l *eventLog) setPhase(phase string) {
	l.mu.Lock()
	l.phase = phase
	l.mu.Unlock()
}

func (l *eventLog) currentPhase() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.phase
}

func (l *eventLog) emit(kind, source, sourceAPI string, pid uint32, data map[string]any) error {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sequence++
	record := event{
		SchemaVersion: schemaVersion,
		RunID:         l.runID,
		Sequence:      l.sequence,
		Kind:          kind,
		AtUTC:         now.UTC(),
		ElapsedNS:     now.Sub(l.start).Nanoseconds(),
		Phase:         l.phase,
		Source:        source,
		SourceAPI:     sourceAPI,
		ProcessID:     pid,
		Data:          data,
	}
	if err := l.encoder.Encode(record); err != nil {
		return fmt.Errorf("encode event %q: %w", kind, err)
	}
	if kind == "pty.input" || kind == "pty.output" {
		stats := l.phaseIO[record.Phase]
		if stats == nil {
			stats = &phaseIO{}
			l.phaseIO[record.Phase] = stats
		}
		if count, ok := data["byte_count"].(int); ok {
			if kind == "pty.input" {
				stats.InputBytes += uint64(count)
			} else {
				stats.OutputBytes += uint64(count)
			}
		}
		if kind == "pty.input" {
			stats.InputChunks++
		} else {
			stats.OutputChunks++
		}
	}
	return nil
}

func (l *eventLog) phaseIOSnapshot() map[string]phaseIO {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot := make(map[string]phaseIO, len(l.phaseIO))
	for phase, stats := range l.phaseIO {
		snapshot[phase] = *stats
	}
	return snapshot
}

func (l *eventLog) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.writer.Flush(); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("flush events file: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("sync events file: %w", err)
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("close events file: %w", err)
	}
	return nil
}

type outputReader struct {
	done          chan error
	promptReady   chan promptObservation
	promptMatcher promptMatcher
	sequence      uint64
	bytes         uint64
}

type promptObservation struct {
	AtUTC         time.Time
	OutputBytes   uint64
	ChunkSequence uint64
}

const promptReadyMarker = "፧"

type promptMatcher struct {
	tail []byte
	seen bool
}

func (m *promptMatcher) observe(chunk []byte, outputBytes, chunkSequence uint64, at time.Time) (promptObservation, bool) {
	if m.seen {
		return promptObservation{}, false
	}
	combined := make([]byte, 0, len(m.tail)+len(chunk))
	combined = append(combined, m.tail...)
	combined = append(combined, chunk...)
	if bytes.Contains(combined, []byte(promptReadyMarker)) {
		m.seen = true
		return promptObservation{AtUTC: at.UTC(), OutputBytes: outputBytes, ChunkSequence: chunkSequence}, true
	}
	keep := len(promptReadyMarker) - 1
	if len(combined) > keep {
		combined = combined[len(combined)-keep:]
	}
	m.tail = append(m.tail[:0], combined...)
	return promptObservation{}, false
}

type waitResult struct {
	state *os.ProcessState
	err   error
}

type processWait struct {
	done   chan struct{}
	result waitResult
}

type memoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type processSampler struct {
	pid              uint32
	handle           windows.Handle
	log              *eventLog
	seenThreadID     map[uint32]struct{}
	maxThreads       uint32
	sampleCount      uint64
	threadInterval   time.Duration
	lastThreadSample time.Time
	lastThreadCount  uint32
	first            *processSample
	last             *processSample
	samples          []processSample
}

type processSample struct {
	Phase       string `json:"phase"`
	AtElapsedNS int64  `json:"at_elapsed_ns"`
	User100NS   uint64 `json:"user_time_100ns"`
	Kernel100NS uint64 `json:"kernel_time_100ns"`
	PageFaults  uint32 `json:"page_fault_count"`
	WorkingSet  uint64 `json:"working_set_bytes"`
	Pagefile    uint64 `json:"private_pagefile_bytes"`
	ReadOps     uint64 `json:"read_operations"`
	WriteOps    uint64 `json:"write_operations"`
	ReadBytes   uint64 `json:"read_bytes"`
	WriteBytes  uint64 `json:"write_bytes"`
	Handles     uint32 `json:"handle_count"`
	ThreadCount uint32 `json:"thread_count,omitempty"`
}

type processCounterDelta struct {
	SampleIntervals      uint64  `json:"sample_intervals"`
	UserCPUMS            float64 `json:"user_cpu_ms"`
	KernelCPUMS          float64 `json:"kernel_cpu_ms"`
	PageFaults           int64   `json:"page_faults"`
	WorkingSetBytes      int64   `json:"working_set_bytes"`
	PrivatePagefileBytes int64   `json:"private_pagefile_bytes"`
	ReadOperations       int64   `json:"read_operations"`
	WriteOperations      int64   `json:"write_operations"`
	ReadBytes            int64   `json:"read_bytes"`
	WriteBytes           int64   `json:"write_bytes"`
}

type runManifest struct {
	SchemaVersion    int               `json:"schema_version"`
	RunID            string            `json:"run_id"`
	StartedAtUTC     time.Time         `json:"started_at_utc"`
	HostOS           string            `json:"host_os"`
	HostArch         string            `json:"host_arch"`
	Executable       string            `json:"executable"`
	ExecutableSHA256 string            `json:"executable_sha256"`
	ExecutableBytes  int64             `json:"executable_bytes"`
	Arguments        []string          `json:"arguments"`
	WorkingDir       string            `json:"working_directory"`
	ProcessID        uint32            `json:"process_id"`
	ParentProcessID  uint32            `json:"parent_process_id"`
	PTY              map[string]any    `json:"pty"`
	Startup          map[string]any    `json:"startup"`
	TypingSeed       int64             `json:"typing_seed"`
	Schedule         []map[string]any  `json:"schedule"`
	Sampling         map[string]any    `json:"sampling"`
	Artifacts        map[string]string `json:"artifacts"`
}

type runSummary struct {
	SchemaVersion       int                            `json:"schema_version"`
	RunID               string                         `json:"run_id"`
	StartedAtUTC        time.Time                      `json:"started_at_utc"`
	FinishedAtUTC       time.Time                      `json:"finished_at_utc"`
	DurationNS          int64                          `json:"duration_ns"`
	ProcessID           uint32                         `json:"process_id"`
	ProcessExitCode     *int                           `json:"process_exit_code,omitempty"`
	ProcessExitError    string                         `json:"process_exit_error,omitempty"`
	PTYOutputBytes      uint64                         `json:"pty_output_bytes"`
	PTYOutputChunks     uint64                         `json:"pty_output_chunks"`
	ProcessSampleCount  uint64                         `json:"process_sample_count"`
	MaxThreadCount      uint32                         `json:"max_thread_count"`
	UniqueThreadIDsSeen int                            `json:"unique_thread_ids_seen"`
	ProcessCounterDelta map[string]any                 `json:"process_counter_delta,omitempty"`
	PhaseCounterDeltas  map[string]processCounterDelta `json:"phase_counter_deltas"`
	PTYByPhase          map[string]phaseIO             `json:"pty_io_by_phase"`
	Artifacts           map[string]string              `json:"artifacts"`
}

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	psapi                     = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessTimes       = kernel32.NewProc("GetProcessTimes")
	procGetProcessIoCounters  = kernel32.NewProc("GetProcessIoCounters")
	procGetProcessHandleCount = kernel32.NewProc("GetProcessHandleCount")
	procGetProcessMemoryInfo  = psapi.NewProc("GetProcessMemoryInfo")
)

func main() {
	cfg := parseFlags()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "mohabbat-next e2e:", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	cfg := config{}
	flag.StringVar(&cfg.executable, "exe", filepath.Join("..", "..", "release", "dist", "kabibi.exe"), "Executable to launch in ConPTY")
	flag.Var(&cfg.appArgs, "arg", "Argument for the target executable; may be repeated")
	flag.StringVar(&cfg.workingDir, "dir", filepath.Join("..", ".."), "Target working directory")
	flag.StringVar(&cfg.outputDir, "out-dir", filepath.Join("..", "..", "target", "mohabbat-next-e2e"), "Directory for run artifacts")
	flag.IntVar(&cfg.cols, "cols", 120, "ConPTY columns")
	flag.IntVar(&cfg.rows, "rows", 40, "ConPTY rows")
	flag.DurationVar(&cfg.startupTimeout, "startup-timeout", 90*time.Second, "Maximum time to wait for the Kabibi prompt marker")
	flag.DurationVar(&cfg.idleDuration, "idle", 2*time.Second, "Idle capture duration before typing")
	flag.StringVar(&cfg.slowText, "slow-text", "slow-type-0123456789", "Text sent one key at a time during the slow burst")
	flag.DurationVar(&cfg.slowInterval, "slow-interval", 0, "Fixed interval between slow-burst keys; 0 uses human-like cadence")
	flag.Int64Var(&cfg.typingSeed, "typing-seed", 1, "Seed for reproducible human-like typing cadence")
	flag.StringVar(&cfg.fastText, "fast-text", "FAST0123456789abcdefghijklmnopqrstuvwxyz", "Text sent one key at a time during the fast burst")
	flag.DurationVar(&cfg.fastInterval, "fast-interval", 20*time.Millisecond, "Interval between fast-burst key writes")
	flag.StringVar(&cfg.editText, "edit-text", "EDIT-12345", "Text sent and then erased during the edit burst")
	flag.DurationVar(&cfg.sampleInterval, "sample-interval", 100*time.Millisecond, "Process counter sample interval")
	flag.DurationVar(&cfg.threadInterval, "thread-sample-interval", time.Second, "Thread ID/count enumeration interval")
	flag.DurationVar(&cfg.drainDuration, "drain", time.Second, "Time to collect output after the final typing burst")
	flag.BoolVar(&cfg.cooperativeExit, "cooperative-exit", true, "Send Ctrl+C then 'exit' + Enter to trigger clean shutdown before falling back to Kill")
	flag.DurationVar(&cfg.cooperativeExitTimeout, "cooperative-exit-timeout", 10*time.Second, "Maximum wait for cooperative exit before terminating")
	flag.Parse()
	return cfg
}

func run(cfg config) (runErr error) {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	executable, err := filepath.Abs(cfg.executable)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	executableInfo, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("stat executable %q: %w", executable, err)
	}
	executableHash, err := hashFile(executable)
	if err != nil {
		return err
	}
	workDir, err := filepath.Abs(cfg.workingDir)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	if info, statErr := os.Stat(workDir); statErr != nil || !info.IsDir() {
		if statErr != nil {
			return fmt.Errorf("stat working directory %q: %w", workDir, statErr)
		}
		return fmt.Errorf("working directory is not a directory: %q", workDir)
	}
	outRoot, err := filepath.Abs(cfg.outputDir)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return fmt.Errorf("create output directory %q: %w", outRoot, err)
	}

	runID := time.Now().UTC().Format("20060102T150405.000000000Z")
	runDir := filepath.Join(outRoot, runID)
	if err := os.Mkdir(runDir, 0o755); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	eventsPath := filepath.Join(runDir, "events.jsonl")
	manifestPath := filepath.Join(runDir, "manifest.json")
	summaryPath := filepath.Join(runDir, "summary.json")

	startedAt := time.Now()
	log, err := openEventLog(eventsPath, runID, startedAt)
	if err != nil {
		return err
	}
	logClosed := false
	defer func() {
		if !logClosed {
			if closeErr := log.close(); closeErr != nil && runErr == nil {
				runErr = closeErr
			}
		}
	}()

	cmd := exec.Command(executable, cfg.appArgs...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	terminal, err := pty.Start(cmd, pty.Options{Cols: cfg.cols, Rows: cfg.rows, Term: "xterm-256color"})
	if err != nil {
		return fmt.Errorf("start target in ConPTY: %w", err)
	}
	pid := uint32(cmd.Process.Pid)
	if err := log.emit("process.started", "harness", "os/exec.Cmd.Start + ConPTY CreateProcess", pid, map[string]any{
		"executable": executable, "arguments": cfg.appArgs, "working_directory": workDir,
		"parent_process_id": uint32(os.Getpid()),
	}); err != nil {
		_ = terminal.Close()
		_ = cmd.Process.Kill()
		return err
	}
	fmt.Printf("Started %s (PID %d) in ConPTY; writing diagnostics to %s\n", executable, pid, runDir)

	readState := &outputReader{
		done:        make(chan error, 1),
		promptReady: make(chan promptObservation, 1),
	}
	readerDone := make(chan struct{})
	go readPTY(terminal, pid, log, readState, readerDone)

	processState := &processWait{done: make(chan struct{})}
	go func() {
		state, waitErr := cmd.Process.Wait()
		processState.result = waitResult{state: state, err: waitErr}
		close(processState.done)
	}()

	cleaned := false
	defer func() {
		if !cleaned {
			_ = terminal.Close()
			select {
			case <-processState.done:
			default:
				_ = cmd.Process.Kill()
				<-processState.done
			}
			<-readerDone
		}
	}()

	prompt, err := waitForPrompt(readState, processState, cfg.startupTimeout)
	if err != nil {
		return err
	}
	if err := log.emit("startup.prompt_ready", "harness", "ConPTY UTF-8 prompt marker", pid, map[string]any{
		"marker":                 promptReadyMarker,
		"unicode_code_point":     "U+1367",
		"output_bytes_seen":      prompt.OutputBytes,
		"output_chunk_sequence":  prompt.ChunkSequence,
		"marker_observed_at_utc": prompt.AtUTC,
	}); err != nil {
		return err
	}

	sampler, err := newProcessSampler(pid, log)
	if err != nil {
		return err
	}
	sampler.threadInterval = cfg.threadInterval
	defer windows.CloseHandle(sampler.handle)

	manifest := makeManifest(runID, startedAt, executable, executableHash, executableInfo.Size(), cfg, workDir, pid, eventsPath, summaryPath)
	if err := writeJSON(manifestPath, manifest); err != nil {
		return err
	}

	captureStart := time.Now()
	log.setPhase("capture.idle")
	if err := log.emit("capture.started", "harness", "capture phase boundary", pid, nil); err != nil {
		return err
	}
	stopSampling := make(chan struct{})
	samplingDone := make(chan struct{})
	go sampleProcess(sampler, cfg.sampleInterval, stopSampling, samplingDone)
	samplingStopped := false
	defer func() {
		if !samplingStopped {
			close(stopSampling)
			<-samplingDone
		}
	}()

	if err := sleepUntilExit(cfg.idleDuration, processState); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "capture.idle", "finished", map[string]any{
		"duration_ns": cfg.idleDuration.Nanoseconds(),
	}); err != nil {
		return err
	}

	if err := phaseMarker(log, pid, "typing.slow", "started", map[string]any{
		"input_bytes": len(cfg.slowText), "input_interval_ns": cfg.slowInterval.Nanoseconds(),
		"typing_model": typingModel(cfg.slowInterval),
	}); err != nil {
		return err
	}
	typingRNG := rand.New(rand.NewSource(cfg.typingSeed))
	if err := sendText(terminal, log, pid, "typing.slow", cfg.slowText, cfg.slowInterval, typingRNG, processState); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "typing.slow", "finished", map[string]any{"input_bytes": len(cfg.slowText)}); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "capture.between_bursts", "started", nil); err != nil {
		return err
	}
	if err := sleepUntilExit(500*time.Millisecond, processState); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "capture.between_bursts", "finished", nil); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "typing.fast", "started", map[string]any{
		"input_bytes": len(cfg.fastText), "input_interval_ns": cfg.fastInterval.Nanoseconds(),
	}); err != nil {
		return err
	}
	if err := sendText(terminal, log, pid, "typing.fast", cfg.fastText, cfg.fastInterval, typingRNG, processState); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "typing.fast", "finished", map[string]any{"input_bytes": len(cfg.fastText)}); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "capture.between_bursts", "started", nil); err != nil {
		return err
	}
	if err := sleepUntilExit(500*time.Millisecond, processState); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "capture.between_bursts", "finished", nil); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "typing.edit", "started", map[string]any{
		"input_bytes": len(cfg.editText), "erase_byte": "0x7f",
	}); err != nil {
		return err
	}
	if err := sendText(terminal, log, pid, "typing.edit", cfg.editText, 0, typingRNG, processState); err != nil {
		return err
	}
	for i := 0; i < len(cfg.editText); i++ {
		previous := byte(0x7f)
		if i == 0 {
			previous = cfg.editText[len(cfg.editText)-1]
		}
		delay := humanKeyDelay(previous, 0x7f, typingRNG)
		if err := sleepUntilExit(delay, processState); err != nil {
			return err
		}
		if err := sendInput(terminal, log, pid, "typing.edit", []byte{0x7f}, map[string]any{
			"key_name": "DEL", "edit_index": i, "purpose": "erase edit burst text",
			"delay_before_ns": delay.Nanoseconds(), "typing_model": "human-like-qwerty-cadence",
		}); err != nil {
			return err
		}
	}
	if err := phaseMarker(log, pid, "typing.edit", "finished", map[string]any{
		"typed_bytes": len(cfg.editText), "erase_bytes": len(cfg.editText),
	}); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "capture.drain", "started", map[string]any{
		"duration_ns": cfg.drainDuration.Nanoseconds(),
	}); err != nil {
		return err
	}
	if err := sleepUntilExit(cfg.drainDuration, processState); err != nil {
		return err
	}
	if err := phaseMarker(log, pid, "capture.drain", "finished", nil); err != nil {
		return err
	}
	log.setPhase("capture.stopping")
	if err := log.emit("capture.finished", "harness", "capture phase boundary", pid, map[string]any{
		"capture_duration_ns": time.Since(captureStart).Nanoseconds(),
	}); err != nil {
		return err
	}
	close(stopSampling)
	<-samplingDone
	samplingStopped = true

	log.setPhase("shutdown")
	var result waitResult
	exited := false
	select {
	case <-processState.done:
		result = processState.result
		exited = true
	default:
	}
	if !exited && cfg.cooperativeExit {
		if err := log.emit("process.cooperative_exit_requested", "harness", "mohabbat/pty.PTY.Write", pid, map[string]any{
			"sequence": []string{"ctrl+c", "exit\\r"},
			"timeout":  cfg.cooperativeExitTimeout.String(),
		}); err != nil {
			return err
		}
		_, _ = terminal.Write([]byte{0x03})
		time.Sleep(50 * time.Millisecond)
		for _, b := range []byte("exit\r") {
			if _, werr := terminal.Write([]byte{b}); werr != nil {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		timer := time.NewTimer(cfg.cooperativeExitTimeout)
		select {
		case <-processState.done:
			timer.Stop()
			result = processState.result
			exited = true
		case <-timer.C:
		}
	}
	if !exited {
		if err := log.emit("process.terminate_requested", "harness", "os.Process.Kill", pid, map[string]any{
			"reason": "terminate target after completing the capture",
		}); err != nil {
			return err
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("terminate target after capture: %w", err)
		}
		<-processState.done
		result = processState.result
	}
	_ = terminal.Close()
	<-readerDone
	cleaned = true

	exitData := map[string]any{}
	if result.state != nil {
		exitData["exit_code"] = result.state.ExitCode()
	}
	if result.err != nil {
		exitData["wait_error"] = result.err.Error()
	}
	if err := log.emit("process.exited", "os", "os.Process.Wait", pid, exitData); err != nil {
		return err
	}
	summary := makeSummary(runID, startedAt, pid, result, readState, sampler, log, eventsPath, manifestPath, summaryPath)
	if err := writeJSON(summaryPath, summary); err != nil {
		return err
	}
	if err := log.emit("run.finished", "harness", "run summary writer", pid, map[string]any{
		"summary_path": summaryPath, "events_path": eventsPath,
	}); err != nil {
		return err
	}
	if err := log.close(); err != nil {
		logClosed = true
		return err
	}
	logClosed = true

	fmt.Printf("Completed capture: %s\n", runDir)
	fmt.Printf("PTY output: %d bytes in %d chunks; process samples: %d; max threads: %d\n",
		summary.PTYOutputBytes, summary.PTYOutputChunks, summary.ProcessSampleCount, summary.MaxThreadCount)
	return result.err
}

func validateConfig(cfg config) error {
	if cfg.cols <= 0 || cfg.rows <= 0 || cfg.cols > 32767 || cfg.rows > 32767 {
		return fmt.Errorf("PTY dimensions must be in [1,32767]")
	}
	if cfg.startupTimeout <= 0 {
		return fmt.Errorf("startup timeout must be positive")
	}
	if cfg.idleDuration < 0 || cfg.slowInterval < 0 || cfg.fastInterval < 0 ||
		cfg.sampleInterval <= 0 || cfg.threadInterval <= 0 || cfg.drainDuration < 0 {
		return fmt.Errorf("durations must be nonnegative and sample intervals must be positive")
	}
	for label, text := range map[string]string{
		"slow-text": cfg.slowText, "fast-text": cfg.fastText, "edit-text": cfg.editText,
	} {
		if text == "" {
			return fmt.Errorf("%s must not be empty", label)
		}
		for i := 0; i < len(text); i++ {
			if text[i] < 0x20 || text[i] > 0x7e {
				return fmt.Errorf("%s must contain printable ASCII bytes only", label)
			}
		}
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open executable for hashing: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash executable: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readPTY(terminal pty.PTY, pid uint32, log *eventLog, state *outputReader, done chan<- struct{}) {
	defer close(done)
	buffer := make([]byte, 32*1024)
	for {
		n, err := terminal.Read(buffer)
		if n > 0 {
			now := time.Now()
			seq := atomic.AddUint64(&state.sequence, 1)
			outputBytes := atomic.AddUint64(&state.bytes, uint64(n))
			data := map[string]any{
				"chunk_sequence": seq,
				"byte_count":     n,
				"bytes_base64":   base64.StdEncoding.EncodeToString(buffer[:n]),
				"read_api":       "PTY.Read -> ConPTY output pipe",
			}
			if utf8.Valid(buffer[:n]) {
				data["text_utf8"] = string(buffer[:n])
			}
			if emitErr := log.emit("pty.output", "conpty", "mohabbat/pty.PTY.Read", pid, data); emitErr != nil {
				_ = log.emit("harness.error", "harness", "eventLog.emit", pid, map[string]any{"error": emitErr.Error()})
			}
			if observation, found := state.promptMatcher.observe(buffer[:n], outputBytes, seq, now); found {
				state.promptReady <- observation
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = log.emit("pty.read_error", "conpty", "mohabbat/pty.PTY.Read", pid, map[string]any{"error": err.Error()})
			}
			state.done <- err
			return
		}
	}
}

func waitForPrompt(state *outputReader, process *processWait, timeout time.Duration) (promptObservation, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	select {
	case observation := <-state.promptReady:
		return observation, nil
	case <-process.done:
		result := process.result
		return promptObservation{}, fmt.Errorf("target exited before prompt marker %q appeared (state=%v, error=%v)", promptReadyMarker, result.state, result.err)
	case err := <-state.done:
		return promptObservation{}, fmt.Errorf("PTY output ended before prompt marker %q appeared: %v", promptReadyMarker, err)
	case <-deadline.C:
		return promptObservation{}, fmt.Errorf("prompt marker %q was not observed within %s", promptReadyMarker, timeout)
	}
}

func sendText(terminal pty.PTY, log *eventLog, pid uint32, phase, text string, interval time.Duration, rng *rand.Rand, process *processWait) error {
	log.setPhase(phase)
	for i := 0; i < len(text); i++ {
		delay := interval
		if interval == 0 && i > 0 {
			delay = humanKeyDelay(text[i-1], text[i], rng)
		}
		if err := sleepUntilExit(delay, process); err != nil {
			return err
		}
		char := []byte{text[i]}
		if err := sendInput(terminal, log, pid, phase, char, map[string]any{
			"key_index":                             i,
			"key_byte":                              text[i],
			"key_name":                              printableKey(text[i]),
			"burst_length_bytes":                    len(text),
			"delay_before_ns":                       delay.Nanoseconds(),
			"typing_model":                          typingModel(interval),
			"write_completion_is_not_a_screen_echo": true,
		}); err != nil {
			return err
		}
	}
	return nil
}

func phaseMarker(log *eventLog, pid uint32, phase, boundary string, details map[string]any) error {
	log.setPhase(phase)
	return log.emit("phase."+boundary, "harness", "workload schedule", pid, details)
}

func sendInput(terminal pty.PTY, log *eventLog, pid uint32, phase string, data []byte, details map[string]any) error {
	log.setPhase(phase)
	started := time.Now()
	n, err := terminal.Write(data)
	completed := time.Now()
	fields := map[string]any{
		"input_sequence":      atomic.AddUint64(&inputSequence, 1),
		"byte_count":          len(data),
		"bytes_base64":        base64.StdEncoding.EncodeToString(data),
		"write_return_bytes":  n,
		"write_duration_ns":   completed.Sub(started).Nanoseconds(),
		"input_started_utc":   started.UTC(),
		"input_completed_utc": completed.UTC(),
		"write_api":           "PTY.Write -> ConPTY input pipe",
		"causal_note":         "This timestamps delivery into ConPTY, not guest key handling or a verified visual repaint.",
	}
	for key, value := range details {
		fields[key] = value
	}
	if emitErr := log.emit("pty.input", "conpty", "mohabbat/pty.PTY.Write", pid, fields); emitErr != nil {
		return emitErr
	}
	if err != nil {
		return fmt.Errorf("write PTY input: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("short PTY write: wrote %d of %d bytes", n, len(data))
	}
	return nil
}

func typingModel(interval time.Duration) string {
	if interval > 0 {
		return "fixed-interval"
	}
	return "human-like-qwerty-cadence"
}

func humanKeyDelay(previous, current byte, rng *rand.Rand) time.Duration {
	switch {
	case previous == 0x7f:
		return randomDuration(rng, 75*time.Millisecond, 140*time.Millisecond)
	case current == 0x7f:
		return randomDuration(rng, 90*time.Millisecond, 170*time.Millisecond)
	case strings.ContainsRune(".,?!;:", rune(previous)):
		return randomDuration(rng, 220*time.Millisecond, 550*time.Millisecond)
	case previous == ' ':
		return randomDuration(rng, 80*time.Millisecond, 150*time.Millisecond)
	}

	previousHand, previousOK := qwertyHand(previous)
	currentHand, currentOK := qwertyHand(current)
	if previousOK && currentOK && previousHand != currentHand {
		return randomDuration(rng, 45*time.Millisecond, 95*time.Millisecond)
	}
	return randomDuration(rng, 110*time.Millisecond, 210*time.Millisecond)
}

func randomDuration(rng *rand.Rand, min, max time.Duration) time.Duration {
	return min + time.Duration(rng.Int63n(int64(max-min)+1))
}

func qwertyHand(key byte) (hand byte, ok bool) {
	if key >= 'A' && key <= 'Z' {
		key += 'a' - 'A'
	}
	switch {
	case strings.ContainsRune("qaz1", rune(key)):
		return 0, true
	case strings.ContainsRune("wsx2", rune(key)):
		return 0, true
	case strings.ContainsRune("edc3", rune(key)):
		return 0, true
	case strings.ContainsRune("rfvtgb45", rune(key)):
		return 0, true
	case strings.ContainsRune("yhnujm67", rune(key)):
		return 1, true
	case strings.ContainsRune("ik8", rune(key)):
		return 1, true
	case strings.ContainsRune("ol9", rune(key)):
		return 1, true
	case strings.ContainsRune("p;0-=[]'\\/", rune(key)):
		return 1, true
	default:
		return 0, false
	}
}

var inputSequence uint64

func printableKey(b byte) string {
	if b >= 0x20 && b <= 0x7e {
		return string(b)
	}
	return fmt.Sprintf("0x%02x", b)
}

func sleepUntilExit(duration time.Duration, process *processWait) error {
	if duration == 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-process.done:
		result := process.result
		return fmt.Errorf("target exited during capture (state=%v, error=%v)", result.state, result.err)
	}
}

func newProcessSampler(pid uint32, log *eventLog) (*processSampler, error) {
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ,
		false,
		pid,
	)
	if err != nil {
		return nil, fmt.Errorf("open target process %d for counters: %w", pid, err)
	}
	if err := log.emit("process.sampling_started", "harness", "OpenProcess(PROCESS_QUERY_INFORMATION|PROCESS_VM_READ)", pid, map[string]any{
		"sample_sources": []string{
			"kernel32!GetProcessTimes",
			"kernel32!GetProcessIoCounters",
			"kernel32!GetProcessHandleCount",
			"psapi!GetProcessMemoryInfo",
			"kernel32!CreateToolhelp32Snapshot(TH32CS_SNAPTHREAD)",
		},
	}); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &processSampler{
		pid:          pid,
		handle:       handle,
		log:          log,
		seenThreadID: make(map[uint32]struct{}),
	}, nil
}

func sampleProcess(sampler *processSampler, interval time.Duration, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		sampler.sample()
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func (s *processSampler) sample() {
	start := time.Now()
	data := map[string]any{
		"sample_started_utc": start.UTC(),
	}
	var errorsByCounter []string
	sample := processSample{
		Phase:       s.log.currentPhase(),
		AtElapsedNS: time.Since(s.log.start).Nanoseconds(),
	}

	var creation, exit, kernel, user windows.Filetime
	r1, _, callErr := procGetProcessTimes.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r1 == 0 {
		errorsByCounter = append(errorsByCounter, "GetProcessTimes: "+callError(callErr).Error())
	} else {
		sample.User100NS = filetimeValue(user)
		sample.Kernel100NS = filetimeValue(kernel)
	}

	var io ioCounters
	r1, _, callErr = procGetProcessIoCounters.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&io)))
	if r1 == 0 {
		errorsByCounter = append(errorsByCounter, "GetProcessIoCounters: "+callError(callErr).Error())
	} else {
		sample.ReadOps = io.ReadOperationCount
		sample.WriteOps = io.WriteOperationCount
		sample.ReadBytes = io.ReadTransferCount
		sample.WriteBytes = io.WriteTransferCount
	}

	var handles uint32
	r1, _, callErr = procGetProcessHandleCount.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&handles)))
	if r1 == 0 {
		errorsByCounter = append(errorsByCounter, "GetProcessHandleCount: "+callError(callErr).Error())
	} else {
		sample.Handles = handles
	}

	memory := memoryCounters{Size: uint32(unsafe.Sizeof(memoryCounters{}))}
	r1, _, callErr = procGetProcessMemoryInfo.Call(
		uintptr(s.handle),
		uintptr(unsafe.Pointer(&memory)),
		uintptr(memory.Size),
	)
	if r1 == 0 {
		errorsByCounter = append(errorsByCounter, "GetProcessMemoryInfo: "+callError(callErr).Error())
	} else {
		sample.PageFaults = memory.PageFaultCount
		sample.WorkingSet = uint64(memory.WorkingSetSize)
		sample.Pagefile = uint64(memory.PagefileUsage)
	}

	threadSampleDue := s.lastThreadSample.IsZero() || time.Since(s.lastThreadSample) >= s.threadInterval
	data["thread_sample_due"] = threadSampleDue
	if threadSampleDue {
		threadSnapshotStart := time.Now()
		threadIDs, threadErr := enumerateThreads(s.pid)
		data["thread_snapshot_duration_ns"] = time.Since(threadSnapshotStart).Nanoseconds()
		s.lastThreadSample = time.Now()
		if threadErr != nil {
			errorsByCounter = append(errorsByCounter, "thread snapshot: "+threadErr.Error())
		} else {
			s.lastThreadCount = uint32(len(threadIDs))
			sample.ThreadCount = s.lastThreadCount
			sort.Slice(threadIDs, func(i, j int) bool { return threadIDs[i] < threadIDs[j] })
			newIDs := make([]uint32, 0, len(threadIDs))
			for _, id := range threadIDs {
				if _, found := s.seenThreadID[id]; !found {
					s.seenThreadID[id] = struct{}{}
					newIDs = append(newIDs, id)
				}
			}
			if sample.ThreadCount > s.maxThreads {
				s.maxThreads = sample.ThreadCount
			}
			data["thread_ids"] = threadIDs
			data["new_thread_ids_first_seen_this_sample"] = newIDs
			data["unique_thread_ids_seen_total"] = len(s.seenThreadID)
		}
	} else {
		data["thread_count_last_observed"] = s.lastThreadCount
		data["thread_observation_age_ns"] = time.Since(s.lastThreadSample).Nanoseconds()
	}

	sample.AtElapsedNS = time.Since(s.log.start).Nanoseconds()
	data["sample_completed_utc"] = time.Now().UTC()
	data["sample_duration_ns"] = time.Since(start).Nanoseconds()
	data["sample_phase"] = sample.Phase
	data["sample"] = sample
	if len(errorsByCounter) > 0 {
		data["counter_errors"] = errorsByCounter
	}
	if s.first == nil {
		first := sample
		s.first = &first
	}
	last := sample
	s.last = &last
	s.samples = append(s.samples, sample)
	if err := s.log.emit("process.sample", "windows.process", "Win32 process counters", s.pid, data); err != nil {
		_ = s.log.emit("harness.error", "harness", "eventLog.emit", s.pid, map[string]any{"error": err.Error()})
	}
	s.sampleCount++
}

func (s *processSampler) phaseCounterDeltas() map[string]processCounterDelta {
	deltas := make(map[string]processCounterDelta)
	for i := 1; i < len(s.samples); i++ {
		previous, current := s.samples[i-1], s.samples[i]
		if previous.Phase != current.Phase {
			continue
		}
		delta := deltas[current.Phase]
		delta.SampleIntervals++
		delta.UserCPUMS += nonnegativeCounterDelta(current.User100NS, previous.User100NS) / 10000
		delta.KernelCPUMS += nonnegativeCounterDelta(current.Kernel100NS, previous.Kernel100NS) / 10000
		delta.PageFaults += int64(current.PageFaults) - int64(previous.PageFaults)
		delta.WorkingSetBytes += int64(current.WorkingSet) - int64(previous.WorkingSet)
		delta.PrivatePagefileBytes += int64(current.Pagefile) - int64(previous.Pagefile)
		delta.ReadOperations += int64(current.ReadOps) - int64(previous.ReadOps)
		delta.WriteOperations += int64(current.WriteOps) - int64(previous.WriteOps)
		delta.ReadBytes += int64(current.ReadBytes) - int64(previous.ReadBytes)
		delta.WriteBytes += int64(current.WriteBytes) - int64(previous.WriteBytes)
		deltas[current.Phase] = delta
	}
	return deltas
}

func nonnegativeCounterDelta(current, previous uint64) float64 {
	if current < previous {
		return 0
	}
	return float64(current - previous)
}

func enumerateThreads(pid uint32) ([]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
			return []uint32{}, nil
		}
		return nil, fmt.Errorf("Thread32First: %w", err)
	}
	ids := make([]uint32, 0, 16)
	for {
		if entry.OwnerProcessID == pid {
			ids = append(ids, entry.ThreadID)
		}
		entry.Size = uint32(unsafe.Sizeof(windows.ThreadEntry32{}))
		err = windows.Thread32Next(snapshot, &entry)
		if err != nil {
			if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("Thread32Next: %w", err)
		}
	}
	return ids, nil
}

func filetimeValue(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}

func callError(err error) error {
	if err != nil && !errors.Is(err, syscall.Errno(0)) {
		return err
	}
	return syscall.GetLastError()
}

func makeManifest(runID string, startedAt time.Time, executable, executableHash string, executableBytes int64, cfg config, workDir string, pid uint32, eventsPath, summaryPath string) runManifest {
	parentPID := uint32(os.Getpid())
	schedule := []map[string]any{
		{"phase": "capture.idle", "duration_ns": cfg.idleDuration.Nanoseconds(), "purpose": "idle baseline before synthetic typing"},
		{"phase": "typing.slow", "text": cfg.slowText, "input_interval_ns": cfg.slowInterval.Nanoseconds(), "typing_model": typingModel(cfg.slowInterval), "purpose": "paced single-byte typing"},
		{"phase": "typing.fast", "text": cfg.fastText, "input_interval_ns": cfg.fastInterval.Nanoseconds(), "purpose": "rapid single-byte typing"},
		{"phase": "typing.edit", "text": cfg.editText, "erase_byte": "0x7f", "typing_model": "human-like-qwerty-cadence", "purpose": "type then backspace a short string"},
		{"phase": "capture.drain", "duration_ns": cfg.drainDuration.Nanoseconds(), "purpose": "observe completion/redraw backlog after final key"},
	}
	return runManifest{
		SchemaVersion:    schemaVersion,
		RunID:            runID,
		StartedAtUTC:     startedAt.UTC(),
		HostOS:           runtime.GOOS,
		HostArch:         runtime.GOARCH,
		Executable:       executable,
		ExecutableSHA256: executableHash,
		ExecutableBytes:  executableBytes,
		Arguments:        append([]string{}, cfg.appArgs...),
		WorkingDir:       workDir,
		ProcessID:        pid,
		ParentProcessID:  parentPID,
		PTY: map[string]any{
			"implementation": "mohabbat/pty",
			"transport":      "Windows ConPTY",
			"cols":           cfg.cols,
			"rows":           cfg.rows,
			"term":           "xterm-256color",
		},
		Startup: map[string]any{
			"readiness_marker":     promptReadyMarker,
			"unicode_code_point":   "U+1367",
			"timeout_ns":           cfg.startupTimeout.Nanoseconds(),
			"post_ready_idle_ns":   cfg.idleDuration.Nanoseconds(),
			"marker_source":        "UTF-8 bytes observed in ConPTY output",
			"input_sent_before_it": false,
		},
		TypingSeed: cfg.typingSeed,
		Schedule:   schedule,
		Sampling: map[string]any{
			"interval_ns": cfg.sampleInterval.Nanoseconds(),
			"process_id":  pid,
			"metrics": []string{
				"cumulative user/kernel CPU time (100-ns units)",
				"cumulative page faults",
				"working set and private pagefile bytes",
				"cumulative process I/O operations and bytes",
				"process handle count",
				"thread IDs/count and unique IDs seen",
			},
			"thread_sample_interval_ns": cfg.threadInterval.Nanoseconds(),
			"sampling_overhead_note":    "Process samples run in a harness goroutine; each record includes total query duration. Thread enumeration uses its own slower cadence and records its individual duration.",
		},
		Artifacts: map[string]string{
			"events_jsonl":  eventsPath,
			"manifest_json": filepath.Join(filepath.Dir(eventsPath), "manifest.json"),
			"summary_json":  summaryPath,
		},
	}
}

func makeSummary(runID string, startedAt time.Time, pid uint32, result waitResult, output *outputReader, sampler *processSampler, log *eventLog, eventsPath, manifestPath, summaryPath string) runSummary {
	summary := runSummary{
		SchemaVersion:       schemaVersion,
		RunID:               runID,
		StartedAtUTC:        startedAt.UTC(),
		FinishedAtUTC:       time.Now().UTC(),
		DurationNS:          time.Since(startedAt).Nanoseconds(),
		ProcessID:           pid,
		ProcessSampleCount:  sampler.sampleCount,
		MaxThreadCount:      sampler.maxThreads,
		UniqueThreadIDsSeen: len(sampler.seenThreadID),
		PhaseCounterDeltas:  sampler.phaseCounterDeltas(),
		PTYByPhase:          log.phaseIOSnapshot(),
		Artifacts: map[string]string{
			"events_jsonl":  eventsPath,
			"manifest_json": manifestPath,
			"summary_json":  summaryPath,
		},
	}
	summary.PTYOutputBytes = atomic.LoadUint64(&output.bytes)
	summary.PTYOutputChunks = atomic.LoadUint64(&output.sequence)
	if sampler.first != nil && sampler.last != nil {
		first, last := sampler.first, sampler.last
		summary.ProcessCounterDelta = map[string]any{
			"user_cpu_ms":            float64(last.User100NS-first.User100NS) / 10000,
			"kernel_cpu_ms":          float64(last.Kernel100NS-first.Kernel100NS) / 10000,
			"page_faults":            int64(last.PageFaults) - int64(first.PageFaults),
			"working_set_bytes":      int64(last.WorkingSet) - int64(first.WorkingSet),
			"private_pagefile_bytes": int64(last.Pagefile) - int64(first.Pagefile),
			"read_operations":        int64(last.ReadOps) - int64(first.ReadOps),
			"write_operations":       int64(last.WriteOps) - int64(first.WriteOps),
			"read_bytes":             int64(last.ReadBytes) - int64(first.ReadBytes),
			"write_bytes":            int64(last.WriteBytes) - int64(first.WriteBytes),
		}
	}
	if result.state != nil {
		code := result.state.ExitCode()
		summary.ProcessExitCode = &code
	}
	if result.err != nil {
		summary.ProcessExitError = result.err.Error()
	}
	return summary
}

func writeJSON(path string, value any) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
