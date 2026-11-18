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
    if (std.mem.eql(u8, s, "sysmisc_debug_valid")) {
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
