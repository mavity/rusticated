package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type TestStatus string

const (
	StatusPass     TestStatus = "PASS"
	StatusFail     TestStatus = "FAIL"
	StatusDisabled TestStatus = "DISABLED"
)

type TestResult struct {
	Suite    string
	Name     string
	Status   TestStatus
	Duration time.Duration
	Detail   string
}

type Runner struct {
	NextDir   string
	BuildDir  string
	DistExe   string
	Results   []TestResult
	FailCount int
	PassCount int
	DisCount  int
}

func NewRunner(nextDir, distExe string) *Runner {
	return &Runner{
		NextDir:  nextDir,
		BuildDir: filepath.Join(nextDir, ".build"),
		DistExe:  distExe,
	}
}

func (r *Runner) RunTest(suite, name string, fn func() error) {
	fmt.Fprintf(os.Stdout, "RUN   [%s] %s\n", suite, name)
	start := time.Now()
	err := fn()
	dur := time.Since(start)
	if err != nil {
		fmt.Fprintf(os.Stdout, "FAIL  [%s] %s (%s): %v\n", suite, name, dur.Round(time.Millisecond), err)
		r.Results = append(r.Results, TestResult{
			Suite:    suite,
			Name:     name,
			Status:   StatusFail,
			Duration: dur,
			Detail:   err.Error(),
		})
		r.FailCount++
	} else {
		fmt.Fprintf(os.Stdout, "PASS  [%s] %s (%s)\n", suite, name, dur.Round(time.Millisecond))
		r.Results = append(r.Results, TestResult{
			Suite:    suite,
			Name:     name,
			Status:   StatusPass,
			Duration: dur,
		})
		r.PassCount++
	}
}

// RecordDisabled records a single falsifiable sentinel test when a platform or runtime
// environment is unavailable on the current host, including the exact probe command and symptom.
func (r *Runner) RecordDisabled(suite, name, reason string) {
	fmt.Fprintf(os.Stdout, "DISABLED [%s] %s\n  ↳ Falsifiable symptom: %s\n", suite, name, reason)
	r.Results = append(r.Results, TestResult{
		Suite:  suite,
		Name:   name,
		Status: StatusDisabled,
		Detail: reason,
	})
	r.DisCount++
}

func (r *Runner) PrintSummary(totalDuration time.Duration) {
	fmt.Fprintln(os.Stdout, "\n================================================================================")
	fmt.Fprintln(os.Stdout, "🧪  Mohabbat-Next Unified Test Summary")
	fmt.Fprintln(os.Stdout, "================================================================================")
	for _, res := range r.Results {
		switch res.Status {
		case StatusPass:
			fmt.Fprintf(os.Stdout, "  ✅ PASS     [%-22s] %-36s (%s)\n", res.Suite, res.Name, res.Duration.Round(time.Millisecond))
		case StatusDisabled:
			fmt.Fprintf(os.Stdout, "  ⚪ DISABLED [%-22s] %-36s\n     ↳ %s\n", res.Suite, res.Name, res.Detail)
		case StatusFail:
			fmt.Fprintf(os.Stdout, "  ❌ FAIL     [%-22s] %-36s (%s)\n     ↳ %s\n", res.Suite, res.Name, res.Duration.Round(time.Millisecond), res.Detail)
		}
	}
	fmt.Fprintln(os.Stdout, "--------------------------------------------------------------------------------")
	fmt.Fprintf(os.Stdout, "  Total: %d passed, %d disabled (sentinel), %d failed  [Duration: %s]\n",
		r.PassCount, r.DisCount, r.FailCount, totalDuration.Round(time.Millisecond))
	fmt.Fprintln(os.Stdout, "================================================================================")
}
