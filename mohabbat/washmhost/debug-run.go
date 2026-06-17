//go:build ignore

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run washmhost/debug-run.go <package-dir> [args...]")
		os.Exit(1)
	}

	projectDir := os.Args[1]
	projectName := filepath.Base(projectDir)

	// Determine workspace root. We assume we are run from the workspace root.
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
		os.Exit(1)
	}

	workspaceRoot := wd
	// Minimal check to see if we are likely in the right place.
	if _, err := os.Stat(filepath.Join(workspaceRoot, "washmhost")); err != nil {
		// Try parent if we are inside washmhost
		if filepath.Base(workspaceRoot) == "washmhost" {
			workspaceRoot = filepath.Dir(workspaceRoot)
		} else {
			fmt.Fprintf(os.Stderr, "Error: Could not find washmhost in current directory (%s). Please run from the workspace root.\n", wd)
			os.Exit(1)
		}
	}

	overlayPath := filepath.Join(workspaceRoot, "target", "overlay.json")
	if _, err := os.Stat(overlayPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "\n!!! ERROR: %s MISSING !!!\n", overlayPath)
		fmt.Fprintf(os.Stderr, "You need to generate the overlay first. Run:\n")
		fmt.Fprintf(os.Stderr, "  cargo run -p prebuild\n\n")
		os.Exit(1)
	}

	outputWasm := filepath.Join(workspaceRoot, "target", projectName+".wasm")

	fmt.Printf("🍆 Building Go package: %s -> %s\n", projectDir, outputWasm)

	// Resolve specific Go binary for the required version from mohabbat/go.mod
	goBin := "go"
	goModPath := filepath.Join(workspaceRoot, "mohabbat", "go.mod")
	if f, err := os.Open(goModPath); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "go ") {
				ver := strings.TrimSpace(strings.TrimPrefix(line, "go "))
				// check %HOME%/sdk/go{ver}/bin/go
				home, _ := os.UserHomeDir()
				if home != "" {
					sdkBin := filepath.Join(home, "sdk", "go"+ver, "bin", "go")
					if runtime.GOOS == "windows" {
						sdkBin += ".exe"
					}
					if _, err := os.Stat(sdkBin); err == nil {
						fmt.Printf("     > using Go binary from %%HOME%%/sdk: %s\n", sdkBin)
						goBin = sdkBin
						break
					}
				}
			}
		}
		f.Close()
	}

	// Normalize projectDir to absolute path to avoid confusion when changing CWD
	absProjectDir, err := filepath.Abs(projectDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting absolute path for %s: %v\n", projectDir, err)
		os.Exit(1)
	}

	cmd := exec.Command(goBin, "build", "-buildmode=c-shared", "-overlay", overlayPath, "-o", outputWasm, ".")
	cmd.Dir = absProjectDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n!!! GO BUILD FAILED !!!\n%v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🍆 Post-processing %s (rename _initialize -> run)\n", outputWasm)
	if err := renameInitializeToRun(outputWasm); err != nil {
		fmt.Fprintf(os.Stderr, "!!! WASM POST-PROCESS FAILED !!!\n%v\n", err)
		os.Exit(1)
	}

	// Ensure the output wasm is runnable on Unix-like systems (though we are on Windows, good to follow Mohabbat)
	if runtime.GOOS != "windows" {
		_ = os.Chmod(outputWasm, 0o755)
	}

	fmt.Printf("🍆 Running %s under washmhost\n", outputWasm)

	// Run washmhost
	// Pass any additional arguments
	runArgs := []string{"run", ".", "payload.wasm"}
	if len(os.Args) > 2 {
		runArgs = append(runArgs, os.Args[2:]...)
	}

	runCmd := exec.Command("go", runArgs...)
	runCmd.Dir = filepath.Join(workspaceRoot, "washmhost")
	runCmd.Env = append(os.Environ(), "MOHABBAT_WASM_FD="+outputWasm)
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	runCmd.Stdin = os.Stdin

	if err := runCmd.Run(); err != nil {
		// If it's just a non-zero exit code from the guest, we might not want to "error loudly" here,
		// but let's follow the user's desire for friction-less debugging.
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "washmhost execution failed: %v\n", err)
		os.Exit(1)
	}
}

func renameInitializeToRun(wasmPath string) error {
	data, err := os.ReadFile(wasmPath)
	if err != nil {
		return err
	}

	if len(data) < 8 {
		return fmt.Errorf("invalid wasm file")
	}

	pos := 8
	var newData []byte
	newData = append(newData, data[:8]...)

	for pos < len(data) {
		sectionID := data[pos]
		pos++
		size, n, err := readVarUint32(data[pos:])
		if err != nil {
			return err
		}
		pos += n
		sectionEnd := pos + int(size)

		if sectionID == 7 { // Export section
			exportData := data[pos:sectionEnd]
			count, n2, err := readVarUint32(exportData)
			if err != nil {
				return err
			}

			var newExportSec []byte
			newExportSec = append(newExportSec, encodeVarUint32(count)...)

			p := n2
			found := false
			for i := uint32(0); i < count; i++ {
				nameLen, n3, err := readVarUint32(exportData[p:])
				if err != nil {
					return err
				}
				p += n3
				name := string(exportData[p : p+int(nameLen)])
				p += int(nameLen)

				kind := exportData[p]
				p++
				idx, n4, err := readVarUint32(exportData[p:])
				if err != nil {
					return err
				}
				p += n4

				if name == "_initialize" {
					name = "run"
					found = true
				}

				newExportSec = append(newExportSec, encodeVarUint32(uint32(len(name)))...)
				newExportSec = append(newExportSec, name...)
				newExportSec = append(newExportSec, kind)
				newExportSec = append(newExportSec, encodeVarUint32(idx)...)
			}

			if found {
				newData = append(newData, sectionID)
				newData = append(newData, encodeVarUint32(uint32(len(newExportSec)))...)
				newData = append(newData, newExportSec...)
			} else {
				newData = append(newData, data[pos-n-1:sectionEnd]...)
			}
		} else {
			newData = append(newData, data[pos-n-1:sectionEnd]...)
		}
		pos = sectionEnd
	}

	return os.WriteFile(wasmPath, newData, 0o644)
}

func readVarUint32(data []byte) (uint32, int, error) {
	var res uint32
	var shift uint
	for i, b := range data {
		res |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			return res, i + 1, nil
		}
		shift += 7
		if shift >= 32 {
			break
		}
	}
	return 0, 0, fmt.Errorf("invalid leb128")
}

func encodeVarUint32(v uint32) []byte {
	var res []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			res = append(res, b|0x80)
		} else {
			res = append(res, b)
			break
		}
	}
	return res
}
