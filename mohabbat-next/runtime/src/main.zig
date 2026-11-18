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
    var has_init = false;
    var has_start = false;
    for (exports.items) |exp| {
        if (exp.kind != .func) continue;
        if (std.mem.eql(u8, exp.name, "run")) return .run;
        if (std.mem.eql(u8, exp.name, "_initialize")) has_init = true;
        if (std.mem.eql(u8, exp.name, "_start")) has_start = true;
    }
    if (has_init) return .initialize;
    if (has_start) return .start;
    return .none;
}

fn invokeEntry(g: *RuntimeGlobals) void {
    const name: []const u8 = switch (g.entry_kind) {
        .run => "run",
        .initialize => "_initialize",
        .start => "_start",
        .none => return,
    };
    var results: [0]zwasm.Value = .{};
    g.instance.invoke(name, &.{}, &results) catch panicMsg("zwasm: guest entry trapped");
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
        g.instance = g.linker.instantiate(&g.module, .{}) catch panicMsg("zwasm: Linker.instantiate failed");
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
