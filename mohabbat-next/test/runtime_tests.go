package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func (r *Runner) RunStage3CraneliftRuntimeTests() {
	compDir := filepath.Join(r.BuildDir, "components")
	rtAarch64 := filepath.Join(compDir, "runtime-aarch64.bin")
	rtX86_64 := filepath.Join(compDir, "runtime-x86_64.bin")

	if runtime.GOOS == "windows" {
		if runtime.GOARCH == "arm64" {
			// 1. Native AArch64 Movable Cranelift Runtime Slice
			r.RunTest("runtime/aarch64-native", "cold-jit-and-warm-retriggers", func() error {
				cmd := exec.Command(r.DistExe, "--test-runtime", rtAarch64)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				return cmd.Run()
			})

			// 2. Emulated x86_64 Movable Cranelift Runtime Slice
			x64Exe := filepath.Join(r.BuildDir, "MohabbatWindowsHost-x64.exe")
			if _, err := os.Stat(x64Exe); err == nil {
				r.RunTest("runtime/x86_64-emulated", "cold-jit-and-warm-retriggers", func() error {
					cmd := exec.Command(x64Exe, "--test-runtime", rtX86_64)
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					return cmd.Run()
				})
			} else {
				r.RecordDisabled("runtime/x86_64-emulated", "cold-jit-and-warm-retriggers", fmt.Sprintf("x64 host binary not available: %v", err))
			}
		} else if runtime.GOARCH == "amd64" {
			r.RunTest("runtime/x86_64-native", "cold-jit-and-warm-retriggers", func() error {
				cmd := exec.Command(r.DistExe, "--test-runtime", rtX86_64)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				return cmd.Run()
			})
			r.RecordDisabled("runtime/aarch64", "cold-jit-and-warm-retriggers", "Windows amd64 host cannot execute AArch64 movable machine code natively")
		}
		return
	}

	r.RecordDisabled(
		"runtime/jit-slice",
		"cold-jit-and-warm-retriggers",
		fmt.Sprintf("host GOOS=%s runtime loader harness not yet wired (tested on Windows CLR host)", runtime.GOOS),
	)
}
