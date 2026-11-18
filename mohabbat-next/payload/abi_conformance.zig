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
    if (std.mem.eql(u8, s, "misc")) {
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
