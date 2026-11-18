package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

var nativeAbiSuites = []string{"misc", "timer", "fs", "links", "net", "proc"}

func (r *Runner) RunStage3NativeAbiConformanceTests() {
	if runtime.GOOS != "windows" {
		return
	}

	tmpRoot, err := os.MkdirTemp("", "mohabbat-abi-*")
	if err != nil {
		r.RunTest("abi/native", "setup", func() error { return err })
		return
	}
	defer os.RemoveAll(tmpRoot)

	for _, suite := range nativeAbiSuites {
		suite := suite
		r.RunTest("abi/native-"+runtime.GOARCH, suite, func() error {
			workDir := filepath.Join(tmpRoot, "native-"+suite)
			if err := os.MkdirAll(workDir, 0o755); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, r.DistExe, "--abi-test", workDir, suite)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w\n%s", err, string(out))
			}
			return nil
		})
	}
}
