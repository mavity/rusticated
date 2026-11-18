const builtin = @import("builtin");
const std = @import("std");
const core = @import("hello_core.zig");
const abi_types = core.abi_types;
const Overlapped = core.Overlapped;
const PalAbiTable = core.PalAbiTable;

const MAX_ARGUMENT_BYTES: usize = 16 * 1024;
const MAX_ARGUMENT_COUNT: usize = 2048;
const MAX_TRANSFER_BYTES: usize = 4096;
const MAX_OUTPUT_BYTES: usize = MAX_TRANSFER_BYTES * 2 + 512;
const MAX_CONCURRENT_READS: usize = 64;
const MAX_HANDLES: usize = 16;
const HANDLE_NAME_BYTES: usize = 32;

const PHASE_READY: u32 = 0;
const PHASE_OPERATION: u32 = 1;
const PHASE_OUTPUT: u32 = 2;
const PHASE_CONCURRENT_WAIT: u32 = 3;

const OP_OPEN: u32 = 1;
const OP_WRITE: u32 = 2;
const OP_READ: u32 = 3;
const OP_PREAD: u32 = 4;
const OP_SEEK: u32 = 5;
const OP_TRUNCATE: u32 = 6;
const OP_CLOSE: u32 = 7;

const Handle = extern struct {
    used: bool = false,
    closed: bool = false,
    name: [HANDLE_NAME_BYTES]u8 = @splat(0),
    name_len: usize = 0,
    token: u64 = 0,
};

const ConcurrentRead = extern struct {
    overlapped: Overlapped = .{},
    offset: i64 = 0,
    byte: u8 = 0,
};

const FileIoGlobals = extern struct {
    initialized: bool = false,
    phase: u32 = 0,
    operation: u32 = 0,
    command_index: u32 = 0,
    current_index: u32 = 0,
    argument_count: usize = 0,
    argument_index: usize = 0,
    argument_length: usize = 0,
    argument_cursor: usize = 0,
    arguments: [MAX_ARGUMENT_BYTES]u8 = @splat(0),
    handles: [MAX_HANDLES]Handle = @splat(.{}),
    current_handle: u64 = 0,
    current_handle_slot: usize = 0,
    current_name: [HANDLE_NAME_BYTES]u8 = @splat(0),
    current_name_len: usize = 0,
    overlapped: Overlapped = .{},
    positional_offset: i64 = 0,
    base_offset: i64 = 0,
    requested: usize = 0,
    transferred: usize = 0,
    data: [MAX_TRANSFER_BYTES]u8 = @splat(0),
    concurrent_read_count: usize = 0,
    concurrent_reads: [MAX_CONCURRENT_READS]ConcurrentRead = @splat(.{}),
    output: [MAX_OUTPUT_BYTES]u8 = @splat(0),
    output_length: usize = 0,
    output_offset: usize = 0,
};

inline fn getAbi() *const PalAbiTable {
    return core.abi();
}

inline fn getGlobals() *FileIoGlobals {
    return core.globalsPtr(FileIoGlobals);
}

fn fail(code: i32) noreturn {
    getAbi().process_exit(code);
}

fn nextToken(g: *FileIoGlobals) ?[]const u8 {
    if (g.argument_index >= g.argument_count or g.argument_cursor >= g.argument_length) {
        return null;
    }
    const start = g.argument_cursor;
    var end = start;
    while (end < g.argument_length and g.arguments[end] != 0) : (end += 1) {}
    if (end == g.argument_length) return null;
    g.argument_cursor = end + 1;
    g.argument_index += 1;
    return g.arguments[start..end];
}

fn requiredToken(g: *FileIoGlobals) []const u8 {
    return nextToken(g) orelse fail(2);
}

fn parseUnsigned(tok: []const u8) ?u64 {
    if (tok.len == 0) return null;
    var value: u64 = 0;
    for (tok) |b| {
        if (b < '0' or b > '9') return null;
        value = std.math.mul(u64, value, 10) catch return null;
        value = std.math.add(u64, value, b - '0') catch return null;
    }
    return value;
}

fn parseSigned(tok: []const u8) ?i64 {
    if (tok.len == 0) return null;
    const negative = tok[0] == '-';
    const start: usize = if (negative) 1 else 0;
    if (start == tok.len) return null;
    const limit: u64 = if (negative) @as(u64, std.math.maxInt(i64)) + 1 else @as(u64, std.math.maxInt(i64));
    var value: u64 = 0;
    for (tok[start..]) |b| {
        if (b < '0' or b > '9') return null;
        value = std.math.mul(u64, value, 10) catch return null;
        value = std.math.add(u64, value, b - '0') catch return null;
        if (value > limit) return null;
    }
    if (negative) {
        if (value == @as(u64, std.math.maxInt(i64)) + 1) return std.math.minInt(i64);
        return -@as(i64, @intCast(value));
    }
    return @intCast(value);
}

fn parseName(tok: []const u8, out: *[HANDLE_NAME_BYTES]u8) ?usize {
    if (tok.len == 0 or tok.len > HANDLE_NAME_BYTES) return null;
    for (tok, 0..) |b, i| {
        const ok = (b >= 'a' and b <= 'z') or (b >= 'A' and b <= 'Z') or (b >= '0' and b <= '9') or b == '_' or b == '-';
        if (!ok) return null;
        out[i] = b;
    }
    return tok.len;
}

fn parseAccess(tok: []const u8) ?u32 {
    if (std.mem.eql(u8, tok, "ro")) return 0;
    if (std.mem.eql(u8, tok, "wo")) return 1;
    if (std.mem.eql(u8, tok, "rw")) return 2;
    return null;
}

fn parseFlags(tok: []const u8) ?u32 {
    if (std.mem.eql(u8, tok, "-")) return 0;
    if (tok.len == 0) return null;
    var flags: u32 = 0;
    var start: usize = 0;
    while (start < tok.len) {
        var end = start;
        while (end < tok.len and tok[end] != ',') : (end += 1) {}
        if (end == start) return null;
        const opt = tok[start..end];
        const flag: u32 = if (std.mem.eql(u8, opt, "create"))
            abi_types.OPEN_FLAG_CREATE
        else if (std.mem.eql(u8, opt, "exclusive"))
            abi_types.OPEN_FLAG_EXCL
        else if (std.mem.eql(u8, opt, "truncate"))
            abi_types.OPEN_FLAG_TRUNC
        else if (std.mem.eql(u8, opt, "append"))
            abi_types.OPEN_FLAG_APPEND
        else if (std.mem.eql(u8, opt, "directory"))
            abi_types.OPEN_FLAG_DIRECTORY
        else
            return null;
        if ((flags & flag) != 0) return null;
        flags |= flag;
        start = end + 1;
    }
    return flags;
}

fn hexNibble(b: u8) ?u8 {
    if (b >= '0' and b <= '9') return b - '0';
    if (b >= 'a' and b <= 'f') return b - 'a' + 10;
    if (b >= 'A' and b <= 'F') return b - 'A' + 10;
    return null;
}

fn parseHex(tok: []const u8, out: *[MAX_TRANSFER_BYTES]u8) ?usize {
    if ((tok.len % 2) != 0 or tok.len / 2 > out.len) return null;
    const count = tok.len / 2;
    for (0..count) |i| {
        const hi = hexNibble(tok[i * 2]) orelse return null;
        const lo = hexNibble(tok[i * 2 + 1]) orelse return null;
        out[i] = (hi << 4) | lo;
    }
    return count;
}

fn findHandle(g: *const FileIoGlobals, name: []const u8) ?usize {
    for (g.handles, 0..) |h, i| {
        if (h.used and h.name_len == name.len and std.mem.eql(u8, h.name[0..h.name_len], name)) {
            return i;
        }
    }
    return null;
}

fn appendOutput(g: *FileIoGlobals, bytes: []const u8) void {
    if (g.output_length + bytes.len > g.output.len) fail(90);
    @memcpy(g.output[g.output_length..][0..bytes.len], bytes);
    g.output_length += bytes.len;
}

fn appendNumber(g: *FileIoGlobals, value_in: u64) void {
    var value = value_in;
    var digits: [20]u8 = undefined;
    var count: usize = 0;
    while (true) {
        digits[count] = @intCast('0' + (value % 10));
        count += 1;
        value /= 10;
        if (value == 0) break;
    }
    while (count > 0) {
        count -= 1;
        appendOutput(g, digits[count .. count + 1]);
    }
}

fn appendHex(g: *FileIoGlobals, len: usize) void {
    const hex = "0123456789abcdef";
    for (0..len) |i| {
        const b = g.data[i];
        const pair = [2]u8{ hex[b >> 4], hex[b & 0x0f] };
        appendOutput(g, &pair);
    }
}

fn appendOperationName(g: *FileIoGlobals) void {
    const name: []const u8 = switch (g.operation) {
        OP_OPEN => "open",
        OP_WRITE => "write",
        OP_READ => "read",
        OP_PREAD => "pread",
        OP_SEEK => "seek",
        OP_TRUNCATE => "truncate",
        OP_CLOSE => "close",
        else => fail(90),
    };
    appendOutput(g, name);
}

fn scheduleOutput(g: *FileIoGlobals) void {
    g.phase = PHASE_OUTPUT;
    g.output_offset = 0;
    scheduleOutputChunk(g);
}

fn scheduleOutputChunk(g: *FileIoGlobals) void {
    g.overlapped = .{};
    const remaining = g.output_length - g.output_offset;
    getAbi().write(&g.overlapped, 1, @ptrCast(&g.output[g.output_offset]), remaining);
}

fn beginRecord(g: *FileIoGlobals, success: bool) void {
    g.output_length = 0;
    appendNumber(g, g.current_index);
    appendOutput(g, if (success) " OK " else " ERR ");
    appendOperationName(g);
    if (g.current_name_len != 0) {
        appendOutput(g, " ");
        appendOutput(g, g.current_name[0..g.current_name_len]);
    }
}

fn queueError(g: *FileIoGlobals, err_code: u32) void {
    beginRecord(g, false);
    appendOutput(g, " error=");
    appendNumber(g, err_code);
    if (g.operation == OP_READ or g.operation == OP_PREAD or g.operation == OP_WRITE) {
        appendOutput(g, " bytes=");
        appendNumber(g, g.transferred);
    }
    if (g.operation == OP_READ or g.operation == OP_PREAD) {
        appendOutput(g, " hex=");
        appendHex(g, g.transferred);
    }
    appendOutput(g, "\n");
    scheduleOutput(g);
}

fn queueSuccess(g: *FileIoGlobals, eof: bool) void {
    beginRecord(g, true);
    switch (g.operation) {
        OP_OPEN, OP_CLOSE, OP_TRUNCATE => {},
        OP_WRITE => {
            appendOutput(g, " bytes=");
            appendNumber(g, g.transferred);
        },
        OP_READ, OP_PREAD => {
            appendOutput(g, " bytes=");
            appendNumber(g, g.transferred);
            appendOutput(g, if (eof) " eof=1 hex=" else " eof=0 hex=");
            appendHex(g, g.transferred);
        },
        OP_SEEK => {
            appendOutput(g, " position=");
            appendNumber(g, g.overlapped.result_ext);
        },
        else => fail(90),
    }
    appendOutput(g, "\n");
    scheduleOutput(g);
}

fn scheduleTransfer(g: *FileIoGlobals) void {
    const remaining = g.requested - g.transferred;
    g.overlapped = .{};
    switch (g.operation) {
        OP_WRITE => getAbi().write(&g.overlapped, g.current_handle, @ptrCast(&g.data[g.transferred]), remaining),
        OP_READ => getAbi().read(&g.overlapped, g.current_handle, @ptrCast(&g.data[g.transferred]), remaining),
        OP_PREAD => {
            const off = std.math.add(i64, g.base_offset, @intCast(g.transferred)) catch {
                queueError(g, abi_types.ERROR_INVALID);
                return;
            };
            g.positional_offset = off;
            g.overlapped.flags = abi_types.READ_FLAG_POSITIONAL;
            getAbi().read(&g.overlapped, g.current_handle, @ptrCast(&g.data[g.transferred]), remaining);
        },
        else => fail(90),
    }
    g.phase = PHASE_OPERATION;
}

fn queueConcurrentReadResults(g: *FileIoGlobals) void {
    g.output_length = 0;
    for (0..g.concurrent_read_count) |i| {
        const req = &g.concurrent_reads[i];
        appendNumber(g, g.current_index);
        appendOutput(g, ".");
        appendNumber(g, i);
        if (req.overlapped.@"error" == 0) {
            const count: usize = @intCast(req.overlapped.result_ext);
            if (count > 1) fail(90);
            appendOutput(g, " OK pread ");
            appendOutput(g, g.current_name[0..g.current_name_len]);
            appendOutput(g, " bytes=");
            appendNumber(g, count);
            appendOutput(g, if (count == 0) " eof=1 hex=" else " eof=0 hex=");
            if (count == 1) {
                g.data[0] = req.byte;
                appendHex(g, 1);
            }
        } else {
            appendOutput(g, " ERR pread ");
            appendOutput(g, g.current_name[0..g.current_name_len]);
            appendOutput(g, " error=");
            appendNumber(g, req.overlapped.@"error");
            appendOutput(g, " bytes=0 hex=");
        }
        appendOutput(g, "\n");
    }
    scheduleOutput(g);
}

fn startCommand(g: *FileIoGlobals) void {
    const command = requiredToken(g);
    g.current_index = g.command_index;
    g.command_index += 1;
    g.current_name_len = 0;
    g.current_handle = 0;
    g.transferred = 0;

    if (std.mem.eql(u8, command, "pread-many")) {
        g.operation = OP_PREAD;
        const name = requiredToken(g);
        g.current_name_len = parseName(name, &g.current_name) orelse fail(2);
        const slot = findHandle(g, g.current_name[0..g.current_name_len]) orelse {
            queueError(g, abi_types.ERROR_BADF);
            return;
        };
        const cnt_u64 = parseUnsigned(requiredToken(g)) orelse fail(2);
        if (cnt_u64 < 2 or cnt_u64 > MAX_CONCURRENT_READS) fail(2);
        const count: usize = @intCast(cnt_u64);
        g.current_handle_slot = slot;
        g.current_handle = g.handles[slot].token;
        g.concurrent_read_count = count;
        for (0..count) |i| {
            g.concurrent_reads[i] = .{};
            g.concurrent_reads[i].offset = @intCast(i);
            g.concurrent_reads[i].overlapped.flags = abi_types.READ_FLAG_POSITIONAL;
            getAbi().read(
                &g.concurrent_reads[i].overlapped,
                g.current_handle,
                @ptrCast(&g.concurrent_reads[i].byte),
                1,
            );
        }
        g.phase = PHASE_CONCURRENT_WAIT;
        return;
    }

    if (std.mem.eql(u8, command, "open")) {
        g.operation = OP_OPEN;
        const name = requiredToken(g);
        g.current_name_len = parseName(name, &g.current_name) orelse fail(2);
        const path = requiredToken(g);
        const access = parseAccess(requiredToken(g)) orelse fail(2);
        const flags = parseFlags(requiredToken(g)) orelse fail(2);
        if (path.len == 0) fail(2);
        if (findHandle(g, g.current_name[0..g.current_name_len]) != null) {
            queueError(g, abi_types.ERROR_EXIST);
            return;
        }
        var slot: usize = 0;
        while (slot < g.handles.len and g.handles[slot].used) : (slot += 1) {}
        if (slot == g.handles.len) {
            queueError(g, abi_types.ERROR_NOMEM);
            return;
        }
        g.current_handle_slot = slot;
        g.overlapped = .{};
        getAbi().path_open(&g.overlapped, path.ptr, path.len, access | flags);
        g.phase = PHASE_OPERATION;
        return;
    }

    if (std.mem.eql(u8, command, "write")) {
        g.operation = OP_WRITE;
    } else if (std.mem.eql(u8, command, "read")) {
        g.operation = OP_READ;
    } else if (std.mem.eql(u8, command, "pread")) {
        g.operation = OP_PREAD;
    } else if (std.mem.eql(u8, command, "seek")) {
        g.operation = OP_SEEK;
    } else if (std.mem.eql(u8, command, "truncate")) {
        g.operation = OP_TRUNCATE;
    } else if (std.mem.eql(u8, command, "close")) {
        g.operation = OP_CLOSE;
    } else {
        fail(2);
    }

    const name = requiredToken(g);
    g.current_name_len = parseName(name, &g.current_name) orelse fail(2);
    const slot = findHandle(g, g.current_name[0..g.current_name_len]) orelse {
        queueError(g, abi_types.ERROR_BADF);
        return;
    };
    g.current_handle_slot = slot;
    g.current_handle = g.handles[slot].token;

    switch (g.operation) {
        OP_CLOSE => {
            if (g.handles[slot].closed) {
                queueError(g, abi_types.ERROR_BADF);
                return;
            }
            getAbi().handle_close(g.current_handle);
            g.handles[slot].closed = true;
            queueSuccess(g, false);
        },
        OP_WRITE => {
            g.requested = parseHex(requiredToken(g), &g.data) orelse fail(2);
            g.transferred = 0;
            scheduleTransfer(g);
        },
        OP_READ => {
            const len_u64 = parseUnsigned(requiredToken(g)) orelse fail(2);
            if (len_u64 > MAX_TRANSFER_BYTES) fail(2);
            g.requested = @intCast(len_u64);
            g.transferred = 0;
            scheduleTransfer(g);
        },
        OP_PREAD => {
            g.base_offset = parseSigned(requiredToken(g)) orelse fail(2);
            const len_u64 = parseUnsigned(requiredToken(g)) orelse fail(2);
            if (len_u64 > MAX_TRANSFER_BYTES) fail(2);
            g.requested = @intCast(len_u64);
            g.transferred = 0;
            scheduleTransfer(g);
        },
        OP_SEEK => {
            const wt = requiredToken(g);
            const whence: u32 = if (std.mem.eql(u8, wt, "start"))
                0
            else if (std.mem.eql(u8, wt, "current"))
                1
            else if (std.mem.eql(u8, wt, "end"))
                2
            else
                fail(2);
            const off = parseSigned(requiredToken(g)) orelse fail(2);
            g.overlapped = .{};
            getAbi().seek(&g.overlapped, g.current_handle, off, whence);
            g.phase = PHASE_OPERATION;
        },
        OP_TRUNCATE => {
            const len = parseSigned(requiredToken(g)) orelse fail(2);
            g.overlapped = .{};
            getAbi().handle_truncate(&g.overlapped, g.current_handle, len);
            g.phase = PHASE_OPERATION;
        },
        else => fail(90),
    }
}

fn completeOperation(g: *FileIoGlobals) void {
    if (g.overlapped.@"error" != 0) {
        queueError(g, g.overlapped.@"error");
        return;
    }
    switch (g.operation) {
        OP_OPEN => {
            const token = g.overlapped.result_ext;
            if (token < 3) {
                queueError(g, abi_types.ERROR_INVALID);
                return;
            }
            g.handles[g.current_handle_slot] = .{
                .used = true,
                .closed = false,
                .name = g.current_name,
                .name_len = g.current_name_len,
                .token = token,
            };
            queueSuccess(g, false);
        },
        OP_WRITE => {
            const count: usize = @intCast(g.overlapped.result_ext);
            const remaining = g.requested - g.transferred;
            if (count > remaining) fail(90);
            if (count == 0 and remaining != 0) {
                queueError(g, abi_types.ERROR_IO);
                return;
            }
            g.transferred += count;
            if (g.transferred < g.requested) {
                scheduleTransfer(g);
            } else {
                queueSuccess(g, false);
            }
        },
        OP_READ, OP_PREAD => {
            const count: usize = @intCast(g.overlapped.result_ext);
            const remaining = g.requested - g.transferred;
            if (count > remaining) fail(90);
            if (count == 0) {
                queueSuccess(g, g.transferred < g.requested);
                return;
            }
            g.transferred += count;
            if (g.transferred < g.requested) {
                scheduleTransfer(g);
            } else {
                queueSuccess(g, false);
            }
        },
        OP_SEEK, OP_TRUNCATE => queueSuccess(g, false),
        else => fail(90),
    }
}

pub fn resumeActivation() callconv(abi_types.pal_cc) void {
    const g = getGlobals();
    if (!g.initialized) {
        g.* = .{};
        g.initialized = true;
        const packed_args = getAbi().get_args(&g.arguments, g.arguments.len);
        if (packed_args == std.math.maxInt(u64)) fail(2);
        g.argument_count = @intCast(packed_args >> 32);
        g.argument_length = @intCast(@as(u32, @truncate(packed_args)));
        if (g.argument_count == 0 or
            g.argument_count > MAX_ARGUMENT_COUNT or
            g.argument_length > MAX_ARGUMENT_BYTES or
            (g.argument_length != 0 and g.arguments[g.argument_length - 1] != 0))
        {
            fail(2);
        }
    }

    while (true) {
        switch (g.phase) {
            PHASE_READY => {
                if (g.argument_index == g.argument_count) {
                    if (g.argument_cursor != g.argument_length) fail(2);
                    getAbi().process_exit(0);
                }
                startCommand(g);
            },
            PHASE_OPERATION => {
                if ((g.overlapped.flags & abi_types.FLAG_COMPLETED) == 0) return;
                completeOperation(g);
            },
            PHASE_CONCURRENT_WAIT => {
                for (0..g.concurrent_read_count) |i| {
                    if ((g.concurrent_reads[i].overlapped.flags & abi_types.FLAG_COMPLETED) == 0) return;
                }
                queueConcurrentReadResults(g);
            },
            PHASE_OUTPUT => {
                if ((g.overlapped.flags & abi_types.FLAG_COMPLETED) == 0) return;
                if (g.overlapped.@"error" != 0) fail(90);
                const count: usize = @intCast(g.overlapped.result_ext);
                const remaining = g.output_length - g.output_offset;
                if (count == 0 or count > remaining) fail(90);
                g.output_offset += count;
                if (g.output_offset == g.output_length) {
                    g.phase = PHASE_READY;
                } else {
                    scheduleOutputChunk(g);
                }
            },
            else => fail(90),
        }
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

test "fileio token parsers" {
    try std.testing.expectEqual(@as(?u64, 42), parseUnsigned("42"));
    try std.testing.expectEqual(@as(?u64, null), parseUnsigned(""));
    try std.testing.expectEqual(@as(?i64, -123), parseSigned("-123"));
    try std.testing.expectEqual(@as(?u32, 2), parseAccess("rw"));
    try std.testing.expectEqual(@as(?u32, abi_types.OPEN_FLAG_CREATE | abi_types.OPEN_FLAG_TRUNC), parseFlags("create,truncate"));
    var buf: [MAX_TRANSFER_BYTES]u8 = undefined;
    const n = parseHex("4142", &buf).?;
    try std.testing.expectEqual(@as(usize, 2), n);
    try std.testing.expectEqualStrings("AB", buf[0..n]);
}
