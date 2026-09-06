package main

import (
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type dylibTarget struct {
	goos   string
	goarch string
}

func findWorkspaceRoot() string {
	starts := []string{}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if len(os.Args) > 0 && os.Args[0] != "" {
		starts = append(starts, filepath.Dir(os.Args[0]))
	}
	for _, start := range starts {
		for dir := start; dir != ""; {
			if _, err := os.Stat(filepath.Join(dir, "sysroot.toml")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

func resolveGoBinary() string {
	goExe := "go"
	if runtime.GOOS == "windows" {
		goExe = "go.exe"
	}
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		cand := filepath.Join(goroot, "bin", goExe)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return goExe
}

// detectDylibTarget inspects a native dynamic library and returns the GOOS/GOARCH
// that a satellite must target to load it.
func detectDylibTarget(path string) (dylibTarget, error) {
	switch runtime.GOOS {
	case "windows":
		f, err := pe.Open(path)
		if err != nil {
			return dylibTarget{}, err
		}
		defer f.Close()

		switch f.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			return dylibTarget{goos: "windows", goarch: "amd64"}, nil
		case pe.IMAGE_FILE_MACHINE_ARM64:
			return dylibTarget{goos: "windows", goarch: "arm64"}, nil
		case pe.IMAGE_FILE_MACHINE_I386:
			return dylibTarget{goos: "windows", goarch: "386"}, nil
		default:
			return dylibTarget{}, fmt.Errorf("unsupported PE machine 0x%x", f.Machine)
		}
	default:
		return dylibTarget{goos: runtime.GOOS, goarch: runtime.GOARCH}, nil
	}
}
