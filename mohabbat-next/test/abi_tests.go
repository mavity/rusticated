package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

	if wslPath, err := exec.LookPath("wsl.exe"); err == nil {
		ctxCheck, cancelCheck := context.WithTimeout(context.Background(), wslStartupTimeout)
		defer cancelCheck()
		if out, err := exec.CommandContext(ctxCheck, wslPath, "-e", "uname", "-m").Output(); err == nil {
			arch := strings.TrimSpace(string(out))
			drive := strings.ToLower(r.DistExe[:1])
			unixBundle := "/mnt/" + drive + "/" + filepath.ToSlash(r.DistExe[3:])
			for _, suite := range nativeAbiSuites {
				suite := suite
				r.RunTest("abi/wsl-"+arch, suite, func() error {
					wslDir := fmt.Sprintf("/tmp/mohabbat-abi-%d-%s", os.Getpid(), suite)
					script := fmt.Sprintf("mkdir -p %s && bash %s --abi-test %s %s; rc=$?; rm -rf %s; exit $rc",
						wslDir, unixBundle, wslDir, suite, wslDir)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, wslPath, "-e", "sh", "-c", script)
					cmdOut, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("%w\n%s", err, string(cmdOut))
					}
					return nil
				})
			}
		}
	}
}
