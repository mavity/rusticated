package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"ai"
)

const vscodeJobIdleLease = 10 * time.Minute

type vscodeToolRequest struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type vscodeToolResponse struct {
	Type      string `json:"type,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Result    any    `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

type vscodeToolJob struct {
	session  *ExecutionSession
	managed  *ManagedPTY
	localID  int
	lastUsed time.Time
}

// VSCodeToolServer provides one-shot exec sessions and routes term_* calls to
// the ejected PTY they identify. Completed exec sessions are not reused.
type VSCodeToolServer struct {
	mu        sync.Mutex
	jobs      map[int]*vscodeToolJob
	nextID    int
	cwd       string
	idleLease time.Duration
}

func NewVSCodeToolServer(cwd string, idleLease time.Duration) *VSCodeToolServer {
	if idleLease <= 0 {
		idleLease = vscodeJobIdleLease
	}
	return &VSCodeToolServer{
		jobs:      make(map[int]*vscodeToolJob),
		nextID:    1,
		cwd:       cwd,
		idleLease: idleLease,
	}
}

// RunVSCodeToolServer serves VS Code tool calls until stdin closes.
func RunVSCodeToolServer(ctx context.Context, input io.Reader, output io.Writer, cwd string) error {
	return NewVSCodeToolServer(cwd, vscodeJobIdleLease).Serve(ctx, input, output)
}

// Dispatch executes one tool request and returns its structured result.
func (s *VSCodeToolServer) Dispatch(ctx context.Context, name string, arguments json.RawMessage) (any, error) {
	switch name {
	case "exec":
		return s.exec(ctx, arguments)
	case "term_read", "term_input", "term_kill":
		return s.term(ctx, name, arguments)
	default:
		return nil, fmt.Errorf("unknown Kabibi terminal tool %q", name)
	}
}

func (s *VSCodeToolServer) exec(ctx context.Context, arguments json.RawMessage) (any, error) {
	session, err := NewExecutionSession(s.cwd)
	if err != nil {
		return nil, fmt.Errorf("initialize shell execution: %w", err)
	}
	session.SetAIEnabled(false)
	tool := BuildKabibiTools(session)["exec"]
	result, err := tool.Execute(ctx, ai.ToolCall{
		Name:         "exec",
		RawArguments: nonEmptyJSON(arguments),
	}, nil)
	if err != nil {
		return nil, closeSessionAfterError(session, err)
	}
	resultMap, ok := result.(map[string]any)
	if !ok {
		return nil, closeSessionAfterError(session, fmt.Errorf("exec returned unexpected result type %T", result))
	}
	if resultMap["still_running"] != true {
		if closeErr := session.Close(); closeErr != nil {
			return nil, fmt.Errorf("close completed shell execution: %w", closeErr)
		}
		return resultMap, nil
	}

	localID, ok := resultMap["pid_id"].(int)
	if !ok || localID <= 0 {
		return nil, closeSessionAfterError(session, fmt.Errorf("exec returned invalid background pid_id %v", resultMap["pid_id"]))
	}
	managed, ok := session.LookupManagedPTY(localID)
	if !ok {
		return nil, closeSessionAfterError(session, fmt.Errorf("background process %d disappeared before registration", localID))
	}

	s.mu.Lock()
	visibleID, allocErr := s.allocateIDLocked()
	var job *vscodeToolJob
	if allocErr == nil {
		job = &vscodeToolJob{
			session:  session,
			managed:  managed,
			localID:  localID,
			lastUsed: time.Now(),
		}
		s.jobs[visibleID] = job
	}
	s.mu.Unlock()
	if allocErr != nil {
		var cleanupErrs []error
		if err := managed.Kill("SIGTERM"); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("terminate unregistered process %d: %w", localID, err))
		}
		if err := session.Close(); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("close unregistered shell session %d: %w", localID, err))
		}
		return nil, errors.Join(append([]error{allocErr}, cleanupErrs...)...)
	}

	resultMap["pid_id"] = visibleID
	remapToolResultText(resultMap, localID, visibleID)
	go s.releaseSessionOnExit(job)
	return resultMap, nil
}

func (s *VSCodeToolServer) term(ctx context.Context, name string, arguments json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var idInput struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(nonEmptyJSON(arguments), &idInput); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", name, err)
	}
	if idInput.ID <= 0 {
		return nil, errors.New("job id must be a positive integer")
	}

	s.mu.Lock()
	job, ok := s.jobs[idInput.ID]
	if ok {
		job.lastUsed = time.Now()
	}
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("managed process id=%d not found", idInput.ID)
	}

	localArgs, err := remapToolArguments(arguments, job.localID)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	switch name {
	case "term_read":
		var input TermReadInput
		if err := json.Unmarshal(localArgs, &input); err != nil {
			return nil, fmt.Errorf("decode term_read arguments: %w", err)
		}
		result, err = readManagedPTY(ctx, job.managed, idInput.ID, input)
	case "term_input":
		var input TermInputInput
		if err := json.Unmarshal(localArgs, &input); err != nil {
			return nil, fmt.Errorf("decode term_input arguments: %w", err)
		}
		result, err = inputManagedPTY(ctx, job.managed, idInput.ID, input)
	case "term_kill":
		var input TermKillInput
		if err := json.Unmarshal(localArgs, &input); err != nil {
			return nil, fmt.Errorf("decode term_kill arguments: %w", err)
		}
		result, err = killManagedPTY(job.managed, idInput.ID, input)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *VSCodeToolServer) allocateIDLocked() (int, error) {
	maxInt := int(^uint(0) >> 1)
	for attempts := 0; attempts <= len(s.jobs); attempts++ {
		id := s.nextID
		if id <= 0 {
			id = 1
		}
		if id == maxInt {
			s.nextID = 1
		} else {
			s.nextID = id + 1
		}
		if _, exists := s.jobs[id]; !exists {
			return id, nil
		}
	}
	return 0, errors.New("no available tool-visible job IDs")
}

func (s *VSCodeToolServer) releaseSessionOnExit(job *vscodeToolJob) {
	<-job.managed.doneCh
	if err := s.closeJobSession(job); err != nil {
		fmt.Fprintf(os.Stderr, "kabibi: close completed shell session %d: %v\n", job.localID, err)
	}
}

func closeSessionAfterError(session *ExecutionSession, cause error) error {
	if err := session.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close shell session: %w", err))
	}
	return cause
}

func (s *VSCodeToolServer) closeJobSession(job *vscodeToolJob) error {
	s.mu.Lock()
	session := job.session
	job.session = nil
	s.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	return nil
}

func (s *VSCodeToolServer) Sweep(now time.Time) error {
	s.mu.Lock()
	expired := make([]struct {
		id  int
		job *vscodeToolJob
	}, 0)
	for id, job := range s.jobs {
		if now.Sub(job.lastUsed) >= s.idleLease {
			expired = append(expired, struct {
				id  int
				job *vscodeToolJob
			}{id: id, job: job})
			delete(s.jobs, id)
		}
	}
	s.mu.Unlock()
	var cleanupErrors []error
	for _, entry := range expired {
		if err := entry.job.managed.Kill("SIGTERM"); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("terminate expired managed process %d: %w", entry.job.localID, err))
		}
		if err := s.closeJobSession(entry.job); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("close expired shell session %d: %w", entry.job.localID, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (s *VSCodeToolServer) Close() error {
	s.mu.Lock()
	jobs := make([]*vscodeToolJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	clear(s.jobs)
	s.mu.Unlock()

	var closeErrors []error
	for _, job := range jobs {
		if err := job.managed.Kill("SIGTERM"); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("terminate managed process %d: %w", job.localID, err))
		}
		if err := s.closeJobSession(job); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close shell session %d: %w", job.localID, err))
		}
	}
	return errors.Join(closeErrors...)
}

func (s *VSCodeToolServer) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if input == nil || output == nil {
		return errors.New("tool server requires input and output streams")
	}
	var outputMu sync.Mutex
	writeResponse := func(response vscodeToolResponse) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		return json.NewEncoder(output).Encode(response)
	}
	if err := writeResponse(vscodeToolResponse{Type: "ready"}); err != nil {
		return fmt.Errorf("write tool server ready message: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var requestsMu sync.Mutex
	requests := make(map[string]context.CancelFunc)
	var requestsWG sync.WaitGroup

	sweepDone := make(chan struct{})
	defer close(sweepDone)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-sweepDone:
				return
			case <-runCtx.Done():
				return
			case now := <-ticker.C:
				if err := s.Sweep(now); err != nil {
					fmt.Fprintf(os.Stderr, "kabibi: idle job cleanup: %v\n", err)
				}
			}
		}
	}()

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var request vscodeToolRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if writeErr := writeResponse(vscodeToolResponse{Error: fmt.Sprintf("invalid tool request: %v", err)}); writeErr != nil {
				cancel()
				requestsWG.Wait()
				return errors.Join(fmt.Errorf("decode tool request: %w", err), writeErr, s.Close())
			}
			continue
		}
		if request.Type == "cancel" {
			requestsMu.Lock()
			stop := requests[request.RequestID]
			requestsMu.Unlock()
			if stop != nil {
				stop()
			}
			continue
		}
		if request.Type != "" && request.Type != "invoke" {
			if err := writeResponse(vscodeToolResponse{
				RequestID: request.RequestID,
				Error:     fmt.Sprintf("unsupported tool request type %q", request.Type),
			}); err != nil {
				cancel()
				requestsWG.Wait()
				return errors.Join(fmt.Errorf("write tool server response: %w", err), s.Close())
			}
			continue
		}
		if request.RequestID == "" {
			if err := writeResponse(vscodeToolResponse{Error: "request_id is required"}); err != nil {
				cancel()
				requestsWG.Wait()
				return errors.Join(fmt.Errorf("write tool server response: %w", err), s.Close())
			}
			continue
		}

		requestsMu.Lock()
		if _, exists := requests[request.RequestID]; exists {
			requestsMu.Unlock()
			if err := writeResponse(vscodeToolResponse{
				RequestID: request.RequestID,
				Error:     "duplicate request_id",
			}); err != nil {
				cancel()
				requestsWG.Wait()
				return errors.Join(fmt.Errorf("write tool server response: %w", err), s.Close())
			}
			continue
		}
		requestCtx, requestCancel := context.WithCancel(runCtx)
		requests[request.RequestID] = requestCancel
		requestsMu.Unlock()

		requestsWG.Add(1)
		go func(request vscodeToolRequest, requestCtx context.Context, requestCancel context.CancelFunc) {
			defer requestsWG.Done()
			defer requestCancel()
			defer func() {
				requestsMu.Lock()
				delete(requests, request.RequestID)
				requestsMu.Unlock()
			}()
			result, err := s.Dispatch(requestCtx, request.Name, request.Arguments)
			response := vscodeToolResponse{RequestID: request.RequestID, Result: result}
			if err != nil {
				response.Result = nil
				response.Error = err.Error()
			}
			if writeErr := writeResponse(response); writeErr != nil {
				cancel()
			}
		}(request, requestCtx, requestCancel)
	}

	scanErr := scanner.Err()
	cancel()
	requestsMu.Lock()
	for _, stop := range requests {
		stop()
	}
	requestsMu.Unlock()
	requestsWG.Wait()
	closeErr := s.Close()
	if scanErr != nil {
		return errors.Join(fmt.Errorf("read tool server input: %w", scanErr), closeErr)
	}
	return closeErr
}

func nonEmptyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

func remapToolArguments(raw json.RawMessage, localID int) (json.RawMessage, error) {
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(nonEmptyJSON(raw), &arguments); err != nil {
		return nil, fmt.Errorf("decode terminal tool arguments: %w", err)
	}
	if arguments == nil {
		return nil, errors.New("terminal tool arguments must be a JSON object")
	}
	id, err := json.Marshal(localID)
	if err != nil {
		return nil, fmt.Errorf("encode managed process ID: %w", err)
	}
	arguments["id"] = id
	remapped, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("encode terminal tool arguments: %w", err)
	}
	return remapped, nil
}

func remapToolResultText(result map[string]any, localID, visibleID int) {
	for _, key := range []string{"result"} {
		text, ok := result[key].(string)
		if !ok {
			continue
		}
		text = strings.ReplaceAll(text, fmt.Sprintf("pid_id: %d", localID), fmt.Sprintf("pid_id: %d", visibleID))
		text = strings.ReplaceAll(text, fmt.Sprintf("id=%d", localID), fmt.Sprintf("id=%d", visibleID))
		result[key] = text
	}
}
