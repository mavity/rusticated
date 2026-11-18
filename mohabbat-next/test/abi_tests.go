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

var nativeAbiSuites = []string{
	"misc", "timer", "fs", "links", "net", "proc",
	"lifecycle_tty", "lifecycle_std_write", "lifecycle_invalid_handles",
	"lifecycle_std_close", "lifecycle_double_close", "lifecycle_handle_tokens",
	"lifecycle_stale", "lifecycle_token_reuse", "lifecycle_overlapped_once",
	"lifecycle_cancel", "lifecycle_close_reuse",
	"hostenv_initialized", "hostenv_handles", "hostenv_standard_count",
	"hostenv_stdin", "hostenv_stdout", "hostenv_stderr",
	"hostenv_next_handle", "hostenv_active_ops", "hostenv_timers",
	"hostenv_signals", "hostenv_outstanding",
	"reg_basic", "reg_unique", "reg_active", "reg_counter",
	"reg_struct", "reg_primitive", "reg_overwrite", "reg_nil",
	"reg_zero", "reg_max", "reg_cancel_state", "reg_concurrency",
	"reg_after_cleanup", "reg_memory",
}

var nativeAbiExpectedPasses = map[string][]string{
	"misc":                      {"misc/get_time", "misc/get_random", "misc/cwd_roundtrip", "misc/get_platform_info", "misc/tty_get_size"},
	"timer":                     {"timer/zero_inline", "timer/async_15ms", "timer/cancel"},
	"fs":                        {"fs/mkdir", "fs/open", "fs/write", "fs/truncate", "fs/path_stat", "fs/rename", "fs/dir_read", "fs/remove_dir"},
	"links":                     {"links/hardlink", "links/symlink", "links/readlink"},
	"net":                       {"net/lookup", "net/listen", "net/connect", "net/accept", "net/echo"},
	"proc":                      {"proc/pipe", "proc/pipe_rw"},
	"lifecycle_tty":             {"lifecycle/tty"},
	"lifecycle_std_write":       {"lifecycle/std_write"},
	"lifecycle_invalid_handles": {"lifecycle/invalid_handles"},
	"lifecycle_std_close":       {"lifecycle/std_close"},
	"lifecycle_double_close":    {"lifecycle/double_close"},
	"lifecycle_handle_tokens":   {"lifecycle/handle_tokens"},
	"lifecycle_stale":           {"lifecycle/stale"},
	"lifecycle_token_reuse":     {"lifecycle/token_reuse"},
	"lifecycle_overlapped_once": {"lifecycle/overlapped_once"},
	"lifecycle_cancel":          {"lifecycle/cancel"},
	"lifecycle_close_reuse":     {"lifecycle/close_reuse"},
	"hostenv_initialized":       {"hostenv/initialized"},
	"hostenv_handles":           {"hostenv/handles"},
	"hostenv_standard_count":    {"hostenv/standard_count"},
	"hostenv_stdin":             {"hostenv/stdin"},
	"hostenv_stdout":            {"hostenv/stdout"},
	"hostenv_stderr":            {"hostenv/stderr"},
	"hostenv_next_handle":       {"hostenv/next_handle"},
	"hostenv_active_ops":        {"hostenv/active_ops"},
	"hostenv_timers":            {"hostenv/timers"},
	"hostenv_signals":           {"hostenv/signals"},
	"hostenv_outstanding":       {"hostenv/outstanding"},
	"reg_basic": {"register/basic"},
	"reg_unique": {"register/unique"},
	"reg_active": {"register/active"},
	"reg_counter": {"register/counter"},
	"reg_struct": {"register/struct"},
	"reg_primitive": {"register/primitive"},
	"reg_overwrite": {"register/overwrite"},
	"reg_nil": {"register/nil"},
	"reg_zero": {"register/zero"},
	"reg_max": {"register/max"},
	"reg_cancel_state": {"register/cancel_state"},
	"reg_concurrency": {"register/concurrency"},
	"reg_after_cleanup": {"register/after_cleanup"},
	"reg_memory": {"register/memory"},
}

func validateNativeAbiOutput(suite string, output []byte) error {
	expected := nativeAbiExpectedPasses[suite]
	if len(expected) == 0 {
		return fmt.Errorf("no expected PASS cases configured for suite %q", suite)
	}
	seen := make(map[string]int)
	var failures []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "PASS "):
			seen[strings.TrimPrefix(line, "PASS ")]++
		case strings.HasPrefix(line, "FAIL "):
			failures = append(failures, line)
		}
	}
	if len(failures) != 0 {
		return fmt.Errorf("suite reported failures:\n%s\noutput:\n%s", strings.Join(failures, "\n"), string(output))
	}
	var missing, unexpected []string
	allowed := make(map[string]bool, len(expected))
	for _, name := range expected {
		allowed[name] = true
		switch seen[name] {
		case 0:
			missing = append(missing, name)
		case 1:
		default:
			unexpected = append(unexpected, fmt.Sprintf("%s (repeated %d times)", name, seen[name]))
		}
	}
	for name := range seen {
		if !allowed[name] {
			unexpected = append(unexpected, name)
		}
	}
	if len(missing) != 0 || len(unexpected) != 0 {
		return fmt.Errorf("suite output mismatch: missing=%v unexpected=%v\noutput:\n%s", missing, unexpected, string(output))
	}
	return nil
}

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
			return validateNativeAbiOutput(suite, out)
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
					return validateNativeAbiOutput(suite, cmdOut)
				})
			}
		}
	}
}
