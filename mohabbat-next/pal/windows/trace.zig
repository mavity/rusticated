const std = @import("std");
const win = @import("win_ffi.zig");
const mem = @import("mem.zig");

const PalState = mem.PalState;

pub const SLAB_BYTES: usize = 50 * 1024 * 1024;
pub const RECORD_SIZE: usize = @sizeOf(TraceRecord);

pub const AbiId = enum(u16) {
    alloc_mem = 1,
    protect_mem = 2,
    free_mem = 3,
    flush_icache = 4,
    host_panic = 5,
    rusticated_debug = 6,
    process_exit = 7,
    get_time = 8,
    get_random = 9,
    get_args = 10,
    get_env = 11,
    get_cwd = 12,
    set_cwd = 13,
    get_platform_info = 14,
    timer_set = 15,
    cancel = 16,
    read = 17,
    write = 18,
    seek = 19,
    handle_truncate = 20,
    handle_close = 21,
    path_open = 22,
    dir_read = 23,
    path_stat = 24,
    path_link = 25,
    path_symlink = 26,
    path_readlink = 27,
    path_chmod = 28,
    path_set_times = 29,
    path_remove = 30,
    path_mkdir = 31,
    path_rename = 32,
    net_open = 33,
    net_accept = 34,
    net_lookup = 35,
    net_cert_verify = 36,
    process_spawn = 37,
    process_pipe = 38,
    process_wait = 39,
    process_signal = 40,
    signal_wait = 41,
    tty_set_mode = 42,
    tty_get_size = 43,
    tty_set_size = 44,
    fd_isatty = 45,
    system_info_wait = 46,
};

pub const EventKind = enum(u16) {
    init = 1,
    loop_entry_enter = 2,
    loop_entry_exit = 3,
    loop_timer_scan = 4,
    loop_timer_fire = 5,
    loop_iocp_enter = 6,
    loop_iocp_exit = 7,
    loop_iocp_item = 8,
    abi_enter = 9,
    abi_exit = 10,
    worker_read_start = 24,
    worker_read_done = 25,
    worker_write_start = 26,
    worker_write_done = 27,
    process_exit = 28,
    host_panic = 29,
};

pub const TraceRecord = extern struct {
    qpc: u64,
    thread_id: u32,
    kind: u16,
    flags: u16,
    arg0: u64,
    arg1: u64,
};

comptime {
    if (@sizeOf(TraceRecord) != 32) {
        @compileError("TraceRecord must be 32 bytes");
    }
}

fn abiName(id: u16) []const u8 {
    return switch (id) {
        @intFromEnum(AbiId.alloc_mem) => "alloc_mem",
        @intFromEnum(AbiId.protect_mem) => "protect_mem",
        @intFromEnum(AbiId.free_mem) => "free_mem",
        @intFromEnum(AbiId.flush_icache) => "flush_icache",
        @intFromEnum(AbiId.host_panic) => "host_panic",
        @intFromEnum(AbiId.rusticated_debug) => "rusticated_debug",
        @intFromEnum(AbiId.process_exit) => "process_exit",
        @intFromEnum(AbiId.get_time) => "get_time",
        @intFromEnum(AbiId.get_random) => "get_random",
        @intFromEnum(AbiId.get_args) => "get_args",
        @intFromEnum(AbiId.get_env) => "get_env",
        @intFromEnum(AbiId.get_cwd) => "get_cwd",
        @intFromEnum(AbiId.set_cwd) => "set_cwd",
        @intFromEnum(AbiId.get_platform_info) => "get_platform_info",
        @intFromEnum(AbiId.timer_set) => "timer_set",
        @intFromEnum(AbiId.cancel) => "cancel",
        @intFromEnum(AbiId.read) => "read",
        @intFromEnum(AbiId.write) => "write",
        @intFromEnum(AbiId.seek) => "seek",
        @intFromEnum(AbiId.handle_truncate) => "handle_truncate",
        @intFromEnum(AbiId.handle_close) => "handle_close",
        @intFromEnum(AbiId.path_open) => "path_open",
        @intFromEnum(AbiId.dir_read) => "dir_read",
        @intFromEnum(AbiId.path_stat) => "path_stat",
        @intFromEnum(AbiId.path_link) => "path_link",
        @intFromEnum(AbiId.path_symlink) => "path_symlink",
        @intFromEnum(AbiId.path_readlink) => "path_readlink",
        @intFromEnum(AbiId.path_chmod) => "path_chmod",
        @intFromEnum(AbiId.path_set_times) => "path_set_times",
        @intFromEnum(AbiId.path_remove) => "path_remove",
        @intFromEnum(AbiId.path_mkdir) => "path_mkdir",
        @intFromEnum(AbiId.path_rename) => "path_rename",
        @intFromEnum(AbiId.net_open) => "net_open",
        @intFromEnum(AbiId.net_accept) => "net_accept",
        @intFromEnum(AbiId.net_lookup) => "net_lookup",
        @intFromEnum(AbiId.net_cert_verify) => "net_cert_verify",
        @intFromEnum(AbiId.process_spawn) => "process_spawn",
        @intFromEnum(AbiId.process_pipe) => "process_pipe",
        @intFromEnum(AbiId.process_wait) => "process_wait",
        @intFromEnum(AbiId.process_signal) => "process_signal",
        @intFromEnum(AbiId.signal_wait) => "signal_wait",
        @intFromEnum(AbiId.tty_set_mode) => "tty_set_mode",
        @intFromEnum(AbiId.tty_get_size) => "tty_get_size",
        @intFromEnum(AbiId.tty_set_size) => "tty_set_size",
        @intFromEnum(AbiId.fd_isatty) => "fd_isatty",
        @intFromEnum(AbiId.system_info_wait) => "system_info_wait",
        else => "unknown",
    };
}

fn kindName(kind: u16) []const u8 {
    return switch (kind) {
        @intFromEnum(EventKind.init) => "init",
        @intFromEnum(EventKind.loop_entry_enter) => "loop_entry_enter",
        @intFromEnum(EventKind.loop_entry_exit) => "loop_entry_exit",
        @intFromEnum(EventKind.loop_timer_scan) => "loop_timer_scan",
        @intFromEnum(EventKind.loop_timer_fire) => "loop_timer_fire",
        @intFromEnum(EventKind.loop_iocp_enter) => "loop_iocp_enter",
        @intFromEnum(EventKind.loop_iocp_exit) => "loop_iocp_exit",
        @intFromEnum(EventKind.loop_iocp_item) => "loop_iocp_item",
        @intFromEnum(EventKind.abi_enter) => "abi_enter",
        @intFromEnum(EventKind.abi_exit) => "abi_exit",
        @intFromEnum(EventKind.worker_read_start) => "worker_read_start",
        @intFromEnum(EventKind.worker_read_done) => "worker_read_done",
        @intFromEnum(EventKind.worker_write_start) => "worker_write_start",
        @intFromEnum(EventKind.worker_write_done) => "worker_write_done",
        @intFromEnum(EventKind.process_exit) => "process_exit",
        @intFromEnum(EventKind.host_panic) => "host_panic",
        else => "unknown",
    };
}

pub fn init(s: *PalState) void {
    s.p_qpc = @ptrCast(@alignCast(mem.resolveKernel32(s, "QueryPerformanceCounter") orelse return));
    s.p_qpf = @ptrCast(@alignCast(mem.resolveKernel32(s, "QueryPerformanceFrequency") orelse return));
    s.p_get_current_thread_id = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetCurrentThreadId") orelse return));

    var freq: u64 = 0;
    if (s.p_qpf.?( &freq) == 0 or freq == 0) return;

    const va = s.p_virtual_alloc orelse return;
    const buf = va(null, SLAB_BYTES, 0x3000, 0x04) orelse return;
    s.trace_buf = buf;
    s.trace_cap = @intCast(SLAB_BYTES / RECORD_SIZE);
    s.trace_count = 0;
    s.trace_stopped = 0;
    s.trace_dropped = 0;
    s.trace_qpc_freq = freq;

    var start_qpc: u64 = 0;
    _ = s.p_qpc.?(&start_qpc);
    s.trace_qpc_start = start_qpc;

    record(s, .init, 0, freq, SLAB_BYTES);
}

pub fn record(s: *PalState, kind: EventKind, flags: u16, arg0: u64, arg1: u64) void {
    if (@atomicLoad(u32, &s.trace_stopped, .acquire) != 0) return;
    const buf = s.trace_buf orelse return;
    const cap = s.trace_cap;
    if (cap == 0) return;

    const idx = @atomicRmw(u32, &s.trace_count, .Add, 1, .monotonic);
    if (idx >= cap) {
        _ = @atomicRmw(u32, &s.trace_dropped, .Add, 1, .monotonic);
        return;
    }

    var qpc: u64 = 0;
    if (s.p_qpc) |qpc_fn| {
        _ = qpc_fn(&qpc);
    }
    const tid: u32 = if (s.p_get_current_thread_id) |tid_fn| tid_fn() else 0;

    const records: [*]TraceRecord = @ptrCast(@alignCast(buf));
    records[idx] = .{
        .qpc = qpc,
        .thread_id = tid,
        .kind = @intFromEnum(kind),
        .flags = flags,
        .arg0 = arg0,
        .arg1 = arg1,
    };
}

fn writeChunk(s: *PalState, h: win.HANDLE, data: []const u8) void {
    const wf = s.p_write_file orelse return;
    var off: usize = 0;
    while (off < data.len) {
        var written: u32 = 0;
        const n: u32 = @intCast(@min(data.len - off, 0x7fff_ffff));
        if (wf(h, data.ptr + off, n, &written, null) == 0 or written == 0) return;
        off += written;
    }
}

pub fn dump(s: *PalState) void {
    if (@atomicRmw(u32, &s.trace_stopped, .Xchg, 1, .acq_rel) != 0) return;
    const buf = s.trace_buf orelse return;
    const create_file = s.p_create_file_w orelse return;
    const close_handle = s.p_close_handle orelse return;

    const trace_path = std.unicode.utf8ToUtf16LeStringLiteral("kabibi-pal-trace.tsv");
    // GENERIC_WRITE = 0x40000000, FILE_SHARE_READ = 1, CREATE_ALWAYS = 2, FILE_ATTRIBUTE_NORMAL = 0x80
    const h = create_file(trace_path, 0x4000_0000, 1, null, 2, 0x80, null);
    if (h == null or h == win.INVALID_HANDLE_VALUE) return;
    defer _ = close_handle(h);

    const raw_count = @atomicLoad(u32, &s.trace_count, .acquire);
    const total: usize = @min(raw_count, s.trace_cap);
    const dropped = @atomicLoad(u32, &s.trace_dropped, .acquire);
    const freq: u64 = if (s.trace_qpc_freq != 0) s.trace_qpc_freq else 1;
    const start = s.trace_qpc_start;

    var out: [4096]u8 = undefined;
    var pos: usize = 0;

    if (std.fmt.bufPrint(
        &out,
        "# qpc_freq={d} qpc_start={d} records={d} dropped={d}\nseq\tdelta_ns\tthread_id\tkind\tflags\targ0\targ1\n",
        .{ freq, start, total, dropped },
    )) |hdr| {
        writeChunk(s, h, hdr);
    } else |_| {}

    const records: [*]const TraceRecord = @ptrCast(@alignCast(buf));
    for (0..total) |i| {
        const rec = records[i];
        const delta_ticks: u64 = if (rec.qpc >= start) rec.qpc - start else 0;
        const whole_s: u64 = delta_ticks / freq;
        const rem_ticks: u64 = delta_ticks % freq;
        const rem_ns: u64 = if (freq <= 18_000_000_000)
            (rem_ticks * 1_000_000_000) / freq
        else
            rem_ticks / (freq / 1_000_000_000);
        const delta_ns: u64 = whole_s * 1_000_000_000 + rem_ns;

        if (pos > out.len - 256) {
            writeChunk(s, h, out[0..pos]);
            pos = 0;
        }
        const line = if (rec.kind == @intFromEnum(EventKind.abi_enter) or rec.kind == @intFromEnum(EventKind.abi_exit))
            std.fmt.bufPrint(
                out[pos..],
                "{d}\t{d}\t{d}\t{s}:{s}\t{d}\t{d}\t{d}\n",
                .{ i, delta_ns, rec.thread_id, kindName(rec.kind), abiName(rec.flags), rec.flags, rec.arg0, rec.arg1 },
            ) catch {
                writeChunk(s, h, out[0..pos]);
                pos = 0;
                continue;
            }
        else
            std.fmt.bufPrint(
                out[pos..],
                "{d}\t{d}\t{d}\t{s}\t{d}\t{d}\t{d}\n",
                .{ i, delta_ns, rec.thread_id, kindName(rec.kind), rec.flags, rec.arg0, rec.arg1 },
            ) catch {
                writeChunk(s, h, out[0..pos]);
                pos = 0;
                continue;
            };
        pos += line.len;
    }
    if (pos > 0) {
        writeChunk(s, h, out[0..pos]);
    }
}
