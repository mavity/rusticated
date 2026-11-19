const builtin = @import("builtin");
const std = @import("std");
const zwasm = @import("zwasm");
const pal_abi = @import("pal_abi");
const pal_bridge = @import("pal_bridge.zig");
const wasm_env = @import("wasm_env.zig");

pub const std_options: std.Options = .{
    .page_size_min = 4096,
    .page_size_max = 16384,
};

const EntryKind = enum(u8) {
    none = 0,
    run = 1,
    initialize = 2,
    start = 3,
};

const EntrySelection = struct {
    has_run: bool = false,
    has_init: bool = false,
    has_start: bool = false,

    fn observe(self: *EntrySelection, name: []const u8, is_func: bool) void {
        if (!is_func) return;
        if (std.mem.eql(u8, name, "run")) self.has_run = true;
        if (std.mem.eql(u8, name, "_initialize")) self.has_init = true;
        if (std.mem.eql(u8, name, "_start")) self.has_start = true;
    }

    fn result(self: EntrySelection) EntryKind {
        if (self.has_run) return .run;
        if (self.has_init) return .initialize;
        if (self.has_start) return .start;
        return .none;
    }
};

const RuntimeGlobals = struct {
    initialized: bool = false,
    arena: pal_bridge.PalArenaState = .{},
    engine: zwasm.Engine = undefined,
    module: zwasm.Module = undefined,
    linker: zwasm.Linker = undefined,
    instance: zwasm.Instance = undefined,
    entry_kind: EntryKind = .none,
};

var g_runtime: RuntimeGlobals = .{};

inline fn globals() *RuntimeGlobals {
    return &g_runtime;
}

fn panicMsg(msg: []const u8) noreturn {
    pal_bridge.abi().host_panic(msg.ptr, msg.len);
}

fn validateImports(module: *zwasm.Module, alloc: std.mem.Allocator) void {
    var imports = module.imports(alloc) catch panicMsg("zwasm: module.imports failed");
    defer imports.deinit();
    for (imports.items) |imp| {
        if (!std.mem.eql(u8, imp.module, "env")) {
            panicMsg("zwasm: non-env import rejected (WASI/foreign modules prohibited)");
        }
    }
}

fn findEntryKind(module: *zwasm.Module, alloc: std.mem.Allocator) EntryKind {
    var exports = module.exports(alloc) catch panicMsg("zwasm: module.exports failed");
    defer exports.deinit();
    var selection: EntrySelection = .{};
    for (exports.items) |exp| {
        selection.observe(exp.name, exp.kind == .func);
    }
    return selection.result();
}

fn invokeEntry(g: *RuntimeGlobals) void {
    const name: []const u8 = switch (g.entry_kind) {
        .run => "run",
        .initialize => "_initialize",
        .start => "_start",
        .none => return,
    };
    var results: [0]zwasm.Value = .{};
    g.instance.invoke(name, &.{}, &results) catch |err| {
        var msg: [96]u8 = undefined;
        const detail = std.fmt.bufPrint(&msg, "zwasm: guest entry trapped: {s}", .{@errorName(err)}) catch
            panicMsg("zwasm: guest entry trapped");
        panicMsg(detail);
    };
}

const runtime_code_end_ptr = @extern([*]const u8, .{ .name = "runtime_code_end", .visibility = .hidden });

fn applyRwPrefixRelocs() void {
    const base_addr = @intFromPtr(pal_bridge.sliceOrigin());
    const end_ptr: [*]const u8 = @ptrCast(runtime_code_end_ptr);
    const magic = std.mem.readInt(u32, end_ptr[0..4], .little);
    if (magic != 0x52454C4F) return;
    const rwinit_len = std.mem.readInt(u32, end_ptr[4..8], .little);
    const reloc_count = std.mem.readInt(u32, end_ptr[8..12], .little);
    const rw_dst: [*]u8 = @ptrFromInt(base_addr - 0x18000);
    if (rwinit_len > 0) {
        @memcpy(rw_dst[0..rwinit_len], end_ptr[12..][0..rwinit_len]);
    }
    const relocs_ptr = end_ptr + 12 + rwinit_len;
    for (0..reloc_count) |i| {
        const off = std.mem.readInt(i32, relocs_ptr[i * 4 ..][0..4], .little);
        const slot_addr: usize = @intCast(@as(isize, @intCast(base_addr)) + off);
        const slot: *usize = @ptrFromInt(slot_addr);
        slot.* +%= base_addr;
    }
}

const RuntimeHeader = extern struct {
    initialized: u8 = 0,
    _pad: [7]u8 = @splat(0),
    mem_base: ?[*]u8 = null,
};

fn publishRuntimeHeader(g: *RuntimeGlobals) void {
    const hdr: *RuntimeHeader = @ptrCast(@alignCast(pal_bridge.sliceOrigin() - 0x0400));
    hdr.initialized = 1;
    if (g.instance.memory()) |m| {
        hdr.mem_base = m.slice().ptr;
    } else {
        hdr.mem_base = null;
    }
}

pub fn runtimeActivation() callconv(pal_abi.pal_cc) void {
    const g = globals();
    if (!g.initialized) {
        applyRwPrefixRelocs();
        g.* = .{};
        g.initialized = true;

        const wasm_ptr = pal_bridge.abi().wasm_bytes orelse panicMsg("zwasm: wasm_bytes is null");
        const wasm_len = pal_bridge.abi().wasm_len;
        if (wasm_len == 0) panicMsg("zwasm: wasm_len is 0");

        const alloc = pal_bridge.makePalAllocator(&g.arena);
        g.engine = zwasm.Engine.init(alloc, .{}) catch panicMsg("zwasm: Engine.init failed");
        g.module = g.engine.compile(wasm_ptr[0..wasm_len]) catch panicMsg("zwasm: Module.compile failed");
        validateImports(&g.module, alloc);
        g.entry_kind = findEntryKind(&g.module, alloc);

        g.linker = zwasm.Linker.init(&g.engine);
        wasm_env.registerAll(&g.linker) catch panicMsg("zwasm: Linker.registerAll failed");
        g.instance = g.linker.instantiate(&g.module, .{ .fuel = .unmetered }) catch panicMsg("zwasm: Linker.instantiate failed");
    }

    invokeEntry(g);
    publishRuntimeHeader(g);
}

comptime {
    if (!builtin.is_test) {
        pal_bridge.exportIntrinsics();
        pal_bridge.exportJitHooks();
        @export(&runtimeActivation, .{ .name = "runtime_activation" });
        switch (builtin.cpu.arch) {
            .x86_64 => asm (
                \\.section .text.entry,"ax"
                \\.globl runtime_code_start
                \\runtime_code_start:
                \\jmp runtime_activation
            ),
            .aarch64 => asm (
                \\.section .text.entry,"ax"
                \\.globl runtime_code_start
                \\runtime_code_start:
                \\b runtime_activation
            ),
            else => @compileError("Unsupported arch"),
        }
    }
}

test "zwasm runtime unit smoke" {
    // Verify basic compilation of pal_bridge + wasm_env + zwasm
    _ = @import("pal_bridge.zig");
    try std.testing.expectEqual(@as(usize, 384), @sizeOf(pal_abi.PalAbiTable));
}

const TestExport = struct {
    name: []const u8,
    kind: u8 = 0,
};

fn appendTestByte(buf: *[512]u8, len: *usize, byte: u8) void {
    buf[len.*] = byte;
    len.* += 1;
}

fn appendTestLeb(buf: *[512]u8, len: *usize, value: usize) void {
    var remaining = value;
    while (remaining >= 0x80) {
        appendTestByte(buf, len, @as(u8, @truncate(remaining)) | 0x80);
        remaining >>= 7;
    }
    appendTestByte(buf, len, @truncate(remaining));
}

fn entryKindForTest(exports: []const TestExport, has_memory: bool) !EntryKind {
    var wasm: [512]u8 = undefined;
    var len: usize = 0;
    for ("\x00asm\x01\x00\x00\x00") |byte| appendTestByte(&wasm, &len, byte);

    appendTestByte(&wasm, &len, 1);
    appendTestByte(&wasm, &len, 4);
    for (&[_]u8{ 1, 0x60, 0, 0 }) |byte| appendTestByte(&wasm, &len, byte);

    appendTestByte(&wasm, &len, 3);
    appendTestByte(&wasm, &len, 2);
    for (&[_]u8{ 1, 0 }) |byte| appendTestByte(&wasm, &len, byte);

    if (has_memory) {
        appendTestByte(&wasm, &len, 5);
        appendTestByte(&wasm, &len, 3);
        for (&[_]u8{ 1, 0, 1 }) |byte| appendTestByte(&wasm, &len, byte);
    }

    var export_payload_len: usize = 1;
    for (exports) |entry_export| export_payload_len += 1 + entry_export.name.len + 2;
    appendTestByte(&wasm, &len, 7);
    appendTestLeb(&wasm, &len, export_payload_len);
    appendTestLeb(&wasm, &len, exports.len);
    for (exports) |entry_export| {
        appendTestLeb(&wasm, &len, entry_export.name.len);
        for (entry_export.name) |byte| appendTestByte(&wasm, &len, byte);
        appendTestByte(&wasm, &len, entry_export.kind);
        appendTestByte(&wasm, &len, 0);
    }

    appendTestByte(&wasm, &len, 10);
    appendTestByte(&wasm, &len, 4);
    for (&[_]u8{ 1, 2, 0, 0x0b }) |byte| appendTestByte(&wasm, &len, byte);

    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const allocator = arena.allocator();
    var engine = try zwasm.Engine.init(allocator, .{});
    var module = try engine.compile(wasm[0..len]);
    var module_exports = try module.exports(allocator);
    defer module_exports.deinit();
    var selection: EntrySelection = .{};
    for (module_exports.items) |exp| {
        selection.observe(exp.name, exp.kind == .func);
    }
    return selection.result();
}

test "runtime entry selection honors exported function precedence" {
    const run = [_]TestExport{.{ .name = "run" }};
    const initialize = [_]TestExport{.{ .name = "_initialize" }};
    const start = [_]TestExport{.{ .name = "_start" }};
    const none = [_]TestExport{};
    const run_and_initialize = [_]TestExport{ .{ .name = "_initialize" }, .{ .name = "run" } };
    const initialize_and_start = [_]TestExport{ .{ .name = "_start" }, .{ .name = "_initialize" } };
    const start_and_run = [_]TestExport{ .{ .name = "_start" }, .{ .name = "run" } };
    const memory_and_start = [_]TestExport{ .{ .name = "memory", .kind = 2 }, .{ .name = "_start" } };
    const run_and_memory = [_]TestExport{ .{ .name = "run" }, .{ .name = "memory", .kind = 2 } };
    const memory_only = [_]TestExport{.{ .name = "memory", .kind = 2 }};

    const Case = struct {
        name: []const u8,
        exports: []const TestExport,
        has_memory: bool = false,
        want: EntryKind,
    };
    const cases = [_]Case{
        .{ .name = "run only", .exports = &run, .want = .run },
        .{ .name = "initialize only", .exports = &initialize, .want = .initialize },
        .{ .name = "start only", .exports = &start, .want = .start },
        .{ .name = "no exports", .exports = &none, .want = .none },
        .{ .name = "run takes precedence over initialize", .exports = &run_and_initialize, .want = .run },
        .{ .name = "initialize takes precedence over start", .exports = &initialize_and_start, .want = .initialize },
        .{ .name = "run takes precedence regardless of export order", .exports = &start_and_run, .want = .run },
        .{ .name = "non-function export is ignored", .exports = &memory_and_start, .has_memory = true, .want = .start },
        .{ .name = "run is found before a non-function export", .exports = &run_and_memory, .has_memory = true, .want = .run },
        .{ .name = "non-function exports do not select an entry", .exports = &memory_only, .has_memory = true, .want = .none },
    };

    for (cases) |case| {
        try std.testing.expectEqual(case.want, try entryKindForTest(case.exports, case.has_memory));
    }
}
