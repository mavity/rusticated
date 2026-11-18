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
	"TestResolveGuestDriveRootedPathDoesNotPrefixCwd",
	"TestSysGetPlatformInfoCanonicalizesHostPaths",
	"TestSysGetPlatformInfoSerializesSystemContext",
	"TestSystemInfoWaitCompletesWhenContextChanges",
	"TestCancelSystemInfoWaitRemovesWaiterAndTimer",
	"abi_proc_args_metadata", "abi_proc_args_full", "abi_proc_args_small_exact",
	"abi_proc_env_metadata", "abi_proc_env_full", "abi_proc_env_aliases",
	"abi_proc_env_preserved_dirs", "abi_proc_env_percent", "abi_proc_env_no_host_leak",
	"abi_proc_cwd", "abi_proc_cwd_invalid", "abi_proc_spawn_wait",
	"abi_proc_signal_invalid", "abi_proc_wait_invalid", "abi_proc_invalid_exe",
	"abi_proc_pty",
	"TestGuestEnvCanonicalizesWindowsPathAliases",
	"TestGuestEnvDoesNotExposeWashmhostHostSettings",
	"TestGuestEnvPreservesWindowsUserDirs",
	"TestGuestEnvExpandsPercentVarInPath",
	"TestGuestEnvDoesNotRewriteTempOrUserPaths",
	"TestSysProcEnv",
	"TestStartWithPtyChildCanUseControllingTerminal",
	"TestGuestRuntimeEnvOmitsCwdValues",
	"TestHostEnvTracksModuleCwd",
	"TestGuestRuntimeEnvDoesNotInjectHostHomeAndTemp",
	"TestCurrentHostSystemContextIgnoresInventedEnvVars",
	"TestDecodeWashmhostGuestArgs",
	"TestRunHostHonorsTestTimeout",
	"TestSysFs",
	"TestSysHandleTruncate",
	"TestMapErrnoEFBIG",
	"TestSysReadExtended",
	"TestSysWriteExtended",
	"TestDirEntryUnknownTypeUsesFileKind",
	"TestSysDirReadExtended",
	"TestSysPathSetTimes",
	"TestSysPathSetTimesNoFollowSymlink",
	"TestPathLinkOperations",
	"TestCreateAbiStat",
	"TestCreateAbiStatUsesNativeMetadata",
	"TestStatModeAndKindPreservesUnixTypeAndSpecialBits",
	"TestPosixModeToGoFileModePreservesSpecialBits",
	"TestMarshalAbiStat",
	"TestSysNet",
	"misc", "timer", "fs", "links", "net", "proc",
	"sys_time_now_exhaustive", "sys_get_time_exhaustive",
	"sys_get_random_exhaustive", "sys_timer_set_extended",
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
	"sysfs_01_create_open", "sysfs_02_write_read",
	"sysfs_03_stat_file", "sysfs_04_stat_directory",
	"sysfs_05_missing", "sysfs_06_chmod",
	"sysfs_07_directory_read", "sysfs_08_read_past_eof",
	"sysfs_09_read_only_handle", "sysfs_10_read_from_directory",
	"sysfs_11_double_close", "sysfs_12_invalid_handle",
	"sysfs_13_jail_break",
}

var nativeAbiExpectedPasses = map[string][]string{
	"TestResolveGuestDriveRootedPathDoesNotPrefixCwd": {
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/01",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/02",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/03",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/04",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/05",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/06",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/07",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/08",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/09",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/10",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/11",
		"ResolveGuestDriveRootedPathDoesNotPrefixCwd/12",
	},
	"TestSysGetPlatformInfoCanonicalizesHostPaths": {
		"SysGetPlatformInfoCanonicalizesHostPaths/01",
		"SysGetPlatformInfoCanonicalizesHostPaths/02",
		"SysGetPlatformInfoCanonicalizesHostPaths/03",
		"SysGetPlatformInfoCanonicalizesHostPaths/04",
		"SysGetPlatformInfoCanonicalizesHostPaths/05",
		"SysGetPlatformInfoCanonicalizesHostPaths/06",
		"SysGetPlatformInfoCanonicalizesHostPaths/07",
		"SysGetPlatformInfoCanonicalizesHostPaths/08",
		"SysGetPlatformInfoCanonicalizesHostPaths/09",
		"SysGetPlatformInfoCanonicalizesHostPaths/10",
		"SysGetPlatformInfoCanonicalizesHostPaths/11",
		"SysGetPlatformInfoCanonicalizesHostPaths/12",
	},
	"TestSysGetPlatformInfoSerializesSystemContext": {
		"SysGetPlatformInfoSerializesSystemContext/01",
		"SysGetPlatformInfoSerializesSystemContext/02",
		"SysGetPlatformInfoSerializesSystemContext/03",
		"SysGetPlatformInfoSerializesSystemContext/04",
		"SysGetPlatformInfoSerializesSystemContext/05",
		"SysGetPlatformInfoSerializesSystemContext/06",
		"SysGetPlatformInfoSerializesSystemContext/07",
		"SysGetPlatformInfoSerializesSystemContext/08",
		"SysGetPlatformInfoSerializesSystemContext/09",
		"SysGetPlatformInfoSerializesSystemContext/10",
		"SysGetPlatformInfoSerializesSystemContext/11",
		"SysGetPlatformInfoSerializesSystemContext/12",
	},
	"TestSystemInfoWaitCompletesWhenContextChanges": {
		"SystemInfoWaitCompletesWhenContextChanges/01",
		"SystemInfoWaitCompletesWhenContextChanges/02",
		"SystemInfoWaitCompletesWhenContextChanges/03",
		"SystemInfoWaitCompletesWhenContextChanges/04",
		"SystemInfoWaitCompletesWhenContextChanges/05",
		"SystemInfoWaitCompletesWhenContextChanges/06",
		"SystemInfoWaitCompletesWhenContextChanges/07",
		"SystemInfoWaitCompletesWhenContextChanges/08",
		"SystemInfoWaitCompletesWhenContextChanges/09",
		"SystemInfoWaitCompletesWhenContextChanges/10",
		"SystemInfoWaitCompletesWhenContextChanges/11",
		"SystemInfoWaitCompletesWhenContextChanges/12",
	},
	"TestCancelSystemInfoWaitRemovesWaiterAndTimer": {
		"CancelSystemInfoWaitRemovesWaiterAndTimer/01",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/02",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/03",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/04",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/05",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/06",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/07",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/08",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/09",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/10",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/11",
		"CancelSystemInfoWaitRemovesWaiterAndTimer/12",
	},
	"abi_proc_args_metadata":      {"abi_proc/args_metadata"},
	"abi_proc_args_full":          {"abi_proc/args_full_retrieval"},
	"abi_proc_args_small_exact":   {"abi_proc/args_small_buffer", "abi_proc/args_exact_buffer"},
	"abi_proc_env_metadata":       {"abi_proc/env_metadata"},
	"abi_proc_env_full":           {"abi_proc/env_full_retrieval"},
	"abi_proc_env_aliases":        {"abi_proc/env_windows_aliases"},
	"abi_proc_env_preserved_dirs": {"abi_proc/env_preserved_user_temp_dirs"},
	"abi_proc_env_percent":        {"abi_proc/env_percent_expansion"},
	"abi_proc_env_no_host_leak":   {"abi_proc/env_no_host_setting_leak"},
	"abi_proc_cwd":                {"abi_proc/cwd_exact_and_small"},
	"abi_proc_cwd_invalid":        {"abi_proc/cwd_invalid"},
	"abi_proc_spawn_wait":         {"abi_proc/spawn", "abi_proc/spawn_wait"},
	"abi_proc_signal_invalid":     {"abi_proc/signal_invalid_handle"},
	"abi_proc_wait_invalid":       {"abi_proc/wait_invalid_handle"},
	"abi_proc_invalid_exe":        {"abi_proc/invalid_executable"},
	"abi_proc_pty":                {"abi_proc/pty_size", "abi_proc/pty_write", "abi_proc/pty_read"},
	"TestGuestEnvCanonicalizesWindowsPathAliases": {
		"GuestEnvCanonicalizesWindowsPathAliases/01_env_metadata",
		"GuestEnvCanonicalizesWindowsPathAliases/02_path_key_canonical",
		"GuestEnvCanonicalizesWindowsPathAliases/03_pathext_key_canonical",
		"GuestEnvCanonicalizesWindowsPathAliases/04_path_separator_preserved",
		"GuestEnvCanonicalizesWindowsPathAliases/05_home_key_not_rewritten",
		"GuestEnvCanonicalizesWindowsPathAliases/06_uppercase_aliases",
		"GuestEnvCanonicalizesWindowsPathAliases/07_lowercase_aliases_absent",
		"GuestEnvCanonicalizesWindowsPathAliases/08_mixed_case_aliases_absent",
		"GuestEnvCanonicalizesWindowsPathAliases/09_empty_buffer_safe",
		"GuestEnvCanonicalizesWindowsPathAliases/10_exact_buffer_safe",
		"GuestEnvCanonicalizesWindowsPathAliases/11_small_buffer_safe",
		"GuestEnvCanonicalizesWindowsPathAliases/12_windows_string_shape",
	},
	"TestGuestEnvDoesNotExposeWashmhostHostSettings": {
		"GuestEnvDoesNotExposeWashmhostHostSettings/01_guest_args_hidden",
		"GuestEnvDoesNotExposeWashmhostHostSettings/02_test_timeout_hidden",
		"GuestEnvDoesNotExposeWashmhostHostSettings/03_uppercase_setting_hidden",
		"GuestEnvDoesNotExposeWashmhostHostSettings/04_mixed_case_setting_hidden",
		"GuestEnvDoesNotExposeWashmhostHostSettings/05_prefix_isolated",
		"GuestEnvDoesNotExposeWashmhostHostSettings/06_null_boundary",
		"GuestEnvDoesNotExposeWashmhostHostSettings/07_env_metadata",
		"GuestEnvDoesNotExposeWashmhostHostSettings/08_env_full_retrieval",
		"GuestEnvDoesNotExposeWashmhostHostSettings/09_env_small_buffer",
		"GuestEnvDoesNotExposeWashmhostHostSettings/10_env_exact_buffer",
		"GuestEnvDoesNotExposeWashmhostHostSettings/11_guest_only_keys",
		"GuestEnvDoesNotExposeWashmhostHostSettings/12_host_leak_absent",
	},
	"TestGuestEnvPreservesWindowsUserDirs": {
		"GuestEnvPreservesWindowsUserDirs/01_localappdata_preserved",
		"GuestEnvPreservesWindowsUserDirs/02_userprofile_preserved",
		"GuestEnvPreservesWindowsUserDirs/03_appdata_preserved",
		"GuestEnvPreservesWindowsUserDirs/04_temp_preserved",
		"GuestEnvPreservesWindowsUserDirs/05_backslashes_preserved",
		"GuestEnvPreservesWindowsUserDirs/06_drive_letter_preserved",
		"GuestEnvPreservesWindowsUserDirs/07_users_segment_preserved",
		"GuestEnvPreservesWindowsUserDirs/08_appdata_segment_preserved",
		"GuestEnvPreservesWindowsUserDirs/09_no_posix_rewrite",
		"GuestEnvPreservesWindowsUserDirs/10_exact_metadata",
		"GuestEnvPreservesWindowsUserDirs/11_small_buffer",
		"GuestEnvPreservesWindowsUserDirs/12_full_retrieval",
	},
	"TestGuestEnvExpandsPercentVarInPath": {
		"GuestEnvExpandsPercentVarInPath/01_path_metadata",
		"GuestEnvExpandsPercentVarInPath/02_percent_tools_expansion",
		"GuestEnvExpandsPercentVarInPath/03_path_prefix_expansion",
		"GuestEnvExpandsPercentVarInPath/04_system32_suffix_preserved",
		"GuestEnvExpandsPercentVarInPath/05_percent_syntax_removed",
		"GuestEnvExpandsPercentVarInPath/06_percent_var_lookup",
		"GuestEnvExpandsPercentVarInPath/07_missing_percent_var_safe",
		"GuestEnvExpandsPercentVarInPath/08_windows_separator_preserved",
		"GuestEnvExpandsPercentVarInPath/09_env_full_retrieval",
		"GuestEnvExpandsPercentVarInPath/10_env_exact_buffer",
		"GuestEnvExpandsPercentVarInPath/11_env_small_buffer",
		"GuestEnvExpandsPercentVarInPath/12_path_alias_canonical",
	},
	"TestGuestEnvDoesNotRewriteTempOrUserPaths": {
		"GuestEnvDoesNotRewriteTempOrUserPaths/01_tmp_preserved",
		"GuestEnvDoesNotRewriteTempOrUserPaths/02_temp_preserved",
		"GuestEnvDoesNotRewriteTempOrUserPaths/03_userprofile_preserved",
		"GuestEnvDoesNotRewriteTempOrUserPaths/04_userprofile_no_slash_rewrite",
		"GuestEnvDoesNotRewriteTempOrUserPaths/05_temp_no_slash_rewrite",
		"GuestEnvDoesNotRewriteTempOrUserPaths/06_tmp_no_slash_rewrite",
		"GuestEnvDoesNotRewriteTempOrUserPaths/07_windows_drive_preserved",
		"GuestEnvDoesNotRewriteTempOrUserPaths/08_users_segment_preserved",
		"GuestEnvDoesNotRewriteTempOrUserPaths/09_appdata_segment_preserved",
		"GuestEnvDoesNotRewriteTempOrUserPaths/10_full_env_retrieval",
		"GuestEnvDoesNotRewriteTempOrUserPaths/11_exact_env_buffer",
		"GuestEnvDoesNotRewriteTempOrUserPaths/12_small_env_buffer",
	},
	"TestSysProcEnv": {
		"TestSysProcEnv/01_args_metadata",
		"TestSysProcEnv/02_args_full_retrieval",
		"TestSysProcEnv/03_args_exact_buffer",
		"TestSysProcEnv/04_args_small_buffer",
		"TestSysProcEnv/05_env_metadata",
		"TestSysProcEnv/06_env_full_retrieval",
		"TestSysProcEnv/07_env_exact_buffer",
		"TestSysProcEnv/08_env_small_buffer",
		"TestSysProcEnv/09_cwd_metadata",
		"TestSysProcEnv/10_invalid_cwd",
		"TestSysProcEnv/11_invalid_wait_handle",
		"TestSysProcEnv/12_invalid_signal_handle",
	},
	"TestStartWithPtyChildCanUseControllingTerminal": {
		"TestStartWithPtyChildCanUseControllingTerminal/01_spawn_request_reported",
		"TestStartWithPtyChildCanUseControllingTerminal/02_pty_spawn_error_abi",
		"TestStartWithPtyChildCanUseControllingTerminal/03_pty_size_unsupported",
		"TestStartWithPtyChildCanUseControllingTerminal/04_pty_write_unsupported",
		"TestStartWithPtyChildCanUseControllingTerminal/05_pty_read_unsupported",
		"TestStartWithPtyChildCanUseControllingTerminal/06_controlling_terminal_unsupported",
		"TestStartWithPtyChildCanUseControllingTerminal/07_child_wait_unsupported",
		"TestStartWithPtyChildCanUseControllingTerminal/08_ready_output_unsupported",
		"TestStartWithPtyChildCanUseControllingTerminal/09_input_echo_unsupported",
		"TestStartWithPtyChildCanUseControllingTerminal/10_close_pty_unsupported",
		"TestStartWithPtyChildCanUseControllingTerminal/11_abi_error_reported",
		"TestStartWithPtyChildCanUseControllingTerminal/12_no_fabricated_success",
	},
	"TestGuestRuntimeEnvOmitsCwdValues": {
		"GuestRuntimeEnvOmitsCwdValues",
	},
	"TestHostEnvTracksModuleCwd": {
		"HostEnvTracksModuleCwd",
	},
	"TestGuestRuntimeEnvDoesNotInjectHostHomeAndTemp": {
		"GuestRuntimeEnvDoesNotInjectHostHomeAndTemp",
	},
	"TestCurrentHostSystemContextIgnoresInventedEnvVars": {
		"HC/01", "HC/02", "HC/03", "HC/04", "HC/05", "HC/06",
		"HC/07", "HC/08", "HC/09", "HC/10", "HC/11", "HC/12",
	},
	"TestDecodeWashmhostGuestArgs": {
		"GA/01", "GA/02", "GA/03", "GA/04", "GA/05", "GA/06",
		"GA/07", "GA/08", "GA/09", "GA/10", "GA/11", "GA/12",
	},
	"TestRunHostHonorsTestTimeout": {
		"HT/01", "HT/02", "HT/03", "HT/04", "HT/05", "HT/06",
		"HT/07", "HT/08", "HT/09", "HT/10", "HT/11", "HT/12",
	},
	"TestSysFs": {
		"TestSysFs/01_create_directory", "TestSysFs/02_create_open", "TestSysFs/03_write",
		"TestSysFs/04_seek", "TestSysFs/05_read", "TestSysFs/06_stat_file",
		"TestSysFs/07_stat_directory", "TestSysFs/08_missing", "TestSysFs/09_chmod",
		"TestSysFs/10_special_bits", "TestSysFs/11_directory_on_file", "TestSysFs/12_double_close_and_stale",
	},
	"TestSysHandleTruncate": {
		"TestSysHandleTruncate/01_open", "TestSysHandleTruncate/02_write_initial",
		"TestSysHandleTruncate/03_seek", "TestSysHandleTruncate/04_shrink",
		"TestSysHandleTruncate/05_offset_unchanged", "TestSysHandleTruncate/06_extend",
		"TestSysHandleTruncate/07_zero", "TestSysHandleTruncate/08_negative",
		"TestSysHandleTruncate/09_invalid_handle", "TestSysHandleTruncate/10_large_invalid",
		"TestSysHandleTruncate/11_stale_handle", "TestSysHandleTruncate/12_directory_handle",
	},
	"TestMapErrnoEFBIG": {
		"TestMapErrnoEFBIG/01_open", "TestMapErrnoEFBIG/02_zero", "TestMapErrnoEFBIG/03_negative",
		"TestMapErrnoEFBIG/04_max_i64", "TestMapErrnoEFBIG/05_large_signed",
		"TestMapErrnoEFBIG/06_seek", "TestMapErrnoEFBIG/07_invalid_handle",
		"TestMapErrnoEFBIG/08_repeat_negative", "TestMapErrnoEFBIG/09_extend",
		"TestMapErrnoEFBIG/10_reset", "TestMapErrnoEFBIG/11_repeat_max",
		"TestMapErrnoEFBIG/12_stale",
	},
	"TestSysReadExtended": {
		"TestSysReadExtended/01_open", "TestSysReadExtended/02_write_fixture",
		"TestSysReadExtended/03_reopen", "TestSysReadExtended/04_partial",
		"TestSysReadExtended/05_zero", "TestSysReadExtended/06_large_request_partial",
		"TestSysReadExtended/07_eof", "TestSysReadExtended/08_seek_positioned",
		"TestSysReadExtended/09_positioned_read", "TestSysReadExtended/10_invalid_handle",
		"TestSysReadExtended/11_invalid_memory", "TestSysReadExtended/12_stale_handle_and_cancel",
	},
	"TestSysWriteExtended": {
		"TestSysWriteExtended/01_open", "TestSysWriteExtended/02_small",
		"TestSysWriteExtended/03_zero", "TestSysWriteExtended/04_stdout",
		"TestSysWriteExtended/05_stderr", "TestSysWriteExtended/06_invalid_handle",
		"TestSysWriteExtended/07_sequential_position", "TestSysWriteExtended/08_sequential_write",
		"TestSysWriteExtended/09_seek", "TestSysWriteExtended/10_overlap",
		"TestSysWriteExtended/11_cancel", "TestSysWriteExtended/12_stale_handle",
	},
	"TestDirEntryUnknownTypeUsesFileKind": {
		"TestDirEntryUnknownTypeUsesFileKind/01_regular_unknown_type",
		"TestDirEntryUnknownTypeUsesFileKind/02_directory_unknown_type",
		"TestDirEntryUnknownTypeUsesFileKind/03_symlink_unknown_type",
		"TestDirEntryUnknownTypeUsesFileKind/04_socket_unknown_type",
		"TestDirEntryUnknownTypeUsesFileKind/05_fifo_unknown_type",
		"TestDirEntryUnknownTypeUsesFileKind/06_empty_name",
		"TestDirEntryUnknownTypeUsesFileKind/07_nonzero_kind",
		"TestDirEntryUnknownTypeUsesFileKind/08_kind_mask",
		"TestDirEntryUnknownTypeUsesFileKind/09_null_entry",
		"TestDirEntryUnknownTypeUsesFileKind/10_info_failure",
		"TestDirEntryUnknownTypeUsesFileKind/11_name_stability",
		"TestDirEntryUnknownTypeUsesFileKind/12_type_roundtrip",
	},
	"TestSysDirReadExtended": {
		"TestSysDirReadExtended/01_empty_directory", "TestSysDirReadExtended/02_single_entry",
		"TestSysDirReadExtended/03_many_entries", "TestSysDirReadExtended/04_small_buffer",
		"TestSysDirReadExtended/05_exact_buffer", "TestSysDirReadExtended/06_fragmented_reads",
		"TestSysDirReadExtended/07_eof", "TestSysDirReadExtended/08_subdirectory_kind",
		"TestSysDirReadExtended/09_invalid_handle", "TestSysDirReadExtended/10_closed_handle",
		"TestSysDirReadExtended/11_zero_capacity", "TestSysDirReadExtended/12_repeated_scan",
	},
	"TestSysPathSetTimes": {
		"TestSysPathSetTimes/01_set_both", "TestSysPathSetTimes/02_mtime_only",
		"TestSysPathSetTimes/03_atime_only", "TestSysPathSetTimes/04_atime_now",
		"TestSysPathSetTimes/05_mtime_now", "TestSysPathSetTimes/06_pre_epoch",
		"TestSysPathSetTimes/07_missing_path", "TestSysPathSetTimes/08_invalid_nsec",
		"TestSysPathSetTimes/09_conflicting_flags", "TestSysPathSetTimes/10_directory",
		"TestSysPathSetTimes/11_empty_path", "TestSysPathSetTimes/12_repeat_set",
	},
	"TestSysPathSetTimesNoFollowSymlink": {
		"TestSysPathSetTimesNoFollowSymlink/01_symlink_mtime", "TestSysPathSetTimesNoFollowSymlink/02_target_unchanged",
		"TestSysPathSetTimesNoFollowSymlink/03_symlink_atime", "TestSysPathSetTimesNoFollowSymlink/04_no_follow_missing",
		"TestSysPathSetTimesNoFollowSymlink/05_regular_file", "TestSysPathSetTimesNoFollowSymlink/06_directory",
		"TestSysPathSetTimesNoFollowSymlink/07_invalid_nsec", "TestSysPathSetTimesNoFollowSymlink/08_conflicting_flags",
		"TestSysPathSetTimesNoFollowSymlink/09_relative_symlink", "TestSysPathSetTimesNoFollowSymlink/10_dangling_symlink",
		"TestSysPathSetTimesNoFollowSymlink/11_repeat_set", "TestSysPathSetTimesNoFollowSymlink/12_remove_symlink",
	},
	"TestPathLinkOperations": {
		"TestPathLinkOperations/01_hardlink_create", "TestPathLinkOperations/02_hardlink_same_file",
		"TestPathLinkOperations/03_hardlink_missing_source", "TestPathLinkOperations/04_hardlink_destination_exists",
		"TestPathLinkOperations/05_symlink_create", "TestPathLinkOperations/06_symlink_readlink",
		"TestPathLinkOperations/07_symlink_follow", "TestPathLinkOperations/08_symlink_no_follow",
		"TestPathLinkOperations/09_relative_symlink", "TestPathLinkOperations/10_dangling_symlink",
		"TestPathLinkOperations/11_cross_directory", "TestPathLinkOperations/12_repeat_remove",
	},
	"TestCreateAbiStat": {
		"TestCreateAbiStat/01_regular", "TestCreateAbiStat/02_directory",
		"TestCreateAbiStat/03_symlink", "TestCreateAbiStat/04_socket",
		"TestCreateAbiStat/05_fifo", "TestCreateAbiStat/06_empty_file",
		"TestCreateAbiStat/07_large_file", "TestCreateAbiStat/08_no_permissions",
		"TestCreateAbiStat/09_full_permissions", "TestCreateAbiStat/10_epoch",
		"TestCreateAbiStat/11_future", "TestCreateAbiStat/12_layout",
	},
	"TestCreateAbiStatUsesNativeMetadata": {
		"TestCreateAbiStatUsesNativeMetadata/01_mtime", "TestCreateAbiStatUsesNativeMetadata/02_size",
		"TestCreateAbiStatUsesNativeMetadata/03_regular_kind", "TestCreateAbiStatUsesNativeMetadata/04_directory_kind",
		"TestCreateAbiStatUsesNativeMetadata/05_mode", "TestCreateAbiStatUsesNativeMetadata/06_uid_gid",
		"TestCreateAbiStatUsesNativeMetadata/07_atime", "TestCreateAbiStatUsesNativeMetadata/08_ctime",
		"TestCreateAbiStatUsesNativeMetadata/09_nlink", "TestCreateAbiStatUsesNativeMetadata/10_inode",
		"TestCreateAbiStatUsesNativeMetadata/11_device", "TestCreateAbiStatUsesNativeMetadata/12_valid_mask",
	},
	"TestStatModeAndKindPreservesUnixTypeAndSpecialBits": {
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/01_setuid",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/02_setgid",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/03_sticky",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/04_symlink",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/05_socket",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/06_fifo",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/07_char_device",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/08_block_device",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/09_regular",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/10_directory",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/11_permissions_only",
		"TestStatModeAndKindPreservesUnixTypeAndSpecialBits/12_special_bits_mask",
	},
	"TestPosixModeToGoFileModePreservesSpecialBits": {
		"TestPosixModeToGoFileModePreservesSpecialBits/01_setuid",
		"TestPosixModeToGoFileModePreservesSpecialBits/02_setgid",
		"TestPosixModeToGoFileModePreservesSpecialBits/03_sticky",
		"TestPosixModeToGoFileModePreservesSpecialBits/04_permissions",
		"TestPosixModeToGoFileModePreservesSpecialBits/05_no_special",
		"TestPosixModeToGoFileModePreservesSpecialBits/06_all_special",
		"TestPosixModeToGoFileModePreservesSpecialBits/07_regular_type",
		"TestPosixModeToGoFileModePreservesSpecialBits/08_directory_type",
		"TestPosixModeToGoFileModePreservesSpecialBits/09_symlink_type",
		"TestPosixModeToGoFileModePreservesSpecialBits/10_socket_type",
		"TestPosixModeToGoFileModePreservesSpecialBits/11_zero_mode",
		"TestPosixModeToGoFileModePreservesSpecialBits/12_max_permissions",
	},
	"TestMarshalAbiStat": {
		"TestMarshalAbiStat/01_kind", "TestMarshalAbiStat/02_mode",
		"TestMarshalAbiStat/03_uid_gid", "TestMarshalAbiStat/04_size",
		"TestMarshalAbiStat/05_mtime", "TestMarshalAbiStat/06_atime",
		"TestMarshalAbiStat/07_ctime", "TestMarshalAbiStat/08_birthtime",
		"TestMarshalAbiStat/09_nlink_inode", "TestMarshalAbiStat/10_dev_rdev",
		"TestMarshalAbiStat/11_blksize_blocks", "TestMarshalAbiStat/12_valid_mask",
	},
	"TestSysNet": {
		"TestSysNet/01_tcp_loopback", "TestSysNet/02_tcp_connect",
		"TestSysNet/03_tcp_accept", "TestSysNet/04_tcp_read",
		"TestSysNet/05_tcp_write", "TestSysNet/06_tcp_close",
		"TestSysNet/07_udp_loopback", "TestSysNet/08_udp_bind",
		"TestSysNet/09_udp_read", "TestSysNet/10_udp_write",
		"TestSysNet/11_ipv4_address", "TestSysNet/12_stale_handle",
	},
	"misc": {"misc/get_time", "misc/get_random", "misc/cwd_roundtrip", "misc/get_platform_info", "misc/tty_get_size"},
	"sys_time_now_exhaustive": {
		"time_now/01_basic_success", "time_now/02_monotonicity", "time_now/03_unix_epoch",
		"time_now/04_precision", "time_now/05_no_side_effect", "time_now/06_stress",
		"time_now/07_concurrency", "time_now/08_context_independence", "time_now/09_module_independence",
		"time_now/10_stack_isolation", "time_now/11_accuracy", "time_now/12_high_range",
		"time_now/13_rapid_sequence", "time_now/14_repeat_calls",
	},
	"sys_get_time_exhaustive": {
		"get_time/01_basic_success", "get_time/02_monotonicity", "get_time/03_consistent_with_now",
		"get_time/04_large_epoch_check", "get_time/05_stress_load", "get_time/06_nil_module_shaped",
		"get_time/07_goroutine_safety", "get_time/08_no_allocations_during_call", "get_time/09_incremental_check",
		"get_time/10_stack_isolation", "get_time/11_negative_time_check", "get_time/12_non_nil_result",
		"get_time/13_high_bit_usage", "get_time/14_repeat_calls",
	},
	"sys_get_random_exhaustive": {
		"get_random/01_small_read", "get_random/02_typical_read", "get_random/03_zero_read",
		"get_random/04_large_read", "get_random/05_invalid_memory_shaped", "get_random/06_randomness_data",
		"get_random/07_uniqueness", "get_random/08_boundary_write", "get_random/09_stress_concurrent",
		"get_random/10_memory_persistence", "get_random/11_stack_parameter_verification",
		"get_random/12_large_read_shaped", "get_random/13_distribution", "get_random/14_full_buffer",
	},
	"sys_timer_set_extended": {
		"timer/01_basic_set_and_fire", "timer/02_overwrite_stops_old", "timer/03_zero_timeout",
		"timer/04_cancel_completed_immediately", "timer/05_cancel_timer", "timer/06_timer_removes_active",
		"timer/07_high_frequency_overwrite", "timer/08_multiple_concurrent", "timer/09_large_timeout_cancel",
		"timer/10_host_cancel_shaped", "timer/11_near_end_of_memory", "timer/12_poll_multiple",
		"timer/13_precision_check", "timer/14_queue_pressure",
	},
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
	"sysfs_01_create_open":              {"sysfs/01_create_open"},
	"sysfs_02_write_read":               {"sysfs/02_write_read"},
	"sysfs_03_stat_file":                {"sysfs/03_stat_file"},
	"sysfs_04_stat_directory":           {"sysfs/04_stat_directory"},
	"sysfs_05_missing":                  {"sysfs/05_missing"},
	"sysfs_06_chmod":                    {"sysfs/06_chmod"},
	"sysfs_07_directory_read":           {"sysfs/07_directory_read"},
	"sysfs_08_read_past_eof":            {"sysfs/08_read_past_eof"},
	"sysfs_09_read_only_handle":         {"sysfs/09_read_only_handle"},
	"sysfs_10_read_from_directory":      {"sysfs/10_read_from_directory"},
	"sysfs_11_double_close":             {"sysfs/11_double_close"},
	"sysfs_12_invalid_handle":           {"sysfs/12_invalid_handle"},
	"sysfs_13_jail_break":               {"sysfs/13_jail_break"},
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
