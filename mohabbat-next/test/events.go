package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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

type toolchainInfo struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

type targetSummary struct {
	Passed               int `json:"passed"`
	Failed               int `json:"failed"`
	Skipped              int `json:"skipped"`
	Disabled             int `json:"disabled"`
	TestsStarted         int `json:"tests_started"`
	InfrastructureErrors int `json:"infrastructure_errors"`
}

type testSummary struct {
	RunID                string                   `json:"run_id"`
	Host                 string                   `json:"host"`
	StartedAt            string                   `json:"started_at"`
	FinishedAt           string                   `json:"finished_at"`
	Passed               int                      `json:"passed"`
	Failed               int                      `json:"failed"`
	Skipped              int                      `json:"skipped"`
	Disabled             int                      `json:"disabled"`
	TestsStarted         int                      `json:"tests_started"`
	InfrastructureErrors int                      `json:"infrastructure_errors"`
	Targets              map[string]targetSummary `json:"targets,omitempty"`
	EventLog             string                   `json:"event_log"`
}

type testManifest struct {
	RunID                string                   `json:"run_id"`
	Host                 string                   `json:"host"`
	StartedAt            string                   `json:"started_at"`
	FinishedAt           string                   `json:"finished_at"`
	Filters              []string                 `json:"filters,omitempty"`
	Toolchains           map[string]toolchainInfo `json:"toolchains,omitempty"`
	Passed               int                      `json:"passed"`
	Failed               int                      `json:"failed"`
	Skipped              int                      `json:"skipped"`
	Disabled             int                      `json:"disabled"`
	TestsStarted         int                      `json:"tests_started"`
	InfrastructureErrors int                      `json:"infrastructure_errors"`
	Targets              map[string]targetSummary `json:"targets,omitempty"`
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
	mu         sync.Mutex
	events     *os.File
	summary    testSummary
	filters    []string
	toolchains map[string]toolchainInfo
	logDir     string
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
			Targets:   make(map[string]targetSummary),
			EventLog:  path,
		},
	}, nil
}

func (s *eventSink) setRunMetadata(filters []string, toolchains map[string]toolchainInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filters = append([]string(nil), filters...)
	if len(toolchains) > 0 {
		s.toolchains = make(map[string]toolchainInfo, len(toolchains))
		for k, v := range toolchains {
			s.toolchains[k] = v
		}
	} else {
		s.toolchains = nil
	}
}

func targetKey(runner, target string) string {
	if runner == "" && target == "" {
		return ""
	}
	return runner + "/" + target
}

func (s *eventSink) updateTarget(runner, target string, update func(*targetSummary)) {
	key := targetKey(runner, target)
	if key == "" {
		return
	}
	if s.summary.Targets == nil {
		s.summary.Targets = make(map[string]targetSummary)
	}
	ts := s.summary.Targets[key]
	update(&ts)
	s.summary.Targets[key] = ts
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
			s.updateTarget(event.Runner, event.Target, func(ts *targetSummary) { ts.Passed++ })
		}
		fmt.Printf("PASS  [%s/%s] %s :: %s\n", event.Runner, event.Target, event.Suite, event.Name)
	case "fail":
		if event.Name != "" {
			s.summary.Failed++
			s.updateTarget(event.Runner, event.Target, func(ts *targetSummary) { ts.Failed++ })
		}
		fmt.Printf("FAIL  [%s/%s] %s :: %s %s\n", event.Runner, event.Target, event.Suite, event.Name, event.Detail)
	case "skip":
		if event.Name != "" {
			s.summary.Skipped++
			s.updateTarget(event.Runner, event.Target, func(ts *targetSummary) { ts.Skipped++ })
		}
		fmt.Printf("SKIP  [%s/%s] %s :: %s\n", event.Runner, event.Target, event.Suite, event.Name)
	case "disabled":
		s.summary.Disabled++
		s.updateTarget(event.Runner, event.Target, func(ts *targetSummary) { ts.Disabled++ })
		if event.Name != "" && event.Name != event.Suite {
			fmt.Printf("DISAB [%s/%s] %s :: %s (%s)\n", event.Runner, event.Target, event.Suite, event.Name, event.Detail)
		} else {
			fmt.Printf("DISAB [%s/%s] %s (%s)\n", event.Runner, event.Target, event.Suite, event.Detail)
		}
	case "infra_error":
		s.summary.InfrastructureErrors++
		s.updateTarget(event.Runner, event.Target, func(ts *targetSummary) { ts.InfrastructureErrors++ })
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
			s.updateTarget(event.Runner, event.Target, func(ts *targetSummary) { ts.TestsStarted++ })
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
	manifest := testManifest{
		RunID:                s.summary.RunID,
		Host:                 s.summary.Host,
		StartedAt:            s.summary.StartedAt,
		FinishedAt:           s.summary.FinishedAt,
		Filters:              s.filters,
		Toolchains:           s.toolchains,
		Passed:               s.summary.Passed,
		Failed:               s.summary.Failed,
		Skipped:              s.summary.Skipped,
		Disabled:             s.summary.Disabled,
		TestsStarted:         s.summary.TestsStarted,
		InfrastructureErrors: s.summary.InfrastructureErrors,
		Targets:              s.summary.Targets,
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode test manifest: %w", err)
	}
	manifestPath := filepath.Join(s.logDir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		return fmt.Errorf("write test manifest %s: %w", manifestPath, err)
	}
	if len(s.summary.Targets) > 0 {
		keys := make([]string, 0, len(s.summary.Targets))
		for k := range s.summary.Targets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Println("Targets:")
		for _, k := range keys {
			ts := s.summary.Targets[k]
			fmt.Printf("  %-28s passed=%d failed=%d skipped=%d disabled=%d started=%d infra_errors=%d\n",
				k, ts.Passed, ts.Failed, ts.Skipped, ts.Disabled, ts.TestsStarted, ts.InfrastructureErrors)
		}
	}
	fmt.Printf("Summary: %d passed, %d failed, %d skipped, %d disabled, %d infrastructure errors\n",
		s.summary.Passed, s.summary.Failed, s.summary.Skipped, s.summary.Disabled, s.summary.InfrastructureErrors)
	return nil
}
