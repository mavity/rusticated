package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGoJSONEvents(t *testing.T) {
	parse := parseGoJSON("go", "native-windows-arm64", "example")
	tests := []struct {
		line string
		kind string
		name string
	}{
		{`{"Action":"run","Package":"example/pkg","Test":"TestOne"}`, "run", "TestOne"},
		{`{"Action":"pass","Package":"example/pkg","Test":"TestOne","Elapsed":0.2}`, "pass", "TestOne"},
		{`{"Action":"fail","Package":"example/pkg","Test":"TestTwo"}`, "fail", "TestTwo"},
		{`{"Action":"skip","Package":"example/pkg","Test":"TestThree"}`, "skip", "TestThree"},
	}
	for _, test := range tests {
		events := parse("stdout", test.line)
		if len(events) != 1 || events[0].Kind != test.kind || events[0].Name != test.name {
			t.Fatalf("parse(%q) = %#v", test.line, events)
		}
	}
}

func TestParseVerboseGoOutput(t *testing.T) {
	parse := parseVerboseGoTest("go", "rusticated-wasm", "example/pkg")
	for _, test := range []struct{ line, kind, name string }{
		{"=== RUN   TestOne", "run", "TestOne"},
		{"--- PASS: TestOne (0.00s)", "pass", "TestOne"},
		{"--- FAIL: TestTwo (0.01s)", "fail", "TestTwo"},
		{"--- SKIP: TestThree (0.00s)", "skip", "TestThree"},
	} {
		events := parse("stdout", test.line)
		if len(events) != 1 || events[0].Kind != test.kind || events[0].Name != test.name {
			t.Fatalf("parse(%q) = %#v", test.line, events)
		}
	}
}

func TestRustEventJSON(t *testing.T) {
	parse := parseRustTestOutput("rust", "rusticated-wasm", "crate")
	event := struct {
		Type  string `json:"type"`
		Event string `json:"event"`
		Name  string `json:"name"`
	}{"test", "started", "tests::works"}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	events := parse("stdout", string(data))
	if len(events) != 1 || events[0].Kind != "run" || events[0].Name != event.Name {
		t.Fatalf("parse Rust event = %#v", events)
	}
}

func TestZigTestOutputEvents(t *testing.T) {
	parse := parseZigTestOutput("zig", "native-windows-arm64", "guest.zig")
	events := parse("stdout", "1/1 guest.test.ABI struct sizes and offsets...OK")
	if len(events) != 2 || events[0].Kind != "run" || events[1].Kind != "pass" ||
		events[0].Name != "ABI struct sizes and offsets" {
		t.Fatalf("parse Zig output = %#v", events)
	}
}

func TestParseNodeTAP(t *testing.T) {
	parse := parseNodeTAP("node", "native-windows-arm64", "mohabbat/washmhost/js/node/index.test.js")
	for _, tc := range []struct {
		line string
		kind string
		name string
	}{
		{"ok 1 - marshalStat encodes the 112-byte Rusticated stat record", "pass", "marshalStat encodes the 112-byte Rusticated stat record"},
		{"not ok 2 - failing case", "fail", "failing case"},
		{"ok 3 - Windows directory stats include execute bits for shell cd # SKIP", "skip", "Windows directory stats include execute bits for shell cd"},
		{"ok 4 - skipped with reason # skip not supported on this host", "skip", "skipped with reason"},
	} {
		events := parse("stdout", tc.line)
		if len(events) != 2 || events[0].Kind != "run" || events[1].Kind != tc.kind ||
			events[0].Name != tc.name || events[1].Name != tc.name {
			t.Fatalf("parseNodeTAP(%q) = %#v", tc.line, events)
		}
	}

	// Diagnostic and indented subtest lines should not be counted as top-level test results.
	for _, line := range []string{
		"TAP version 13",
		"# Subtest: something",
		"    ok 1 - nested subtest",
		"1..4",
	} {
		events := parse("stdout", line)
		if len(events) != 1 || events[0].Kind != "output" {
			t.Fatalf("parseNodeTAP(%q) = %#v, want single output event", line, events)
		}
	}
}

func TestProjectFilterMatchesModulesAndPackages(t *testing.T) {
	run := &testRun{filter: []string{"kabibi", "mohabbat-next/build", "wasm-abi-test"}}
	for _, name := range []string{
		"kabibi", "kabibi/ui", "github.com/mavity/rusticated/kabibi/ui/app",
		"mohabbat-next/build", "mohabbat/wasm-abi-test",
	} {
		if !run.matches(name) {
			t.Errorf("%q should match", name)
		}
	}
	for _, name := range []string{"ai", "demo-go"} {
		if run.matches(name) {
			t.Errorf("%q should not match", name)
		}
	}
}

func TestProjectFilterDoesNotSelectAncestorPackages(t *testing.T) {
	run := &testRun{filter: []string{"mohabbat/mohabbat/regast"}}
	if run.matches("mohabbat/mohabbat") {
		t.Fatal("filter matched an ancestor package")
	}
	if !run.matches("mohabbat/mohabbat/regast") {
		t.Fatal("filter did not match the selected package")
	}
}

func TestEventSinkPersistsEventsSummaryAndManifest(t *testing.T) {
	dir := t.TempDir()
	sink, err := newEventSink(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	sink.setRunMetadata([]string{"kabibi", "demo"}, map[string]toolchainInfo{
		"go":   {Available: true, Path: "/usr/bin/go", Version: "go version go1.24"},
		"node": {Available: false, Error: "not found"},
	})

	for _, ev := range []testEvent{
		{Kind: "run", Runner: "go", Target: "native", Suite: "pkg", Name: "works"},
		{Kind: "pass", Runner: "go", Target: "native", Suite: "pkg", Name: "works"},
		{Kind: "run", Runner: "go", Target: "native", Suite: "pkg", Name: "broken"},
		{Kind: "fail", Runner: "go", Target: "native", Suite: "pkg", Name: "broken", Detail: "boom"},
		{Kind: "skip", Runner: "rust", Target: "rusticated-wasm", Suite: "crate", Name: "ignored_test"},
		{Kind: "disabled", Runner: "go", Target: "rusticated-wasm", Suite: "pkg/sub", Name: "pkg/sub", Detail: "go list: missing build tag"},
		{Kind: "infra_error", Runner: "zig", Target: "native", Suite: "guest.zig", Detail: "timeout"},
	} {
		if err := sink.emit(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.finish(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 7 {
		t.Fatalf("event log has %d lines, want 7", len(lines))
	}
	var event testEvent
	if err := json.Unmarshal(lines[5], &event); err != nil {
		t.Fatal(err)
	}
	if event.Kind != "disabled" || event.Runner != "go" || event.Target != "rusticated-wasm" {
		t.Fatalf("persisted disabled event = %#v", event)
	}

	data, err = os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary testSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Passed != 1 || summary.Failed != 1 || summary.Skipped != 1 ||
		summary.Disabled != 1 || summary.TestsStarted != 2 || summary.InfrastructureErrors != 1 ||
		summary.RunID != "run-1" {
		t.Fatalf("persisted summary = %#v", summary)
	}

	goNative := summary.Targets["go/native"]
	if goNative.Passed != 1 || goNative.Failed != 1 || goNative.TestsStarted != 2 {
		t.Fatalf("go/native target summary = %#v", goNative)
	}
	goWasm := summary.Targets["go/rusticated-wasm"]
	if goWasm.Disabled != 1 {
		t.Fatalf("go/rusticated-wasm target summary = %#v", goWasm)
	}
	rustWasm := summary.Targets["rust/rusticated-wasm"]
	if rustWasm.Skipped != 1 {
		t.Fatalf("rust/rusticated-wasm target summary = %#v", rustWasm)
	}
	zigNative := summary.Targets["zig/native"]
	if zigNative.InfrastructureErrors != 1 {
		t.Fatalf("zig/native target summary = %#v", zigNative)
	}

	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest testManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RunID != "run-1" || manifest.Host == "" || manifest.StartedAt == "" || manifest.FinishedAt == "" {
		t.Fatalf("manifest metadata missing: %#v", manifest)
	}
	if len(manifest.Filters) != 2 || manifest.Filters[0] != "kabibi" || manifest.Filters[1] != "demo" {
		t.Fatalf("manifest filters = %#v", manifest.Filters)
	}
	if !manifest.Toolchains["go"].Available || manifest.Toolchains["node"].Available {
		t.Fatalf("manifest toolchains = %#v", manifest.Toolchains)
	}
	if manifest.Disabled != 1 || manifest.Passed != 1 || manifest.Failed != 1 {
		t.Fatalf("manifest counts = %#v", manifest)
	}
}

func TestTestRunDisableHelperEmitsDisabledEvent(t *testing.T) {
	run, err := newTestRun(t.TempDir(), time.Minute, time.Minute, []string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := run.toolchains["go"]; !ok {
		t.Fatalf("expected 'go' in probed toolchains: %#v", run.toolchains)
	}
	run.disable("go", "rusticated-wasm", "mohabbat/demo", "node --version", "node not installed")
	if run.sink.summary.Disabled != 1 {
		t.Fatalf("disabled = %d, want 1", run.sink.summary.Disabled)
	}
	if got := run.sink.summary.Targets["go/rusticated-wasm"].Disabled; got != 1 {
		t.Fatalf("target disabled = %d, want 1", got)
	}
	// Finish will report an infra_error because TestsStarted == 0, which is expected.
	_ = run.finish()
	if _, err := os.Stat(filepath.Join(run.logDir, "manifest.json")); err != nil {
		t.Fatalf("manifest.json not written: %v", err)
	}
}

func TestFinishRejectsRunsWithoutTestCases(t *testing.T) {
	run, err := newTestRun(t.TempDir(), time.Minute, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.finish(); err == nil {
		t.Fatal("finish should fail when no test cases ran")
	}
	if run.sink.summary.InfrastructureErrors != 1 {
		t.Fatalf("infrastructure errors = %d, want 1", run.sink.summary.InfrastructureErrors)
	}
}

func TestSupportsGOOS(t *testing.T) {
	allOS := goModule{Path: "u-root", WasmTest: true}
	if !supportsGOOS(allOS, "windows") || !supportsGOOS(allOS, "linux") || !supportsGOOS(allOS, "darwin") {
		t.Fatal("module without NativeGOOS should support all GOOS values")
	}

	unixOnly := goModule{Path: "mohabbat/washmhost", NativeGOOS: []string{"linux", "darwin"}}
	if supportsGOOS(unixOnly, "windows") {
		t.Fatal("unix-only module should not support windows")
	}
	if !supportsGOOS(unixOnly, "linux") || !supportsGOOS(unixOnly, "darwin") {
		t.Fatal("unix-only module should support linux and darwin")
	}
}

func TestSupportsX64Emulated(t *testing.T) {
	ok, probe, symptom := supportsX64Emulated("windows", "arm64")
	if !ok || probe != "" || symptom != "" {
		t.Fatalf("windows/arm64 should support x64-emulated, got ok=%v probe=%q symptom=%q", ok, probe, symptom)
	}

	for _, host := range []struct{ goos, goarch string }{
		{"windows", "amd64"},
		{"linux", "arm64"},
		{"darwin", "arm64"},
	} {
		ok, probe, symptom := supportsX64Emulated(host.goos, host.goarch)
		if ok || probe == "" || symptom == "" {
			t.Fatalf("%s/%s should not support x64-emulated, got ok=%v probe=%q symptom=%q", host.goos, host.goarch, ok, probe, symptom)
		}
	}
}

func TestWorkspaceGoModulesIncludesURoot(t *testing.T) {
	var found bool
	for _, mod := range workspaceGoModules {
		if mod.Path == "u-root" {
			found = true
			if !mod.WasmTest {
				t.Fatal("u-root should have WasmTest enabled")
			}
			break
		}
	}
	if !found {
		t.Fatal("u-root not found in workspaceGoModules")
	}
}
