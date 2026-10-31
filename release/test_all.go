package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	releaseTestTimeout       = "2m"
	releaseTestRunnerTimeout = 20 * time.Minute
)

var firstPartyGoModules = []string{
	".",
	"ai",
	"dload",
	"kabibi",
	"demo-go",
	"demo-go/curl",
	"demo-go/server",
	"demo-go/trivial",
	"mohabbat/washmhost",
	"ocr4",
	"platform",
	"pope",
	"pty",
}

type testMode struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	GOOS     string `json:"goos,omitempty"`
	GOARCH   string `json:"goarch,omitempty"`
	Platform string `json:"platform,omitempty"`
}

type testPackage struct {
	ImportPath string
	Dir        string
}

type testKey struct {
	Package string
	Name    string
}

type testMatrixReport struct {
	Host        string            `json:"host"`
	Modes       []testMode        `json:"modes"`
	Unavailable []string          `json:"unavailable,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
	LogDir      string            `json:"log_dir"`
	Tests       []testMatrixEntry `json:"tests"`
}

type testMatrixEntry struct {
	Package string          `json:"package"`
	Test    string          `json:"test"`
	Results map[string]bool `json:"results"`
}

type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

func runTestAll(ws string) error {
	logDir, err := createTestEvidenceDir(ws)
	if err != nil {
		return err
	}

	modes, unavailable := detectTestModes()
	report := testMatrixReport{
		Host:        runtime.GOOS + "/" + runtime.GOARCH,
		Modes:       modes,
		Unavailable: unavailable,
		LogDir:      logDir,
	}
	fmt.Printf("🍆  Test matrix host: %s\n", report.Host)
	for _, reason := range report.Unavailable {
		fmt.Printf("🍆  Test mode omitted: %s\n", reason)
	}

	packages, warnings := discoverWASITestPackages(ws)
	report.Warnings = append(report.Warnings, warnings...)
	results := make(map[testKey]map[string]bool)
	testsRun := make(map[string]int, len(modes))

	for _, mode := range modes {
		fmt.Printf("\n🍆  Collecting %s results\n", mode.Label)
		if mode.Kind == "go" {
			count, modeWarnings := runNativeTestMode(ws, mode, logDir, results)
			testsRun[mode.ID] += count
			report.Warnings = append(report.Warnings, modeWarnings...)
			continue
		}

		for i, pkg := range packages {
			fmt.Printf("🍆  [%d/%d] %s\n", i+1, len(packages), pkg.ImportPath)
			count, warning := runRusticatedPackage(ws, mode, i+1, pkg, logDir, results)
			testsRun[mode.ID] += count
			if warning != "" {
				report.Warnings = append(report.Warnings, warning)
			}
		}
	}

	activeModes := make([]testMode, 0, len(modes))
	for _, mode := range modes {
		if testsRun[mode.ID] == 0 {
			reason := fmt.Sprintf("%s could not execute any tests; its column was omitted", mode.Label)
			report.Unavailable = append(report.Unavailable, reason)
			fmt.Printf("🍆  Test mode omitted: %s\n", reason)
			continue
		}
		activeModes = append(activeModes, mode)
	}
	report.Modes = activeModes
	report.Tests = buildTestMatrix(results, activeModes)
	printTestMatrix(report.Tests, activeModes)
	if len(report.Warnings) > 0 {
		fmt.Println("\n🍆  Test matrix warnings:")
		for _, warning := range report.Warnings {
			fmt.Printf("  - %s\n", warning)
		}
	}

	reportPath := filepath.Join(logDir, "matrix.json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode test matrix report: %w", err)
	}
	if err := os.WriteFile(reportPath, data, 0o644); err != nil {
		return fmt.Errorf("write test matrix report: %w", err)
	}
	fmt.Printf("\n🍆  Test evidence: %s\n", logDir)

	failedCells := 0
	for _, test := range report.Tests {
		for _, mode := range activeModes {
			if !test.Results[mode.ID] {
				failedCells++
			}
		}
	}
	fmt.Printf("🍆  Matrix summary: %d tests, %d passing cells, %d failing cells\n",
		len(report.Tests), len(report.Tests)*len(activeModes)-failedCells, failedCells)
	if len(report.Tests) == 0 {
		return fmt.Errorf("no tests executed; see %s", reportPath)
	}
	if failedCells > 0 {
		return fmt.Errorf("%d failing test/mode cells across %d tests; see %s",
			failedCells, len(report.Tests), reportPath)
	}
	if len(report.Warnings) > 0 {
		return fmt.Errorf("test matrix completed with warnings; see %s", reportPath)
	}
	return nil
}

func createTestEvidenceDir(ws string) (string, error) {
	root := filepath.Join(ws, "target", "release-test-all")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create test evidence root: %w", err)
	}
	logDir := filepath.Join(root, time.Now().Format("20060102-150405.000000000"))
	if err := os.Mkdir(logDir, 0o755); err != nil {
		return "", fmt.Errorf("create test evidence directory: %w", err)
	}
	return logDir, nil
}

func detectTestModes() ([]testMode, []string) {
	unavailable := make([]string, 0, 2)
	canRunAMD64 := false
	if runtime.GOARCH == "arm64" {
		switch runtime.GOOS {
		case "windows":
			canRunAMD64 = true
		case "darwin":
			var reason string
			canRunAMD64, reason = canRunDarwinAMD64()
			if !canRunAMD64 {
				unavailable = append(unavailable, "AMD64 modes omitted: "+reason)
			}
		case "linux":
			var reason string
			canRunAMD64, reason = canRunLinuxAMD64()
			if !canRunAMD64 {
				unavailable = append(unavailable, "AMD64 modes omitted: "+reason)
			}
		}
	}

	nodeVersion := ""
	if version, err := usableNode(); err == nil {
		nodeVersion = version
	} else {
		unavailable = append(unavailable, "Rusticated Node omitted: "+err.Error())
	}

	modes := planTestModes(runtime.GOOS, runtime.GOARCH, nodeVersion, canRunAMD64)
	return modes, unavailable
}

func canRunDarwinAMD64() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/arch", "-x86_64", "/usr/bin/true")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("Rosetta is unavailable: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return true, ""
}

func canRunLinuxAMD64() (bool, string) {
	for _, registration := range []string{"qemu-x86_64", "qemu-x86_64-static"} {
		path := filepath.Join("/proc/sys/fs/binfmt_misc", registration)
		data, err := os.ReadFile(path)
		if err == nil && linuxAMD64BinfmtEnabled(data) {
			return true, ""
		}
	}
	return false, "no enabled qemu-x86_64 binfmt registration was found"
}

func linuxAMD64BinfmtEnabled(data []byte) bool {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "enabled" {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(lines[1]), "interpreter ")
}

func usableNode() (string, error) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return "", fmt.Errorf("Node executable not found on PATH")
	}
	cmd := exec.Command(nodePath, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("Node version probe failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	version := strings.TrimSpace(string(output))
	if _, err := nodeMajorVersion(version); err != nil {
		return "", err
	}
	return version, nil
}

func nodeMajorVersion(version string) (int, error) {
	majorText := strings.TrimPrefix(version, "v")
	majorText, _, _ = strings.Cut(majorText, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return 0, fmt.Errorf("could not parse Node version %q", version)
	}
	if major < 18 {
		return 0, fmt.Errorf("Node %s found; Node 18 or newer is required", version)
	}
	return major, nil
}

func planTestModes(goos, goarch, nodeVersion string, canRunAMD64 bool) []testMode {
	modes := []testMode{{
		ID:     "go-native",
		Label:  fmt.Sprintf("Native Go %s/%s", goos, goarch),
		Kind:   "go",
		GOOS:   goos,
		GOARCH: goarch,
	}}
	if canRunAMD64 && goarch != "amd64" {
		modes = append(modes, testMode{
			ID:     "go-amd64",
			Label:  fmt.Sprintf("Go %s/amd64", goos),
			Kind:   "go",
			GOOS:   goos,
			GOARCH: "amd64",
		})
	}

	modes = append(modes, testMode{
		ID:     "rusticated-default",
		Label:  fmt.Sprintf("Rusticated %s/%s", goos, goarch),
		Kind:   "rusticated",
		GOOS:   goos,
		GOARCH: goarch,
	})
	if nodeVersion != "" {
		modes = append(modes, testMode{
			ID:       "rusticated-node",
			Label:    "Rusticated Node " + nodeVersion,
			Kind:     "rusticated",
			Platform: "node",
		})
	}
	if canRunAMD64 && goarch != "amd64" {
		modes = append(modes, testMode{
			ID:       "rusticated-amd64",
			Label:    fmt.Sprintf("Rusticated %s/amd64", goos),
			Kind:     "rusticated",
			GOOS:     goos,
			GOARCH:   "amd64",
			Platform: "amd64",
		})
	}
	return modes
}

func discoverWASITestPackages(ws string) ([]testPackage, []string) {
	packagesByImportPath := make(map[string]testPackage)
	warnings := make([]string, 0)
	for _, module := range firstPartyGoModules {
		moduleDir := filepath.Join(ws, filepath.FromSlash(module))
		cmd := exec.Command("go", "list", "-e", "-f",
			`{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}|{{.Dir}}{{end}}`, "./...")
		cmd.Dir = moduleDir
		cmd.Env = setEnvironment(os.Environ(), map[string]string{
			"GOOS":        "wasip1",
			"GOARCH":      "wasm",
			"CGO_ENABLED": "0",
		})
		output, err := cmd.CombinedOutput()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("list WASI test packages in %s: %v: %s",
				module, err, strings.TrimSpace(string(output))))
		}
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "go: ") {
				continue
			}
			fields := strings.SplitN(line, "|", 2)
			if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
				warnings = append(warnings, fmt.Sprintf("invalid package listing from %s: %q", module, line))
				continue
			}
			packagesByImportPath[fields[0]] = testPackage{ImportPath: fields[0], Dir: fields[1]}
		}
	}

	packages := make([]testPackage, 0, len(packagesByImportPath))
	for _, pkg := range packagesByImportPath {
		packages = append(packages, pkg)
	}
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].ImportPath < packages[j].ImportPath
	})
	return packages, warnings
}

func runNativeTestMode(ws string, mode testMode, logDir string, results map[testKey]map[string]bool) (int, []string) {
	totalTestCount := 0
	warnings := make([]string, 0)
	moduleIndex := 0
	for _, module := range firstPartyGoModules {
		moduleDir := filepath.Join(ws, filepath.FromSlash(module))
		hasPackages, warning := moduleHasPackages(moduleDir, mode)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if !hasPackages {
			continue
		}

		moduleIndex++
		base := fmt.Sprintf("%s-%02d-%s", mode.ID, moduleIndex, safeLogName(module))
		ctx, cancel := context.WithTimeout(context.Background(), releaseTestRunnerTimeout)
		cmd := exec.CommandContext(ctx, "go", "test", "-json", "-count=1",
			"-timeout="+releaseTestTimeout, "-p=1", "./...")
		cmd.Dir = moduleDir
		cmd.Env = setEnvironment(os.Environ(), map[string]string{
			"GOOS":   mode.GOOS,
			"GOARCH": mode.GOARCH,
		})
		stdoutPath, stderrPath, runErr := runLoggedCommand(ctx, cmd, logDir, base)
		cancel()
		if stdoutPath == "" {
			warnings = append(warnings, fmt.Sprintf("%s tests in %s could not be logged: %v", mode.Label, module, runErr))
			continue
		}
		eventCount, parseErr := collectGoTestJSON(stdoutPath, mode.ID, results)
		totalTestCount += eventCount
		if parseErr != nil {
			warnings = append(warnings, fmt.Sprintf("parse %s output for %s: %v", mode.Label, module, parseErr))
		}
		if runErr != nil {
			if eventCount == 0 {
				addTestResult(results, testKey{Package: module, Name: "(test command)"}, mode.ID, false)
			}
			warnings = append(warnings, fmt.Sprintf("%s tests in %s exited with error: %v; logs: %s, %s",
				mode.Label, module, runErr, stdoutPath, stderrPath))
		}
	}
	return totalTestCount, warnings
}

func moduleHasPackages(moduleDir string, mode testMode) (bool, string) {
	cmd := exec.Command("go", "list", "-e", "-f", "{{.ImportPath}}", "./...")
	cmd.Dir = moduleDir
	cmd.Env = setEnvironment(os.Environ(), map[string]string{"GOOS": mode.GOOS, "GOARCH": mode.GOARCH})
	output, err := cmd.CombinedOutput()
	hasPackages := false
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "go: ") {
			hasPackages = true
			break
		}
	}
	if err != nil {
		return hasPackages, fmt.Sprintf("list %s packages in %s: %v: %s",
			mode.Label, moduleDir, err, strings.TrimSpace(string(output)))
	}
	return hasPackages, ""
}

func runRusticatedPackage(ws string, mode testMode, index int, pkg testPackage, logDir string, results map[testKey]map[string]bool) (int, string) {
	args := []string{"run", ".", pkg.Dir, "-t"}
	if mode.Platform != "" {
		args = append(args, "--platform", mode.Platform)
	}
	args = append(args, "--", "-test.v", "-test.timeout="+releaseTestTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), releaseTestRunnerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = ws
	base := fmt.Sprintf("%s-%03d-%s", mode.ID, index, safeLogName(pkg.ImportPath))
	stdoutPath, stderrPath, runErr := runLoggedCommand(ctx, cmd, logDir, base)
	if stdoutPath == "" {
		return 0, fmt.Sprintf("run %s tests for %s could not be logged: %v",
			mode.Label, pkg.ImportPath, runErr)
	}
	testCount, parseErr := collectVerboseGoTests(stdoutPath, mode.ID, pkg.ImportPath, results)
	if parseErr != nil {
		return testCount, fmt.Sprintf("parse %s output for %s: %v", mode.Label, pkg.ImportPath, parseErr)
	}
	if runErr != nil {
		if testCount == 0 {
			addTestResult(results, testKey{Package: pkg.ImportPath, Name: "(package setup)"}, mode.ID, false)
		}
		return testCount, fmt.Sprintf("%s tests for %s exited with error: %v; logs: %s, %s",
			mode.Label, pkg.ImportPath, runErr, stdoutPath, stderrPath)
	}
	return testCount, ""
}

func runLoggedCommand(ctx context.Context, cmd *exec.Cmd, logDir, base string) (string, string, error) {
	stdoutPath := filepath.Join(logDir, base+".stdout.log")
	stderrPath := filepath.Join(logDir, base+".stderr.log")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		return "", "", fmt.Errorf("create stdout log: %w", err)
	}
	stderr, err := os.Create(stderrPath)
	if err != nil {
		_ = stdout.Close()
		return "", "", fmt.Errorf("create stderr log: %w", err)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	stdoutErr := stdout.Close()
	stderrErr := stderr.Close()
	runErr = errors.Join(runErr, stdoutErr, stderrErr)
	if runErr != nil && ctx.Err() != nil {
		runErr = fmt.Errorf("command exceeded runner timeout: %w", ctx.Err())
	}
	return stdoutPath, stderrPath, runErr
}

func collectGoTestJSON(path, modeID string, results map[testKey]map[string]bool) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open JSON test log: %w", err)
	}
	defer file.Close()
	return consumeTestJSON(file, modeID, results)
}

func collectVerboseGoTests(path, modeID, importPath string, results map[testKey]map[string]bool) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open verbose test log: %w", err)
	}
	defer file.Close()
	return consumeVerboseGoTests(file, modeID, importPath, results)
}

func consumeVerboseGoTests(reader io.Reader, modeID, importPath string, results map[testKey]map[string]bool) (int, error) {
	count := 0
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "=== RUN   "):
			name := firstTestField(strings.TrimPrefix(line, "=== RUN   "))
			if name != "" {
				addTestResult(results, testKey{Package: importPath, Name: name}, modeID, false)
				count++
			}
		case strings.HasPrefix(line, "--- PASS: "):
			name := firstTestField(strings.TrimPrefix(line, "--- PASS: "))
			if name != "" {
				addTestResult(results, testKey{Package: importPath, Name: name}, modeID, true)
			}
		case strings.HasPrefix(line, "--- SKIP: "):
			name := firstTestField(strings.TrimPrefix(line, "--- SKIP: "))
			if name != "" {
				addTestResult(results, testKey{Package: importPath, Name: name}, modeID, true)
			}
		case strings.HasPrefix(line, "--- FAIL: "):
			name := firstTestField(strings.TrimPrefix(line, "--- FAIL: "))
			if name != "" {
				addTestResult(results, testKey{Package: importPath, Name: name}, modeID, false)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("read verbose test log: %w", err)
	}
	return count, nil
}

func firstTestField(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func addTestResult(results map[testKey]map[string]bool, key testKey, modeID string, passed bool) {
	if results[key] == nil {
		results[key] = make(map[string]bool)
	}
	results[key][modeID] = passed
}

func buildTestMatrix(results map[testKey]map[string]bool, modes []testMode) []testMatrixEntry {
	keys := make([]testKey, 0, len(results))
	for key, modeResults := range results {
		for _, mode := range modes {
			if _, found := modeResults[mode.ID]; found {
				keys = append(keys, key)
				break
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Package != keys[j].Package {
			return keys[i].Package < keys[j].Package
		}
		return keys[i].Name < keys[j].Name
	})
	entries := make([]testMatrixEntry, 0, len(keys))
	for _, key := range keys {
		row := testMatrixEntry{
			Package: key.Package,
			Test:    key.Name,
			Results: make(map[string]bool, len(modes)),
		}
		for _, mode := range modes {
			row.Results[mode.ID] = results[key][mode.ID]
		}
		entries = append(entries, row)
	}
	return entries
}

func printTestMatrix(tests []testMatrixEntry, modes []testMode) {
	fmt.Println("\n🍆  Go test matrix (columns first; test identity last)")
	for _, mode := range modes {
		fmt.Printf("%s\t", mode.Label)
	}
	fmt.Println("Package / test")
	for _, test := range tests {
		for _, mode := range modes {
			mark := "❌"
			if test.Results[mode.ID] {
				mark = "✅"
			}
			fmt.Printf("%s\t", mark)
		}
		fmt.Printf("%s :: %s\n", test.Package, test.Test)
	}
	if len(tests) == 0 {
		fmt.Println("(no Go tests discovered)")
	}
}

func safeLogName(value string) string {
	name := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-").Replace(value)
	if len(name) > 100 {
		name = name[len(name)-100:]
	}
	return name
}

func setEnvironment(env []string, values map[string]string) []string {
	for key, value := range values {
		prefix := key + "="
		replaced := false
		for i := range env {
			if !strings.Contains(env[i], "=") {
				continue
			}
			entryKey := env[i][:strings.IndexByte(env[i], '=')+1]
			matches := entryKey == prefix
			if runtime.GOOS == "windows" {
				matches = strings.EqualFold(entryKey, prefix)
			}
			if matches {
				env[i] = prefix + value
				replaced = true
			}
		}
		if !replaced {
			env = append(env, prefix+value)
		}
	}
	return env
}

func consumeTestJSON(reader io.Reader, modeID string, results map[testKey]map[string]bool) (int, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	count := 0
	seenTests := make(map[string]bool)
	for scanner.Scan() {
		var event goTestEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return count, err
		}
		if event.Package == "" {
			continue
		}
		if event.Test == "" {
			if event.Action == "fail" && !seenTests[event.Package] {
				addTestResult(results, testKey{Package: event.Package, Name: "(package setup)"}, modeID, false)
			}
			continue
		}
		key := testKey{Package: event.Package, Name: event.Test}
		switch event.Action {
		case "run":
			seenTests[event.Package] = true
			addTestResult(results, key, modeID, false)
			count++
		case "pass", "skip":
			seenTests[event.Package] = true
			addTestResult(results, key, modeID, true)
		case "fail":
			seenTests[event.Package] = true
			addTestResult(results, key, modeID, false)
		}
	}
	return count, scanner.Err()
}
