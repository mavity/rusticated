package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type runtimeScenario struct {
	name string
	arg  string
}

var runtimeScenarios = []runtimeScenario{
	{name: "empty-function-codegen", arg: "empty"},
	{name: "store-only-codegen", arg: "store"},
	{name: "integer-add-store-codegen", arg: "add"},
	{name: "load-add-store-cold-and-warm", arg: "load-add-store"},
}

func runRuntimeScenarios(r *Runner, suite, hostExe, runtimeBin string) {
	for _, scenario := range runtimeScenarios {
		scenario := scenario
		r.RunTest(suite, scenario.name, func() error {
			cmd := exec.Command(hostExe, "--test-runtime", runtimeBin, scenario.arg)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		})
	}
}

func (r *Runner) RunStage3CraneliftRuntimeTests() {
	compDir := filepath.Join(r.BuildDir, "components")
	rtAarch64 := filepath.Join(compDir, "runtime-aarch64.bin")
	rtX86_64 := filepath.Join(compDir, "runtime-x86_64.bin")

	if runtime.GOOS == "windows" {
		if runtime.GOARCH == "arm64" {
			// 1. Native AArch64 Movable Cranelift Runtime Slice
			runRuntimeScenarios(r, "runtime/aarch64-native", r.DistExe, rtAarch64)

			// 2. Emulated x86_64 Movable Cranelift Runtime Slice
			x64Exe := filepath.Join(r.BuildDir, "MohabbatWindowsHost-x64.exe")
			if _, err := os.Stat(x64Exe); err == nil {
				runRuntimeScenarios(r, "runtime/x86_64-emulated", x64Exe, rtX86_64)
			} else {
				for _, scenario := range runtimeScenarios {
					r.RecordDisabled("runtime/x86_64-emulated", scenario.name, fmt.Sprintf("x64 host binary not available: %v", err))
				}
			}
		} else if runtime.GOARCH == "amd64" {
			runRuntimeScenarios(r, "runtime/x86_64-native", r.DistExe, rtX86_64)
			for _, scenario := range runtimeScenarios {
				r.RecordDisabled("runtime/aarch64", scenario.name, "Windows amd64 host cannot execute AArch64 movable machine code natively")
			}
		}
		return
	}

	for _, scenario := range runtimeScenarios {
		r.RecordDisabled(
			"runtime/jit-slice",
			scenario.name,
			fmt.Sprintf("host GOOS=%s runtime loader harness not yet wired (tested on Windows CLR host)", runtime.GOOS),
		)
	}
}
