package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type testEvent struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Runner  string    `json:"runner"`
	Target  string    `json:"target"`
	Suite   string    `json:"suite,omitempty"`
	Name    string    `json:"name,omitempty"`
	Elapsed float64   `json:"elapsed_seconds,omitempty"`
	Stream  string    `json:"stream,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

type testSummary struct {
	RunID                string `json:"run_id"`
	Host                 string `json:"host"`
	StartedAt            string `json:"started_at"`
	FinishedAt           string `json:"finished_at"`
	Passed               int    `json:"passed"`
	Failed               int    `json:"failed"`
	Skipped              int    `json:"skipped"`
	TestsStarted         int    `json:"tests_started"`
	InfrastructureErrors int    `json:"infrastructure_errors"`
	EventLog             string `json:"event_log"`
}

func hostDescription() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

func hostExecutableName() string {
	if runtime.GOOS == "windows" {
		return "MohabbatNextTestHost.exe"
	}
	return "MohabbatNextTestHost"
}

type eventSink struct {
	mu      sync.Mutex
	events  *os.File
	summary testSummary
	logDir  string
}

func newEventSink(logDir, runID string) (*eventSink, error) {
	path := filepath.Join(logDir, "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create event log %s: %w", path, err)
	}
	return &eventSink{
		events: file,
		logDir: logDir,
		summary: testSummary{
			RunID:     runID,
			Host:      hostDescription(),
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
			EventLog:  path,
		},
	}, nil
}

func (s *eventSink) emit(event testEvent) error {
	event.Time = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.NewEncoder(s.events).Encode(event); err != nil {
		return fmt.Errorf("write test event: %w", err)
	}
	if err := s.events.Sync(); err != nil {
		return fmt.Errorf("flush test event: %w", err)
	}
	switch event.Kind {
	case "pass":
		if event.Name != "" {
			s.summary.Passed++
		}
		fmt.Printf("PASS  [%s/%s] %s :: %s\n", event.Runner, event.Target, event.Suite, event.Name)
	case "fail":
		if event.Name != "" {
			s.summary.Failed++
		}
		fmt.Printf("FAIL  [%s/%s] %s :: %s %s\n", event.Runner, event.Target, event.Suite, event.Name, event.Detail)
	case "skip":
		if event.Name != "" {
			s.summary.Skipped++
		}
		fmt.Printf("SKIP  [%s/%s] %s :: %s\n", event.Runner, event.Target, event.Suite, event.Name)
	case "infra_error":
		s.summary.InfrastructureErrors++
		fmt.Printf("ERROR [%s/%s] %s: %s\n", event.Runner, event.Target, event.Suite, event.Detail)
	case "output":
		if event.Detail != "" {
			fmt.Print(event.Detail)
			if event.Detail[len(event.Detail)-1] != '\n' {
				fmt.Println()
			}
		}
	case "run":
		if event.Name != "" {
			s.summary.TestsStarted++
		}
		fmt.Printf("RUN   [%s/%s] %s\n", event.Runner, event.Target, event.Suite)
	}
	return nil
}

func (s *eventSink) finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summary.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.events.Close(); err != nil {
		return fmt.Errorf("close event log: %w", err)
	}
	data, err := json.MarshalIndent(s.summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode test summary: %w", err)
	}
	path := filepath.Join(s.logDir, "summary.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write test summary %s: %w", path, err)
	}
	fmt.Printf("Summary: %d passed, %d failed, %d skipped, %d infrastructure errors\n",
		s.summary.Passed, s.summary.Failed, s.summary.Skipped, s.summary.InfrastructureErrors)
	return nil
}
