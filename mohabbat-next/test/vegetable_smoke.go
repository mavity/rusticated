package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type vegetableSmokeCase struct {
	name              string
	args              []string
	expectedStdout    string
	expectedStderr    string
	expectedExitCode  int
	expectEmptyStdout bool
}

func (r *testRun) runKabibiVegetableSmoke() {
	if !r.matches("kabibi") {
		return
	}

	outputPath := filepath.Join(r.logDir, "kabibi-smoke")
	if runtime.GOOS == "windows" {
		outputPath += ".bat"
	}
	if err := r.runCommand(commandSpec{
		Runner: "build",
		Target: "native-" + runtime.GOOS + "-" + runtime.GOARCH,
		Suite:  "build Kabibi vegetable",
		Dir:    r.root,
		Args:   []string{"go", "run", ".", "kabibi", "-o", outputPath},
		Parse:  outputEvents("build", "native"),
	}); err != nil {
		return
	}

	for _, test := range []vegetableSmokeCase{
		{name: "--version prints the Kabibi version", args: []string{"--version"}, expectedStdout: "Kabibi "},
		{name: "--help prints CLI usage", args: []string{"--help"}, expectedStdout: "Usage:"},
		{name: "--disable-ai -c writes shell output", args: []string{"--disable-ai", "-c", "echo RUSTICATED_ABI_SMOKE"}, expectedStdout: "RUSTICATED_ABI_SMOKE"},
		{name: "--verbose --version writes diagnostics to stderr", args: []string{"--verbose", "--version"}, expectedStdout: "Kabibi ", expectedStderr: "[kabibi] CLI parse begin:"},
		{name: "unknown shell command returns 127 on stderr", args: []string{"--disable-ai", "-c", "__rusticated_smoke_missing_command__"}, expectedStderr: "executable file not found in $PATH", expectedExitCode: 127, expectEmptyStdout: true},
	} {
		r.runKabibiVegetableSmokeCase(outputPath, test)
	}
}

func (r *testRun) runKabibiVegetableSmokeCase(path string, test vegetableSmokeCase) {
	target := "native-" + runtime.GOOS + "-" + runtime.GOARCH
	foundStdout := test.expectedStdout == ""
	foundStderr := test.expectedStderr == ""
	sawStdout := false
	parse := func(stream, line string) []testEvent {
		switch stream {
		case "stdout":
			sawStdout = true
			foundStdout = foundStdout || strings.Contains(line, test.expectedStdout)
		case "stderr":
			foundStderr = foundStderr || strings.Contains(line, test.expectedStderr)
		}
		return outputEvents("kabibi", target)(stream, line)
	}
	args := append([]string(nil), test.args...)
	if runtime.GOOS == "windows" {
		args = append([]string{"cmd.exe", "/d", "/c", "call", path}, args...)
	} else {
		args = append([]string{path}, args...)
	}
	err := r.runCommand(commandSpec{
		Runner:           "kabibi",
		Target:           target,
		Suite:            test.name,
		Name:             test.name,
		Dir:              r.root,
		Args:             args,
		ExpectedExitCode: test.expectedExitCode,
		Env:              setEnvironment(os.Environ(), map[string]string{"MOHABBAT_VERBOSE": "0"}),
		Parse:            parse,
	})
	if err != nil {
		return
	}
	var problems []string
	if !foundStdout {
		problems = append(problems, fmt.Sprintf("stdout did not contain %q", test.expectedStdout))
	}
	if !foundStderr {
		problems = append(problems, fmt.Sprintf("stderr did not contain %q", test.expectedStderr))
	}
	if test.expectEmptyStdout && sawStdout {
		problems = append(problems, "stdout was not empty")
	}
	if len(problems) > 0 {
		detail := strings.Join(problems, "; ")
		if emitErr := r.sink.emit(testEvent{
			Kind: "fail", Runner: "kabibi", Target: target, Suite: test.name, Name: test.name, Detail: detail,
		}); emitErr != nil {
			r.firstError = errors.Join(r.firstError, emitErr)
		}
		r.firstError = errors.Join(r.firstError, fmt.Errorf("%s: %s", test.name, detail))
		return
	}
	if err := r.sink.emit(testEvent{
		Kind: "pass", Runner: "kabibi", Target: target, Suite: test.name, Name: test.name,
	}); err != nil {
		r.firstError = errors.Join(r.firstError, err)
	}
}
