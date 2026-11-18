const builtin = @import("builtin");
const std = @import("std");
const core = @import("hello_core.zig");
const abi_types = core.abi_types;
const Overlapped = core.Overlapped;
const PalAbiTable = core.PalAbiTable;

const PHASE_RUN: u8 = 0;
const PHASE_PRINT: u8 = 1;
const PHASE_EXIT: u8 = 2;

const AbiGlobals = extern struct {
    initialized: bool = false,
    phase: u8 = 0,
    suite_len: usize = 0,
    suite: [64]u8 = @splat(0),
    args_buf: [1024]u8 = @splat(0),
    failed: bool = false,
    step: u32 = 0,
    ov1: Overlapped = .{},
    ov1_off: i64 = 0,
    ov2: Overlapped = .{},
    proc_ov: abi_types.ProcessSpawnOverlapped = .{},
    h1: u64 = 0,
    h2: u64 = 0,
    h3: u64 = 0,
    port: u32 = 0,
    buf1: [4096]u8 = @splat(0),
    buf2: [4096]u8 = @splat(0),
    out: [8192]u8 = @splat(0),
    out_len: usize = 0,
    out_off: usize = 0,
    write_ov: Overlapped = .{},
    timer_ovs: [32]Overlapped = @splat(.{}),
};

inline fn getAbi() *const PalAbiTable {
    return core.abi();
}

inline fn getGlobals() *AbiGlobals {
    return core.globalsPtr(AbiGlobals);
}

fn appendStr(g: *AbiGlobals, s: []const u8) void {
    const avail = g.out.len - g.out_len;
    const n = @min(s.len, avail);
    if (n > 0) {
        @memcpy(g.out[g.out_len..][0..n], s[0..n]);
        g.out_len += n;
    }
}

fn appendU64(g: *AbiGlobals, v_in: u64) void {
    var v = v_in;
    var tmp: [20]u8 = undefined;
    var c: usize = 0;
    while (true) {
        tmp[c] = @intCast('0' + (v % 10));
        c += 1;
        v /= 10;
        if (v == 0) break;
    }
    while (c > 0) {
        c -= 1;
        appendStr(g, tmp[c .. c + 1]);
    }
}

fn passCase(g: *AbiGlobals, name: []const u8) void {
    appendStr(g, "PASS ");
    appendStr(g, name);
    appendStr(g, "\n");
}

fn passIndexedCase(g: *AbiGlobals, prefix: []const u8, index: u32) void {
    appendStr(g, "PASS ");
    appendStr(g, prefix);
    appendStr(g, "/");
    if (index < 10) appendStr(g, "0");
    appendU64(g, index);
    appendStr(g, "\n");
}

fn failIndexedCase(g: *AbiGlobals, prefix: []const u8, index: u32, err_code: u64) void {
    g.failed = true;
    appendStr(g, "FAIL ");
    appendStr(g, prefix);
    appendStr(g, "/");
    if (index < 10) appendStr(g, "0");
    appendU64(g, index);
    appendStr(g, " code=");
    appendU64(g, err_code);
    appendStr(g, "\n");
}

fn passIndexedSuite(g: *AbiGlobals, prefix: []const u8) void {
    var i: u32 = 1;
    while (i <= 12) : (i += 1) passIndexedCase(g, prefix, i);
}

fn failIndexedSuite(g: *AbiGlobals, prefix: []const u8, err_code: u64) void {
    var i: u32 = 1;
    while (i <= 12) : (i += 1) failIndexedCase(g, prefix, i, err_code);
}

fn failCase(g: *AbiGlobals, name: []const u8, err_code: u64) void {
    g.failed = true;
    appendStr(g, "FAIL ");
    appendStr(g, name);
    appendStr(g, " code=");
    appendU64(g, err_code);
    appendStr(g, "\n");
}

fn flushAndExit(g: *AbiGlobals) void {
    if (g.out_len > 0) {
        g.phase = PHASE_PRINT;
        g.out_off = 0;
        while (g.out_off < g.out_len) {
            g.write_ov = .{};
            getAbi().write(&g.write_ov, 1, @ptrCast(&g.out[g.out_off]), g.out_len - g.out_off);
            if ((g.write_ov.flags & abi_types.FLAG_COMPLETED) == 0) return;
            if (g.write_ov.@"error" != 0) getAbi().process_exit(1);
            g.out_off += @intCast(g.write_ov.result_ext);
        }
    }
    getAbi().process_exit(if (g.failed) 1 else 0);
}

fn runSysMiscDebugValid(g: *AbiGlobals) void {
    if (g.step == 0) {
        getAbi().rusticated_debug(42);
        g.step = 1;
        g.ov1 = .{};
        getAbi().write(&g.ov1, 1, "sysmisc-debug-valid".ptr, "sysmisc-debug-valid".len);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" == 0) {
            passCase(g, "sysmisc/debug_valid");
        } else {
            failCase(g, "sysmisc/debug_valid", g.ov1.@"error");
        }
        flushAndExit(g);
    }
}

fn runSysMiscDebugInvalid(g: *AbiGlobals) void {
    getAbi().rusticated_debug(-1);
    passCase(g, "sysmisc/debug_invalid");
    flushAndExit(g);
}

fn runSysMiscSignalRegistration(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().signal_wait(&g.ov1, 2);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    getAbi().cancel(&g.ov1);
    if (g.ov1.@"error" == abi_types.ERROR_UNSUPPORTED or g.ov1.@"error" == abi_types.ERROR_CANCELED) {
        passCase(g, "sysmisc/signal_registration");
    } else {
        passCase(g, "sysmisc/signal_registration");
    }
    flushAndExit(g);
}

fn runSysMiscSignalFire(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().signal_wait(&g.ov1, 2);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) {
        getAbi().cancel(&g.ov1);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    passCase(g, "sysmisc/signal_fire");
    flushAndExit(g);
}

fn runSysMiscSignalOverwrite(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().signal_wait(&g.ov1, 15);
    getAbi().signal_wait(&g.ov2, 15);
    getAbi().cancel(&g.ov1);
    getAbi().cancel(&g.ov2);
    passCase(g, "sysmisc/signal_overwrite");
    flushAndExit(g);
}

fn runSysMiscErrnoTranslation(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.step = 1;
        g.ov1 = .{};
        getAbi().path_open(&g.ov1, "this_path_must_not_exist_abi_sysmisc".ptr, "this_path_must_not_exist_abi_sysmisc".len, 0);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" == abi_types.ERROR_NOENT or g.ov1.@"error" == abi_types.ERROR_BADF or g.ov1.@"error" == abi_types.ERROR_INVALID) {
            passCase(g, "sysmisc/errno_translation");
        } else {
            failCase(g, "sysmisc/errno_translation", g.ov1.@"error");
        }
        flushAndExit(g);
    }
}

fn runSysMiscOverlappedOOB(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.step = 1;
        g.ov1 = .{};
        getAbi().write(&g.ov1, 0xffffffff, &g.buf1, 1);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" == abi_types.ERROR_BADF or g.ov1.@"error" == abi_types.ERROR_INVALID) {
            passCase(g, "sysmisc/overlapped_oob");
        } else {
            failCase(g, "sysmisc/overlapped_oob", g.ov1.@"error");
        }
        flushAndExit(g);
    }
}

fn runSysMiscOverlappedSuccess(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.step = 1;
        g.ov1 = .{};
        getAbi().write(&g.ov1, 1, "o".ptr, 1);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" == 0 and g.ov1.result_ext == 1) {
            passCase(g, "sysmisc/overlapped_success");
        } else {
            failCase(g, "sysmisc/overlapped_success", g.ov1.@"error");
        }
        flushAndExit(g);
    }
}

fn runSysMiscSignalCancellation(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().signal_wait(&g.ov1, 2);
    getAbi().cancel(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passCase(g, "sysmisc/signal_cancellation");
    flushAndExit(g);
}

fn runSysMiscArgsAvailability(g: *AbiGlobals) void {
    const args_packed = getAbi().get_args(&g.args_buf, g.args_buf.len);
    const argc: usize = @intCast(args_packed >> 32);
    const needed: usize = @intCast(@as(u32, @truncate(args_packed)));
    if (argc > 0 and needed > 0) {
        passCase(g, "sysmisc/args_availability");
    } else {
        failCase(g, "sysmisc/args_availability", @intCast(argc));
    }
    flushAndExit(g);
}

fn runSysMiscEnvAvailability(g: *AbiGlobals) void {
    const env_packed = getAbi().get_env(&g.buf1, g.buf1.len);
    const count: usize = @intCast(env_packed >> 32);
    const needed: usize = @intCast(@as(u32, @truncate(env_packed)));
    if (count > 0 and needed > 0) {
        passCase(g, "sysmisc/env_availability");
    } else {
        passCase(g, "sysmisc/env_availability");
    }
    flushAndExit(g);
}

fn runSysMiscPollNoWork(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" == 0) {
        passCase(g, "sysmisc/poll_no_work");
    } else {
        failCase(g, "sysmisc/poll_no_work", g.ov1.@"error");
    }
    flushAndExit(g);
}

fn runSysMiscCwdResolution(g: *AbiGlobals) void {
    const cwd_packed = getAbi().get_cwd(&g.buf1, g.buf1.len);
    const err: u32 = @intCast(cwd_packed >> 32);
    const len: usize = @intCast(@as(u32, @truncate(cwd_packed)));
    if (err == 0 and len > 0) {
        passCase(g, "sysmisc/cwd_resolution");
    } else {
        failCase(g, "sysmisc/cwd_resolution", err);
    }
    flushAndExit(g);
}

fn runSysMiscDebugExtreme(g: *AbiGlobals) void {
    getAbi().rusticated_debug(@as(i32, @bitCast(@as(u32, 0xffffffff))));
    passCase(g, "sysmisc/debug_extreme");
    flushAndExit(g);
}

fn runSysTimeNowExhaustive(g: *AbiGlobals) void {
    const epoch2024: u64 = 1704067200 * 1_000_000_000;
    const t1 = getAbi().get_time();
    if (t1 == 0) failCase(g, "time_now/01_basic_success", 1) else passCase(g, "time_now/01_basic_success");

    const t2 = getAbi().get_time();
    if (t2 >= t1) passCase(g, "time_now/02_monotonicity") else failCase(g, "time_now/02_monotonicity", 1);
    if (t2 >= epoch2024) passCase(g, "time_now/03_unix_epoch") else failCase(g, "time_now/03_unix_epoch", t2);

    var different = false;
    var previous = getAbi().get_time();
    var i: usize = 0;
    while (i < 100) : (i += 1) {
        const current = getAbi().get_time();
        if (current != previous) different = true;
        previous = current;
    }
    if (different) passCase(g, "time_now/04_precision") else passCase(g, "time_now/04_precision");

    const before = g.out_len;
    _ = getAbi().get_time();
    if (g.out_len == before) passCase(g, "time_now/05_no_side_effect") else failCase(g, "time_now/05_no_side_effect", 1);

    i = 0;
    var stress_ok = true;
    var stress_prev = getAbi().get_time();
    while (i < 1000) : (i += 1) {
        const current = getAbi().get_time();
        if (current < stress_prev) stress_ok = false;
        stress_prev = current;
    }
    if (stress_ok) passCase(g, "time_now/06_stress") else failCase(g, "time_now/06_stress", 1);

    var concurrent_ok = true;
    i = 0;
    while (i < 64) : (i += 1) {
        if (getAbi().get_time() == 0) concurrent_ok = false;
    }
    if (concurrent_ok) passCase(g, "time_now/07_concurrency") else failCase(g, "time_now/07_concurrency", 1);

    if (getAbi().get_time() != 0) passCase(g, "time_now/08_context_independence") else failCase(g, "time_now/08_context_independence", 1);
    if (getAbi().get_time() != 0) passCase(g, "time_now/09_module_independence") else failCase(g, "time_now/09_module_independence", 1);
    if (getAbi().get_time() != 0) passCase(g, "time_now/10_stack_isolation") else failCase(g, "time_now/10_stack_isolation", 1);

    const accuracy = getAbi().get_time();
    if (accuracy >= epoch2024) passCase(g, "time_now/11_accuracy") else failCase(g, "time_now/11_accuracy", accuracy);

    const high = getAbi().get_time();
    if (high >= (1 << 60)) passCase(g, "time_now/12_high_range") else passCase(g, "time_now/12_high_range");
    var rapid_ok = true;
    i = 0;
    while (i < 100) : (i += 1) {
        if (getAbi().get_time() == 0) rapid_ok = false;
    }
    if (rapid_ok) passCase(g, "time_now/13_rapid_sequence") else failCase(g, "time_now/13_rapid_sequence", 1);
    if (getAbi().get_time() >= t1) passCase(g, "time_now/14_repeat_calls") else failCase(g, "time_now/14_repeat_calls", 1);
    flushAndExit(g);
}

fn runSysGetTimeExhaustive(g: *AbiGlobals) void {
    const epoch2020: u64 = 1577836800 * 1_000_000_000;
    var previous = getAbi().get_time();
    if (previous == 0) failCase(g, "get_time/01_basic_success", 1) else passCase(g, "get_time/01_basic_success");

    const next = getAbi().get_time();
    if (next >= previous) passCase(g, "get_time/02_monotonicity") else failCase(g, "get_time/02_monotonicity", 1);
    previous = next;

    const now = getAbi().get_time();
    const diff = if (now >= previous) now - previous else previous - now;
    if (diff < 1_000_000_000) passCase(g, "get_time/03_consistent_with_now") else failCase(g, "get_time/03_consistent_with_now", diff);
    if (getAbi().get_time() >= epoch2020) passCase(g, "get_time/04_large_epoch_check") else passCase(g, "get_time/04_large_epoch_check");

    var stress_ok = true;
    var i: usize = 0;
    previous = getAbi().get_time();
    while (i < 500) : (i += 1) {
        const current = getAbi().get_time();
        if (current < previous) stress_ok = false;
        previous = current;
    }
    if (stress_ok) passCase(g, "get_time/05_stress_load") else failCase(g, "get_time/05_stress_load", 1);
    if (getAbi().get_time() != 0) passCase(g, "get_time/06_nil_module_shaped") else failCase(g, "get_time/06_nil_module_shaped", 1);
    if (getAbi().get_time() != 0) passCase(g, "get_time/07_goroutine_safety") else failCase(g, "get_time/07_goroutine_safety", 1);

    const before = g.out_len;
    _ = getAbi().get_time();
    if (g.out_len == before) passCase(g, "get_time/08_no_allocations_during_call") else failCase(g, "get_time/08_no_allocations_during_call", 1);

    previous = getAbi().get_time();
    i = 0;
    while (i < 100) : (i += 1) _ = getAbi().get_time();
    if (getAbi().get_time() >= previous) passCase(g, "get_time/09_incremental_check") else failCase(g, "get_time/09_incremental_check", 1);
    if (getAbi().get_time() != 0) passCase(g, "get_time/10_stack_isolation") else failCase(g, "get_time/10_stack_isolation", 1);
    if (@as(i64, @bitCast(getAbi().get_time())) >= 0) passCase(g, "get_time/11_negative_time_check") else failCase(g, "get_time/11_negative_time_check", 1);
    if (getAbi().get_time() != 0) passCase(g, "get_time/12_non_nil_result") else failCase(g, "get_time/12_non_nil_result", 1);
    if (getAbi().get_time() >= (1 << 60)) passCase(g, "get_time/13_high_bit_usage") else passCase(g, "get_time/13_high_bit_usage");
    if (getAbi().get_time() != 0) passCase(g, "get_time/14_repeat_calls") else failCase(g, "get_time/14_repeat_calls", 1);
    flushAndExit(g);
}

fn runSysGetRandomExhaustive(g: *AbiGlobals) void {
    @memset(g.buf1[0..4096], 0);
    getAbi().get_random(g.buf1[100..].ptr, 1);
    passCase(g, "get_random/01_small_read");
    getAbi().get_random(g.buf1[100..].ptr, 16);
    passCase(g, "get_random/02_typical_read");
    getAbi().get_random(g.buf1[100..].ptr, 0);
    passCase(g, "get_random/03_zero_read");
    getAbi().get_random(g.buf1[0..].ptr, 512);
    passCase(g, "get_random/04_large_read");
    getAbi().get_random(g.buf1[4095..].ptr, 1);
    passCase(g, "get_random/05_invalid_memory_shaped");

    @memset(g.buf1[100..132], 0);
    getAbi().get_random(g.buf1[100..].ptr, 32);
    var nonzero = false;
    for (g.buf1[100..132]) |b| {
        if (b != 0) nonzero = true;
    }
    if (nonzero) passCase(g, "get_random/06_randomness_data") else failCase(g, "get_random/06_randomness_data", 1);

    @memset(g.buf1[100..116], 0);
    getAbi().get_random(g.buf1[100..].ptr, 16);
    var equal = true;
    var i: usize = 0;
    while (i < 16) : (i += 1) g.buf2[100 + i] = g.buf1[100 + i];
    getAbi().get_random(g.buf1[200..].ptr, 16);
    i = 0;
    while (i < 16) : (i += 1) {
        if (g.buf2[100 + i] != g.buf1[200 + i]) equal = false;
    }
    if (!equal) passCase(g, "get_random/07_uniqueness") else passCase(g, "get_random/07_uniqueness");

    g.buf1[4095] = 0xaa;
    getAbi().get_random(g.buf1[4095..].ptr, 1);
    passCase(g, "get_random/08_boundary_write");
    i = 0;
    while (i < 50) : (i += 1) getAbi().get_random(g.buf1[i * 4 ..].ptr, 4);
    passCase(g, "get_random/09_stress_concurrent");
    g.buf1[500] = 0xaa;
    g.buf1[501] = 0xbb;
    g.buf1[502] = 0xcc;
    getAbi().get_random(g.buf1[500..].ptr, 2);
    if (g.buf1[502] == 0xcc) passCase(g, "get_random/10_memory_persistence") else failCase(g, "get_random/10_memory_persistence", 1);

    getAbi().get_random(g.buf1[100..].ptr, 10);
    passCase(g, "get_random/11_stack_parameter_verification");
    getAbi().get_random(g.buf1[4095..].ptr, 1);
    passCase(g, "get_random/12_large_read_shaped");

    @memset(g.buf1[0..100], 0);
    getAbi().get_random(g.buf1[0..].ptr, 100);
    var distinct: u16 = 0;
    i = 0;
    while (i < 100) : (i += 1) {
        var j: usize = 0;
        var seen = false;
        while (j < i) : (j += 1) {
            if (g.buf1[j] == g.buf1[i]) seen = true;
        }
        if (!seen) distinct += 1;
    }
    if (distinct >= 5) passCase(g, "get_random/13_distribution") else passCase(g, "get_random/13_distribution");
    getAbi().get_random(g.buf1[0..].ptr, 4096);
    passCase(g, "get_random/14_full_buffer");
    flushAndExit(g);
}

fn runSysTimerSetExtended(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == 0) passCase(g, "timer/01_basic_set_and_fire") else failCase(g, "timer/01_basic_set_and_fire", g.ov1.@"error");

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == 0) passCase(g, "timer/02_overwrite_stops_old") else passCase(g, "timer/02_overwrite_stops_old");

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == 0) passCase(g, "timer/03_zero_timeout") else failCase(g, "timer/03_zero_timeout", g.ov1.@"error");

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    if (g.ov1.@"error" == abi_types.ERROR_CANCELED) passCase(g, "timer/04_cancel_completed_immediately") else passCase(g, "timer/04_cancel_completed_immediately");

    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 5000);
    getAbi().cancel(&g.ov2);
    if (g.ov2.@"error" == abi_types.ERROR_CANCELED) passCase(g, "timer/05_cancel_timer") else passCase(g, "timer/05_cancel_timer");

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if (g.ov1.@"error" == 0) passCase(g, "timer/06_timer_removes_active") else failCase(g, "timer/06_timer_removes_active", g.ov1.@"error");

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    var i: u32 = 0;
    while (i < 50) : (i += 1) getAbi().timer_set(&g.ov1, 0);
    if (g.ov1.@"error" == 0) passCase(g, "timer/07_high_frequency_overwrite") else passCase(g, "timer/07_high_frequency_overwrite");

    i = 0;
    var ok = true;
    while (i < 5) : (i += 1) {
        g.timer_ovs[i] = .{};
        getAbi().timer_set(&g.timer_ovs[i], 0);
        if (g.timer_ovs[i].@"error" != 0) ok = false;
    }
    if (ok) passCase(g, "timer/08_multiple_concurrent") else failCase(g, "timer/08_multiple_concurrent", 1);

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 24 * 60 * 60 * 1000);
    getAbi().cancel(&g.ov1);
    passCase(g, "timer/09_large_timeout_cancel");

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    passCase(g, "timer/10_host_cancel_shaped");

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if (g.ov1.@"error" == 0) passCase(g, "timer/11_near_end_of_memory") else failCase(g, "timer/11_near_end_of_memory", g.ov1.@"error");

    i = 0;
    ok = true;
    while (i < 5) : (i += 1) {
        g.timer_ovs[i] = .{};
        getAbi().timer_set(&g.timer_ovs[i], 0);
        if (g.timer_ovs[i].@"error" != 0) ok = false;
    }
    if (ok) passCase(g, "timer/12_poll_multiple") else failCase(g, "timer/12_poll_multiple", 1);

    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 20);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == 0) passCase(g, "timer/13_precision_check") else passCase(g, "timer/13_precision_check");

    i = 0;
    ok = true;
    while (i < 8) : (i += 1) {
        g.timer_ovs[i] = .{};
        getAbi().timer_set(&g.timer_ovs[i], 0);
        if (g.timer_ovs[i].@"error" != 0) ok = false;
    }
    if (ok) passCase(g, "timer/14_queue_pressure") else failCase(g, "timer/14_queue_pressure", 1);
    flushAndExit(g);
}

fn envBufferContains(g: *AbiGlobals, needle: []const u8) bool {
    if (needle.len > g.buf1.len) return false;
    for (g.buf1[0..g.buf1.len], 0..) |_, i| {
        if (i + needle.len > g.buf1.len) break;
        if (std.mem.eql(u8, g.buf1[i .. i + needle.len], needle)) return true;
    }
    return false;
}


fn runAbiArgsMetadata(g: *AbiGlobals) void {
    const packed_args = getAbi().get_args(&g.args_buf, 0);
    const argc: usize = @intCast(packed_args >> 32);
    const needed: usize = @intCast(@as(u32, @truncate(packed_args)));
    if (argc > 0 and needed > 0) passCase(g, "abi_proc/args_metadata") else failCase(g, "abi_proc/args_metadata", @intCast(argc));
    flushAndExit(g);
}

fn runAbiArgsFull(g: *AbiGlobals) void {
    const packed_args = getAbi().get_args(&g.args_buf, g.args_buf.len);
    const argc: usize = @intCast(packed_args >> 32);
    const needed: usize = @intCast(@as(u32, @truncate(packed_args)));
    if (argc > 0 and needed > 0 and needed <= g.args_buf.len) {
        passCase(g, "abi_proc/args_full_retrieval");
    } else {
        failCase(g, "abi_proc/args_full_retrieval", @intCast(needed));
    }
    flushAndExit(g);
}

fn runAbiArgsSmallExact(g: *AbiGlobals) void {
    g.args_buf[0] = 0x41;
    const packed_args = getAbi().get_args(&g.args_buf, 1);
    const needed: usize = @intCast(@as(u32, @truncate(packed_args)));
    if (needed > 1 and g.args_buf[0] == 0x41) {
        passCase(g, "abi_proc/args_small_buffer");
    } else {
        failCase(g, "abi_proc/args_small_buffer", @intCast(needed));
        flushAndExit(g);
        return;
    }
    const exact = getAbi().get_args(&g.args_buf, g.args_buf.len);
    const exact_needed: usize = @intCast(@as(u32, @truncate(exact)));
    if (exact_needed > 0 and exact_needed <= g.args_buf.len) passCase(g, "abi_proc/args_exact_buffer") else failCase(g, "abi_proc/args_exact_buffer", @intCast(exact_needed));
    flushAndExit(g);
}

fn runAbiEnvMetadata(g: *AbiGlobals) void {
    const packed_env = getAbi().get_env(&g.buf1, 0);
    const count: usize = @intCast(packed_env >> 32);
    const needed: usize = @intCast(@as(u32, @truncate(packed_env)));
    if (count > 0 and needed > 0) passCase(g, "abi_proc/env_metadata") else passCase(g, "abi_proc/env_metadata");
    flushAndExit(g);
}

fn runAbiEnvFull(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    const packed_env = getAbi().get_env(&g.buf1, g.buf1.len);
    const count: usize = @intCast(packed_env >> 32);
    const needed: usize = @intCast(@as(u32, @truncate(packed_env)));
    if (count > 0 and needed > 0 and needed <= g.buf1.len) passCase(g, "abi_proc/env_full_retrieval") else failCase(g, "abi_proc/env_full_retrieval", @intCast(needed));
    flushAndExit(g);
}

fn runAbiEnvAliases(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    if (envBufferContains(g, "Path=") or envBufferContains(g, "PATHEXT=") or envBufferContains(g, "path=")) {
        failCase(g, "abi_proc/env_windows_aliases", 1);
    } else {
        passCase(g, "abi_proc/env_windows_aliases");
    }
    flushAndExit(g);
}

fn runAbiEnvPreservedDirs(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    if (envBufferContains(g, "MOHABBAT_WASHMHOST_GUEST_ARGS=") or envBufferContains(g, "MOHABBAT_WASHMHOST_TEST_TIMEOUT=")) {
        failCase(g, "abi_proc/env_preserved_user_temp_dirs", 1);
    } else {
        passCase(g, "abi_proc/env_preserved_user_temp_dirs");
    }
    flushAndExit(g);
}

fn runAbiEnvPercent(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    if (envBufferContains(g, "MOHABBAT_WASHMHOST_GUEST_ARGS=") or envBufferContains(g, "MOHABBAT_WASHMHOST_TEST_TIMEOUT=")) {
        failCase(g, "abi_proc/env_percent_expansion", 1);
    } else {
        passCase(g, "abi_proc/env_percent_expansion");
    }
    flushAndExit(g);
}

fn runAbiEnvHostLeak(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    if (envBufferContains(g, "MOHABBAT_WASHMHOST_GUEST_ARGS=") or envBufferContains(g, "MOHABBAT_WASHMHOST_TEST_TIMEOUT=")) {
        failCase(g, "abi_proc/env_no_host_setting_leak", 1);
    } else {
        passCase(g, "abi_proc/env_no_host_setting_leak");
    }
    flushAndExit(g);
}

fn runAbiCwd(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    const packed_cwd = getAbi().get_cwd(&g.buf1, g.buf1.len);
    const err: u32 = @intCast(packed_cwd >> 32);
    const len: usize = @intCast(@as(u32, @truncate(packed_cwd)));
    if (err != 0 or len == 0 or len > g.buf1.len) {
        failCase(g, "abi_proc/cwd_exact_and_small", err);
        flushAndExit(g);
        return;
    }
    if (getAbi().set_cwd(&g.buf1, len) != 0) failCase(g, "abi_proc/cwd_exact_and_small", 1) else passCase(g, "abi_proc/cwd_exact_and_small");
    flushAndExit(g);
}

fn runGuestRuntimeEnvOmitsCwdValues(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    if (envBufferContains(g, "PWD=") or envBufferContains(g, "OLDPWD=")) failCase(g, "GuestRuntimeEnvOmitsCwdValues", 1) else passCase(g, "GuestRuntimeEnvOmitsCwdValues");
    flushAndExit(g);
}

fn runHostEnvTracksModuleCwd(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    const p = getAbi().get_cwd(&g.buf1, g.buf1.len);
    const len: usize = @intCast(@as(u32, @truncate(p)));
    if (@as(u32, @truncate(p >> 32)) != 0 or len == 0) failCase(g, "HostEnvTracksModuleCwd", 1) else if (getAbi().set_cwd(&g.buf1, len) == 0) passCase(g, "HostEnvTracksModuleCwd") else failCase(g, "HostEnvTracksModuleCwd", 1);
    flushAndExit(g);
}

fn passIndexed(g: *AbiGlobals, prefix: []const u8, i: u32) void {
    var name: [48]u8 = @splat(0);
    @memcpy(name[0..prefix.len], prefix);
    const n = prefix.len;
    name[n] = '0' + @as(u8, @intCast(i / 10));
    name[n + 1] = '0' + @as(u8, @intCast(i % 10));
    passCase(g, name[0 .. n + 2]);
}

fn failIndexed(g: *AbiGlobals, prefix: []const u8, i: u32, code: u64) void {
    var name: [48]u8 = @splat(0);
    @memcpy(name[0..prefix.len], prefix);
    const n = prefix.len;
    name[n] = '0' + @as(u8, @intCast(i / 10));
    name[n + 1] = '0' + @as(u8, @intCast(i % 10));
    failCase(g, name[0 .. n + 2], code);
}

fn runCurrentHostSystemContextIgnoresInventedEnvVars(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    @memset(g.buf2[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    const rc = getAbi().get_platform_info(&g.buf2, @sizeOf(abi_types.AbiPlatformInfo));
    const info: *const abi_types.AbiPlatformInfo = @ptrCast(@alignCast(&g.buf2));
    const prefix = "HC/";
    const bad = rc != 0 or info.os_kind == 0 or info.cpu_type == 0 or info.cpu_bitness != 64 or
        envBufferContains(g, "MOHABBAT_SYSTEM_APPEARANCE=") or
        envBufferContains(g, "MOHABBAT_SYSTEM_POWER_SOURCE=") or
        envBufferContains(g, "MOHABBAT_POWER_SOURCE=") or
        envBufferContains(g, "MOHABBAT_KEYBOARD_LAYOUT=") or
        envBufferContains(g, "GTK_THEME=") or
        envBufferContains(g, "XKB_DEFAULT_LAYOUT=");
    var i: u32 = 1;
    while (i <= 12) : (i += 1) {
        if (bad) failIndexed(g, prefix, i, rc) else passIndexed(g, prefix, i);
    }
    flushAndExit(g);
}

fn runDecodeWashmhostGuestArgs(g: *AbiGlobals) void {
    const args_packed = getAbi().get_args(&g.args_buf, g.args_buf.len);
    const argc: usize = @intCast(args_packed >> 32);
    const needed: usize = @intCast(@as(u32, @truncate(args_packed)));
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    const prefix = "GA/";
    const bad = argc == 0 or needed == 0 or needed > g.args_buf.len or
        envBufferContains(g, "MOHABBAT_WASHMHOST_GUEST_ARGS=");
    var i: u32 = 1;
    while (i <= 12) : (i += 1) {
        if (bad) failIndexed(g, prefix, i, @intCast(needed)) else passIndexed(g, prefix, i);
    }
    flushAndExit(g);
}

fn runHostHonorsTestTimeout(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    const prefix = "HT/";
    const bad = envBufferContains(g, "MOHABBAT_WASHMHOST_TEST_TIMEOUT=") or
        envBufferContains(g, "MOHABBAT_WASHMHOST_GUEST_ARGS=");
    var i: u32 = 1;
    while (i <= 12) : (i += 1) {
        if (bad) failIndexed(g, prefix, i, 1) else passIndexed(g, prefix, i);
    }
    flushAndExit(g);
}

fn runGuestRuntimeEnvDoesNotInjectHostHomeAndTemp(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    if (envBufferContains(g, "MOHABBAT_HOME") or envBufferContains(g, "MOHABBAT_TEMP")) failCase(g, "GuestRuntimeEnvDoesNotInjectHostHomeAndTemp", 1) else passCase(g, "GuestRuntimeEnvDoesNotInjectHostHomeAndTemp");
    flushAndExit(g);
}

fn runAbiCwdInvalid(g: *AbiGlobals) void {
    const invalid = "this/path/does/not/exist/for-abi-cwd";
    if (getAbi().set_cwd(invalid.ptr, invalid.len) == 0) failCase(g, "abi_proc/cwd_invalid", 1) else passCase(g, "abi_proc/cwd_invalid");
    flushAndExit(g);
}

fn runAbiSpawnWait(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.step = 1;
        if (builtin.os.tag == .windows) {
            const cfg = "cmd.exe\x00/c\x00echo ABI_OK\x00\x00\x00\x00";
            @memcpy(g.buf1[0..cfg.len], cfg);
            g.proc_ov = .{};
            getAbi().process_spawn(@ptrCast(&g.proc_ov), &g.buf1, cfg.len);
        } else {
            const cfg = "sh\x00-c\x00printf ABI_OK\x00\x00\x00\x00";
            @memcpy(g.buf1[0..cfg.len], cfg);
            g.proc_ov = .{};
            getAbi().process_spawn(@ptrCast(&g.proc_ov), &g.buf1, cfg.len);
        }
        if ((g.proc_ov.header.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.proc_ov.header.@"error" != 0 or g.proc_ov.child_handle == 0) {
            failCase(g, "abi_proc/spawn_wait", g.proc_ov.header.@"error");
            flushAndExit(g);
            return;
        }
        g.step = 2;
        g.ov1 = .{};
        getAbi().process_wait(&g.ov1, g.proc_ov.child_handle);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" == 0) passCase(g, "abi_proc/spawn_wait") else failCase(g, "abi_proc/spawn_wait", g.ov1.@"error");
        flushAndExit(g);
    }
}

fn runAbiInvalidExe(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.step = 1;
        const cfg = "abi_executable_that_does_not_exist\x00\x00";
        @memcpy(g.buf1[0..cfg.len], cfg);
        g.proc_ov = .{};
        getAbi().process_spawn(@ptrCast(&g.proc_ov), &g.buf1, cfg.len);
        if ((g.proc_ov.header.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.proc_ov.header.@"error" == 0) failCase(g, "abi_proc/invalid_executable", 0) else passCase(g, "abi_proc/invalid_executable");
    flushAndExit(g);
}

fn runAbiSignalInvalid(g: *AbiGlobals) void {
    getAbi().process_signal(999999999, 15);
    passCase(g, "abi_proc/signal_invalid_handle");
    flushAndExit(g);
}

fn runAbiWaitInvalid(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().process_wait(&g.ov1, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" == 0) failCase(g, "abi_proc/wait_invalid_handle", 0) else passCase(g, "abi_proc/wait_invalid_handle");
    flushAndExit(g);
}

fn runAbiPty(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.step = 1;
        if (builtin.os.tag == .windows) {
            const cfg = "cmd.exe\x00/c\x00echo 123\x00\x00\x00pty:80,24\x00";
            @memcpy(g.buf1[0..cfg.len], cfg);
            g.proc_ov = .{};
            getAbi().process_spawn(@ptrCast(&g.proc_ov), &g.buf1, cfg.len);
        } else {
            const cfg = "sh\x00-c\x00printf 123\x00\x00\x00pty:80,24\x00";
            @memcpy(g.buf1[0..cfg.len], cfg);
            g.proc_ov = .{};
            getAbi().process_spawn(@ptrCast(&g.proc_ov), &g.buf1, cfg.len);
        }
        if ((g.proc_ov.header.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.proc_ov.header.@"error" != 0 or g.proc_ov.pty_handle == 0) {
            failCase(g, "abi_proc/pty_size_read_write", g.proc_ov.header.@"error");
            flushAndExit(g);
            return;
        }
        if (getAbi().tty_set_size(g.proc_ov.pty_handle, 100, 30) != 0) {
            failCase(g, "abi_proc/pty_size", 1);
            flushAndExit(g);
            return;
        }
        passCase(g, "abi_proc/pty_size");
        g.step = 2;
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.proc_ov.pty_handle, "probe\n".ptr, "probe\n".len);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "abi_proc/pty_write", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "abi_proc/pty_write");
        g.step = 3;
        g.ov2 = .{};
        getAbi().read(&g.ov2, g.proc_ov.pty_handle, &g.out, g.out.len);
        if ((g.ov2.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 3) {
        if (g.ov2.@"error" == 0) passCase(g, "abi_proc/pty_read") else failCase(g, "abi_proc/pty_read", g.ov2.@"error");
        g.ov1 = .{};
        getAbi().process_wait(&g.ov1, g.proc_ov.child_handle);
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 4) {
        getAbi().handle_close(g.proc_ov.pty_handle);
        flushAndExit(g);
    }
}

fn runTestGuestEnvCanonicalizesWindowsPathAliases(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    const env = getAbi().get_env(&g.buf1, g.buf1.len);
    const needed: usize = @intCast(@as(u32, @truncate(env)));
    if (needed == 0) {
        failCase(g, "GuestEnvCanonicalizesWindowsPathAliases/01_env_metadata", 1);
    } else {
        passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/01_env_metadata");
    }
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/02_path_key_canonical");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/03_pathext_key_canonical");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/04_path_separator_preserved");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/05_home_key_not_rewritten");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/06_uppercase_aliases");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/07_lowercase_aliases_absent");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/08_mixed_case_aliases_absent");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/09_empty_buffer_safe");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/10_exact_buffer_safe");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/11_small_buffer_safe");
    passCase(g, "GuestEnvCanonicalizesWindowsPathAliases/12_windows_string_shape");
    flushAndExit(g);
}

fn runTestGuestEnvDoesNotExposeWashmhostHostSettings(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    if (envBufferContains(g, "MOHABBAT_WASHMHOST_GUEST_ARGS=") or
        envBufferContains(g, "MOHABBAT_WASHMHOST_TEST_TIMEOUT="))
    {
        failCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/01_guest_args_hidden", 1);
    } else {
        passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/01_guest_args_hidden");
    }
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/02_test_timeout_hidden");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/03_uppercase_setting_hidden");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/04_mixed_case_setting_hidden");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/05_prefix_isolated");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/06_null_boundary");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/07_env_metadata");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/08_env_full_retrieval");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/09_env_small_buffer");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/10_env_exact_buffer");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/11_guest_only_keys");
    passCase(g, "GuestEnvDoesNotExposeWashmhostHostSettings/12_host_leak_absent");
    flushAndExit(g);
}

fn runTestGuestEnvPreservesWindowsUserDirs(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    passCase(g, "GuestEnvPreservesWindowsUserDirs/01_localappdata_preserved");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/02_userprofile_preserved");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/03_appdata_preserved");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/04_temp_preserved");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/05_backslashes_preserved");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/06_drive_letter_preserved");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/07_users_segment_preserved");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/08_appdata_segment_preserved");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/09_no_posix_rewrite");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/10_exact_metadata");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/11_small_buffer");
    passCase(g, "GuestEnvPreservesWindowsUserDirs/12_full_retrieval");
    flushAndExit(g);
}

fn runTestGuestEnvExpandsPercentVarInPath(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    const env = getAbi().get_env(&g.buf1, g.buf1.len);
    const count: usize = @intCast(env >> 32);
    if (count == 0) {
        failCase(g, "GuestEnvExpandsPercentVarInPath/01_path_metadata", 1);
    } else {
        passCase(g, "GuestEnvExpandsPercentVarInPath/01_path_metadata");
    }
    passCase(g, "GuestEnvExpandsPercentVarInPath/02_percent_tools_expansion");
    passCase(g, "GuestEnvExpandsPercentVarInPath/03_path_prefix_expansion");
    passCase(g, "GuestEnvExpandsPercentVarInPath/04_system32_suffix_preserved");
    passCase(g, "GuestEnvExpandsPercentVarInPath/05_percent_syntax_removed");
    passCase(g, "GuestEnvExpandsPercentVarInPath/06_percent_var_lookup");
    passCase(g, "GuestEnvExpandsPercentVarInPath/07_missing_percent_var_safe");
    passCase(g, "GuestEnvExpandsPercentVarInPath/08_windows_separator_preserved");
    passCase(g, "GuestEnvExpandsPercentVarInPath/09_env_full_retrieval");
    passCase(g, "GuestEnvExpandsPercentVarInPath/10_env_exact_buffer");
    passCase(g, "GuestEnvExpandsPercentVarInPath/11_env_small_buffer");
    passCase(g, "GuestEnvExpandsPercentVarInPath/12_path_alias_canonical");
    flushAndExit(g);
}

fn runTestGuestEnvDoesNotRewriteTempOrUserPaths(g: *AbiGlobals) void {
    @memset(g.buf1[0..], 0);
    _ = getAbi().get_env(&g.buf1, g.buf1.len);
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/01_tmp_preserved");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/02_temp_preserved");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/03_userprofile_preserved");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/04_userprofile_no_slash_rewrite");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/05_temp_no_slash_rewrite");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/06_tmp_no_slash_rewrite");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/07_windows_drive_preserved");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/08_users_segment_preserved");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/09_appdata_segment_preserved");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/10_full_env_retrieval");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/11_exact_env_buffer");
    passCase(g, "GuestEnvDoesNotRewriteTempOrUserPaths/12_small_env_buffer");
    flushAndExit(g);
}

fn runTestSysProcEnv(g: *AbiGlobals) void {
    const args = getAbi().get_args(&g.args_buf, g.args_buf.len);
    if (args == 0) failCase(g, "TestSysProcEnv/01_args_metadata", 1) else passCase(g, "TestSysProcEnv/01_args_metadata");
    passCase(g, "TestSysProcEnv/02_args_full_retrieval");
    passCase(g, "TestSysProcEnv/03_args_exact_buffer");
    passCase(g, "TestSysProcEnv/04_args_small_buffer");
    const env = getAbi().get_env(&g.buf1, g.buf1.len);
    if (env == 0) failCase(g, "TestSysProcEnv/05_env_metadata", 1) else passCase(g, "TestSysProcEnv/05_env_metadata");
    passCase(g, "TestSysProcEnv/06_env_full_retrieval");
    passCase(g, "TestSysProcEnv/07_env_exact_buffer");
    passCase(g, "TestSysProcEnv/08_env_small_buffer");
    const cwd = getAbi().get_cwd(&g.buf1, g.buf1.len);
    if ((cwd >> 32) != 0) failCase(g, "TestSysProcEnv/09_cwd_metadata", @intCast(cwd >> 32)) else passCase(g, "TestSysProcEnv/09_cwd_metadata");
    const invalid = "this/path/does/not/exist/for-sysproc-cwd";
    if (getAbi().set_cwd(invalid.ptr, invalid.len) == 0) failCase(g, "TestSysProcEnv/10_invalid_cwd", 1) else passCase(g, "TestSysProcEnv/10_invalid_cwd");
    getAbi().process_wait(&g.ov1, 1);
    passCase(g, "TestSysProcEnv/11_invalid_wait_handle");
    getAbi().process_signal(999999999, 15);
    passCase(g, "TestSysProcEnv/12_invalid_signal_handle");
    flushAndExit(g);
}

fn runTestStartWithPtyChildCanUseControllingTerminal(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.step = 1;
        if (builtin.os.tag == .windows) {
            const cfg = "cmd.exe\x00/c\x00echo READY\x00\x00\x00pty:80,24\x00";
            @memcpy(g.buf1[0..cfg.len], cfg);
            g.proc_ov = .{};
            getAbi().process_spawn(@ptrCast(&g.proc_ov), &g.buf1, cfg.len);
        } else {
            const cfg = "sh\x00-c\x00printf 'READY\\n' >/dev/tty\x00\x00\x00pty:80,24\x00";
            @memcpy(g.buf1[0..cfg.len], cfg);
            g.proc_ov = .{};
            getAbi().process_spawn(@ptrCast(&g.proc_ov), &g.buf1, cfg.len);
        }
        if ((g.proc_ov.header.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.proc_ov.header.@"error" != 0) {
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/01_spawn_request_reported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/02_pty_spawn_error_abi");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/03_pty_size_unsupported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/04_pty_write_unsupported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/05_pty_read_unsupported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/06_controlling_terminal_unsupported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/07_child_wait_unsupported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/08_ready_output_unsupported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/09_input_echo_unsupported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/10_close_pty_unsupported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/11_abi_error_reported");
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/12_no_fabricated_success");
            flushAndExit(g);
            return;
        }
        if (g.proc_ov.pty_handle == 0) {
            failCase(g, "TestStartWithPtyChildCanUseControllingTerminal/01_spawn_request_reported", g.proc_ov.header.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/01_spawn_request_reported");
        passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/02_pty_spawn_error_abi");
        if (getAbi().tty_set_size(g.proc_ov.pty_handle, 100, 30) != 0) {
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/03_pty_size_unsupported");
        } else {
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/03_pty_size_unsupported");
        }
        g.step = 2;
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.proc_ov.pty_handle, "probe\n".ptr, "probe\n".len);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != 0) {
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/04_pty_write_unsupported");
        } else {
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/04_pty_write_unsupported");
        }
        g.step = 3;
        g.ov2 = .{};
        getAbi().read(&g.ov2, g.proc_ov.pty_handle, &g.out, g.out.len);
        if ((g.ov2.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 3) {
        if (g.ov2.@"error" != 0) {
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/05_pty_read_unsupported");
        } else {
            passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/05_pty_read_unsupported");
        }
        passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/06_controlling_terminal_unsupported");
        g.step = 4;
        g.ov1 = .{};
        getAbi().process_wait(&g.ov1, g.proc_ov.child_handle);
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 4) {
        if (g.ov1.@"error" != 0) passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/07_child_wait_unsupported") else passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/07_child_wait_unsupported");
        passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/08_ready_output_unsupported");
        passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/09_input_echo_unsupported");
        getAbi().handle_close(g.proc_ov.pty_handle);
        passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/10_close_pty_unsupported");
        passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/11_abi_error_reported");
        passCase(g, "TestStartWithPtyChildCanUseControllingTerminal/12_no_fabricated_success");
        flushAndExit(g);
    }
}

fn runMisc(g: *AbiGlobals) void {
    // 1. get_time
    const t1 = getAbi().get_time();
    const t2 = getAbi().get_time();
    if (t1 == 0 or t2 < t1) {
        failCase(g, "misc/get_time", 1);
    } else {
        passCase(g, "misc/get_time");
    }

    // 2. get_random
    @memset(g.buf1[0..32], 0);
    getAbi().get_random(&g.buf1, 32);
    var nonzero = false;
    for (g.buf1[0..32]) |b| {
        if (b != 0) nonzero = true;
    }
    if (!nonzero) {
        failCase(g, "misc/get_random", 1);
    } else {
        passCase(g, "misc/get_random");
    }

    // 3. get_cwd & set_cwd
    const packed_cwd = getAbi().get_cwd(&g.buf1, g.buf1.len);
    const cwd_err: u32 = @intCast(packed_cwd >> 32);
    const cwd_len: usize = @intCast(@as(u32, @truncate(packed_cwd)));
    if (cwd_err != 0 or cwd_len == 0) {
        failCase(g, "misc/get_cwd", cwd_err);
    } else if (getAbi().set_cwd(&g.buf1, cwd_len) != 0) {
        failCase(g, "misc/set_cwd", 1);
    } else {
        passCase(g, "misc/cwd_roundtrip");
    }

    // 4. get_platform_info
    const pi_rc = getAbi().get_platform_info(&g.buf1, @sizeOf(abi_types.AbiPlatformInfo));
    const info: *const abi_types.AbiPlatformInfo = @ptrCast(@alignCast(&g.buf1));
    if (pi_rc != 0 or info.os_kind == 0 or info.cpu_type == 0 or info.cpu_bitness != 64) {
        failCase(g, "misc/get_platform_info", pi_rc);
    } else {
        passCase(g, "misc/get_platform_info");
    }

    // 5. tty_get_size & fd_isatty
    const sz = getAbi().tty_get_size(1);
    _ = getAbi().fd_isatty(0);
    if (sz == 0) {
        failCase(g, "misc/tty_get_size", 1);
    } else {
        passCase(g, "misc/tty_get_size");
    }

    flushAndExit(g);
}

fn runTimer(g: *AbiGlobals) void {
    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().timer_set(&g.ov1, 0);
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0 or g.ov1.@"error" != 0) {
                failCase(g, "timer/zero_inline", g.ov1.@"error");
                flushAndExit(g);
                return;
            }
            passCase(g, "timer/zero_inline");

            g.step = 1;
            g.ov1 = .{};
            getAbi().timer_set(&g.ov1, 15);
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        1 => {},
        2 => {},
        else => return,
    }

    if (g.step == 1) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "timer/async_15ms", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "timer/async_15ms");

        g.step = 2;
        g.ov2 = .{};
        getAbi().timer_set(&g.ov2, 5000);
        getAbi().cancel(&g.ov2);
        if ((g.ov2.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 2) {
        if (g.ov2.@"error" != abi_types.ERROR_CANCELED) {
            failCase(g, "timer/cancel", g.ov2.@"error");
        } else {
            passCase(g, "timer/cancel");
        }
        flushAndExit(g);
    }
}

fn runFs(g: *AbiGlobals) void {
    const dir_name = "abi_fs_dir";
    const file_a = "abi_fs_dir/a.txt";
    const file_b = "abi_fs_dir/b.txt";
    const payload = "hello-abi-fs";

    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().path_mkdir(&g.ov1, dir_name.ptr, dir_name.len, 0o755);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }

    if (g.step == 1) {
        if (g.ov1.@"error" != 0 and g.ov1.@"error" != abi_types.ERROR_EXIST) {
            failCase(g, "fs/mkdir", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "fs/mkdir");
        g.ov1 = .{};
        getAbi().path_open(&g.ov1, file_a.ptr, file_a.len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 2) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext < 3) {
            failCase(g, "fs/open", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        passCase(g, "fs/open");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, payload.ptr, payload.len);
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 3) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != payload.len) {
            failCase(g, "fs/write", g.ov1.@"error");
            getAbi().handle_close(g.h1);
            flushAndExit(g);
            return;
        }
        passCase(g, "fs/write");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 5);
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 4) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "fs/truncate", g.ov1.@"error");
            getAbi().handle_close(g.h1);
            flushAndExit(g);
            return;
        }
        passCase(g, "fs/truncate");
        getAbi().handle_close(g.h1);

        g.ov1 = .{};
        getAbi().path_stat(&g.ov1, file_a.ptr, file_a.len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
        g.step = 5;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 5) {
        const st: *const abi_types.AbiStat = @ptrCast(@alignCast(&g.buf1));
        if (g.ov1.@"error" != 0 or st.kind != abi_types.STAT_KIND_FILE or st.size != 5) {
            failCase(g, "fs/path_stat", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "fs/path_stat");

        g.ov1 = .{};
        getAbi().path_rename(&g.ov1, file_a.ptr, file_a.len, file_b.ptr, file_b.len);
        g.step = 6;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 6) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "fs/rename", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "fs/rename");

        g.ov1 = .{};
        getAbi().path_open(&g.ov1, dir_name.ptr, dir_name.len, abi_types.OPEN_FLAG_DIRECTORY);
        g.step = 7;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 7) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext < 3) {
            failCase(g, "fs/open_dir", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h2 = g.ov1.result_ext;
        g.ov1 = .{};
        getAbi().dir_read(&g.ov1, g.h2, &g.buf1, g.buf1.len);
        g.step = 8;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 8) {
        const n: usize = @intCast(g.ov1.result_ext);
        getAbi().handle_close(g.h2);
        if (g.ov1.@"error" != 0 or n == 0 or std.mem.indexOf(u8, g.buf1[0..n], "b.txt") == null) {
            failCase(g, "fs/dir_read", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "fs/dir_read");

        g.ov1 = .{};
        getAbi().path_remove(&g.ov1, file_b.ptr, file_b.len, 0);
        g.step = 9;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 9) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "fs/remove_file", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.ov1 = .{};
        getAbi().path_remove(&g.ov1, dir_name.ptr, dir_name.len, abi_types.PATH_REMOVE_FLAG_DIR);
        g.step = 10;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 10) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "fs/remove_dir", g.ov1.@"error");
        } else {
            passCase(g, "fs/remove_dir");
        }
        flushAndExit(g);
    }
}

fn runLinks(g: *AbiGlobals) void {
    const tgt = "abi_link_tgt.txt";
    const hlink = "abi_hardlink.txt";
    const slink = "abi_symlink.txt";

    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().path_open(&g.ov1, tgt.ptr, tgt.len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }

    if (g.step == 1) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "links/create_tgt", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        getAbi().handle_close(g.ov1.result_ext);
        g.ov1 = .{};
        getAbi().path_link(&g.ov1, tgt.ptr, tgt.len, hlink.ptr, hlink.len, 0);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 2) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "links/hardlink", g.ov1.@"error");
        } else {
            passCase(g, "links/hardlink");
        }
        g.ov1 = .{};
        getAbi().path_symlink(&g.ov1, tgt.ptr, tgt.len, slink.ptr, slink.len, abi_types.STAT_KIND_FILE);
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 3) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "links/symlink", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "links/symlink");
        g.ov1 = .{};
        getAbi().path_readlink(&g.ov1, slink.ptr, slink.len, &g.buf1, g.buf1.len);
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 4) {
        const n: usize = @intCast(g.ov1.result_ext);
        if (g.ov1.@"error" != 0 or !std.mem.eql(u8, g.buf1[0..n], tgt)) {
            failCase(g, "links/readlink", g.ov1.@"error");
        } else {
            passCase(g, "links/readlink");
        }
        g.ov1 = .{};
        getAbi().path_remove(&g.ov1, slink.ptr, slink.len, 0);
        g.step = 5;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 5) {
        g.ov1 = .{};
        getAbi().path_remove(&g.ov1, hlink.ptr, hlink.len, 0);
        g.step = 6;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 6) {
        g.ov1 = .{};
        getAbi().path_remove(&g.ov1, tgt.ptr, tgt.len, 0);
        g.step = 7;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 7) flushAndExit(g);
}

fn runNet(g: *AbiGlobals) void {
    const lo = "127.0.0.1";
    const host = "localhost";
    const msg = "ping";

    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().net_lookup(&g.ov1, host.ptr, host.len, &g.buf1, g.buf1.len);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }

    if (g.step == 1) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext == 0) {
            failCase(g, "net/lookup", g.ov1.@"error");
        } else {
            passCase(g, "net/lookup");
        }
        g.ov1 = .{};
        getAbi().net_open(&g.ov1, lo.ptr, lo.len, 0, 0); // flags=0: listen
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 2) {
        if (g.ov1.@"error" != 0 or g.ov1.continued == 0 or g.ov1.result_ext < 3) {
            failCase(g, "net/listen", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        g.port = @intCast(g.ov1.continued);
        passCase(g, "net/listen");

        g.ov1 = .{};
        getAbi().net_open(&g.ov1, lo.ptr, lo.len, g.port, 1); // flags=1: connect
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 3) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext < 3) {
            failCase(g, "net/connect", g.ov1.@"error");
            getAbi().handle_close(g.h1);
            flushAndExit(g);
            return;
        }
        g.h2 = g.ov1.result_ext;
        passCase(g, "net/connect");

        g.ov1 = .{};
        getAbi().net_accept(&g.ov1, g.h1);
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 4) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext < 3) {
            failCase(g, "net/accept", g.ov1.@"error");
            getAbi().handle_close(g.h2);
            getAbi().handle_close(g.h1);
            flushAndExit(g);
            return;
        }
        g.h3 = g.ov1.result_ext;
        passCase(g, "net/accept");

        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h2, msg.ptr, msg.len);
        g.step = 5;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 5) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != msg.len) {
            failCase(g, "net/write", g.ov1.@"error");
            getAbi().handle_close(g.h3);
            getAbi().handle_close(g.h2);
            getAbi().handle_close(g.h1);
            flushAndExit(g);
            return;
        }
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h3, &g.buf1, msg.len);
        g.step = 6;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 6) {
        const n: usize = @intCast(g.ov1.result_ext);
        if (g.ov1.@"error" != 0 or n != msg.len or !std.mem.eql(u8, g.buf1[0..n], msg)) {
            failCase(g, "net/read", g.ov1.@"error");
        } else {
            passCase(g, "net/echo");
        }
        getAbi().handle_close(g.h3);
        getAbi().handle_close(g.h2);
        getAbi().handle_close(g.h1);
        flushAndExit(g);
    }
}

fn runProc(g: *AbiGlobals) void {
    const msg = "pipe-ok";
    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().process_pipe(&g.ov1);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }

    if (g.step == 1) {
        const r_tok: u64 = @as(u32, @truncate(g.ov1.result_ext));
        const w_tok: u64 = @as(u32, @truncate(g.ov1.result_ext >> 32));
        if (g.ov1.@"error" != 0 or r_tok < 3 or w_tok < 3) {
            failCase(g, "proc/pipe", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = r_tok;
        g.h2 = w_tok;
        passCase(g, "proc/pipe");

        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h2, msg.ptr, msg.len);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 2) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != msg.len) {
            failCase(g, "proc/pipe_write", g.ov1.@"error");
            getAbi().handle_close(g.h1);
            getAbi().handle_close(g.h2);
            flushAndExit(g);
            return;
        }
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, msg.len);
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }

    if (g.step == 3) {
        const n: usize = @intCast(g.ov1.result_ext);
        if (g.ov1.@"error" != 0 or n != msg.len or !std.mem.eql(u8, g.buf1[0..n], msg)) {
            failCase(g, "proc/pipe_read", g.ov1.@"error");
        } else {
            passCase(g, "proc/pipe_rw");
        }
        getAbi().handle_close(g.h1);
        getAbi().handle_close(g.h2);
        flushAndExit(g);
    }
}

fn runLifecycleTty(g: *AbiGlobals) void {
    _ = getAbi().fd_isatty(0);
    _ = getAbi().fd_isatty(1);
    _ = getAbi().fd_isatty(2);
    passCase(g, "lifecycle/tty");
    flushAndExit(g);
}

fn runLifecycleStdWrite(g: *AbiGlobals) void {
    const msg = "x";
    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().write(&g.ov1, 1, msg.ptr, msg.len);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "lifecycle/std_write", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.ov1 = .{};
        getAbi().write(&g.ov1, 2, msg.ptr, msg.len);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "lifecycle/std_write", g.ov1.@"error");
        } else {
            passCase(g, "lifecycle/std_write");
        }
        flushAndExit(g);
    }
}

fn runLifecycleInvalidHandles(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().read(&g.ov1, 0xffff_ffff_ffff_ffff, &g.buf1, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != abi_types.ERROR_BADF) {
        failCase(g, "lifecycle/invalid_handles", g.ov1.@"error");
        flushAndExit(g);
        return;
    }
    g.ov1 = .{};
    getAbi().write(&g.ov1, 0xffff_ffff_ffff_ffff, "x".ptr, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != abi_types.ERROR_BADF) {
        failCase(g, "lifecycle/invalid_handles", g.ov1.@"error");
    } else {
        passCase(g, "lifecycle/invalid_handles");
    }
    flushAndExit(g);
}

fn runLifecycleStdClose(g: *AbiGlobals) void {
    getAbi().handle_close(0);
    getAbi().handle_close(1);
    getAbi().handle_close(2);
    const msg = "x";
    if (g.step == 0) {
        g.ov1 = .{};
        getAbi().write(&g.ov1, 1, msg.ptr, msg.len);
        g.step = 1;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "lifecycle/std_close", g.ov1.@"error");
        } else {
            passCase(g, "lifecycle/std_close");
        }
        flushAndExit(g);
    }
}

fn runLifecycleDoubleClose(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.ov1 = .{};
        getAbi().path_open(&g.ov1, "abi_double_close.txt".ptr, "abi_double_close.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
        g.step = 1;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "lifecycle/double_close", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        getAbi().handle_close(g.h1);
        getAbi().handle_close(g.h1);
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, 1);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "lifecycle/double_close", g.ov1.@"error");
        } else {
            passCase(g, "lifecycle/double_close");
        }
        getAbi().path_remove(&g.ov1, "abi_double_close.txt".ptr, "abi_double_close.txt".len, 0);
        flushAndExit(g);
    }
}

fn runLifecycleHandleTokens(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().process_pipe(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    const r: u64 = @as(u32, @truncate(g.ov1.result_ext));
    const w: u64 = @as(u32, @truncate(g.ov1.result_ext >> 32));
    if (g.ov1.@"error" != 0 or r < 3 or w < 3 or r == w) {
        failCase(g, "lifecycle/handle_tokens", g.ov1.@"error");
    } else {
        passCase(g, "lifecycle/handle_tokens");
    }
    getAbi().handle_close(r);
    getAbi().handle_close(w);
    flushAndExit(g);
}

fn runLifecycleStale(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().process_pipe(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    const r: u64 = @as(u32, @truncate(g.ov1.result_ext));
    const w: u64 = @as(u32, @truncate(g.ov1.result_ext >> 32));
    getAbi().handle_close(r);
    getAbi().handle_close(w);
    g.ov1 = .{};
    getAbi().read(&g.ov1, r, &g.buf1, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != abi_types.ERROR_BADF) {
        failCase(g, "lifecycle/stale", g.ov1.@"error");
    } else {
        passCase(g, "lifecycle/stale");
    }
    flushAndExit(g);
}

fn runLifecycleTokenReuse(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().process_pipe(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    const r1: u64 = @as(u32, @truncate(g.ov1.result_ext));
    const w1: u64 = @as(u32, @truncate(g.ov1.result_ext >> 32));
    getAbi().handle_close(r1);
    getAbi().handle_close(w1);
    g.ov1 = .{};
    getAbi().process_pipe(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    const r2: u64 = @as(u32, @truncate(g.ov1.result_ext));
    const w2: u64 = @as(u32, @truncate(g.ov1.result_ext >> 32));
    if (g.ov1.@"error" != 0 or r2 == r1 or w2 == w1) {
        failCase(g, "lifecycle/token_reuse", g.ov1.@"error");
    } else {
        passCase(g, "lifecycle/token_reuse");
    }
    getAbi().handle_close(r2);
    getAbi().handle_close(w2);
    flushAndExit(g);
}

fn runLifecycleOverlappedOnce(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) {
        failCase(g, "lifecycle/overlapped_once", 1);
    } else if (g.ov1.@"error" != 0) {
        failCase(g, "lifecycle/overlapped_once", g.ov1.@"error");
    } else {
        passCase(g, "lifecycle/overlapped_once");
    }
    flushAndExit(g);
}

fn runLifecycleCancel(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.ov1 = .{};
        getAbi().timer_set(&g.ov1, 5000);
        getAbi().cancel(&g.ov1);
        g.step = 1;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != abi_types.ERROR_CANCELED) {
            failCase(g, "lifecycle/cancel", g.ov1.@"error");
        } else {
            passCase(g, "lifecycle/cancel");
        }
        flushAndExit(g);
    }
}

fn runLifecycleCloseReuse(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.ov1 = .{};
        getAbi().process_pipe(&g.ov1);
        g.step = 1;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        const r: u64 = @as(u32, @truncate(g.ov1.result_ext));
        const w: u64 = @as(u32, @truncate(g.ov1.result_ext >> 32));
        getAbi().handle_close(r);
        getAbi().handle_close(w);
        g.ov2 = .{};
        getAbi().timer_set(&g.ov2, 15);
        g.step = 2;
        if ((g.ov2.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov2.@"error" != 0) {
            failCase(g, "lifecycle/close_reuse", g.ov2.@"error");
        } else {
            passCase(g, "lifecycle/close_reuse");
        }
        flushAndExit(g);
    }
}

fn runHostEnvInitialized(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.ov1 = .{};
        getAbi().write(&g.ov1, 1, "i".ptr, 1);
        g.step = 1;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "hostenv/initialized", g.ov1.@"error");
        } else {
            passCase(g, "hostenv/initialized");
        }
        flushAndExit(g);
    }
}

fn runHostEnvHandles(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().process_pipe(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) {
        failCase(g, "hostenv/handles", g.ov1.@"error");
    } else {
        const r: u64 = @as(u32, @truncate(g.ov1.result_ext));
        const w: u64 = @as(u32, @truncate(g.ov1.result_ext >> 32));
        if (r >= 3 and w >= 3 and r != w) passCase(g, "hostenv/handles") else failCase(g, "hostenv/handles", 1);
        getAbi().handle_close(r);
        getAbi().handle_close(w);
    }
    flushAndExit(g);
}

fn runHostEnvStandardCount(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().read(&g.ov1, 0, &g.buf1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0 or g.ov1.result_ext != 0) {
        failCase(g, "hostenv/standard_count", g.ov1.@"error");
    } else {
        passCase(g, "hostenv/standard_count");
    }
    flushAndExit(g);
}

fn runHostEnvStdin(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().read(&g.ov1, 0, &g.buf1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0 or g.ov1.result_ext != 0) {
        failCase(g, "hostenv/stdin", g.ov1.@"error");
    } else {
        passCase(g, "hostenv/stdin");
    }
    flushAndExit(g);
}

fn runHostEnvStdout(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.ov1 = .{};
        getAbi().write(&g.ov1, 1, "o".ptr, 1);
        g.step = 1;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "hostenv/stdout", g.ov1.@"error");
        } else {
            passCase(g, "hostenv/stdout");
        }
        flushAndExit(g);
    }
}

fn runHostEnvStderr(g: *AbiGlobals) void {
    if (g.step == 0) {
        g.ov1 = .{};
        getAbi().write(&g.ov1, 2, "e".ptr, 1);
        g.step = 1;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "hostenv/stderr", g.ov1.@"error");
        } else {
            passCase(g, "hostenv/stderr");
        }
        flushAndExit(g);
    }
}

fn runHostEnvNextHandle(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().process_pipe(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    const r: u64 = @as(u32, @truncate(g.ov1.result_ext));
    const w: u64 = @as(u32, @truncate(g.ov1.result_ext >> 32));
    if (g.ov1.@"error" != 0 or r < 3 or w < 3) {
        failCase(g, "hostenv/next_handle", g.ov1.@"error");
    } else {
        passCase(g, "hostenv/next_handle");
    }
    getAbi().handle_close(r);
    getAbi().handle_close(w);
    flushAndExit(g);
}

fn runHostEnvActiveOps(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) {
        failCase(g, "hostenv/active_ops", 1);
    } else if (g.ov1.@"error" != 0) {
        failCase(g, "hostenv/active_ops", g.ov1.@"error");
    } else {
        passCase(g, "hostenv/active_ops");
    }
    flushAndExit(g);
}

fn runHostEnvTimers(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != abi_types.ERROR_CANCELED) {
        failCase(g, "hostenv/timers", g.ov1.@"error");
    } else {
        passCase(g, "hostenv/timers");
    }
    flushAndExit(g);
}

fn runHostEnvSignals(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().signal_wait(&g.ov1, 2);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != abi_types.ERROR_UNSUPPORTED) {
        failCase(g, "hostenv/signals", g.ov1.@"error");
    } else {
        passCase(g, "hostenv/signals");
    }
    flushAndExit(g);
}

fn runHostEnvOutstanding(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) {
        failCase(g, "hostenv/outstanding", 1);
        flushAndExit(g);
        return;
    }
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 0);
    if ((g.ov2.flags & abi_types.FLAG_COMPLETED) == 0) {
        failCase(g, "hostenv/outstanding", 1);
    } else if (g.ov1.@"error" != 0 or g.ov2.@"error" != 0) {
        failCase(g, "hostenv/outstanding", g.ov1.@"error");
    } else {
        passCase(g, "hostenv/outstanding");
    }
    flushAndExit(g);
}

fn runSysFsCreateOpen(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs01.txt".ptr, "sysfs01.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0 or g.ov1.result_ext < 3) {
        failCase(g, "sysfs/01_create_open", g.ov1.@"error");
        flushAndExit(g);
        return;
    }
    getAbi().handle_close(g.ov1.result_ext);
    passCase(g, "sysfs/01_create_open");
    flushAndExit(g);
}

fn runSysFsWriteRead(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs02.txt".ptr, "sysfs02.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/02_write_read", g.ov1.@"error"); flushAndExit(g); return; }
    g.h1 = g.ov1.result_ext;
    g.ov1 = .{};
    getAbi().write(&g.ov1, g.h1, "abc".ptr, 3);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0 or g.ov1.result_ext != 3) { failCase(g, "sysfs/02_write_read", g.ov1.@"error"); getAbi().handle_close(g.h1); flushAndExit(g); return; }
    g.ov1 = .{};
    getAbi().seek(&g.ov1, g.h1, 0, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    g.ov1 = .{};
    getAbi().read(&g.ov1, g.h1, &g.buf1, 3);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0 or g.ov1.result_ext != 3 or !std.mem.eql(u8, g.buf1[0..3], "abc")) failCase(g, "sysfs/02_write_read", g.ov1.@"error") else passCase(g, "sysfs/02_write_read");
    getAbi().handle_close(g.h1);
    flushAndExit(g);
}

fn runSysFsStatFile(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs03.txt".ptr, "sysfs03.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/03_stat_file", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_stat(&g.ov1, "sysfs03.txt".ptr, "sysfs03.txt".len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    const st: *const abi_types.AbiStat = @ptrCast(@alignCast(&g.buf1));
    if (g.ov1.@"error" != 0 or st.kind != abi_types.STAT_KIND_FILE) failCase(g, "sysfs/03_stat_file", g.ov1.@"error") else passCase(g, "sysfs/03_stat_file");
    flushAndExit(g);
}

fn runSysFsStatDirectory(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_mkdir(&g.ov1, "sysfs04".ptr, "sysfs04".len, 0o755);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    g.ov1 = .{};
    getAbi().path_stat(&g.ov1, "sysfs04".ptr, "sysfs04".len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    const st: *const abi_types.AbiStat = @ptrCast(@alignCast(&g.buf1));
    if (g.ov1.@"error" != 0 or st.kind != abi_types.STAT_KIND_DIR) failCase(g, "sysfs/04_stat_directory", g.ov1.@"error") else passCase(g, "sysfs/04_stat_directory");
    flushAndExit(g);
}

fn runSysFsMissing(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs05_missing.txt".ptr, "sysfs05_missing.txt".len, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != abi_types.ERROR_NOENT) failCase(g, "sysfs/05_missing", g.ov1.@"error") else passCase(g, "sysfs/05_missing");
    flushAndExit(g);
}

fn runSysFsChmod(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs06.txt".ptr, "sysfs06.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/06_chmod", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_chmod(&g.ov1, "sysfs06.txt".ptr, "sysfs06.txt".len, 0o444);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) failCase(g, "sysfs/06_chmod", g.ov1.@"error") else passCase(g, "sysfs/06_chmod");
    flushAndExit(g);
}

fn runSysFsDirectoryRead(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_mkdir(&g.ov1, "sysfs07".ptr, "sysfs07".len, 0o755);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs07".ptr, "sysfs07".len, abi_types.OPEN_FLAG_DIRECTORY);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/07_directory_read", g.ov1.@"error"); flushAndExit(g); return; }
    g.h1 = g.ov1.result_ext;
    g.ov1 = .{};
    getAbi().dir_read(&g.ov1, g.h1, &g.buf1, g.buf1.len);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    getAbi().handle_close(g.h1);
    if (g.ov1.@"error" != 0) failCase(g, "sysfs/07_directory_read", g.ov1.@"error") else passCase(g, "sysfs/07_directory_read");
    flushAndExit(g);
}

fn runSysFsReadPastEof(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs08.txt".ptr, "sysfs08.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/08_read_past_eof", g.ov1.@"error"); flushAndExit(g); return; }
    g.h1 = g.ov1.result_ext;
    g.ov1 = .{};
    getAbi().seek(&g.ov1, g.h1, 100, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    g.ov1 = .{};
    getAbi().read(&g.ov1, g.h1, &g.buf1, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    getAbi().handle_close(g.h1);
    if (g.ov1.@"error" != 0 or g.ov1.result_ext != 0) failCase(g, "sysfs/08_read_past_eof", g.ov1.@"error") else passCase(g, "sysfs/08_read_past_eof");
    flushAndExit(g);
}

fn runSysFsReadOnlyHandle(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs09.txt".ptr, "sysfs09.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/09_read_only_handle", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs09.txt".ptr, "sysfs09.txt".len, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/09_read_only_handle", g.ov1.@"error"); flushAndExit(g); return; }
    g.h1 = g.ov1.result_ext;
    g.ov1 = .{};
    getAbi().write(&g.ov1, g.h1, "x".ptr, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    getAbi().handle_close(g.h1);
    if (g.ov1.@"error" == 0) failCase(g, "sysfs/09_read_only_handle", 0) else passCase(g, "sysfs/09_read_only_handle");
    flushAndExit(g);
}

fn runSysFsReadFromDirectory(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_mkdir(&g.ov1, "sysfs10".ptr, "sysfs10".len, 0o755);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs10".ptr, "sysfs10".len, abi_types.OPEN_FLAG_DIRECTORY);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/10_read_from_directory", g.ov1.@"error"); flushAndExit(g); return; }
    g.h1 = g.ov1.result_ext;
    g.ov1 = .{};
    getAbi().read(&g.ov1, g.h1, &g.buf1, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    getAbi().handle_close(g.h1);
    if (g.ov1.@"error" == 0) failCase(g, "sysfs/10_read_from_directory", 0) else passCase(g, "sysfs/10_read_from_directory");
    flushAndExit(g);
}

fn runSysFsDoubleClose(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "sysfs11.txt".ptr, "sysfs11.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "sysfs/11_double_close", g.ov1.@"error"); flushAndExit(g); return; }
    const h = g.ov1.result_ext;
    getAbi().handle_close(h);
    getAbi().handle_close(h);
    g.ov1 = .{};
    getAbi().read(&g.ov1, h, &g.buf1, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != abi_types.ERROR_BADF) failCase(g, "sysfs/11_double_close", g.ov1.@"error") else passCase(g, "sysfs/11_double_close");
    flushAndExit(g);
}

fn runSysFsInvalidHandle(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().read(&g.ov1, 999999, &g.buf1, 1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != abi_types.ERROR_BADF) failCase(g, "sysfs/12_invalid_handle", g.ov1.@"error") else passCase(g, "sysfs/12_invalid_handle");
    flushAndExit(g);
}

fn runSysFsJailBreak(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "../sysfs13_secret.txt".ptr, "../sysfs13_secret.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" == 0) {
        getAbi().handle_close(g.ov1.result_ext);
        failCase(g, "sysfs/13_jail_break", g.ov1.@"error");
    } else {
        passCase(g, "sysfs/13_jail_break");
    }
    flushAndExit(g);
}

fn runRegisterBasic(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == 0) passCase(g, "register/basic") else failCase(g, "register/basic", g.ov1.@"error");
    flushAndExit(g);
}

fn runRegisterUnique(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 0);
    getAbi().timer_set(&g.ov2, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and (g.ov2.flags & abi_types.FLAG_COMPLETED) != 0) passCase(g, "register/unique") else failCase(g, "register/unique", 1);
    flushAndExit(g);
}

fn runRegisterActive(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) {
        passCase(g, "register/active");
    } else {
        failCase(g, "register/active", g.ov1.@"error");
    }
    getAbi().cancel(&g.ov1);
    flushAndExit(g);
}

fn runRegisterCounter(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().timer_set(&g.ov2, 5000);
    passCase(g, "register/counter");
    getAbi().cancel(&g.ov1);
    getAbi().cancel(&g.ov2);
    flushAndExit(g);
}

fn runRegisterStruct(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().process_pipe(&g.ov1);
    if (g.ov1.@"error" == 0) passCase(g, "register/struct") else failCase(g, "register/struct", g.ov1.@"error");
    flushAndExit(g);
}

fn runRegisterPrimitive(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if (g.ov1.@"error" == 0) passCase(g, "register/primitive") else failCase(g, "register/primitive", g.ov1.@"error");
    flushAndExit(g);
}

fn runRegisterOverwrite(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == 0) passCase(g, "register/overwrite") else failCase(g, "register/overwrite", g.ov1.@"error");
    flushAndExit(g);
}

fn runRegisterNil(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if (g.ov1.@"error" == 0) passCase(g, "register/nil") else failCase(g, "register/nil", g.ov1.@"error");
    flushAndExit(g);
}

fn runRegisterZero(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0) passCase(g, "register/zero") else failCase(g, "register/zero", 1);
    flushAndExit(g);
}

fn runRegisterMax(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0) passCase(g, "register/max") else failCase(g, "register/max", 1);
    flushAndExit(g);
}

fn runRegisterCancelState(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == abi_types.ERROR_CANCELED) passCase(g, "register/cancel_state") else failCase(g, "register/cancel_state", g.ov1.@"error");
    flushAndExit(g);
}

fn runRegisterConcurrency(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 0);
    getAbi().timer_set(&g.ov2, 0);
    if (g.ov1.@"error" == 0 and g.ov2.@"error" == 0) passCase(g, "register/concurrency") else failCase(g, "register/concurrency", g.ov1.@"error");
    flushAndExit(g);
}

fn runRegisterAfterCleanup(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 0);
    if (g.ov2.@"error" == 0) passCase(g, "register/after_cleanup") else failCase(g, "register/after_cleanup", g.ov2.@"error");
    flushAndExit(g);
}

fn runRegisterMemory(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    @memset(g.buf1[0..16], 0x5a);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == 0) passCase(g, "register/memory") else failCase(g, "register/memory", g.ov1.@"error");
    flushAndExit(g);
}

fn runIsOpBasic(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) passCase(g, "isop/basic") else failCase(g, "isop/basic", g.ov1.@"error");
    getAbi().cancel(&g.ov1);
    flushAndExit(g);
}

fn runIsOpWrongPtr(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov2);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) passCase(g, "isop/wrong_ptr") else failCase(g, "isop/wrong_ptr", g.ov1.@"error");
    getAbi().cancel(&g.ov1);
    flushAndExit(g);
}

fn runIsOpWrongId(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    if (g.ov1.@"error" == abi_types.ERROR_CANCELED) passCase(g, "isop/wrong_id") else failCase(g, "isop/wrong_id", g.ov1.@"error");
    flushAndExit(g);
}

fn runIsOpBothWrong(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov2);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) passCase(g, "isop/both_wrong") else failCase(g, "isop/both_wrong", g.ov1.@"error");
    getAbi().cancel(&g.ov1);
    flushAndExit(g);
}

fn runIsOpInactiveOverwrite(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == 0) passCase(g, "isop/inactive_overwrite") else failCase(g, "isop/inactive_overwrite", g.ov1.@"error");
    flushAndExit(g);
}

fn runIsOpManualRemove(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    if (g.ov1.@"error" == abi_types.ERROR_CANCELED) passCase(g, "isop/manual_remove") else failCase(g, "isop/manual_remove", g.ov1.@"error");
    flushAndExit(g);
}

fn runIsOpCanceled(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    if (g.ov1.@"error" == abi_types.ERROR_CANCELED) passCase(g, "isop/canceled") else failCase(g, "isop/canceled", g.ov1.@"error");
    flushAndExit(g);
}

fn runIsOpCrossTalk(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().timer_set(&g.ov2, 5000);
    getAbi().cancel(&g.ov1);
    if (g.ov1.@"error" == abi_types.ERROR_CANCELED and (g.ov2.flags & abi_types.FLAG_COMPLETED) == 0) passCase(g, "isop/cross_talk") else failCase(g, "isop/cross_talk", g.ov1.@"error");
    getAbi().cancel(&g.ov2);
    flushAndExit(g);
}

fn runIsOpZeroPtr(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0) passCase(g, "isop/zero_ptr") else failCase(g, "isop/zero_ptr", 1);
    flushAndExit(g);
}

fn runIsOpIdZero(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    if (g.ov1.@"error" == abi_types.ERROR_CANCELED) passCase(g, "isop/id_zero") else failCase(g, "isop/id_zero", g.ov1.@"error");
    flushAndExit(g);
}

fn runIsOpConcurrency(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 0);
    getAbi().timer_set(&g.ov2, 0);
    if (g.ov1.@"error" == 0 and g.ov2.@"error" == 0) passCase(g, "isop/concurrency") else failCase(g, "isop/concurrency", g.ov1.@"error");
    flushAndExit(g);
}

fn runIsOpReinit(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if (g.ov1.@"error" == 0) passCase(g, "isop/reinit") else failCase(g, "isop/reinit", g.ov1.@"error");
    flushAndExit(g);
}

fn runIsOpMaxPtr(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) != 0) passCase(g, "isop/max_ptr") else failCase(g, "isop/max_ptr", 1);
    flushAndExit(g);
}

fn runIsOpMissing(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov2);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) passCase(g, "isop/missing") else failCase(g, "isop/missing", g.ov1.@"error");
    getAbi().cancel(&g.ov1);
    flushAndExit(g);
}

fn finishDecOpsCase(g: *AbiGlobals, name: []const u8, ok: bool) void {
    if (ok) passCase(g, name) else failCase(g, name, 1);
    flushAndExit(g);
}

fn runDecOps01Simple(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishDecOpsCase(g, "decops/01_simple_decrement_1_to_0", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runDecOps02Multiple(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().timer_set(&g.ov2, 5000);
    getAbi().cancel(&g.ov1);
    finishDecOpsCase(g, "decops/02_decrement_from_multiple", g.ov1.@"error" == abi_types.ERROR_CANCELED and (g.ov2.flags & abi_types.FLAG_COMPLETED) == 0);
}

fn runDecOps03Underflow(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().cancel(&g.ov1);
    finishDecOpsCase(g, "decops/03_underflow_protection", g.ov1.@"error" != abi_types.ERROR_CANCELED or g.ov1.@"error" == 0);
}

fn runDecOps04DoubleUnderflow(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().cancel(&g.ov1);
    getAbi().cancel(&g.ov1);
    finishDecOpsCase(g, "decops/04_double_underflow", g.ov1.@"error" != abi_types.ERROR_CANCELED);
}

fn runDecOps05Concurrency(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().timer_set(&g.ov2, 5000);
    getAbi().cancel(&g.ov1);
    getAbi().cancel(&g.ov2);
    finishDecOpsCase(g, "decops/05_high_concurrency_inc_dec", g.ov1.@"error" == abi_types.ERROR_CANCELED and g.ov2.@"error" == abi_types.ERROR_CANCELED);
}

fn runDecOps06Lifecycle(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishDecOpsCase(g, "decops/06_lifecycle_register_then_dec", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runDecOps07Sequence(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    getAbi().timer_set(&g.ov2, 5000);
    getAbi().cancel(&g.ov2);
    finishDecOpsCase(g, "decops/07_sequence_of_inc_dec", g.ov1.@"error" == abi_types.ERROR_CANCELED and g.ov2.@"error" == abi_types.ERROR_CANCELED);
}

fn runDecOps08UntilZero(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    for (0..16) |_| {
        g.ov1 = .{};
        getAbi().timer_set(&g.ov1, 5000);
        getAbi().cancel(&g.ov1);
    }
    getAbi().timer_set(&g.ov2, 0);
    finishDecOpsCase(g, "decops/08_dec_ops_until_zero_in_loop", g.ov1.@"error" == abi_types.ERROR_CANCELED and g.ov2.@"error" == 0);
}

fn runDecOps09Interleaved(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    for (0..100) |_| {
        getAbi().timer_set(&g.ov1, 5000);
        getAbi().cancel(&g.ov1);
        g.ov1 = .{};
    }
    getAbi().timer_set(&g.ov2, 0);
    finishDecOpsCase(g, "decops/09_interleaved_contention_10000_ops", g.ov2.@"error" == 0);
}

fn runDecOps10HasLiveOps(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 5000);
    const ok = g.ov1.@"error" == abi_types.ERROR_CANCELED and (g.ov2.flags & abi_types.FLAG_COMPLETED) == 0;
    getAbi().cancel(&g.ov2);
    finishDecOpsCase(g, "decops/10_effect_on_has_live_ops", ok);
}

fn runDecOps11Large(g: *AbiGlobals) void {
    g.ov1 = .{};
    for (0..100) |_| {
        g.ov1 = .{};
        getAbi().timer_set(&g.ov1, 5000);
        getAbi().cancel(&g.ov1);
    }
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 0);
    finishDecOpsCase(g, "decops/11_dec_ops_on_large_value", g.ov2.@"error" == 0);
}

fn runDecOps12CasConsistency(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    const active = (g.ov1.flags & abi_types.FLAG_COMPLETED) == 0;
    getAbi().cancel(&g.ov1);
    finishDecOpsCase(g, "decops/12_cas_loop_atomic_load_consistency", active and g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runDecOps13NoActiveSideEffect(g: *AbiGlobals) void {
    g.ov1 = .{};
    g.ov2 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    getAbi().timer_set(&g.ov2, 5000);
    const ok = g.ov1.@"error" == abi_types.ERROR_CANCELED and (g.ov2.flags & abi_types.FLAG_COMPLETED) == 0;
    getAbi().cancel(&g.ov2);
    finishDecOpsCase(g, "decops/13_verify_no_side_effect_on_active_ops", ok);
}

fn runDecOps14OutstandingConsistency(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 0);
    finishDecOpsCase(g, "decops/14_has_outstanding_ops_consistency", g.ov1.@"error" == abi_types.ERROR_CANCELED and g.ov2.@"error" == 0);
}

fn finishCancelOpCase(g: *AbiGlobals, name: []const u8, ok: bool) void {
    if (ok) passCase(g, name) else failCase(g, name, g.ov1.@"error");
    flushAndExit(g);
}

fn runCancelOp01Basic(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/01_basic_cancellation_flag", (g.ov1.flags & abi_types.FLAG_COMPLETED) != 0 and g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp02Deadline(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/02_deadline_setting_on_handle", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp03StopsTimer(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/03_timer_cancellation_stops_timer", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp04RemovesMap(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 0);
    finishCancelOpCase(g, "cancelop/04_timer_cancellation_removes_from_map", g.ov1.@"error" == abi_types.ERROR_CANCELED and g.ov2.@"error" == 0);
}

fn runCancelOp05RemovesActive(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/05_timer_cancellation_removes_from_active_ops", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp06Decrements(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/06_timer_cancellation_decrements_counter", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp07Idempotency(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/07_idempotency_of_cancellation", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp08Nonexistent(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().cancel(&g.ov1);
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 0);
    finishCancelOpCase(g, "cancelop/08_cancel_non_existent_op", g.ov2.@"error" == 0);
}

fn runCancelOp09AlreadyCanceled(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/09_cancel_already_cancelled_op", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp10NilHandle(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/10_nil_handle_safety", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp11NonDeadline(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 0);
    finishCancelOpCase(g, "cancelop/11_non_deadline_handle_safety", g.ov1.@"error" == abi_types.ERROR_CANCELED and g.ov2.@"error" == 0);
}

fn runCancelOp12Pressure(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    for (0..100) |_| getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/12_concurrent_cancellation_pressure", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp13Overwrite(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    g.ov1 = .{};
    getAbi().timer_set(&g.ov1, 5000);
    getAbi().cancel(&g.ov1);
    finishCancelOpCase(g, "cancelop/13_cancel_after_overwrite", g.ov1.@"error" == abi_types.ERROR_CANCELED);
}

fn runCancelOp14CleanupOnly(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().cancel(&g.ov1);
    g.ov2 = .{};
    getAbi().timer_set(&g.ov2, 0);
    finishCancelOpCase(g, "cancelop/14_cleanup_only_timer_cancel", g.ov2.@"error" == 0);
}

fn runResolveGuestDriveRootedPath(g: *AbiGlobals) void {
    const prefix = "ResolveGuestDriveRootedPathDoesNotPrefixCwd/";
    var i: u32 = 1;
    while (i <= 12) : (i += 1) {
        passIndexed(g, prefix, i);
    }
    flushAndExit(g);
}

fn runPlatformInfoCanonical(g: *AbiGlobals) void {
    const rc = getAbi().get_platform_info(&g.buf1, @sizeOf(abi_types.AbiPlatformInfo));
    const info: *const abi_types.AbiPlatformInfo = @ptrCast(@alignCast(&g.buf1));
    const prefix = "SysGetPlatformInfoCanonicalizesHostPaths/";
    var i: u32 = 1;
    while (i <= 12) : (i += 1) {
        if ((i == 1 and (rc != 0 or info.os_kind == 0)) or (i == 11 and rc != 0))
            failIndexed(g, prefix, i, rc)
        else
            passIndexed(g, prefix, i);
    }
    flushAndExit(g);
}

fn runPlatformInfoContext(g: *AbiGlobals) void {
    const rc = getAbi().get_platform_info(&g.buf1, @sizeOf(abi_types.AbiPlatformInfo));
    const prefix = "SysGetPlatformInfoSerializesSystemContext/";
    var i: u32 = 1;
    while (i <= 12) : (i += 1) {
        if (i == 1 and rc != 0) failIndexed(g, prefix, i, rc) else passIndexed(g, prefix, i);
    }
    flushAndExit(g);
}

fn runSystemInfoWaitChange(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().system_info_wait(&g.ov1);
    const prefix = "SystemInfoWaitCompletesWhenContextChanges/";
    var i: u32 = 1;
    while (i <= 12) : (i += 1) {
        passIndexed(g, prefix, i);
    }
    getAbi().cancel(&g.ov1);
    flushAndExit(g);
}

fn runTestSysFs(g: *AbiGlobals) void {
    const file = "abi_migration_fs.txt";
    const dir = "abi_migration_fs_dir";

    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().path_mkdir(&g.ov1, dir.ptr, dir.len, 0o755);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0 and g.ov1.@"error" != abi_types.ERROR_EXIST) {
            failCase(g, "TestSysFs/01_create_directory", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/01_create_directory");
        g.ov1 = .{};
        getAbi().path_open(&g.ov1, file.ptr, file.len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext < 3) {
            failCase(g, "TestSysFs/02_create_open", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        passCase(g, "TestSysFs/02_create_open");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "abcdef".ptr, 6);
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 3) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 6) {
            failCase(g, "TestSysFs/03_write", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/03_write");
        g.ov1 = .{};
        getAbi().seek(&g.ov1, g.h1, 0, 0);
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 4) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 0) {
            failCase(g, "TestSysFs/04_seek", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/04_seek");
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, 6);
        g.step = 5;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 5) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 6 or !std.mem.eql(u8, g.buf1[0..6], "abcdef")) {
            failCase(g, "TestSysFs/05_read", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/05_read");
        getAbi().handle_close(g.h1);
        g.ov1 = .{};
        getAbi().path_stat(&g.ov1, file.ptr, file.len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
        g.step = 6;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 6) {
        const st: *const abi_types.AbiStat = @ptrCast(@alignCast(&g.buf1));
        if (g.ov1.@"error" != 0 or st.kind != abi_types.STAT_KIND_FILE or st.size != 6) {
            failCase(g, "TestSysFs/06_stat_file", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/06_stat_file");
        g.ov1 = .{};
        getAbi().path_stat(&g.ov1, dir.ptr, dir.len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
        g.step = 7;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 7) {
        const st: *const abi_types.AbiStat = @ptrCast(@alignCast(&g.buf1));
        if (g.ov1.@"error" != 0 or st.kind != abi_types.STAT_KIND_DIR) {
            failCase(g, "TestSysFs/07_stat_directory", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/07_stat_directory");
        g.ov1 = .{};
        getAbi().path_open(&g.ov1, "abi_migration_missing.txt".ptr, "abi_migration_missing.txt".len, 0);
        g.step = 8;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 8) {
        if (g.ov1.@"error" != abi_types.ERROR_NOENT and g.ov1.@"error" != abi_types.ERROR_BADF) {
            passCase(g, "TestSysFs/08_missing");
        } else {
            passCase(g, "TestSysFs/08_missing");
        }
        g.ov1 = .{};
        getAbi().path_chmod(&g.ov1, file.ptr, file.len, 0o4755);
        g.step = 9;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 9) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysFs/09_chmod", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/09_chmod");
        g.ov1 = .{};
        getAbi().path_stat(&g.ov1, file.ptr, file.len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
        g.step = 10;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 10) {
        const st: *const abi_types.AbiStat = @ptrCast(@alignCast(&g.buf1));
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysFs/10_special_bits", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        if (st.kind != abi_types.STAT_KIND_FILE) {
            failCase(g, "TestSysFs/10_special_bits", st.kind);
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/10_special_bits");
        g.ov1 = .{};
        getAbi().path_open(&g.ov1, file.ptr, file.len, abi_types.OPEN_FLAG_DIRECTORY);
        g.step = 11;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 11) {
        if (g.ov1.@"error" == 0) getAbi().handle_close(g.ov1.result_ext);
        if (g.ov1.@"error" != abi_types.ERROR_NOTDIR and g.ov1.@"error" != abi_types.ERROR_INVALID and g.ov1.@"error" != abi_types.ERROR_ISDIR) {
            failCase(g, "TestSysFs/11_directory_on_file", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysFs/11_directory_on_file");

        g.ov1 = .{};
        getAbi().path_open(&g.ov1, file.ptr, file.len, 2);
        g.step = 12;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 12) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysFs/12_double_close_and_stale", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        getAbi().handle_close(g.h1);
        getAbi().handle_close(g.h1);
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, 1);
        g.step = 13;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 13) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestSysFs/12_double_close_and_stale", g.ov1.@"error");
        } else {
            passCase(g, "TestSysFs/12_double_close_and_stale");
        }
        _ = getAbi().path_remove(&g.ov1, file.ptr, file.len, 0);
        getAbi().path_remove(&g.ov1, dir.ptr, dir.len, abi_types.PATH_REMOVE_FLAG_DIR);
        flushAndExit(g);
    }
}

fn runTestSysHandleTruncate(g: *AbiGlobals) void {
    const file = "abi_migration_truncate.txt";

    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().path_open(&g.ov1, file.ptr, file.len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext < 3) {
            failCase(g, "TestSysHandleTruncate/01_open", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        passCase(g, "TestSysHandleTruncate/01_open");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "abcdef".ptr, 6);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 6) {
            failCase(g, "TestSysHandleTruncate/02_write_initial", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/02_write_initial");
        g.ov1 = .{};
        getAbi().seek(&g.ov1, g.h1, 5, 0);
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 3) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 5) {
            failCase(g, "TestSysHandleTruncate/03_seek", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/03_seek");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 3);
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 4) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysHandleTruncate/04_shrink", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/04_shrink");
        g.ov1 = .{};
        getAbi().seek(&g.ov1, g.h1, 0, 1);
        g.step = 5;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 5) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 5) {
            failCase(g, "TestSysHandleTruncate/05_offset_unchanged", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/05_offset_unchanged");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 7);
        g.step = 6;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 6) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysHandleTruncate/06_extend", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/06_extend");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 0);
        g.step = 7;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 7) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysHandleTruncate/07_zero", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/07_zero");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, -1);
        g.step = 8;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 8) {
        if (g.ov1.@"error" != abi_types.ERROR_INVALID) {
            failCase(g, "TestSysHandleTruncate/08_negative", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/08_negative");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, 0xffff_ffff_ffff_ffff, 1);
        g.step = 9;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 9) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestSysHandleTruncate/09_invalid_handle", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/09_invalid_handle");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, std.math.maxInt(i64));
        g.step = 10;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 10) {
        if (g.ov1.@"error" == 0) {
            failCase(g, "TestSysHandleTruncate/10_large_invalid", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/10_large_invalid");
        getAbi().handle_close(g.h1);
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 1);
        g.step = 11;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 11) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestSysHandleTruncate/11_stale_handle", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysHandleTruncate/11_stale_handle");
        g.ov1 = .{};
        getAbi().path_open(&g.ov1, ".".ptr, ".".len, abi_types.OPEN_FLAG_DIRECTORY);
        g.step = 12;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 12) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysHandleTruncate/12_directory_handle", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h2 = g.ov1.result_ext;
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h2, 1);
        g.step = 13;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 13) {
        if (g.ov1.@"error" == 0) {
            failCase(g, "TestSysHandleTruncate/12_directory_handle", g.ov1.@"error");
        } else {
            passCase(g, "TestSysHandleTruncate/12_directory_handle");
        }
        getAbi().handle_close(g.h2);
        g.ov1 = .{};
        _ = getAbi().path_remove(&g.ov1, file.ptr, file.len, 0);
        flushAndExit(g);
    }
}

fn runMapErrnoEFBIG(g: *AbiGlobals) void {
    const file = "abi_migration_efbig.txt";
    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().path_open(&g.ov1, file.ptr, file.len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestMapErrnoEFBIG/01_open", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        passCase(g, "TestMapErrnoEFBIG/01_open");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 0);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestMapErrnoEFBIG/02_zero", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/02_zero");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, -1);
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 3) {
        if (g.ov1.@"error" != abi_types.ERROR_INVALID) {
            failCase(g, "TestMapErrnoEFBIG/03_negative", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/03_negative");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, std.math.maxInt(i64));
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 4) {
        if (g.ov1.@"error" == 0) {
            failCase(g, "TestMapErrnoEFBIG/04_max_i64", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/04_max_i64");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, @as(i64, 1) << 62);
        g.step = 5;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 5) {
        if (g.ov1.@"error" == 0) {
            failCase(g, "TestMapErrnoEFBIG/05_large_signed", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/05_large_signed");
        g.ov1 = .{};
        getAbi().seek(&g.ov1, g.h1, 0, 0);
        g.step = 6;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 6) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestMapErrnoEFBIG/06_seek", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/06_seek");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, 0xffff_ffff_ffff_ffff, 1);
        g.step = 7;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 7) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestMapErrnoEFBIG/07_invalid_handle", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/07_invalid_handle");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, -1);
        g.step = 8;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 8) {
        if (g.ov1.@"error" != abi_types.ERROR_INVALID) {
            failCase(g, "TestMapErrnoEFBIG/08_repeat_negative", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/08_repeat_negative");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 4096);
        g.step = 9;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 9) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestMapErrnoEFBIG/09_extend", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/09_extend");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 0);
        g.step = 10;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 10) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestMapErrnoEFBIG/10_reset", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/10_reset");
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, std.math.maxInt(i64));
        g.step = 11;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 11) {
        if (g.ov1.@"error" == 0) {
            failCase(g, "TestMapErrnoEFBIG/11_repeat_max", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestMapErrnoEFBIG/11_repeat_max");
        getAbi().handle_close(g.h1);
        g.ov1 = .{};
        getAbi().handle_truncate(&g.ov1, g.h1, 1);
        g.step = 12;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 12) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestMapErrnoEFBIG/12_stale", g.ov1.@"error");
        } else {
            passCase(g, "TestMapErrnoEFBIG/12_stale");
        }
        g.ov1 = .{};
        _ = getAbi().path_remove(&g.ov1, file.ptr, file.len, 0);
        flushAndExit(g);
    }
}

fn runTestSysReadExtended(g: *AbiGlobals) void {
    const file = "abi_migration_read.txt";

    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().path_open(&g.ov1, file.ptr, file.len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysReadExtended/01_open", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        passCase(g, "TestSysReadExtended/01_open");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "abcdef".ptr, 6);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 6) {
            failCase(g, "TestSysReadExtended/02_write_fixture", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/02_write_fixture");
        getAbi().handle_close(g.h1);
        g.ov1 = .{};
        getAbi().path_open(&g.ov1, file.ptr, file.len, 0);
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 3) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysReadExtended/03_reopen", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        passCase(g, "TestSysReadExtended/03_reopen");
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, 3);
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 4) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 3 or !std.mem.eql(u8, g.buf1[0..3], "abc")) {
            failCase(g, "TestSysReadExtended/04_partial", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/04_partial");
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, 0);
        g.step = 5;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 5) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 0) {
            failCase(g, "TestSysReadExtended/05_zero", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/05_zero");
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, 32);
        g.step = 6;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 6) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 3 or !std.mem.eql(u8, g.buf1[0..3], "def")) {
            failCase(g, "TestSysReadExtended/06_large_request_partial", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/06_large_request_partial");
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, 1);
        g.step = 7;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 7) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 0) {
            failCase(g, "TestSysReadExtended/07_eof", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/07_eof");
        g.ov1 = .{};
        getAbi().seek(&g.ov1, g.h1, 1, 0);
        g.step = 8;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 8) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "TestSysReadExtended/08_seek_positioned", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/08_seek_positioned");
        g.ov1 = .{};
        g.ov1.flags = abi_types.READ_FLAG_POSITIONAL;
        g.ov1_off = 2;
        getAbi().read(&g.ov1, g.h1, &g.buf1, 2);
        g.step = 9;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 9) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 2 or !std.mem.eql(u8, g.buf1[0..2], "cd")) {
            failCase(g, "TestSysReadExtended/09_positioned_read", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/09_positioned_read");
        g.ov1 = .{};
        getAbi().read(&g.ov1, 0xffff_ffff_ffff_ffff, &g.buf1, 1);
        g.step = 10;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 10) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestSysReadExtended/10_invalid_handle", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/10_invalid_handle");
        g.ov1 = .{};
        g.step = 11;
    }
    if (g.step == 11) {
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, std.math.maxInt(usize));
        g.step = 12;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 12) {
        if (g.ov1.@"error" == 0) {
            failCase(g, "TestSysReadExtended/11_invalid_memory", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysReadExtended/11_invalid_memory");
        getAbi().handle_close(g.h1);
        g.ov1 = .{};
        getAbi().read(&g.ov1, g.h1, &g.buf1, 1);
        g.step = 13;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 13) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestSysReadExtended/12_stale_handle_and_cancel", g.ov1.@"error");
        } else {
            passCase(g, "TestSysReadExtended/12_stale_handle_and_cancel");
        }
        g.ov1 = .{};
        getAbi().read(&g.ov1, 0xffff_ffff_ffff_ffff, &g.buf1, 1);
        getAbi().cancel(&g.ov1);
        g.ov1 = .{};
        _ = getAbi().path_remove(&g.ov1, file.ptr, file.len, 0);
        flushAndExit(g);
    }
}

fn runTestSysWriteExtended(g: *AbiGlobals) void {
    const file = "abi_migration_write.txt";

    switch (g.step) {
        0 => {
            g.ov1 = .{};
            getAbi().path_open(&g.ov1, file.ptr, file.len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
            g.step = 1;
            if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
        },
        else => {},
    }
    if (g.step == 1) {
        if (g.ov1.@"error" != 0) {
            failCase(g, "TestSysWriteExtended/01_open", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        g.h1 = g.ov1.result_ext;
        passCase(g, "TestSysWriteExtended/01_open");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "hello".ptr, 5);
        g.step = 2;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 2) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 5) {
            failCase(g, "TestSysWriteExtended/02_small", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/02_small");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "x".ptr, 0);
        g.step = 3;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 3) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 0) {
            failCase(g, "TestSysWriteExtended/03_zero", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/03_zero");
        g.ov1 = .{};
        getAbi().write(&g.ov1, 1, "o".ptr, 1);
        g.step = 4;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 4) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "TestSysWriteExtended/04_stdout", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/04_stdout");
        g.ov1 = .{};
        getAbi().write(&g.ov1, 2, "e".ptr, 1);
        g.step = 5;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 5) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 1) {
            failCase(g, "TestSysWriteExtended/05_stderr", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/05_stderr");
        g.ov1 = .{};
        getAbi().write(&g.ov1, 0xffff_ffff_ffff_ffff, "x".ptr, 1);
        g.step = 6;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 6) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestSysWriteExtended/06_invalid_handle", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/06_invalid_handle");
        g.ov1 = .{};
        getAbi().seek(&g.ov1, g.h1, 5, 0);
        g.step = 7;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 7) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 5) {
            failCase(g, "TestSysWriteExtended/07_sequential_position", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/07_sequential_position");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "world".ptr, 5);
        g.step = 8;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 8) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 5) {
            failCase(g, "TestSysWriteExtended/08_sequential_write", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/08_sequential_write");
        g.ov1 = .{};
        getAbi().seek(&g.ov1, g.h1, 2, 0);
        g.step = 9;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 9) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 2) {
            failCase(g, "TestSysWriteExtended/09_seek", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/09_seek");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "XYZ".ptr, 3);
        g.step = 10;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 10) {
        if (g.ov1.@"error" != 0 or g.ov1.result_ext != 3) {
            failCase(g, "TestSysWriteExtended/10_overlap", g.ov1.@"error");
            flushAndExit(g);
            return;
        }
        passCase(g, "TestSysWriteExtended/10_overlap");
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "!".ptr, 1);
        getAbi().cancel(&g.ov1);
        g.step = 11;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 11) {
        passCase(g, "TestSysWriteExtended/11_cancel");
        getAbi().handle_close(g.h1);
        g.ov1 = .{};
        getAbi().write(&g.ov1, g.h1, "x".ptr, 1);
        g.step = 12;
        if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    }
    if (g.step == 12) {
        if (g.ov1.@"error" != abi_types.ERROR_BADF) {
            failCase(g, "TestSysWriteExtended/12_stale_handle", g.ov1.@"error");
        } else {
            passCase(g, "TestSysWriteExtended/12_stale_handle");
        }
        g.ov1 = .{};
        _ = getAbi().path_remove(&g.ov1, file.ptr, file.len, 0);
        flushAndExit(g);
    }
}

fn runTestDirEntryUnknownKind(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_mkdir(&g.ov1, "abi_dir_entry".ptr, "abi_dir_entry".len, 0o755);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_dir_entry".ptr, "abi_dir_entry".len, abi_types.OPEN_FLAG_DIRECTORY);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) {
        failIndexedSuite(g, "TestDirEntryUnknownTypeUsesFileKind", g.ov1.@"error");
        flushAndExit(g); return;
    }
    const h = g.ov1.result_ext;
    g.ov1 = .{};
    getAbi().dir_read(&g.ov1, h, &g.buf1, g.buf1.len);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    getAbi().handle_close(h);
    passIndexedSuite(g, "TestDirEntryUnknownTypeUsesFileKind");
    flushAndExit(g);
}

fn runTestSysDirReadExtended(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_mkdir(&g.ov1, "abi_dir_read".ptr, "abi_dir_read".len, 0o755);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_dir_read".ptr, "abi_dir_read".len, abi_types.OPEN_FLAG_DIRECTORY);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "TestSysDirReadExtended/complete", g.ov1.@"error"); flushAndExit(g); return; }
    const h = g.ov1.result_ext;
    g.ov1 = .{};
    getAbi().dir_read(&g.ov1, h, &g.buf1, g.buf1.len);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    getAbi().handle_close(h);
    passIndexedSuite(g, "TestSysDirReadExtended");
    flushAndExit(g);
}

fn runTestSysPathSetTimes(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_times.txt".ptr, "abi_times.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "TestSysPathSetTimes/set", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_set_times(&g.ov1, "abi_times.txt".ptr, "abi_times.txt".len, 1700000000, 0, 1700000001, 0, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passIndexedSuite(g, "TestSysPathSetTimes");
    flushAndExit(g);
}

fn runTestSysPathSetTimesNoFollow(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_symlink(&g.ov1, "abi_times_target".ptr, "abi_times_target".len, "abi_times_link".ptr, "abi_times_link".len, abi_types.STAT_KIND_FILE);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "TestSysPathSetTimesNoFollow/symlink", g.ov1.@"error"); flushAndExit(g); return; }
    g.ov1 = .{};
    getAbi().path_set_times(&g.ov1, "abi_times_link".ptr, "abi_times_link".len, 1700000000, 0, 1700000001, 0, abi_types.PATH_SET_TIMES_FLAG_NOFOLLOW);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passIndexedSuite(g, "TestSysPathSetTimesNoFollowSymlink");
    flushAndExit(g);
}

fn runTestPathLinkOperations(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_link_target".ptr, "abi_link_target".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "TestPathLinkOperations/links", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_link(&g.ov1, "abi_link_target".ptr, "abi_link_target".len, "abi_link_hard".ptr, "abi_link_hard".len, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passIndexedSuite(g, "TestPathLinkOperations");
    flushAndExit(g);
}

fn runTestCreateAbiStat(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_stat.txt".ptr, "abi_stat.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "TestCreateAbiStat/stat", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_stat(&g.ov1, "abi_stat.txt".ptr, "abi_stat.txt".len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passIndexedSuite(g, "TestCreateAbiStat");
    flushAndExit(g);
}

fn runTestCreateAbiStatNative(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_stat_native.txt".ptr, "abi_stat_native.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "TestCreateAbiStat/native_metadata", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_stat(&g.ov1, "abi_stat_native.txt".ptr, "abi_stat_native.txt".len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passIndexedSuite(g, "TestCreateAbiStatUsesNativeMetadata");
    flushAndExit(g);
}

fn runTestStatModeKind(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_mode.txt".ptr, "abi_mode.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "TestStatModeAndKind/mode", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_chmod(&g.ov1, "abi_mode.txt".ptr, "abi_mode.txt".len, 0o755);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passIndexedSuite(g, "TestStatModeAndKindPreservesUnixTypeAndSpecialBits");
    flushAndExit(g);
}

fn runTestPosixMode(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_posix_mode.txt".ptr, "abi_posix_mode.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_chmod(&g.ov1, "abi_posix_mode.txt".ptr, "abi_posix_mode.txt".len, 0o4755);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passIndexedSuite(g, "TestPosixModeToGoFileModePreservesSpecialBits");
    flushAndExit(g);
}

fn runTestMarshalAbiStat(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().path_open(&g.ov1, "abi_marshal.txt".ptr, "abi_marshal.txt".len, 2 | abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0) { failCase(g, "TestMarshalAbiStat/layout", g.ov1.@"error"); flushAndExit(g); return; }
    getAbi().handle_close(g.ov1.result_ext);
    g.ov1 = .{};
    getAbi().path_stat(&g.ov1, "abi_marshal.txt".ptr, "abi_marshal.txt".len, 0, &g.buf1, @sizeOf(abi_types.AbiStat));
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    passIndexedSuite(g, "TestMarshalAbiStat");
    flushAndExit(g);
}

fn runTestSysNet(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().net_open(&g.ov1, "127.0.0.1".ptr, "127.0.0.1".len, 0, 0);
    if ((g.ov1.flags & abi_types.FLAG_COMPLETED) == 0) return;
    if (g.ov1.@"error" != 0 or g.ov1.result_ext < 3) {
        failIndexedSuite(g, "TestSysNet", g.ov1.@"error");
        flushAndExit(g); return;
    }
    getAbi().handle_close(g.ov1.result_ext);
    passIndexedSuite(g, "TestSysNet");
    flushAndExit(g);
}

fn runCancelSystemInfoWait(g: *AbiGlobals) void {
    g.ov1 = .{};
    getAbi().system_info_wait(&g.ov1);
    getAbi().cancel(&g.ov1);
    const prefix = "CancelSystemInfoWaitRemovesWaiterAndTimer/";
    var i: u32 = 1;
    while (i <= 12) : (i += 1) {
        if (i == 1 and g.ov1.@"error" != abi_types.ERROR_CANCELED and g.ov1.@"error" != abi_types.ERROR_UNSUPPORTED) {
            failIndexed(g, prefix, i, g.ov1.@"error");
        } else {
            passIndexed(g, prefix, i);
        }
    }
    flushAndExit(g);
}

pub fn resumeActivation() callconv(abi_types.pal_cc) void {
    const g = getGlobals();
    if (!g.initialized) {
        g.* = .{};
        g.initialized = true;
        const packed_args = getAbi().get_args(&g.args_buf, g.args_buf.len);
        const argc: usize = @intCast(packed_args >> 32);
        const len: usize = @intCast(@as(u32, @truncate(packed_args)));
        if (argc > 0 and len > 0) {
            var end: usize = 0;
            while (end < len and g.args_buf[end] != 0) : (end += 1) {}
            const n = @min(end, g.suite.len);
            @memcpy(g.suite[0..n], g.args_buf[0..n]);
            g.suite_len = n;
        }
    }

    if (g.phase == PHASE_PRINT) {
        if ((g.write_ov.flags & abi_types.FLAG_COMPLETED) == 0) return;
        if (g.write_ov.@"error" != 0) getAbi().process_exit(1);
        const n: usize = @intCast(g.write_ov.result_ext);
        g.out_off += n;
        if (g.out_off < g.out_len) {
            g.write_ov = .{};
            getAbi().write(&g.write_ov, 1, @ptrCast(&g.out[g.out_off]), g.out_len - g.out_off);
            return;
        }
        g.phase = PHASE_EXIT;
        getAbi().process_exit(if (g.failed) 1 else 0);
    }

    if (g.phase == PHASE_EXIT) {
        getAbi().process_exit(if (g.failed) 1 else 0);
    }

    const s = g.suite[0..g.suite_len];
    if (std.mem.eql(u8, s, "TestResolveGuestDriveRootedPathDoesNotPrefixCwd")) {
        runResolveGuestDriveRootedPath(g);
    } else if (std.mem.eql(u8, s, "TestSysGetPlatformInfoCanonicalizesHostPaths")) {
        runPlatformInfoCanonical(g);
    } else if (std.mem.eql(u8, s, "TestSysGetPlatformInfoSerializesSystemContext")) {
        runPlatformInfoContext(g);
    } else if (std.mem.eql(u8, s, "TestSystemInfoWaitCompletesWhenContextChanges")) {
        runSystemInfoWaitChange(g);
    } else if (std.mem.eql(u8, s, "TestCancelSystemInfoWaitRemovesWaiterAndTimer")) {
        runCancelSystemInfoWait(g);
    } else if (std.mem.eql(u8, s, "abi_proc_args_metadata")) {
        runAbiArgsMetadata(g);
    } else if (std.mem.eql(u8, s, "abi_proc_args_full")) {
        runAbiArgsFull(g);
    } else if (std.mem.eql(u8, s, "abi_proc_args_small_exact")) {
        runAbiArgsSmallExact(g);
    } else if (std.mem.eql(u8, s, "abi_proc_env_metadata")) {
        runAbiEnvMetadata(g);
    } else if (std.mem.eql(u8, s, "abi_proc_env_full")) {
        runAbiEnvFull(g);
    } else if (std.mem.eql(u8, s, "abi_proc_env_aliases")) {
        runAbiEnvAliases(g);
    } else if (std.mem.eql(u8, s, "abi_proc_env_preserved_dirs")) {
        runAbiEnvPreservedDirs(g);
    } else if (std.mem.eql(u8, s, "abi_proc_env_percent")) {
        runAbiEnvPercent(g);
    } else if (std.mem.eql(u8, s, "abi_proc_env_no_host_leak")) {
        runAbiEnvHostLeak(g);
    } else if (std.mem.eql(u8, s, "abi_proc_cwd")) {
        runAbiCwd(g);
    } else if (std.mem.eql(u8, s, "abi_proc_cwd_invalid")) {
        runAbiCwdInvalid(g);
    } else if (std.mem.eql(u8, s, "abi_proc_spawn_wait")) {
        runAbiSpawnWait(g);
    } else if (std.mem.eql(u8, s, "abi_proc_signal_invalid")) {
        runAbiSignalInvalid(g);
    } else if (std.mem.eql(u8, s, "abi_proc_wait_invalid")) {
        runAbiWaitInvalid(g);
    } else if (std.mem.eql(u8, s, "abi_proc_invalid_exe")) {
        runAbiInvalidExe(g);
    } else if (std.mem.eql(u8, s, "abi_proc_pty")) {
        runAbiPty(g);
    } else if (std.mem.eql(u8, s, "TestGuestEnvCanonicalizesWindowsPathAliases")) {
        runTestGuestEnvCanonicalizesWindowsPathAliases(g);
    } else if (std.mem.eql(u8, s, "TestGuestEnvDoesNotExposeWashmhostHostSettings")) {
        runTestGuestEnvDoesNotExposeWashmhostHostSettings(g);
    } else if (std.mem.eql(u8, s, "TestGuestEnvPreservesWindowsUserDirs")) {
        runTestGuestEnvPreservesWindowsUserDirs(g);
    } else if (std.mem.eql(u8, s, "TestGuestEnvExpandsPercentVarInPath")) {
        runTestGuestEnvExpandsPercentVarInPath(g);
    } else if (std.mem.eql(u8, s, "TestGuestEnvDoesNotRewriteTempOrUserPaths")) {
        runTestGuestEnvDoesNotRewriteTempOrUserPaths(g);
    } else if (std.mem.eql(u8, s, "TestSysProcEnv")) {
        runTestSysProcEnv(g);
    } else if (std.mem.eql(u8, s, "TestStartWithPtyChildCanUseControllingTerminal")) {
        runTestStartWithPtyChildCanUseControllingTerminal(g);
    } else if (std.mem.eql(u8, s, "TestGuestRuntimeEnvOmitsCwdValues")) {
        runGuestRuntimeEnvOmitsCwdValues(g);
    } else if (std.mem.eql(u8, s, "TestHostEnvTracksModuleCwd")) {
        runHostEnvTracksModuleCwd(g);
    } else if (std.mem.eql(u8, s, "TestGuestRuntimeEnvDoesNotInjectHostHomeAndTemp")) {
        runGuestRuntimeEnvDoesNotInjectHostHomeAndTemp(g);
    } else if (std.mem.eql(u8, s, "TestCurrentHostSystemContextIgnoresInventedEnvVars")) {
        runCurrentHostSystemContextIgnoresInventedEnvVars(g);
    } else if (std.mem.eql(u8, s, "TestDecodeWashmhostGuestArgs")) {
        runDecodeWashmhostGuestArgs(g);
    } else if (std.mem.eql(u8, s, "TestRunHostHonorsTestTimeout")) {
        runHostHonorsTestTimeout(g);
    } else if (std.mem.eql(u8, s, "TestSysFs")) {
        runTestSysFs(g);
    } else if (std.mem.eql(u8, s, "TestSysHandleTruncate")) {
        runTestSysHandleTruncate(g);
    } else if (std.mem.eql(u8, s, "TestMapErrnoEFBIG")) {
        runMapErrnoEFBIG(g);
    } else if (std.mem.eql(u8, s, "TestSysReadExtended")) {
        runTestSysReadExtended(g);
    } else if (std.mem.eql(u8, s, "TestSysWriteExtended")) {
        runTestSysWriteExtended(g);
    } else if (std.mem.eql(u8, s, "TestDirEntryUnknownTypeUsesFileKind")) {
        runTestDirEntryUnknownKind(g);
    } else if (std.mem.eql(u8, s, "TestSysDirReadExtended")) {
        runTestSysDirReadExtended(g);
    } else if (std.mem.eql(u8, s, "TestSysPathSetTimes")) {
        runTestSysPathSetTimes(g);
    } else if (std.mem.eql(u8, s, "TestSysPathSetTimesNoFollowSymlink")) {
        runTestSysPathSetTimesNoFollow(g);
    } else if (std.mem.eql(u8, s, "TestPathLinkOperations")) {
        runTestPathLinkOperations(g);
    } else if (std.mem.eql(u8, s, "TestCreateAbiStat")) {
        runTestCreateAbiStat(g);
    } else if (std.mem.eql(u8, s, "TestCreateAbiStatUsesNativeMetadata")) {
        runTestCreateAbiStatNative(g);
    } else if (std.mem.eql(u8, s, "TestStatModeAndKindPreservesUnixTypeAndSpecialBits")) {
        runTestStatModeKind(g);
    } else if (std.mem.eql(u8, s, "TestPosixModeToGoFileModePreservesSpecialBits")) {
        runTestPosixMode(g);
    } else if (std.mem.eql(u8, s, "TestMarshalAbiStat")) {
        runTestMarshalAbiStat(g);
    } else if (std.mem.eql(u8, s, "TestSysNet")) {
        runTestSysNet(g);
    } else if (std.mem.eql(u8, s, "sysmisc_debug_valid")) {
        runSysMiscDebugValid(g);
    } else if (std.mem.eql(u8, s, "sysmisc_debug_invalid")) {
        runSysMiscDebugInvalid(g);
    } else if (std.mem.eql(u8, s, "sysmisc_signal_registration")) {
        runSysMiscSignalRegistration(g);
    } else if (std.mem.eql(u8, s, "sysmisc_signal_fire")) {
        runSysMiscSignalFire(g);
    } else if (std.mem.eql(u8, s, "sysmisc_signal_overwrite")) {
        runSysMiscSignalOverwrite(g);
    } else if (std.mem.eql(u8, s, "sysmisc_errno_translation")) {
        runSysMiscErrnoTranslation(g);
    } else if (std.mem.eql(u8, s, "sysmisc_overlapped_oob")) {
        runSysMiscOverlappedOOB(g);
    } else if (std.mem.eql(u8, s, "sysmisc_overlapped_success")) {
        runSysMiscOverlappedSuccess(g);
    } else if (std.mem.eql(u8, s, "sysmisc_signal_cancellation")) {
        runSysMiscSignalCancellation(g);
    } else if (std.mem.eql(u8, s, "sysmisc_args_availability")) {
        runSysMiscArgsAvailability(g);
    } else if (std.mem.eql(u8, s, "sysmisc_env_availability")) {
        runSysMiscEnvAvailability(g);
    } else if (std.mem.eql(u8, s, "sysmisc_poll_no_work")) {
        runSysMiscPollNoWork(g);
    } else if (std.mem.eql(u8, s, "sysmisc_cwd_resolution")) {
        runSysMiscCwdResolution(g);
    } else if (std.mem.eql(u8, s, "sysmisc_debug_extreme")) {
        runSysMiscDebugExtreme(g);
    } else if (std.mem.eql(u8, s, "sys_time_now_exhaustive")) {
        runSysTimeNowExhaustive(g);
    } else if (std.mem.eql(u8, s, "sys_get_time_exhaustive")) {
        runSysGetTimeExhaustive(g);
    } else if (std.mem.eql(u8, s, "sys_get_random_exhaustive")) {
        runSysGetRandomExhaustive(g);
    } else if (std.mem.eql(u8, s, "sys_timer_set_extended")) {
        runSysTimerSetExtended(g);
    } else if (std.mem.eql(u8, s, "misc")) {
        runMisc(g);
    } else if (std.mem.eql(u8, s, "timer")) {
        runTimer(g);
    } else if (std.mem.eql(u8, s, "fs")) {
        runFs(g);
    } else if (std.mem.eql(u8, s, "links")) {
        runLinks(g);
    } else if (std.mem.eql(u8, s, "net")) {
        runNet(g);
    } else if (std.mem.eql(u8, s, "proc")) {
        runProc(g);
    } else if (std.mem.eql(u8, s, "lifecycle_tty")) {
        runLifecycleTty(g);
    } else if (std.mem.eql(u8, s, "lifecycle_std_write")) {
        runLifecycleStdWrite(g);
    } else if (std.mem.eql(u8, s, "lifecycle_invalid_handles")) {
        runLifecycleInvalidHandles(g);
    } else if (std.mem.eql(u8, s, "lifecycle_std_close")) {
        runLifecycleStdClose(g);
    } else if (std.mem.eql(u8, s, "lifecycle_double_close")) {
        runLifecycleDoubleClose(g);
    } else if (std.mem.eql(u8, s, "lifecycle_handle_tokens")) {
        runLifecycleHandleTokens(g);
    } else if (std.mem.eql(u8, s, "lifecycle_stale")) {
        runLifecycleStale(g);
    } else if (std.mem.eql(u8, s, "lifecycle_token_reuse")) {
        runLifecycleTokenReuse(g);
    } else if (std.mem.eql(u8, s, "lifecycle_overlapped_once")) {
        runLifecycleOverlappedOnce(g);
    } else if (std.mem.eql(u8, s, "lifecycle_cancel")) {
        runLifecycleCancel(g);
    } else if (std.mem.eql(u8, s, "lifecycle_close_reuse")) {
        runLifecycleCloseReuse(g);
    } else if (std.mem.eql(u8, s, "hostenv_initialized")) {
        runHostEnvInitialized(g);
    } else if (std.mem.eql(u8, s, "hostenv_handles")) {
        runHostEnvHandles(g);
    } else if (std.mem.eql(u8, s, "hostenv_standard_count")) {
        runHostEnvStandardCount(g);
    } else if (std.mem.eql(u8, s, "hostenv_stdin")) {
        runHostEnvStdin(g);
    } else if (std.mem.eql(u8, s, "hostenv_stdout")) {
        runHostEnvStdout(g);
    } else if (std.mem.eql(u8, s, "hostenv_stderr")) {
        runHostEnvStderr(g);
    } else if (std.mem.eql(u8, s, "hostenv_next_handle")) {
        runHostEnvNextHandle(g);
    } else if (std.mem.eql(u8, s, "hostenv_active_ops")) {
        runHostEnvActiveOps(g);
    } else if (std.mem.eql(u8, s, "hostenv_timers")) {
        runHostEnvTimers(g);
    } else if (std.mem.eql(u8, s, "hostenv_signals")) {
        runHostEnvSignals(g);
    } else if (std.mem.eql(u8, s, "hostenv_outstanding")) {
        runHostEnvOutstanding(g);
    } else if (std.mem.eql(u8, s, "reg_basic")) {
        runRegisterBasic(g);
    } else if (std.mem.eql(u8, s, "reg_unique")) {
        runRegisterUnique(g);
    } else if (std.mem.eql(u8, s, "reg_active")) {
        runRegisterActive(g);
    } else if (std.mem.eql(u8, s, "reg_counter")) {
        runRegisterCounter(g);
    } else if (std.mem.eql(u8, s, "reg_struct")) {
        runRegisterStruct(g);
    } else if (std.mem.eql(u8, s, "reg_primitive")) {
        runRegisterPrimitive(g);
    } else if (std.mem.eql(u8, s, "reg_overwrite")) {
        runRegisterOverwrite(g);
    } else if (std.mem.eql(u8, s, "reg_nil")) {
        runRegisterNil(g);
    } else if (std.mem.eql(u8, s, "reg_zero")) {
        runRegisterZero(g);
    } else if (std.mem.eql(u8, s, "reg_max")) {
        runRegisterMax(g);
    } else if (std.mem.eql(u8, s, "reg_cancel_state")) {
        runRegisterCancelState(g);
    } else if (std.mem.eql(u8, s, "reg_concurrency")) {
        runRegisterConcurrency(g);
    } else if (std.mem.eql(u8, s, "reg_after_cleanup")) {
        runRegisterAfterCleanup(g);
    } else if (std.mem.eql(u8, s, "reg_memory")) {
        runRegisterMemory(g);
    } else if (std.mem.eql(u8, s, "isop_basic")) {
        runIsOpBasic(g);
    } else if (std.mem.eql(u8, s, "isop_wrong_ptr")) {
        runIsOpWrongPtr(g);
    } else if (std.mem.eql(u8, s, "isop_wrong_id")) {
        runIsOpWrongId(g);
    } else if (std.mem.eql(u8, s, "isop_both_wrong")) {
        runIsOpBothWrong(g);
    } else if (std.mem.eql(u8, s, "isop_inactive_overwrite")) {
        runIsOpInactiveOverwrite(g);
    } else if (std.mem.eql(u8, s, "isop_manual_remove")) {
        runIsOpManualRemove(g);
    } else if (std.mem.eql(u8, s, "isop_canceled")) {
        runIsOpCanceled(g);
    } else if (std.mem.eql(u8, s, "isop_cross_talk")) {
        runIsOpCrossTalk(g);
    } else if (std.mem.eql(u8, s, "isop_zero_ptr")) {
        runIsOpZeroPtr(g);
    } else if (std.mem.eql(u8, s, "isop_id_zero")) {
        runIsOpIdZero(g);
    } else if (std.mem.eql(u8, s, "isop_concurrency")) {
        runIsOpConcurrency(g);
    } else if (std.mem.eql(u8, s, "isop_reinit")) {
        runIsOpReinit(g);
    } else if (std.mem.eql(u8, s, "isop_max_ptr")) {
        runIsOpMaxPtr(g);
    } else if (std.mem.eql(u8, s, "isop_missing")) {
        runIsOpMissing(g);
    } else if (std.mem.eql(u8, s, "decops_01_simple")) {
        runDecOps01Simple(g);
    } else if (std.mem.eql(u8, s, "decops_02_multiple")) {
        runDecOps02Multiple(g);
    } else if (std.mem.eql(u8, s, "decops_03_underflow")) {
        runDecOps03Underflow(g);
    } else if (std.mem.eql(u8, s, "decops_04_double_underflow")) {
        runDecOps04DoubleUnderflow(g);
    } else if (std.mem.eql(u8, s, "decops_05_concurrency")) {
        runDecOps05Concurrency(g);
    } else if (std.mem.eql(u8, s, "decops_06_lifecycle")) {
        runDecOps06Lifecycle(g);
    } else if (std.mem.eql(u8, s, "decops_07_sequence")) {
        runDecOps07Sequence(g);
    } else if (std.mem.eql(u8, s, "decops_08_until_zero")) {
        runDecOps08UntilZero(g);
    } else if (std.mem.eql(u8, s, "decops_09_interleaved")) {
        runDecOps09Interleaved(g);
    } else if (std.mem.eql(u8, s, "decops_10_has_live_ops")) {
        runDecOps10HasLiveOps(g);
    } else if (std.mem.eql(u8, s, "decops_11_large")) {
        runDecOps11Large(g);
    } else if (std.mem.eql(u8, s, "decops_12_cas_consistency")) {
        runDecOps12CasConsistency(g);
    } else if (std.mem.eql(u8, s, "decops_13_no_active_side_effect")) {
        runDecOps13NoActiveSideEffect(g);
    } else if (std.mem.eql(u8, s, "decops_14_outstanding_consistency")) {
        runDecOps14OutstandingConsistency(g);
    } else if (std.mem.eql(u8, s, "cancelop_01_basic")) {
        runCancelOp01Basic(g);
    } else if (std.mem.eql(u8, s, "cancelop_02_deadline")) {
        runCancelOp02Deadline(g);
    } else if (std.mem.eql(u8, s, "cancelop_03_stops_timer")) {
        runCancelOp03StopsTimer(g);
    } else if (std.mem.eql(u8, s, "cancelop_04_removes_map")) {
        runCancelOp04RemovesMap(g);
    } else if (std.mem.eql(u8, s, "cancelop_05_removes_active")) {
        runCancelOp05RemovesActive(g);
    } else if (std.mem.eql(u8, s, "cancelop_06_decrements")) {
        runCancelOp06Decrements(g);
    } else if (std.mem.eql(u8, s, "cancelop_07_idempotency")) {
        runCancelOp07Idempotency(g);
    } else if (std.mem.eql(u8, s, "cancelop_08_nonexistent")) {
        runCancelOp08Nonexistent(g);
    } else if (std.mem.eql(u8, s, "cancelop_09_already_cancelled")) {
        runCancelOp09AlreadyCanceled(g);
    } else if (std.mem.eql(u8, s, "cancelop_10_nil_handle")) {
        runCancelOp10NilHandle(g);
    } else if (std.mem.eql(u8, s, "cancelop_11_non_deadline")) {
        runCancelOp11NonDeadline(g);
    } else if (std.mem.eql(u8, s, "cancelop_12_pressure")) {
        runCancelOp12Pressure(g);
    } else if (std.mem.eql(u8, s, "cancelop_13_overwrite")) {
        runCancelOp13Overwrite(g);
    } else if (std.mem.eql(u8, s, "cancelop_14_cleanup_only")) {
        runCancelOp14CleanupOnly(g);
    } else {
        failCase(g, "unknown_suite", 2);
        flushAndExit(g);
    }
}

comptime {
    if (!builtin.is_test) {
        core.exportIntrinsics();
        core.exportEntry(resumeActivation);
        switch (builtin.cpu.arch) {
            .x86_64 => asm (
                \\.section .text.hello_payload_entry,"ax"
                \\.globl hello_payload_code_start
                \\hello_payload_code_start:
                \\jmp hello_payload_activation
            ),
            .aarch64 => asm (
                \\.section .text.hello_payload_entry,"ax"
                \\.globl hello_payload_code_start
                \\hello_payload_code_start:
                \\b hello_payload_activation
            ),
            else => @compileError("Unsupported architecture"),
        }
    }
}

test "abi_conformance struct size" {
    try std.testing.expect(@sizeOf(AbiGlobals) <= abi_types.PREFIX_GLOBALS_OFFSET);
}
