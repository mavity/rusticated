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
	"sysmisc_debug_valid", "sysmisc_debug_invalid",
	"sysmisc_signal_registration", "sysmisc_signal_fire",
	"sysmisc_signal_overwrite", "sysmisc_errno_translation",
	"sysmisc_overlapped_oob", "sysmisc_overlapped_success",
	"sysmisc_signal_cancellation", "sysmisc_args_availability",
	"sysmisc_env_availability", "sysmisc_poll_no_work",
	"sysmisc_cwd_resolution", "sysmisc_debug_extreme",
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
	"isop_basic", "isop_wrong_ptr", "isop_wrong_id", "isop_both_wrong",
	"isop_inactive_overwrite", "isop_manual_remove", "isop_canceled",
	"isop_cross_talk", "isop_zero_ptr", "isop_id_zero", "isop_concurrency",
	"isop_reinit", "isop_max_ptr", "isop_missing",
	"decops_01_simple", "decops_02_multiple", "decops_03_underflow",
	"decops_04_double_underflow", "decops_05_concurrency", "decops_06_lifecycle",
	"decops_07_sequence", "decops_08_until_zero", "decops_09_interleaved",
	"decops_10_has_live_ops", "decops_11_large", "decops_12_cas_consistency",
	"decops_13_no_active_side_effect", "decops_14_outstanding_consistency",
	"cancelop_01_basic", "cancelop_02_deadline", "cancelop_03_stops_timer",
	"cancelop_04_removes_map", "cancelop_05_removes_active", "cancelop_06_decrements",
	"cancelop_07_idempotency", "cancelop_08_nonexistent", "cancelop_09_already_cancelled",
	"cancelop_10_nil_handle", "cancelop_11_non_deadline", "cancelop_12_pressure",
	"cancelop_13_overwrite", "cancelop_14_cleanup_only",
}

var nativeAbiExpectedPasses = map[string][]string{
	"misc":                              {"misc/get_time", "misc/get_random", "misc/cwd_roundtrip", "misc/get_platform_info", "misc/tty_get_size"},
	"sysmisc_debug_valid":               {"sysmisc/debug_valid"},
	"sysmisc_debug_invalid":             {"sysmisc/debug_invalid"},
	"sysmisc_signal_registration":       {"sysmisc/signal_registration"},
	"sysmisc_signal_fire":               {"sysmisc/signal_fire"},
	"sysmisc_signal_overwrite":          {"sysmisc/signal_overwrite"},
	"sysmisc_errno_translation":         {"sysmisc/errno_translation"},
	"sysmisc_overlapped_oob":            {"sysmisc/overlapped_oob"},
	"sysmisc_overlapped_success":        {"sysmisc/overlapped_success"},
	"sysmisc_signal_cancellation":       {"sysmisc/signal_cancellation"},
	"sysmisc_args_availability":         {"sysmisc/args_availability"},
	"sysmisc_env_availability":          {"sysmisc/env_availability"},
	"sysmisc_poll_no_work":              {"sysmisc/poll_no_work"},
	"sysmisc_cwd_resolution":            {"sysmisc/cwd_resolution"},
	"sysmisc_debug_extreme":             {"sysmisc/debug_extreme"},
	"timer":                             {"timer/zero_inline", "timer/async_15ms", "timer/cancel"},
	"fs":                                {"fs/mkdir", "fs/open", "fs/write", "fs/truncate", "fs/path_stat", "fs/rename", "fs/dir_read", "fs/remove_dir"},
	"links":                             {"links/hardlink", "links/symlink", "links/readlink"},
	"net":                               {"net/lookup", "net/listen", "net/connect", "net/accept", "net/echo"},
	"proc":                              {"proc/pipe", "proc/pipe_rw"},
	"lifecycle_tty":                     {"lifecycle/tty"},
	"lifecycle_std_write":               {"lifecycle/std_write"},
	"lifecycle_invalid_handles":         {"lifecycle/invalid_handles"},
	"lifecycle_std_close":               {"lifecycle/std_close"},
	"lifecycle_double_close":            {"lifecycle/double_close"},
	"lifecycle_handle_tokens":           {"lifecycle/handle_tokens"},
	"lifecycle_stale":                   {"lifecycle/stale"},
	"lifecycle_token_reuse":             {"lifecycle/token_reuse"},
	"lifecycle_overlapped_once":         {"lifecycle/overlapped_once"},
	"lifecycle_cancel":                  {"lifecycle/cancel"},
	"lifecycle_close_reuse":             {"lifecycle/close_reuse"},
	"hostenv_initialized":               {"hostenv/initialized"},
	"hostenv_handles":                   {"hostenv/handles"},
	"hostenv_standard_count":            {"hostenv/standard_count"},
	"hostenv_stdin":                     {"hostenv/stdin"},
	"hostenv_stdout":                    {"hostenv/stdout"},
	"hostenv_stderr":                    {"hostenv/stderr"},
	"hostenv_next_handle":               {"hostenv/next_handle"},
	"hostenv_active_ops":                {"hostenv/active_ops"},
	"hostenv_timers":                    {"hostenv/timers"},
	"hostenv_signals":                   {"hostenv/signals"},
	"hostenv_outstanding":               {"hostenv/outstanding"},
	"reg_basic":                         {"register/basic"},
	"reg_unique":                        {"register/unique"},
	"reg_active":                        {"register/active"},
	"reg_counter":                       {"register/counter"},
	"reg_struct":                        {"register/struct"},
	"reg_primitive":                     {"register/primitive"},
	"reg_overwrite":                     {"register/overwrite"},
	"reg_nil":                           {"register/nil"},
	"reg_zero":                          {"register/zero"},
	"reg_max":                           {"register/max"},
	"reg_cancel_state":                  {"register/cancel_state"},
	"reg_concurrency":                   {"register/concurrency"},
	"reg_after_cleanup":                 {"register/after_cleanup"},
	"reg_memory":                        {"register/memory"},
	"isop_basic":                        {"isop/basic"},
	"isop_wrong_ptr":                    {"isop/wrong_ptr"},
	"isop_wrong_id":                     {"isop/wrong_id"},
	"isop_both_wrong":                   {"isop/both_wrong"},
	"isop_inactive_overwrite":           {"isop/inactive_overwrite"},
	"isop_manual_remove":                {"isop/manual_remove"},
	"isop_canceled":                     {"isop/canceled"},
	"isop_cross_talk":                   {"isop/cross_talk"},
	"isop_zero_ptr":                     {"isop/zero_ptr"},
	"isop_id_zero":                      {"isop/id_zero"},
	"isop_concurrency":                  {"isop/concurrency"},
	"isop_reinit":                       {"isop/reinit"},
	"isop_max_ptr":                      {"isop/max_ptr"},
	"isop_missing":                      {"isop/missing"},
	"decops_01_simple":                  {"decops/01_simple_decrement_1_to_0"},
	"decops_02_multiple":                {"decops/02_decrement_from_multiple"},
	"decops_03_underflow":               {"decops/03_underflow_protection"},
	"decops_04_double_underflow":        {"decops/04_double_underflow"},
	"decops_05_concurrency":             {"decops/05_high_concurrency_inc_dec"},
	"decops_06_lifecycle":               {"decops/06_lifecycle_register_then_dec"},
	"decops_07_sequence":                {"decops/07_sequence_of_inc_dec"},
	"decops_08_until_zero":              {"decops/08_dec_ops_until_zero_in_loop"},
	"decops_09_interleaved":             {"decops/09_interleaved_contention_10000_ops"},
	"decops_10_has_live_ops":            {"decops/10_effect_on_has_live_ops"},
	"decops_11_large":                   {"decops/11_dec_ops_on_large_value"},
	"decops_12_cas_consistency":         {"decops/12_cas_loop_atomic_load_consistency"},
	"decops_13_no_active_side_effect":   {"decops/13_verify_no_side_effect_on_active_ops"},
	"decops_14_outstanding_consistency": {"decops/14_has_outstanding_ops_consistency"},
	"cancelop_01_basic":                 {"cancelop/01_basic_cancellation_flag"},
	"cancelop_02_deadline":              {"cancelop/02_deadline_setting_on_handle"},
	"cancelop_03_stops_timer":           {"cancelop/03_timer_cancellation_stops_timer"},
	"cancelop_04_removes_map":           {"cancelop/04_timer_cancellation_removes_from_map"},
	"cancelop_05_removes_active":        {"cancelop/05_timer_cancellation_removes_from_active_ops"},
	"cancelop_06_decrements":            {"cancelop/06_timer_cancellation_decrements_counter"},
	"cancelop_07_idempotency":           {"cancelop/07_idempotency_of_cancellation"},
	"cancelop_08_nonexistent":           {"cancelop/08_cancel_non_existent_op"},
	"cancelop_09_already_cancelled":     {"cancelop/09_cancel_already_cancelled_op"},
	"cancelop_10_nil_handle":            {"cancelop/10_nil_handle_safety"},
	"cancelop_11_non_deadline":          {"cancelop/11_non_deadline_handle_safety"},
	"cancelop_12_pressure":              {"cancelop/12_concurrent_cancellation_pressure"},
	"cancelop_13_overwrite":             {"cancelop/13_cancel_after_overwrite"},
	"cancelop_14_cleanup_only":          {"cancelop/14_cleanup_only_timer_cancel"},
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
