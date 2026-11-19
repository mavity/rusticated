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

func TestUnsupportedGoPackagesAreExcludedFromWasm(t *testing.T) {
	for _, path := range []string{
		"mohabbat/mohabbat-next/build",
		"mohabbat/mohabbat-next/test",
		"mohabbat/rusticated-go/syscall",
	} {
		if !isExcludedGoWasmPackage(path) {
			t.Errorf("%q should be excluded from Go WASM tests", path)
		}
	}
	if isExcludedGoWasmPackage("mohabbat/mohabbat-next/pal") {
		t.Fatal("portable package should not be excluded")
	}
}

func TestEventSinkPersistsEventsAndSummary(t *testing.T) {
	dir := t.TempDir()
	sink, err := newEventSink(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}

	if err := sink.emit(testEvent{Kind: "run", Runner: "go", Target: "native", Suite: "pkg", Name: "works"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.emit(testEvent{Kind: "pass", Runner: "go", Target: "native", Suite: "pkg", Name: "works"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.finish(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("event log has %d lines, want 2", len(lines))
	}
	var event testEvent
	if err := json.Unmarshal(lines[1], &event); err != nil {
		t.Fatal(err)
	}
	if event.Kind != "pass" || event.Name != "works" {
		t.Fatalf("persisted event = %#v", event)
	}
	data, err = os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary testSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Passed != 1 || summary.TestsStarted != 1 || summary.RunID != "run-1" {
		t.Fatalf("persisted summary = %#v", summary)
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
