package main

import (
	"os"
	"runtime"
	"strings"
)

func main() {
	vegPath := os.Getenv("MOHABBAT_VEGETABLE_PATH")
	inVeg := vegPath != ""

	if inVeg {
		// We used to override shell temp vars here, but that caused permission issues in 'target'.
		// Now we rely on the host-provided /tmp mapping.
	}

	// Parse args manually: [project] [-o out] [-r [args...]]
	rawArgs := os.Args[1:]
	projectDir := ""
	outputPath := ""
	runMode := false
	var runArgs []string

	for i := 0; i < len(rawArgs); {
		// If in a vegetable, skip the vegetable path itself if it appears in args.
		arg := rawArgs[i]
		if inVeg && (arg == vegPath || (runtime.GOOS == "windows" && strings.EqualFold(arg, vegPath))) {
			i++
			continue
		}

		switch arg {
		case "-r":
			runMode = true
			runArgs = rawArgs[i+1:]
			i = len(rawArgs)
		case "-o":
			if i+1 < len(rawArgs) {
				outputPath = rawArgs[i+1]
				i += 2
			} else {
				die("missing argument after -o")
			}
		default:
			if projectDir == "" && !strings.HasPrefix(arg, "-") {
				projectDir = arg
			}
			i++
		}
	}

	ws, err := resolveWorkspace("")
	must(err)

	// Heuristic: if -r was used and projectDir remains empty, check if first runArg is a project.
	if projectDir == "" && runMode && len(runArgs) > 0 {
		if isProject(ws, runArgs[0]) {
			projectDir = runArgs[0]
			runArgs = runArgs[1:]
		}
	}

	switch {
	case runMode:
		// Mode 4: build project to WASM + run immediately under washmhost-go.
		// Defaults to current directory if no projectDir was specified.
		if projectDir == "" {
			projectDir = "."
		}
		must(modeDevRun(ws, projectDir, runArgs))
	case projectDir != "" && outputPath != "" && inVeg:
		// Mode 2: juice bottle refill (running as WASM brain inside a vegetable)
		must(doRefill(ws, projectDir, vegPath, outputPath))
	case projectDir != "" && outputPath != "":
		// Mode 3: native fresh assembly with arbitrary payload
		must(modePackage(ws, projectDir, outputPath))
	default:
		// Mode 1: full build pipeline
		must(modeBuild(ws))
	}
}
