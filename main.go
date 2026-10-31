package main

import (
	"os"
	"runtime"
	"strings"

	"mohabbat/mohabbat"
)

func main() {
	inVeg := runtime.GOOS == "wasip1"
	vegPath := ""
	if inVeg && len(os.Args) > 0 {
		vegPath = os.Args[0]
	}

	if inVeg {
		// We used to override shell temp vars here, but that caused permission issues in 'target'.
		// Now we rely on the host-provided /tmp mapping.
	}

	// Parse args manually: [project] [-o out] [-r [args...]] [-t [args...]]
	rawArgs := os.Args[1:]
	projectDir := ""
	outputPath := ""
	platformOverride := ""
	runMode := false
	testMode := false
	verbose := false
	var runArgs []string

	for i := 0; i < len(rawArgs); {
		// If in a vegetable, skip the vegetable path itself if it appears in args.
		arg := rawArgs[i]
		if inVeg && vegPath != "" && (arg == vegPath || (runtime.GOOS == "windows" && strings.EqualFold(arg, vegPath))) {
			i++
			continue
		}

		switch arg {
		case "-h", "--help":
			os.Stdout.WriteString("Usage: mohab [project] [-o out] [-r [args...]] [--platform name] [-v|--verbose]\n")
			os.Exit(0)
		case "--version":
			ws, err := mohabbat.ResolveWorkspace("")
			mohabbat.Must(err)
			meta := mohabbat.GetBuildMetadata(ws)
			os.Stdout.WriteString(meta.Version + "\n")
			os.Exit(0)
		case "--platform":
			if i+1 < len(rawArgs) {
				platformOverride = rawArgs[i+1]
				i += 2
			} else {
				mohabbat.Die("missing argument after --platform")
			}
		case "-v", "--verbose":
			verbose = true
			i++
		case "-r":
			runMode = true
			runArgs = rawArgs[i+1:]
			i = len(rawArgs)
		case "-t", "--test":
			testMode = true
			runArgs = rawArgs[i+1:]
			i = len(rawArgs)
		case "-o":
			if i+1 < len(rawArgs) {
				outputPath = rawArgs[i+1]
				i += 2
			} else {
				mohabbat.Die("missing argument after -o")
			}
		default:
			if projectDir == "" && !strings.HasPrefix(arg, "-") {
				projectDir = arg
			}
			i++
		}
	}

	ws, err := mohabbat.ResolveWorkspace("")
	mohabbat.Must(err)

	// Clean up runArgs if --platform was placed after -r
	var cleanedRunArgs []string
	for i := 0; i < len(runArgs); i++ {
		if runArgs[i] == "--platform" && i+1 < len(runArgs) {
			platformOverride = runArgs[i+1]
			i++
		} else {
			cleanedRunArgs = append(cleanedRunArgs, runArgs[i])
		}
	}
	runArgs = cleanedRunArgs

	// Heuristic: if -r or -t was used and projectDir remains empty, check if first runArg is a project.
	if projectDir == "" && (runMode || testMode) && len(runArgs) > 0 {
		if mohabbat.IsProject(ws, runArgs[0]) {
			projectDir = runArgs[0]
			runArgs = runArgs[1:]
		}
	}
	runArgs = normalizeRunArgs(runArgs, testMode)

	switch {
	case runMode:
		if projectDir == "" {
			projectDir = "."
		}
		mohabbat.Must(mohabbat.ModeDevRun(ws, projectDir, runArgs, platformOverride, verbose))
	case testMode:
		if projectDir == "" {
			projectDir = "."
		}
		mohabbat.Must(mohabbat.ModeTest(ws, projectDir, runArgs, platformOverride, verbose))
	case projectDir != "" && outputPath != "" && inVeg:
		mohabbat.Must(mohabbat.DoRefill(ws, projectDir, vegPath, outputPath, verbose))
	case projectDir != "" && outputPath != "":
		mohabbat.Must(mohabbat.ModePackage(ws, projectDir, outputPath, verbose))
	default:
		mohabbat.Must(mohabbat.ModeBuild(ws, verbose))
	}
}

func normalizeRunArgs(args []string, testMode bool) []string {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if !testMode {
		return args
	}

	normalized := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-test" && i+1 < len(args) && strings.HasPrefix(args[i+1], ".") {
			normalized = append(normalized, args[i]+args[i+1])
			i++
			continue
		}
		normalized = append(normalized, args[i])
	}
	return normalized
}
