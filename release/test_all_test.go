package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanTestModes(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		goarch      string
		nodeVersion string
		canAMD64    bool
		want        []string
	}{
		{
			name:        "Windows ARM64 with Node and built-in AMD64 emulation",
			goos:        "windows",
			goarch:      "arm64",
			canAMD64:    true,
			nodeVersion: "v24.21.0",
			want: []string{
				"go-native", "go-amd64", "rusticated-default", "rusticated-node", "rusticated-amd64",
			},
		},
		{
			name:   "Windows AMD64 does not duplicate AMD64 or try ARM64",
			goos:   "windows",
			goarch: "amd64",
			want:   []string{"go-native", "rusticated-default"},
		},
		{
			name:        "Linux AMD64 with Node",
			goos:        "linux",
			goarch:      "amd64",
			nodeVersion: "v22.1.0",
			want:        []string{"go-native", "rusticated-default", "rusticated-node"},
		},
		{
			name:     "Linux ARM64 with configured emulation",
			goos:     "linux",
			goarch:   "arm64",
			canAMD64: true,
			want:     []string{"go-native", "go-amd64", "rusticated-default", "rusticated-amd64"},
		},
		{
			name:   "Linux ARM64 without emulation",
			goos:   "linux",
			goarch: "arm64",
			want:   []string{"go-native", "rusticated-default"},
		},
		{
			name:        "Apple Silicon without Rosetta",
			goos:        "darwin",
			goarch:      "arm64",
			nodeVersion: "v20.0.0",
			want:        []string{"go-native", "rusticated-default", "rusticated-node"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes := planTestModes(tt.goos, tt.goarch, tt.nodeVersion, tt.canAMD64)
			got := make([]string, len(modes))
			for i, mode := range modes {
				got[i] = mode.ID
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("mode IDs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLinuxAMD64BinfmtEnabled(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
		want bool
	}{
		{name: "enabled registration", data: "enabled\ninterpreter /usr/bin/qemu-x86_64\nflags: F\n", want: true},
		{name: "disabled registration", data: "disabled\ninterpreter /usr/bin/qemu-x86_64\n", want: false},
		{name: "missing interpreter", data: "enabled\n", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := linuxAMD64BinfmtEnabled([]byte(tt.data)); got != tt.want {
				t.Fatalf("linuxAMD64BinfmtEnabled() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestUsableNodeVersionRequirements(t *testing.T) {
	for _, version := range []struct {
		output string
		want   int
	}{
		{output: "v18.0.0", want: 18},
		{output: "v24.21.0", want: 24},
	} {
		major, err := nodeMajorVersion(version.output)
		if err != nil {
			t.Fatalf("nodeMajorVersion(%q): %v", version.output, err)
		}
		if major != version.want {
			t.Fatalf("nodeMajorVersion(%q) = %d, want %d", version.output, major, version.want)
		}
	}
	if _, err := nodeMajorVersion("v17.9.1"); err == nil {
		t.Fatal("nodeMajorVersion accepted a version below Node 18")
	}
	if _, err := nodeMajorVersion("not-a-version"); err == nil {
		t.Fatal("nodeMajorVersion accepted invalid version output")
	}
}

func TestConsumeVerboseGoTestsCollectsPassFailAndSkip(t *testing.T) {
	output := strings.NewReader(strings.Join([]string{
		"=== RUN   TestPass",
		"--- PASS: TestPass (0.01s)",
		"=== RUN   TestFail",
		"--- FAIL: TestFail (0.01s)",
		"=== RUN   TestSkip",
		"--- SKIP: TestSkip (0.00s)",
		"=== RUN   TestParent",
		"=== RUN   TestParent/child",
		"--- PASS: TestParent/child (0.00s)",
		"--- PASS: TestParent (0.00s)",
	}, "\n"))
	results := make(map[testKey]map[string]bool)
	count, err := consumeVerboseGoTests(output, "node", "example/pkg", results)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}
	for key, want := range map[testKey]bool{
		{Package: "example/pkg", Name: "TestPass"}:         true,
		{Package: "example/pkg", Name: "TestFail"}:         false,
		{Package: "example/pkg", Name: "TestSkip"}:         true,
		{Package: "example/pkg", Name: "TestParent"}:       true,
		{Package: "example/pkg", Name: "TestParent/child"}: true,
	} {
		if got := results[key]["node"]; got != want {
			t.Errorf("%s = %t, want %t", key.Name, got, want)
		}
	}
}

func TestConsumeTestJSONCollectsTestsAndPackageSetupFailures(t *testing.T) {
	events := []map[string]string{
		{"Action": "run", "Package": "example/pass", "Test": "TestPass"},
		{"Action": "pass", "Package": "example/pass", "Test": "TestPass"},
		{"Action": "run", "Package": "example/fail", "Test": "TestFail"},
		{"Action": "fail", "Package": "example/fail", "Test": "TestFail"},
		{"Action": "fail", "Package": "example/setup"},
	}
	var encoded strings.Builder
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(data)
		encoded.WriteByte('\n')
	}
	results := make(map[testKey]map[string]bool)
	count, err := consumeTestJSON(strings.NewReader(encoded.String()), "native", results)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	for key, want := range map[testKey]bool{
		{Package: "example/pass", Name: "TestPass"}:         true,
		{Package: "example/fail", Name: "TestFail"}:         false,
		{Package: "example/setup", Name: "(package setup)"}: false,
	} {
		if got := results[key]["native"]; got != want {
			t.Errorf("%s/%s = %t, want %t", key.Package, key.Name, got, want)
		}
	}
}

func TestBuildTestMatrixLeavesMissingModeResultsAbsent(t *testing.T) {
	results := map[testKey]map[string]bool{
		{Package: "example/pkg", Name: "TestFail"}:   {"native": false},
		{Package: "example/pkg", Name: "TestPass"}:   {"native": true},
	}
	modes := []testMode{
		{ID: "native", Label: "Native"},
		{ID: "node", Label: "Node"},
	}
	matrix := buildTestMatrix(results, modes)
	if len(matrix) != 2 {
		t.Fatalf("matrix length = %d, want 2", len(matrix))
	}
	if matrix[0].Test != "TestFail" || matrix[0].Results["native"] {
		t.Fatalf("failed result = %+v, want TestFail/native false", matrix[0])
	}
	if _, found := matrix[0].Results["node"]; found {
		t.Fatal("missing node result should remain absent")
	}
	if matrix[1].Test != "TestPass" || !matrix[1].Results["native"] {
		t.Fatalf("passing result = %+v, want TestPass/native true", matrix[1])
	}
	if _, found := matrix[1].Results["node"]; found {
		t.Fatal("missing node result should remain absent")
	}
}

func TestTestMatrixMarkDistinguishesPassFailureAndAbsence(t *testing.T) {
	results := map[string]bool{
		"pass": true,
		"fail": false,
	}
	for _, tt := range []struct {
		mode string
		want string
	}{
		{mode: "pass", want: "🟢"},
		{mode: "fail", want: "🔴"},
		{mode: "absent", want: "❌"},
	} {
		if got := testMatrixMark(results, tt.mode); got != tt.want {
			t.Errorf("testMatrixMark(%q) = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func TestBuildTestMatrixDropsResultsFromOmittedModes(t *testing.T) {
	results := map[testKey]map[string]bool{
		{Package: "example/pkg", Name: "(package setup)"}: {"unavailable": false},
	}
	if matrix := buildTestMatrix(results, []testMode{{ID: "native"}}); len(matrix) != 0 {
		t.Fatalf("matrix length = %d, want 0", len(matrix))
	}
}
