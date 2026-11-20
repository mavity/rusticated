package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	zwasmRepoURL     = "https://github.com/zwasm/zwasm.git"
	zwasmUpstreamDir = "zwasm-upstream"
	zwasmOverlayDir  = "zwasm-overlay"
)

type zwasmPatch struct {
	file string
	old  string
	new  string
}

func zwasmOverlayPatches() []zwasmPatch {
	return []zwasmPatch{
		// 1. zwasm.zig: skip C-API comptime exports on freestanding
		{
			file: "src/zwasm.zig",
			old: `comptime {
    _ = @import("api/wasm.zig");`,
			new: `comptime {
    if (@import("builtin").os.tag != .freestanding) {
        _ = @import("api/wasm.zig");`,
		},
		{
			file: "src/zwasm.zig",
			old: `    _ = @import("api/module_serialize.zig");
    _ = @import("api/cross_module.zig");
}`,
			new: `        _ = @import("api/module_serialize.zig");
        _ = @import("api/cross_module.zig");
    }
}`,
		},
		// 2. platform/jit_mem.zig: route freestanding JIT alloc/protect/free through PalAbiTable hooks
		{
			file: "src/platform/jit_mem.zig",
			old: `const linux_rwx: bool = builtin.os.tag == .linux and
    (builtin.cpu.arch == .x86_64 or builtin.cpu.arch == .aarch64);`,
			new: `const linux_rwx: bool = builtin.os.tag == .linux and
    (builtin.cpu.arch == .x86_64 or builtin.cpu.arch == .aarch64);

extern fn mohabbat_jit_alloc(size: usize) callconv(.c) ?[*]u8;
extern fn mohabbat_jit_free(ptr: [*]u8, size: usize) callconv(.c) void;
extern fn mohabbat_jit_set_executable(ptr: [*]u8, size: usize) callconv(.c) i32;
extern fn mohabbat_jit_set_writable(ptr: [*]u8, size: usize) callconv(.c) i32;`,
		},
		{
			file: "src/platform/jit_mem.zig",
			old: `pub fn alloc(size: usize) Error!JitBlock {
    if (size == 0) return Error.AllocationFailed;
    const rounded = (size + page_size - 1) & ~(page_size - 1);

    if (macos_jit) {`,
			new: `pub fn alloc(size: usize) Error!JitBlock {
    if (size == 0) return Error.AllocationFailed;
    const rounded = (size + page_size - 1) & ~(page_size - 1);

    if (builtin.os.tag == .freestanding) {
        const ptr = mohabbat_jit_alloc(rounded) orelse return Error.AllocationFailed;
        const aligned: [*]align(page_size) u8 = @ptrCast(@alignCast(ptr));
        return .{ .bytes = aligned[0..rounded] };
    }

    if (macos_jit) {`,
		},
		{
			file: "src/platform/jit_mem.zig",
			old: `pub fn free(block: JitBlock) void {
    if (block.bytes.len == 0) return;
    if (macos_jit) {`,
			new: `pub fn free(block: JitBlock) void {
    if (block.bytes.len == 0) return;
    if (builtin.os.tag == .freestanding) {
        mohabbat_jit_free(block.bytes.ptr, block.bytes.len);
        return;
    }
    if (macos_jit) {`,
		},
		{
			file: "src/platform/jit_mem.zig",
			old: `extern "c" fn pthread_jit_write_protect_np(enabled: c_int) void;
extern "c" fn sys_icache_invalidate(start: ?*anyopaque, len: usize) void;

/// Flip ` + "`block`" + ` from writable to executable for the current
/// thread + invalidate the I-cache so freshly-written bytes are
/// visible to the CPU's instruction fetch.
pub fn setExecutable(block: JitBlock) Error!void {
    if (macos_jit) {
        pthread_jit_write_protect_np(1); // 1 = re-enable W^X (RX)
        // I-cache invalidation is a no-op on x86_64 (Intel SDM Vol 3 §11.6:
        // self-modifying-code coherency is automatic) but the call is cheap and
        // keeps the macos branch uniform.
        sys_icache_invalidate(@ptrCast(block.bytes.ptr), block.bytes.len);
        return;
    }`,
			new: `const darwin_extern = if (macos_jit) struct {
    extern "c" fn pthread_jit_write_protect_np(enabled: c_int) void;
    extern "c" fn sys_icache_invalidate(start: ?*anyopaque, len: usize) void;
} else struct {};

pub fn setExecutable(block: JitBlock) Error!void {
    if (builtin.os.tag == .freestanding) {
        if (mohabbat_jit_set_executable(block.bytes.ptr, block.bytes.len) != 0) return Error.ProtectionFailed;
        return;
    }
    if (macos_jit) {
        darwin_extern.pthread_jit_write_protect_np(1);
        darwin_extern.sys_icache_invalidate(@ptrCast(block.bytes.ptr), block.bytes.len);
        return;
    }`,
		},
		{
			file: "src/platform/jit_mem.zig",
			old: `pub fn setWritable(block: JitBlock) Error!void {
    _ = block; // Mac uses pthread_jit_write_protect_np (no block);
    // Linux + Windows are no-ops (page is RWX).
    if (macos_jit) {
        pthread_jit_write_protect_np(0); // 0 = disable W^X (RW)
        return;
    }`,
			new: `pub fn setWritable(block: JitBlock) Error!void {
    if (builtin.os.tag == .freestanding) {
        if (mohabbat_jit_set_writable(block.bytes.ptr, block.bytes.len) != 0) return Error.ProtectionFailed;
        return;
    }
    if (macos_jit) {
        darwin_extern.pthread_jit_write_protect_np(0);
        return;
    }`,
		},
		// 3. platform/signal.zig & trap_registry.zig
		{
			file: "src/platform/signal.zig",
			old:  `const enabled = builtin.os.tag != .windows and builtin.os.tag != .wasi;`,
			new:  `const enabled = builtin.os.tag != .windows and builtin.os.tag != .wasi and builtin.os.tag != .freestanding;`,
		},
		{
			file: "src/platform/signal.zig",
			old: `    .linux => std.os.linux.MINSIGSTKSZ,
    else => std.posix.system.MINSIGSTKSZ,`,
			new: `    .linux => std.os.linux.MINSIGSTKSZ,
    .windows, .wasi, .freestanding => 0,
    else => std.posix.system.MINSIGSTKSZ,`,
		},
		{
			file: "src/platform/trap_registry.zig",
			old: `const max_code_regions = 1024;
const max_guarded_regions = 4096;`,
			new: `const is_freestanding = @import("builtin").os.tag == .freestanding;
const max_code_regions = if (is_freestanding) 4 else 1024;
const max_guarded_regions = if (is_freestanding) 4 else 4096;`,
		},
		// 4. engine/codegen/shared/throw_trampoline.zig & eh_registry.zig
		{
			file: "src/engine/codegen/shared/throw_trampoline.zig",
			old:  `.linux, .macos, .freebsd, .netbsd, .openbsd, .dragonfly => asm volatile (std.fmt.comptimePrint(`,
			new:  `.linux, .macos, .freebsd, .netbsd, .openbsd, .dragonfly, .freestanding => asm volatile (std.fmt.comptimePrint(`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old:  `const table_allocator = std.heap.page_allocator;`,
			new: `const freestanding_alloc = struct {
    extern fn mohabbat_raw_alloc(size: usize, align_bytes: usize) callconv(.c) ?[*]u8;
    extern fn mohabbat_raw_free(ptr: [*]u8, size: usize) callconv(.c) void;
    fn fAlloc(_: *anyopaque, len: usize, alignment: std.mem.Alignment, _: usize) ?[*]u8 {
        return mohabbat_raw_alloc(len, alignment.toByteUnits());
    }
    fn fResize(_: *anyopaque, memory: []u8, _: std.mem.Alignment, new_len: usize, _: usize) bool {
        return new_len <= memory.len;
    }
    fn fRemap(_: *anyopaque, memory: []u8, alignment: std.mem.Alignment, new_len: usize, _: usize) ?[*]u8 {
        if (new_len <= memory.len) return memory.ptr;
        const p = mohabbat_raw_alloc(new_len, alignment.toByteUnits()) orelse return null;
        @memcpy(p[0..memory.len], memory);
        mohabbat_raw_free(memory.ptr, memory.len);
        return p;
    }
    fn fFree(_: *anyopaque, memory: []u8, _: std.mem.Alignment, _: usize) void {
        if (memory.len > 0) mohabbat_raw_free(memory.ptr, memory.len);
    }
    var dummy: u8 = 0;
    const alloc: std.mem.Allocator = .{
        .ptr = &dummy,
        .vtable = &.{ .alloc = fAlloc, .resize = fResize, .remap = fRemap, .free = fFree },
    };
};

const table_allocator: std.mem.Allocator = if (@import("builtin").os.tag == .freestanding)
    freestanding_alloc.alloc
else
    std.heap.page_allocator;`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `var rt_gen0_slots: [initial_slots]?*jit_abi.JitRuntime = @splat(null);
var rt_gen0: Table(?*jit_abi.JitRuntime) = .{ .slots = &rt_gen0_slots, .prev = null };
var rts: *Table(?*jit_abi.JitRuntime) = &rt_gen0;`,
			new: `var rt_gen0_slots: [initial_slots]?*jit_abi.JitRuntime = @splat(null);
var rt_gen0: ?Table(?*jit_abi.JitRuntime) = null;
var rts: ?*Table(?*jit_abi.JitRuntime) = null;

fn ensureRts() *Table(?*jit_abi.JitRuntime) {
    if (rts) |p| return p;
    rt_gen0 = .{ .slots = &rt_gen0_slots, .prev = null };
    const p = &(rt_gen0.?);
    @atomicStore(?*Table(?*jit_abi.JitRuntime), &rts, p, .release);
    return p;
}`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `var thunk_gen0_slots: [initial_slots]ThunkRange = @splat(ThunkRange{});
var thunk_gen0: Table(ThunkRange) = .{ .slots = &thunk_gen0_slots, .prev = null };
var thunk_ranges: *Table(ThunkRange) = &thunk_gen0;`,
			new: `var thunk_gen0_slots: [initial_slots]ThunkRange = @splat(ThunkRange{});
var thunk_gen0: ?Table(ThunkRange) = null;
var thunk_ranges: ?*Table(ThunkRange) = null;

fn ensureThunks() *Table(ThunkRange) {
    if (thunk_ranges) |p| return p;
    thunk_gen0 = .{ .slots = &thunk_gen0_slots, .prev = null };
    const p = &(thunk_gen0.?);
    @atomicStore(?*Table(ThunkRange), &thunk_ranges, p, .release);
    return p;
}`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `pub fn register(rt: *jit_abi.JitRuntime) error{OutOfMemory}!void {
    lockTables();
    defer table_lock.unlock();
    for (rts.slots) |slot| if (slot == rt) return;
    while (true) {
        for (rts.slots) |*slot| if (slot.* == null) {
            @atomicStore(?*jit_abi.JitRuntime, slot, rt, .release);
            return;
        };
        try grow(?*jit_abi.JitRuntime, &rts, null);
    }
}`,
			new: `pub fn register(rt: *jit_abi.JitRuntime) error{OutOfMemory}!void {
    lockTables();
    defer table_lock.unlock();
    const head = ensureRts();
    for (head.slots) |slot| if (slot == rt) return;
    while (true) {
        const cur = rts.?;
        for (cur.slots) |*slot| if (slot.* == null) {
            @atomicStore(?*jit_abi.JitRuntime, slot, rt, .release);
            return;
        };
        var nonnull: *Table(?*jit_abi.JitRuntime) = cur;
        try grow(?*jit_abi.JitRuntime, &nonnull, null);
        @atomicStore(?*Table(?*jit_abi.JitRuntime), &rts, nonnull, .release);
    }
}`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `pub fn registerThunkArena(start: usize, len: usize) error{OutOfMemory}!void {
    lockTables();
    defer table_lock.unlock();
    for (thunk_ranges.slots) |slot| if (slot.start == start) return;
    while (true) {
        for (thunk_ranges.slots) |*slot| if (slot.start == 0) {
            beginThunkWrite(slot);
            @atomicStore(usize, &slot.start, start, .monotonic);
            @atomicStore(usize, &slot.len, len, .monotonic);
            endThunkWrite(slot);
            return;
        };
        try grow(ThunkRange, &thunk_ranges, .{});
    }
}`,
			new: `pub fn registerThunkArena(start: usize, len: usize) error{OutOfMemory}!void {
    lockTables();
    defer table_lock.unlock();
    const head = ensureThunks();
    for (head.slots) |slot| if (slot.start == start) return;
    while (true) {
        const cur = thunk_ranges.?;
        for (cur.slots) |*slot| if (slot.start == 0) {
            beginThunkWrite(slot);
            @atomicStore(usize, &slot.start, start, .monotonic);
            @atomicStore(usize, &slot.len, len, .monotonic);
            endThunkWrite(slot);
            return;
        };
        var nonnull: *Table(ThunkRange) = cur;
        try grow(ThunkRange, &nonnull, .{});
        @atomicStore(?*Table(ThunkRange), &thunk_ranges, nonnull, .release);
    }
}`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `pub fn isThunkAddr(addr: usize) bool {
    const tbl = @atomicLoad(*Table(ThunkRange), &thunk_ranges, .acquire);`,
			new: `pub fn isThunkAddr(addr: usize) bool {
    const tbl = @atomicLoad(?*Table(ThunkRange), &thunk_ranges, .acquire) orelse return false;`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `pub fn isCodeAddr(abs_pc: usize) bool {
    if (isThunkAddr(abs_pc)) return true;
    const tbl = @atomicLoad(*Table(?*jit_abi.JitRuntime), &rts, .acquire);`,
			new: `pub fn isCodeAddr(abs_pc: usize) bool {
    if (isThunkAddr(abs_pc)) return true;
    const tbl = @atomicLoad(?*Table(?*jit_abi.JitRuntime), &rts, .acquire) orelse return false;`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `pub fn resolve(abs_pc: usize, ctx: ?*anyopaque) ?unwind.ResolvedFrame {
    _ = ctx;
    const tbl = @atomicLoad(*Table(?*jit_abi.JitRuntime), &rts, .acquire);`,
			new: `pub fn resolve(abs_pc: usize, ctx: ?*anyopaque) ?unwind.ResolvedFrame {
    _ = ctx;
    const tbl = @atomicLoad(?*Table(?*jit_abi.JitRuntime), &rts, .acquire) orelse return null;`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `pub fn runtimeForPc(abs_pc: usize) ?*jit_abi.JitRuntime {
    const tbl = @atomicLoad(*Table(?*jit_abi.JitRuntime), &rts, .acquire);`,
			new: `pub fn runtimeForPc(abs_pc: usize) ?*jit_abi.JitRuntime {
    const tbl = @atomicLoad(?*Table(?*jit_abi.JitRuntime), &rts, .acquire) orelse return null;`,
		},
		{
			file: "src/engine/codegen/shared/eh_registry.zig",
			old: `pub fn codeMapForPc(abs_pc: usize) ?code_map_mod.CodeMap {
    const tbl = @atomicLoad(*Table(?*jit_abi.JitRuntime), &rts, .acquire);`,
			new: `pub fn codeMapForPc(abs_pc: usize) ?code_map_mod.CodeMap {
    const tbl = @atomicLoad(?*Table(?*jit_abi.JitRuntime), &rts, .acquire) orelse return null;`,
		},
		// 5. zwasm/engine.zig, linker.zig, instance.zig
		{
			file: "src/zwasm/engine.zig",
			old:  `        const e = _api_instance.wasm_engine_new() orelse return error.OutOfMemory;`,
			new:  `        const e = _api_instance.wasm_engine_new_with_alloc(alloc) orelse return error.OutOfMemory;`,
		},
		{
			file: "src/zwasm/instance.zig",
			old: `    pub fn wasiExitCode(self: *const Instance) ?u32 {
        const h = _api_instance.activeWasiHost(self.c_store) orelse return null;`,
			new: `    pub fn wasiExitCode(self: *const Instance) ?u32 {
        if (comptime @import("build_options").wasi_level == .none) return null;
        const h = _api_instance.activeWasiHost(self.c_store) orelse return null;`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `pub const Linker = struct {
    engine: *_engine.Engine,
    entries: std.ArrayList(Entry) = .empty,
    ctx_storage: std.ArrayList(CtxEntry) = .empty,
    /// WASI host registered by ` + "`defineWasi`" + `. Linker owns the
    /// allocation; thunks receive the pointer via their ` + "`ctx`" + `
    /// argument (not via ` + "`store.wasi_host`" + `, whose owning allocator
    /// is ` + "`wasm_store_delete`" + `'s c_allocator).
    wasi_host: ?*_wasi_host.Host = null,`,
			new: `const enable_wasi = @import("build_options").wasi_level != .none;

pub const Linker = struct {
    engine: *_engine.Engine,
    entries: std.ArrayList(Entry) = .empty,
    ctx_storage: std.ArrayList(CtxEntry) = .empty,
    wasi_host: if (enable_wasi) ?*_wasi_host.Host else ?*anyopaque = null,`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `        if (self.wasi_host) |h| {
            const hosts = &self.engine.c_store.linker_wasi_hosts;
            for (hosts.items, 0..) |r, i| if (r == @as(*anyopaque, @ptrCast(h))) {
                _ = hosts.swapRemove(i);
                break;
            };
            if (hosts.items.len == 0) {
                hosts.deinit(self.engine.alloc);
                hosts.* = .empty; // the Store still walks it
            }
            h.deinit();
            self.engine.alloc.destroy(h);
            self.wasi_host = null;
        }`,
			new: `        if (comptime enable_wasi) {
            if (self.wasi_host) |h| {
                const hosts = &self.engine.c_store.linker_wasi_hosts;
                for (hosts.items, 0..) |r, i| if (r == @as(*anyopaque, @ptrCast(h))) {
                    _ = hosts.swapRemove(i);
                    break;
                };
                if (hosts.items.len == 0) {
                    hosts.deinit(self.engine.alloc);
                    hosts.* = .empty;
                }
                h.deinit();
                self.engine.alloc.destroy(h);
                self.wasi_host = null;
            }
        }`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `    pub fn defineWasi(self: *Linker, cfg: WasiConfig) !void {
        if (self.wasi_host != null) return error.WasiAlreadyDefined;
        const h = try self.engine.alloc.create(_wasi_host.Host);
        errdefer self.engine.alloc.destroy(h);
        h.* = try _wasi_host.Host.init(self.engine.alloc);
        errdefer h.deinit();

        h.io = cfg.io;

        if (cfg.args.len > 0) try h.setArgs(cfg.args);

        // Queue preopens; ` + "`instantiate`" + ` materialises them (opens via ` + "`io`" + `).
        for (cfg.preopens) |p| try h.addPendingPreopen(p.host_path, p.guest_path);

        if (cfg.envs.len > 0) {
            // setEnvs takes parallel key/value slices; split the pair list
            // into two temporaries (setEnvs dupes, so they need not persist).
            const keys = try self.engine.alloc.alloc([]const u8, cfg.envs.len);
            defer self.engine.alloc.free(keys);
            const vals = try self.engine.alloc.alloc([]const u8, cfg.envs.len);
            defer self.engine.alloc.free(vals);
            for (cfg.envs, 0..) |e, i| {
                keys[i] = e.name;
                vals[i] = e.value;
            }
            try h.setEnvs(keys, vals);
        }

        // Where ` + "`Instance.invoke`" + ` clears and ` + "`wasiExitCode`" + ` finds the status (#490).
        try self.engine.c_store.linker_wasi_hosts.append(self.engine.alloc, @ptrCast(h));
        self.wasi_host = h;
    }`,
			new: `    pub fn defineWasi(self: *Linker, cfg: WasiConfig) !void {
        if (comptime enable_wasi) {
            if (self.wasi_host != null) return error.WasiAlreadyDefined;
            const h = try self.engine.alloc.create(_wasi_host.Host);
            errdefer self.engine.alloc.destroy(h);
            h.* = try _wasi_host.Host.init(self.engine.alloc);
            errdefer h.deinit();
            h.io = cfg.io;
            if (cfg.args.len > 0) try h.setArgs(cfg.args);
            for (cfg.preopens) |p| try h.addPendingPreopen(p.host_path, p.guest_path);
            if (cfg.envs.len > 0) {
                const keys = try self.engine.alloc.alloc([]const u8, cfg.envs.len);
                defer self.engine.alloc.free(keys);
                const vals = try self.engine.alloc.alloc([]const u8, cfg.envs.len);
                defer self.engine.alloc.free(vals);
                for (cfg.envs, 0..) |e, i| {
                    keys[i] = e.name;
                    vals[i] = e.value;
                }
                try h.setEnvs(keys, vals);
            }
            try self.engine.c_store.linker_wasi_hosts.append(self.engine.alloc, @ptrCast(h));
            self.wasi_host = h;
        } else {
            return error.UnsupportedWasiImport;
        }
    }`,
		},
		{
			file: "src/zwasm/linker.zig",
			old:  `                        if (dbg.on("link.trace")) std.debug.print("[link] UnknownImport: {s}::{s}\n", .{ it.module, it.name });`,
			new:  `                        if (comptime @import("builtin").os.tag != .freestanding) if (dbg.on("link.trace")) std.debug.print("[link] UnknownImport: {s}::{s}\n", .{ it.module, it.name });`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `                    if (it.kind != .func) return error.ImportKindMismatch;
                    const host = self.wasi_host orelse return error.UnknownImport;
                    const thunk = _api_wasi.lookupWasiThunk(it.name) orelse return error.UnsupportedWasiImport;
                    bindings_list.append(scratch, .{ .func = .{
                        .host_call = .{ .fn_ptr = thunk, .ctx = @ptrCast(host) },
                        .source = .wasi,
                    } }) catch return error.OutOfMemory;
                    continue;`,
			new: `                    if (comptime enable_wasi) {
                        if (it.kind != .func) return error.ImportKindMismatch;
                        const host = self.wasi_host orelse return error.UnknownImport;
                        const thunk = _api_wasi.lookupWasiThunk(it.name) orelse return error.UnsupportedWasiImport;
                        bindings_list.append(scratch, .{ .func = .{
                            .host_call = .{ .fn_ptr = thunk, .ctx = @ptrCast(host) },
                            .source = .wasi,
                        } }) catch return error.OutOfMemory;
                        continue;
                    } else {
                        return error.UnknownImport;
                    }`,
		},
		{
			file: "src/zwasm/linker.zig",
			old:  `                                    if (dbg.on("link.trace")) std.debug.print("[link] SignatureMismatch: {s}::{s}\n", .{ it.module, it.name });`,
			new:  `                                    if (comptime @import("builtin").os.tag != .freestanding) if (dbg.on("link.trace")) std.debug.print("[link] SignatureMismatch: {s}::{s}\n", .{ it.module, it.name });`,
		},
		{
			file: "src/zwasm/linker.zig",
			old:  `        if (self.wasi_host) |h| h.materializePendingPreopens() catch return error.InstantiateFailed;`,
			new:  `        if (comptime enable_wasi) if (self.wasi_host) |h| h.materializePendingPreopens() catch return error.InstantiateFailed;`,
		},
		// 6. api/instance.zig
		{
			file: "src/api/instance.zig",
			old: `pub inline fn engineIo(e: *const Engine) ?std.Io {
    const t: *std.Io.Threaded = @ptrCast(@alignCast(e.io_threaded orelse return null));
    return t.io();
}`,
			new: `pub inline fn engineIo(e: *const Engine) ?std.Io {
    if (builtin.os.tag == .freestanding) return null;
    const t: *std.Io.Threaded = @ptrCast(@alignCast(e.io_threaded orelse return null));
    return t.io();
}`,
		},
		{
			file: "src/api/instance.zig",
			old: `pub export fn wasm_engine_new() callconv(.c) ?*Engine {
    const tls = struct {
        var dbg_initialised: bool = false;
    };
    if (!tls.dbg_initialised) {
        const raw = std.c.getenv("ZWASM_DEBUG");
        const value: ?[]const u8 = if (raw) |p| std.mem.span(p) else null;
        dbg.initFromEnv(value);
        tls.dbg_initialised = true;
    }
    const alloc = std.heap.c_allocator;
    const e = alloc.create(Engine) catch return null;
    // ADR-0184: the engine owns a ` + "`std.Io.Threaded`" + ` so the C-ABI
    // surface (which cannot receive a Zig io token) can serve WASI
    // preopens / env inheritance. Threads spawn lazily; init only
    // needs the allocator.
    const threaded = alloc.create(std.Io.Threaded) catch {
        alloc.destroy(e);
        return null;
    };
    threaded.* = .init(alloc, .{});
    e.* = .{
        .alloc_ptr = alloc.ptr,
        .alloc_vtable = @ptrCast(alloc.vtable),
        .io_threaded = threaded,
    };
    return e;
}`,
			new: `pub fn wasm_engine_new_with_alloc(alloc: std.mem.Allocator) ?*Engine {
    const e = alloc.create(Engine) catch return null;
    if (comptime builtin.os.tag == .freestanding) {
        e.* = .{
            .alloc_ptr = alloc.ptr,
            .alloc_vtable = @ptrCast(alloc.vtable),
            .io_threaded = null,
        };
        return e;
    }
    const threaded = alloc.create(std.Io.Threaded) catch {
        alloc.destroy(e);
        return null;
    };
    threaded.* = .init(alloc, .{});
    e.* = .{
        .alloc_ptr = alloc.ptr,
        .alloc_vtable = @ptrCast(alloc.vtable),
        .io_threaded = threaded,
    };
    return e;
}

pub export fn wasm_engine_new() callconv(.c) ?*Engine {
    if (comptime builtin.os.tag == .freestanding) return null;
    if (comptime builtin.os.tag != .freestanding) {
        const tls = struct {
            var dbg_initialised: bool = false;
        };
        if (!tls.dbg_initialised) {
            const raw = std.c.getenv("ZWASM_DEBUG");
            const value: ?[]const u8 = if (raw) |p| std.mem.span(p) else null;
            dbg.initFromEnv(value);
            tls.dbg_initialised = true;
        }
        const alloc = c_alloc;
        const e = alloc.create(Engine) catch return null;
        const threaded = alloc.create(std.Io.Threaded) catch {
            alloc.destroy(e);
            return null;
        };
        threaded.* = .init(alloc, .{});
        e.* = .{
            .alloc_ptr = alloc.ptr,
            .alloc_vtable = @ptrCast(alloc.vtable),
            .io_threaded = threaded,
        };
        return e;
    }
}`,
		},
		{
			file: "src/api/instance.zig",
			old: `    if (handle.io_threaded) |t_opaque| {
        const t: *std.Io.Threaded = @ptrCast(@alignCast(t_opaque));
        t.deinit();
        alloc.destroy(t);
    }`,
			new: `    if (comptime builtin.os.tag != .freestanding) {
        if (handle.io_threaded) |t_opaque| {
            const t: *std.Io.Threaded = @ptrCast(@alignCast(t_opaque));
            t.deinit();
            alloc.destroy(t);
        }
    }`,
		},
		{
			file: "src/api/instance.zig",
			old: `    if (handle.wasi_host) |host_opaque| {
        const host: *wasi_host.Host = @ptrCast(@alignCast(host_opaque));
        host.deinit();
        alloc.destroy(host);
    }
    // Hosts a ` + "`zwasm_store_set_wasi`" + ` could not free because an instance
    // had captured them. Nothing can reach them now: every runtime that
    // held one is gone with the instances + zombies reaped above.
    for (handle.retired_wasi_hosts.items) |host_opaque| {
        const host: *wasi_host.Host = @ptrCast(@alignCast(host_opaque));
        host.deinit();
        std.heap.c_allocator.destroy(host);
    }
    handle.retired_wasi_hosts.deinit(std.heap.c_allocator);`,
			new: `    if (comptime builtin.os.tag != .freestanding) {
        if (handle.wasi_host) |host_opaque| {
            const host: *wasi_host.Host = @ptrCast(@alignCast(host_opaque));
            host.deinit();
            alloc.destroy(host);
        }
        for (handle.retired_wasi_hosts.items) |host_opaque| {
            const host: *wasi_host.Host = @ptrCast(@alignCast(host_opaque));
            host.deinit();
            c_alloc.destroy(host);
        }
        handle.retired_wasi_hosts.deinit(c_alloc);
    }`,
		},
		{
			file: "src/api/instance.zig",
			old: `pub export fn zwasm_store_set_wasi(s: ?*Store, h: ?*wasi_host.Host) callconv(.c) void {
    const store = s orelse return;`,
			new: `pub export fn zwasm_store_set_wasi(s: ?*Store, h: ?*wasi_host.Host) callconv(.c) void {
    if (comptime builtin.os.tag == .freestanding) return;
    if (comptime builtin.os.tag != .freestanding) {
        const store = s orelse return;`,
		},
		{
			file: "src/api/instance.zig",
			old: `    store.wasi_host = if (h) |hp| @as(*anyopaque, @ptrCast(hp)) else null;
}

/// ` + "`zwasm_store_wasi_exit_code(*Store, *u32)`",
			new: `        store.wasi_host = if (h) |hp| @as(*anyopaque, @ptrCast(hp)) else null;
    }
}

/// ` + "`zwasm_store_wasi_exit_code(*Store, *u32)`",
		},
		{
			file: "src/api/instance.zig",
			old: `pub fn activeWasiHost(store: *const Store) ?*wasi_host.Host {
    if (store.wasi_host) |h| {`,
			new: `pub fn activeWasiHost(store: *const Store) ?*wasi_host.Host {
    if (comptime @import("build_options").wasi_level == .none) return null;
    if (store.wasi_host) |h| {`,
		},
		{
			file: "src/api/instance.zig",
			old: `        if (isWasiImport(it) and slotIsNull(imports_array, idx)) {
            if (it.kind != .func) return error.UnsupportedWasiImport;
            const thunk = wasi.lookupWasiThunk(it.name) orelse return error.UnsupportedWasiImport;
            const wasi_host_ptr = store.wasi_host orelse return error.WasiNotConfigured;
            // The binding outlives this call — and outlives the instance
            // too, since ` + "`wasm_instance_delete`" + ` parks the arena holding it
            // on ` + "`store.zombies`" + `. May over-mark (this also runs on the
            // throwaway arena in ` + "`collectFuncImportTargets`" + `); over-marking
            // only defers a free, so nothing compensates for it.
            //
            // ADR-0224 — reserve the retirement slot HERE, at capture, not at
            // the swap. ` + "`activeWasiHost`" + ` finds a displaced host only through
            // ` + "`retired_wasi_hosts`" + `, so a host that fails to reach that list is
            // not merely leaked (the pre-ADR-0224 cost) but unreadable: its
            // instances stay callable and their exits report nothing.
            // Reserving at capture puts the allocation failure on the
            // instantiation, which can and does report it.
            try store.retired_wasi_hosts.ensureUnusedCapacity(std.heap.c_allocator, 1);
            store.wasi_host_captured = true;
            bindings[idx] = .{ .func = .{
                .host_call = .{ .fn_ptr = thunk, .ctx = wasi_host_ptr },
                .source = .wasi,
            } };`,
			new: `        if (isWasiImport(it) and slotIsNull(imports_array, idx)) {
            if (comptime @import("build_options").wasi_level == .none) {
                return error.WasiNotConfigured;
            } else {
                if (it.kind != .func) return error.UnsupportedWasiImport;
                const thunk = wasi.lookupWasiThunk(it.name) orelse return error.UnsupportedWasiImport;
                const wasi_host_ptr = store.wasi_host orelse return error.WasiNotConfigured;
                try store.retired_wasi_hosts.ensureUnusedCapacity(c_alloc, 1);
                store.wasi_host_captured = true;
                bindings[idx] = .{ .func = .{
                    .host_call = .{ .fn_ptr = thunk, .ctx = wasi_host_ptr },
                    .source = .wasi,
                } };
            }`,
		},
		{
			file: "src/api/instance.zig",
			old:  `std.heap.c_allocator`,
			new:  `c_alloc`,
		},
		{
			file: "src/api/instance.zig",
			old:  `const std = @import("std");`,
			new: `const std = @import("std");
const c_alloc: std.mem.Allocator = if (builtin.os.tag == .freestanding) undefined else std.heap.c_allocator;`,
		},
		// 7. api/vec.zig, api/types.zig, api/module_introspect.zig, api/trap_surface.zig
		{
			file: "src/api/vec.zig",
			old:  `std.heap.c_allocator`,
			new:  `c_alloc`,
		},
		{
			file: "src/api/types.zig",
			old:  `const ca = std.heap.c_allocator;`,
			new:  `const ca: std.mem.Allocator = if (@import("builtin").os.tag == .freestanding) undefined else std.heap.c_allocator;`,
		},
		{
			file: "src/api/module_introspect.zig",
			old:  `const ca = std.heap.c_allocator;`,
			new:  `const ca: std.mem.Allocator = if (@import("builtin").os.tag == .freestanding) undefined else std.heap.c_allocator;`,
		},
		{
			file: "src/api/trap_surface.zig",
			old:  `std.heap.c_allocator`,
			new:  `c_alloc`,
		},
		{
			file: "src/api/vec.zig",
			old:  `const std = @import("std");`,
			new: `const std = @import("std");
const c_alloc: std.mem.Allocator = if (@import("builtin").os.tag == .freestanding) undefined else std.heap.c_allocator;`,
		},
		{
			file: "src/api/trap_surface.zig",
			old:  `const std = @import("std");`,
			new: `const std = @import("std");
const c_alloc: std.mem.Allocator = if (@import("builtin").os.tag == .freestanding) undefined else std.heap.c_allocator;`,
		},
		{
			file: "src/platform/signal.zig",
			old: `            const rip: usize = @intCast(exception_info.ContextRecord.Rip);
            if (trap_registry.classify(fault_addr, rip)) |stub| {
                exception_info.ContextRecord.Rip = stub;`,
			new: `            const pc = exception_info.ContextRecord.getRegs().ip;
            if (trap_registry.classify(fault_addr, pc)) |stub| {
                exception_info.ContextRecord.setIp(stub);`,
		},
		// 8. wasi/fd.zig, wasi/path.zig, wasi/host.zig, wasi/jit_dispatch.zig
		{
			file: "src/wasi/fd.zig",
			old:  `        self_ino = @intCast((self_file.stat(io) catch return .io).inode);`,
			new: `        const st_self = self_file.stat(io) catch return .io;
        self_ino = if (comptime @import("builtin").os.tag != .freestanding) @intCast(st_self.inode) else 0;`,
		},
		{
			file: "src/wasi/fd.zig",
			old:  `            if (!writeDirent(buf, &written, idx + 1, @intCast(entry.inode), kindToFiletype(entry.kind), entry.name)) break;`,
			new: `            const ino: u64 = if (comptime @import("builtin").os.tag != .freestanding) @intCast(entry.inode) else 0;
            if (!writeDirent(buf, &written, idx + 1, ino, kindToFiletype(entry.kind), entry.name)) break;`,
		},
		{
			file: "src/wasi/fd.zig",
			old: `                .ino = @intCast(st.inode),
                .filetype = kindToFiletype(st.kind),
                .nlink = @intCast(st.nlink),`,
			new: `                .ino = if (comptime @import("builtin").os.tag != .freestanding) @intCast(st.inode) else 0,
                .filetype = kindToFiletype(st.kind),
                .nlink = if (comptime @import("builtin").os.tag != .freestanding) @intCast(st.nlink) else 0,`,
		},
		{
			file: "src/wasi/path.zig",
			old: `        .ino = @intCast(st.inode),
        .filetype = filetypeFromKind(st.kind),
        .nlink = @intCast(st.nlink),`,
			new: `        .ino = if (comptime @import("builtin").os.tag != .freestanding) @intCast(st.inode) else 0,
        .filetype = filetypeFromKind(st.kind),
        .nlink = if (comptime @import("builtin").os.tag != .freestanding) @intCast(st.nlink) else 0,`,
		},
		{
			file: "src/wasi/host.zig",
			old: `    pub fn materializePendingPreopens(self: *Host) !void {
        if (self.pending_preopens.items.len == 0) return;
        const io = self.io orelse return error.NoHostIo;
        while (self.pending_preopens.items.len > 0) {
            const entry = self.pending_preopens.items[0];
            const dir = try std.Io.Dir.cwd().openDir(io, entry.host_path, .{ .iterate = true });
            errdefer dir.close(io);
            try self.owned_preopen_fds.append(self.alloc, dir.handle);
            errdefer _ = self.owned_preopen_fds.pop();
            _ = try self.addPreopen(dir.handle, entry.guest_path);
            self.alloc.free(entry.host_path);
            self.alloc.free(entry.guest_path);
            _ = self.pending_preopens.orderedRemove(0);
        }
    }`,
			new: `    pub fn materializePendingPreopens(self: *Host) !void {
        if (comptime @import("build_options").wasi_level == .none) {
            return;
        } else {
            if (self.pending_preopens.items.len == 0) return;
            const io = self.io orelse return error.NoHostIo;
            while (self.pending_preopens.items.len > 0) {
                const entry = self.pending_preopens.items[0];
                const dir = try std.Io.Dir.cwd().openDir(io, entry.host_path, .{ .iterate = true });
                errdefer dir.close(io);
                try self.owned_preopen_fds.append(self.alloc, dir.handle);
                errdefer _ = self.owned_preopen_fds.pop();
                _ = try self.addPreopen(dir.handle, entry.guest_path);
                self.alloc.free(entry.host_path);
                self.alloc.free(entry.guest_path);
                _ = self.pending_preopens.orderedRemove(0);
            }
        }
    }`,
		},
		{
			file: "src/wasi/jit_dispatch.zig",
			old: `pub fn lookup(module_name: []const u8, field_name: []const u8) ?usize {
    if (!std.mem.eql(u8, module_name, "wasi_snapshot_preview1") and`,
			new: `pub fn lookup(module_name: []const u8, field_name: []const u8) ?usize {
    if (comptime @import("build_options").wasi_level == .none) return null;
    if (comptime @import("build_options").wasi_level != .none) {
        if (!std.mem.eql(u8, module_name, "wasi_snapshot_preview1") and`,
		},
		{
			file: "src/wasi/jit_dispatch.zig",
			old: `    for (table) |p| {
        if (std.mem.eql(u8, p.name, field_name)) return p.ptr;
    }
    return null;
}

/// Walk imports`,
			new: `    for (table) |p| {
        if (std.mem.eql(u8, p.name, field_name)) return p.ptr;
    }
    return null;
    }
}

/// Walk imports`,
		},
		// 9. support/dbg.zig, engine/setup.zig, engine/compile.zig, engine/codegen/shared/compile.zig, ir/analysis/liveness.zig, interp/mvp.zig
		{
			file: "src/support/dbg.zig",
			old:  `    if (builtin.mode == .fast or builtin.mode == .small) {`,
			new:  `    if (builtin.mode == .fast or builtin.mode == .small or builtin.os.tag == .freestanding) {`,
		},
		{
			file: "src/support/dbg.zig",
			old: `pub fn on(comptime mod: []const u8) bool {
    return enabled(mod);
}`,
			new: `pub fn on(comptime mod: []const u8) bool {
    if (builtin.os.tag == .freestanding) return false;
    return enabled(mod);
}`,
		},
		{
			file: "src/engine/setup.zig",
			old: `        if (f_off != linker.IMPORT_SENTINEL_OFFSET and dbg.on("jit.dump")) {
            std.debug.print("[jit.dump] func={d} runtime_addr=0x{x}\n", .{ i, funcptr });
        }`,
			new: `        if (comptime @import("builtin").os.tag != .freestanding) {
            if (f_off != linker.IMPORT_SENTINEL_OFFSET and dbg.on("jit.dump")) {
                std.debug.print("[jit.dump] func={d} runtime_addr=0x{x}\n", .{ i, funcptr });
            }
        }`,
		},
		{
			file: "src/engine/compile.zig",
			old: `        if (dbg.on("jit.dump")) {
            const wasm_idx = num_imports + @as(u32, @intCast(i));
            std.debug.print("[jit.dump] func={d} len={d} hex=", .{ wasm_idx, r.out.bytes.len });
            for (r.out.bytes) |b| std.debug.print("{x:0>2}", .{b});
            std.debug.print("\n", .{});
        }`,
			new: `        if (comptime @import("builtin").os.tag != .freestanding) {
            if (dbg.on("jit.dump")) {
                const wasm_idx = num_imports + @as(u32, @intCast(i));
                std.debug.print("[jit.dump] func={d} len={d} hex=", .{ wasm_idx, r.out.bytes.len });
                for (r.out.bytes) |b| std.debug.print("{x:0>2}", .{b});
                std.debug.print("\n", .{});
            }
        }`,
		},
		{
			file: "src/engine/codegen/shared/compile.zig",
			old: `    if (dbg.on("regverify")) {
        regalloc.verifyWith(&func, alloc, scratch_reservations) catch |e| {
            std.debug.print("[regverify] func[{d}] FAIL: {s} (n_slots={d})\n", .{ func_idx, @errorName(e), alloc.n_slots });
        };
    }`,
			new: `    if (comptime @import("builtin").os.tag != .freestanding) {
        if (dbg.on("regverify")) {
            regalloc.verifyWith(&func, alloc, scratch_reservations) catch |e| {
                std.debug.print("[regverify] func[{d}] FAIL: {s} (n_slots={d})\n", .{ func_idx, @errorName(e), alloc.n_slots });
            };
        }
    }`,
		},
		{
			file: "src/ir/analysis/liveness.zig",
			old:  `std.debug.print(`,
			new:  `if (comptime @import("builtin").os.tag != .freestanding) std.debug.print(`,
		},
		{
			file: "src/interp/mvp.zig",
			old:  `std.debug.print(`,
			new:  `if (comptime @import("builtin").os.tag != .freestanding) std.debug.print(`,
		},
		// 10. runtime/instance/memory_backing.zig: fixed-base linear memory on freestanding
		{
			file: "src/runtime/instance/memory_backing.zig",
			old: `const std = @import("std");
const guarded_mem = @import("../../platform/guarded_mem.zig");`,
			new: `const std = @import("std");
const builtin = @import("builtin");
const guarded_mem = @import("../../platform/guarded_mem.zig");

const freestanding_fixed_mem = if (builtin.os.tag == .freestanding) struct {
    extern fn mohabbat_raw_alloc(size: usize, align_bytes: usize) callconv(.c) ?[*]u8;
    extern fn mohabbat_raw_free(ptr: [*]u8, size: usize) callconv(.c) void;
    const max_guest_linear_bytes: usize = 128 * 1024 * 1024;
} else struct {};`,
		},
		{
			file: "src/runtime/instance/memory_backing.zig",
			old: `pub fn allocBacking(
    alloc: std.mem.Allocator,
    bytes_total: usize,
    idx_type: sections.MemoryEntry.IdxType,
    page_size_log2: u8,
) error{OutOfMemory}!Backing {`,
			new: `pub fn allocBacking(
    alloc: std.mem.Allocator,
    bytes_total: usize,
    idx_type: sections.MemoryEntry.IdxType,
    page_size_log2: u8,
) error{OutOfMemory}!Backing {
    if (comptime builtin.os.tag == .freestanding) {
        if (bytes_total > freestanding_fixed_mem.max_guest_linear_bytes) return error.OutOfMemory;
        const raw = freestanding_fixed_mem.mohabbat_raw_alloc(freestanding_fixed_mem.max_guest_linear_bytes, 65536) orelse return error.OutOfMemory;
        if (bytes_total > 0) @memset(raw[0..bytes_total], 0);
        const aligned: [*]align(guarded_mem.page_size) u8 = @ptrCast(@alignCast(raw));
        return .{
            .bytes = raw[0..bytes_total],
            .reservation = .{
                .base = aligned,
                .reserve_len = freestanding_fixed_mem.max_guest_linear_bytes,
                .committed = bytes_total,
            },
        };
    }`,
		},
		{
			file: "src/runtime/instance/memory_backing.zig",
			old: `pub fn growGuarded(res: *guarded_mem.Reservation, new_bytes: usize) ?[]u8 {
    if (new_bytes > (1 << 32)) return null;`,
			new: `pub fn growGuarded(res: *guarded_mem.Reservation, new_bytes: usize) ?[]u8 {
    if (comptime builtin.os.tag == .freestanding) {
        if (new_bytes > res.reserve_len) return null;
        if (new_bytes > res.committed) {
            @memset(res.base[res.committed..new_bytes], 0);
            res.committed = new_bytes;
        }
        return res.base[0..new_bytes];
    }
    if (new_bytes > (1 << 32)) return null;`,
		},
		{
			file: "src/runtime/instance/memory_backing.zig",
			old: `pub fn freeBacking(alloc: std.mem.Allocator, b: Backing) void {
    if (b.reservation) |res| {`,
			new: `pub fn freeBacking(alloc: std.mem.Allocator, b: Backing) void {
    if (comptime builtin.os.tag == .freestanding) {
        if (b.reservation) |res| {
            freestanding_fixed_mem.mohabbat_raw_free(res.base, res.reserve_len);
            return;
        }
        if (b.bytes.len > 0) alloc.free(b.bytes);
        return;
    }
    if (b.reservation) |res| {`,
		},
		// 12. Enable .jit engine on Linker + raise jit_host_bridge MAX_ARITY to 8
		{
			file: "src/api/handles.zig",
			old: `pub const HostFuncPayload = struct {
    callback: ?WasmFuncCallback = null,
    callback_env: ?WasmFuncCallbackEnv = null,
    env: ?*anyopaque = null,
    finalizer: ?*const fn (?*anyopaque) callconv(.c) void = null,
    params: []zir.ValType,
    results: []zir.ValType,
};`,
			new: `pub const JitCallbackFn = *const fn (*anyopaque, *anyopaque, []const Val, []Val) ?*anyopaque;

pub const HostFuncPayload = struct {
    callback: ?WasmFuncCallback = null,
    callback_env: ?WasmFuncCallbackEnv = null,
    jit_callback: ?JitCallbackFn = null,
    env: ?*anyopaque = null,
    finalizer: ?*const fn (?*anyopaque) callconv(.c) void = null,
    params: []zir.ValType,
    results: []zir.ValType,
};`,
		},
		{
			file: "src/api/jit_host_bridge.zig",
			old: `const MAX_ARITY = 4;`,
			new: `const MAX_ARITY = 8;`,
		},
		{
			file: "src/api/jit_host_bridge.zig",
			old: `    const trap: ?*Trap =
        if (payload.callback_env) |cb|
            cb(payload.env, &args_vec, &res_vec)
        else if (payload.callback) |cb|
            cb(&args_vec, &res_vec)
        else
            return trapResult(rt, r);`,
			new: `    const trap: ?*Trap =
        if (payload.jit_callback) |jcb|
            @ptrCast(@alignCast(jcb(@ptrCast(rt), payload.env.?, args, res_storage[0..nr])))
        else if (payload.callback_env) |cb|
            cb(payload.env, &args_vec, &res_vec)
        else if (payload.callback) |cb|
            cb(&args_vec, &res_vec)
        else
            return trapResult(rt, r);`,
		},
		{
			file: "src/api/jit_host_bridge.zig",
			old: `fn t4(comptime K: usize, comptime r: RetKind) *const fn (*JitRuntime, u64, u64, u64, u64) callconv(.c) RT(r) {
    return &struct {
        fn f(rt: *JitRuntime, a0: u64, a1: u64, a2: u64, a3: u64) callconv(.c) RT(r) {
            const a = [_]u64{ a0, a1, a2, a3 };
            return bridge(rt, K, &a, r);
        }
    }.f;
}`,
			new: `fn t4(comptime K: usize, comptime r: RetKind) *const fn (*JitRuntime, u64, u64, u64, u64) callconv(.c) RT(r) {
    return &struct {
        fn f(rt: *JitRuntime, a0: u64, a1: u64, a2: u64, a3: u64) callconv(.c) RT(r) {
            const a = [_]u64{ a0, a1, a2, a3 };
            return bridge(rt, K, &a, r);
        }
    }.f;
}
fn t5(comptime K: usize, comptime r: RetKind) *const fn (*JitRuntime, u64, u64, u64, u64, u64) callconv(.c) RT(r) {
    return &struct {
        fn f(rt: *JitRuntime, a0: u64, a1: u64, a2: u64, a3: u64, a4: u64) callconv(.c) RT(r) {
            const a = [_]u64{ a0, a1, a2, a3, a4 };
            return bridge(rt, K, &a, r);
        }
    }.f;
}
fn t6(comptime K: usize, comptime r: RetKind) *const fn (*JitRuntime, u64, u64, u64, u64, u64, u64) callconv(.c) RT(r) {
    return &struct {
        fn f(rt: *JitRuntime, a0: u64, a1: u64, a2: u64, a3: u64, a4: u64, a5: u64) callconv(.c) RT(r) {
            const a = [_]u64{ a0, a1, a2, a3, a4, a5 };
            return bridge(rt, K, &a, r);
        }
    }.f;
}
fn t7(comptime K: usize, comptime r: RetKind) *const fn (*JitRuntime, u64, u64, u64, u64, u64, u64, u64) callconv(.c) RT(r) {
    return &struct {
        fn f(rt: *JitRuntime, a0: u64, a1: u64, a2: u64, a3: u64, a4: u64, a5: u64, a6: u64) callconv(.c) RT(r) {
            const a = [_]u64{ a0, a1, a2, a3, a4, a5, a6 };
            return bridge(rt, K, &a, r);
        }
    }.f;
}
fn t8(comptime K: usize, comptime r: RetKind) *const fn (*JitRuntime, u64, u64, u64, u64, u64, u64, u64, u64) callconv(.c) RT(r) {
    return &struct {
        fn f(rt: *JitRuntime, a0: u64, a1: u64, a2: u64, a3: u64, a4: u64, a5: u64, a6: u64, a7: u64) callconv(.c) RT(r) {
            const a = [_]u64{ a0, a1, a2, a3, a4, a5, a6, a7 };
            return bridge(rt, K, &a, r);
        }
    }.f;
}`,
		},
		{
			file: "src/api/jit_host_bridge.zig",
			old: `            4 => ptrFor(t4, r, idx),
            else => null,`,
			new: `            4 => ptrFor(t4, r, idx),
            5 => ptrFor(t5, r, idx),
            6 => ptrFor(t6, r, idx),
            7 => ptrFor(t7, r, idx),
            8 => ptrFor(t8, r, idx),
            else => null,`,
		},
		{
			file: "src/zwasm/memory.zig",
			old: `const _runner = @import("../engine/runner.zig"); // ADR-0200 JIT engine (Zone 2; ` + "`lib`" + `-exempt)`,
			new: `const _runner = @import("../engine/runner.zig"); // ADR-0200 JIT engine (Zone 2; ` + "`lib`" + `-exempt)
const _jit_abi = @import("../engine/codegen/shared/jit_abi.zig");`,
		},
		{
			file: "src/zwasm/memory.zig",
			old: `    pub const Backing = union(enum) {
        interp: *_runtime.Runtime,
        jit: *_runner.JitInstance,
    };`,
			new: `    pub const Backing = union(enum) {
        interp: *_runtime.Runtime,
        jit: *_runner.JitInstance,
        jit_rt: *_jit_abi.JitRuntime,
    };`,
		},
		{
			file: "src/zwasm/memory.zig",
			old: `            .jit => |jit| jit.owned.rt.vm_base[0..jit.owned.rt.mem_limit],`,
			new: `            .jit => |jit| jit.owned.rt.vm_base[0..jit.owned.rt.mem_limit],
            .jit_rt => |jrt| jrt.vm_base[0..jrt.mem_limit],`,
		},
		{
			file: "src/zwasm/caller.zig",
			old: `const _runtime = @import("../runtime/runtime.zig");
const _memory = @import("memory.zig");

pub const Caller = struct {
    rt: *_runtime.Runtime,
    /// Host context registered with the import via ` + "`Linker.defineFuncCtx`" + `
    /// (wasmtime's ` + "`Caller::data`" + `). Null for ` + "`defineFunc`" + `-registered host fns
    /// that need no external state. Recover the typed pointer via ` + "`data`" + `.
    host_data: ?*anyopaque = null,

    pub fn memory(self: Caller) ?_memory.Memory {
        if (self.rt.memory.len == 0) return null;
        // Linker host-fn path is interp-only (ADR-0200): wrap the runtime.
        return .{ .backing = .{ .interp = self.rt } };
    }

    pub fn allocator(self: Caller) std.mem.Allocator {
        return self.rt.alloc;
    }`,
			new: `const _runtime = @import("../runtime/runtime.zig");
const _jit_abi = @import("../engine/codegen/shared/jit_abi.zig");
const _memory = @import("memory.zig");

pub const Caller = struct {
    rt: ?*_runtime.Runtime = null,
    jit_rt: ?*_jit_abi.JitRuntime = null,
    host_data: ?*anyopaque = null,

    pub fn memory(self: Caller) ?_memory.Memory {
        if (self.jit_rt) |jrt| {
            if (jrt.mem_limit == 0) return null;
            return .{ .backing = .{ .jit_rt = jrt } };
        }
        const rt = self.rt orelse return null;
        if (rt.memory.len == 0) return null;
        return .{ .backing = .{ .interp = rt } };
    }

    pub fn allocator(self: Caller) std.mem.Allocator {
        return self.rt.?.alloc;
    }`,
		},
		{
			file: "src/zwasm/host_func_marshal.zig",
			old: `const _caller = @import("caller.zig");

pub const Caller = _caller.Caller;`,
			new: `const _caller = @import("caller.zig");
const _api_handles = @import("../api/handles.zig");
const _jit_abi = @import("../engine/codegen/shared/jit_abi.zig");

pub const Caller = _caller.Caller;`,
		},
		{
			file: "src/zwasm/host_func_marshal.zig",
			old: `/// A RUNTIME-arity host fn:`,
			new: `fn valToZig(comptime T: type, v: _api_handles.Val) T {
    return switch (T) {
        i32 => v.of.i32,
        u32 => @bitCast(v.of.i32),
        i64 => v.of.i64,
        u64 => @bitCast(v.of.i64),
        f32 => v.of.f32,
        f64 => v.of.f64,
        else => @compileError("host fn: unsupported param type " ++ @typeName(T)),
    };
}

fn zigToVal(comptime T: type, v: T) _api_handles.Val {
    return switch (T) {
        i32 => .{ .kind = .i32, .of = .{ .i32 = v } },
        u32 => .{ .kind = .i32, .of = .{ .i32 = @bitCast(v) } },
        i64 => .{ .kind = .i64, .of = .{ .i64 = v } },
        u64 => .{ .kind = .i64, .of = .{ .i64 = @bitCast(v) } },
        f32 => .{ .kind = .f32, .of = .{ .f32 = v } },
        f64 => .{ .kind = .f64, .of = .{ .f64 = v } },
        else => @compileError("host fn: unsupported result type " ++ @typeName(T)),
    };
}

pub fn jitThunkFor(comptime Sig: type) _api_handles.JitCallbackFn {
    const fn_info = @typeInfo(Sig).@"fn";
    return struct {
        fn t(jit_rt_raw: *anyopaque, ctx: *anyopaque, args_vals: []const _api_handles.Val, res_vals: []_api_handles.Val) ?*anyopaque {
            const wrapper: *HostFnCtx(Sig) = @ptrCast(@alignCast(ctx));
            const jrt: *_jit_abi.JitRuntime = @ptrCast(@alignCast(jit_rt_raw));
            const ArgsT = std.meta.ArgsTuple(Sig);
            var args: ArgsT = undefined;
            inline for (1..fn_info.param_types.len) |i| {
                const PT = fn_info.param_types[i].?;
                args[i] = valToZig(PT, args_vals[i - 1]);
            }
            var caller: Caller = .{ .jit_rt = jrt, .host_data = wrapper.host_data };
            args[0] = &caller;
            const ret = @call(.auto, wrapper.user_fn, args);
            const Ret = fn_info.return_type.?;
            if (Ret == void) return null;
            switch (@typeInfo(Ret)) {
                .error_union => |eu| {
                    const ok = ret catch return @ptrFromInt(1);
                    if (eu.payload != void) res_vals[0] = zigToVal(eu.payload, ok);
                },
                else => res_vals[0] = zigToVal(Ret, ret),
            }
            return null;
        }
    }.t;
}

/// A RUNTIME-arity host fn:`,
		},
		{
			file: "src/api/instance.zig",
			old: `fn hostFuncThunk(rt: *runtime.Runtime, ctx: *anyopaque) anyerror!void {`,
			new: `pub fn hostFuncThunkPtr() *const fn (*runtime.Runtime, *anyopaque) anyerror!void {
    return &hostFuncThunk;
}

fn hostFuncThunk(rt: *runtime.Runtime, ctx: *anyopaque) anyerror!void {`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `    pub const HostFuncEntry = struct {
        thunk_fn: *const fn (*_runtime.Runtime, *anyopaque) anyerror!void,
        ctx: *anyopaque,
        params: []const _zir.ValType,
        results: []const _zir.ValType,
    };`,
			new: `    pub const HostFuncEntry = struct {
        thunk_fn: *const fn (*_runtime.Runtime, *anyopaque) anyerror!void,
        jit_thunk_fn: ?_api_handles.JitCallbackFn = null,
        ctx: *anyopaque,
        params: []const _zir.ValType,
        results: []const _zir.ValType,
    };`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `const _api_instance = @import("../api/instance.zig");`,
			new: `const _api_instance = @import("../api/instance.zig");
const _api_handles = @import("../api/handles.zig");`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `            .payload = .{ .host_func = .{
                .thunk_fn = _marshal.thunkFor(Sig),
                .ctx = ctx_ptr,
                .params = sig.params,
                .results = sig.results,
            } },`,
			new: `            .payload = .{ .host_func = .{
                .thunk_fn = _marshal.thunkFor(Sig),
                .jit_thunk_fn = _marshal.jitThunkFor(Sig),
                .ctx = ctx_ptr,
                .params = sig.params,
                .results = sig.results,
            } },`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `                                bindings_list.append(scratch, .{
                                    .func = .{
                                        .host_call = .{ .fn_ptr = host.thunk_fn, .ctx = host.ctx },
                                        .source = .wasi,
                                    },
                                }) catch return error.OutOfMemory;`,
			new: `                                if (opts.engine != .interp and host.jit_thunk_fn != null) {
                                    const payload = self.engine.alloc.create(_api_handles.HostFuncPayload) catch return error.OutOfMemory;
                                    payload.* = .{
                                        .jit_callback = host.jit_thunk_fn,
                                        .env = host.ctx,
                                        .params = @constCast(host.params),
                                        .results = @constCast(host.results),
                                    };
                                    self.ctx_storage.append(self.engine.alloc, .{
                                        .ptr = payload,
                                        .destroy_fn = struct {
                                            fn d(a: Allocator, p: *anyopaque) void {
                                                a.destroy(@as(*_api_handles.HostFuncPayload, @ptrCast(@alignCast(p))));
                                            }
                                        }.d,
                                    }) catch return error.OutOfMemory;
                                    bindings_list.append(scratch, .{
                                        .func = .{
                                            .host_call = .{ .fn_ptr = _api_instance.hostFuncThunkPtr(), .ctx = payload },
                                            .source = .wasi,
                                        },
                                    }) catch return error.OutOfMemory;
                                } else {
                                    bindings_list.append(scratch, .{
                                        .func = .{
                                            .host_call = .{ .fn_ptr = host.thunk_fn, .ctx = host.ctx },
                                            .source = .wasi,
                                        },
                                    }) catch return error.OutOfMemory;
                                }`,
		},
		{
			file: "src/zwasm/linker.zig",
			old: `        const inst_ptr = _api_instance.instantiateInternal(mod.c_store, mod.c_handle, pre.asBuilder(), null, limits, .interp) orelse return error.InstantiateFailed;`,
			new: `        const inst_ptr = _api_instance.instantiateInternal(mod.c_store, mod.c_handle, pre.asBuilder(), null, limits, opts.engine) orelse return error.InstantiateFailed;`,
		},
		// 13. Periodic AArch64 veneer islands in codegen/shared/linker.zig for >128 MiB JIT modules
		{
			file: "src/engine/codegen/shared/linker.zig",
			old: `    var total_size: usize = 0;
    var offsets = try allocator.alloc(u32, total_funcs);
    errdefer allocator.free(offsets);
    // Imports occupy slots [0..num_imports); fill with sentinel.
    @memset(offsets[0..num_imports], IMPORT_SENTINEL_OFFSET);
    for (func_bodies, 0..) |body, i| {
        offsets[num_imports + i] = @intCast(total_size);
        total_size += body.bytes.len;
        // Bodies emit only word-aligned content; no padding needed.
    }
    if (total_size == 0) return Error.AllocationFailed;

    var block = try jit_mem.alloc(total_size);
    errdefer jit_mem.free(block);

    try jit_mem.setWritable(block);
    for (func_bodies, 0..) |body, i| {
        const off = offsets[num_imports + i];
        @memcpy(block.bytes[off..][0..body.bytes.len], body.bytes);
    }

    // Patch every CALL/BL placeholder. Each fixup's byte_offset
    // is function-local; add the function's own base offset to
    // get its absolute byte position. The encoding differs per
    // arch (BL imm26 on ARM64; CALL rel32 on x86_64) — comptime
    // switch picks the right path with no runtime cost.
    for (func_bodies, 0..) |body, i| {
        const base = offsets[num_imports + i];
        for (body.call_fixups) |fx| {
            // CallFixups carry wasm-space indices. Imports are
            // routed via the trap stub by the emit pass — they
            // must never appear here. A sentinel target is a
            // structural emit-pass bug (post-chunk-b invariant).
            if (fx.target_func_idx >= total_funcs) return Error.UnknownCallTarget;
            if (fx.target_func_idx < num_imports) return Error.UnknownCallTarget;
            const fixup_abs: i64 = @as(i64, base) + @as(i64, fx.byte_offset);
            const target_abs: i64 = offsets[fx.target_func_idx];
            switch (builtin_arch.target.cpu.arch) {
                .aarch64 => {
                    const disp_bytes = target_abs - fixup_abs;
                    if (@rem(disp_bytes, 4) != 0) return Error.DisplacementOverflow;
                    const disp_words = @divExact(disp_bytes, 4);
                    // imm26 signed range: ±2^25 words = ±128 MiB.
                    if (disp_words < -(1 << 25) or disp_words >= (1 << 25)) {
                        return Error.DisplacementOverflow;
                    }
                    // ADR-0112 D4: ` + "`is_tail = true`" + ` → B (0x14...);
                    // otherwise BL (0x94...). Same imm26 layout.
                    const new_word = if (fx.is_tail)
                        inst.encB(@intCast(disp_words))
                    else
                        inst.encBL(@intCast(disp_words));
                    std.mem.writeInt(u32, block.bytes[@intCast(fixup_abs)..][0..4], new_word, .little);
                },
                .x86_64 => {
                    // CALL rel32: 5-byte instruction (0xE8 +
                    // disp32). disp = target - (at + 5). i32
                    // signed range = ±2 GiB; flag overflow.
                    const disp_bytes = target_abs - fixup_abs - 5;
                    if (disp_bytes < std.math.minInt(i32) or disp_bytes > std.math.maxInt(i32)) {
                        return Error.DisplacementOverflow;
                    }
                    inst.patchRel32(block.bytes, @intCast(fixup_abs), 5, @intCast(disp_bytes));
                },
                else => @compileError("unsupported host arch for linker patch loop"),
            }
        }
    }`,
			new: `    var total_size: usize = 0;
    var offsets = try allocator.alloc(u32, total_funcs);
    errdefer allocator.free(offsets);
    // Imports occupy slots [0..num_imports); fill with sentinel.
    @memset(offsets[0..num_imports], IMPORT_SENTINEL_OFFSET);
    for (func_bodies, 0..) |body, i| {
        offsets[num_imports + i] = @intCast(total_size);
        total_size += body.bytes.len;
    }
    if (total_size == 0) return Error.AllocationFailed;

    const island_stride: usize = 64 * 1024 * 1024;
    const veneer_thresh: i64 = 48 * 1024 * 1024;
    const veneer_size: usize = 16;
    const use_islands = builtin_arch.target.cpu.arch == .aarch64 and total_size > 120 * 1024 * 1024;

    var island_cursors: []usize = &.{};
    var island_ends: []usize = &.{};
    var func_chunk: []u32 = &.{};
    defer if (use_islands) {
        allocator.free(island_cursors);
        allocator.free(island_ends);
        allocator.free(func_chunk);
    };

    if (use_islands) {
        const num_chunks = (total_size / island_stride) + 1;
        const island_caps = try allocator.alloc(usize, num_chunks);
        defer allocator.free(island_caps);
        @memset(island_caps, 0);

        island_cursors = try allocator.alloc(usize, num_chunks);
        island_ends = try allocator.alloc(usize, num_chunks);
        func_chunk = try allocator.alloc(u32, func_bodies.len);

        for (func_bodies, 0..) |body, i| {
            const raw_base = offsets[num_imports + i];
            const c: usize = @intCast(raw_base / island_stride);
            func_chunk[i] = @intCast(c);
            for (body.call_fixups) |fx| {
                if (fx.target_func_idx >= total_funcs or fx.target_func_idx < num_imports) return Error.UnknownCallTarget;
                const raw_fixup: i64 = @as(i64, raw_base) + @as(i64, fx.byte_offset);
                const raw_target: i64 = offsets[fx.target_func_idx];
                const dist = if (raw_target >= raw_fixup) raw_target - raw_fixup else raw_fixup - raw_target;
                if (dist > veneer_thresh) {
                    island_caps[c] += 1;
                }
            }
        }

        total_size = 0;
        var cur_chunk: usize = 0;
        for (func_bodies, 0..) |body, i| {
            const c: usize = func_chunk[i];
            if (c > cur_chunk) {
                while (cur_chunk < c) : (cur_chunk += 1) {
                    total_size = (total_size + 7) & ~@as(usize, 7);
                    island_cursors[cur_chunk] = total_size;
                    total_size += island_caps[cur_chunk] * veneer_size;
                    island_ends[cur_chunk] = total_size;
                }
            }
            offsets[num_imports + i] = @intCast(total_size);
            total_size += body.bytes.len;
        }
        while (cur_chunk < num_chunks) : (cur_chunk += 1) {
            total_size = (total_size + 7) & ~@as(usize, 7);
            island_cursors[cur_chunk] = total_size;
            total_size += island_caps[cur_chunk] * veneer_size;
            island_ends[cur_chunk] = total_size;
        }
    }

    var block = try jit_mem.alloc(total_size);
    errdefer jit_mem.free(block);

    try jit_mem.setWritable(block);
    for (func_bodies, 0..) |body, i| {
        const off = offsets[num_imports + i];
        @memcpy(block.bytes[off..][0..body.bytes.len], body.bytes);
    }

    for (func_bodies, 0..) |body, i| {
        const base = offsets[num_imports + i];
        for (body.call_fixups) |fx| {
            if (fx.target_func_idx >= total_funcs) return Error.UnknownCallTarget;
            if (fx.target_func_idx < num_imports) return Error.UnknownCallTarget;
            const fixup_abs: i64 = @as(i64, base) + @as(i64, fx.byte_offset);
            const target_abs: i64 = offsets[fx.target_func_idx];
            switch (builtin_arch.target.cpu.arch) {
                .aarch64 => {
                    var disp_bytes = target_abs - fixup_abs;
                    if (@rem(disp_bytes, 4) != 0) return Error.DisplacementOverflow;
                    var disp_words = @divExact(disp_bytes, 4);
                    if (disp_words < -(1 << 25) or disp_words >= (1 << 25)) {
                        if (!use_islands) return Error.DisplacementOverflow;
                        const c: usize = func_chunk[i];
                        const v_off = island_cursors[c];
                        if (v_off + veneer_size > island_ends[c]) return Error.DisplacementOverflow;
                        island_cursors[c] = v_off + veneer_size;

                        const target_addr: u64 = @intFromPtr(block.bytes.ptr) + @as(u64, @intCast(target_abs));
                        std.mem.writeInt(u32, block.bytes[v_off..][0..4], 0x58000050, .little); // LDR X16, #8
                        std.mem.writeInt(u32, block.bytes[v_off + 4 ..][0..4], inst.encBr(16), .little); // BR X16
                        std.mem.writeInt(u64, block.bytes[v_off + 8 ..][0..8], target_addr, .little);

                        disp_bytes = @as(i64, @intCast(v_off)) - fixup_abs;
                        if (@rem(disp_bytes, 4) != 0) return Error.DisplacementOverflow;
                        disp_words = @divExact(disp_bytes, 4);
                        if (disp_words < -(1 << 25) or disp_words >= (1 << 25)) {
                            return Error.DisplacementOverflow;
                        }
                    }
                    const new_word = if (fx.is_tail)
                        inst.encB(@intCast(disp_words))
                    else
                        inst.encBL(@intCast(disp_words));
                    std.mem.writeInt(u32, block.bytes[@intCast(fixup_abs)..][0..4], new_word, .little);
                },
                .x86_64 => {
                    const disp_bytes = target_abs - fixup_abs - 5;
                    if (disp_bytes < std.math.minInt(i32) or disp_bytes > std.math.maxInt(i32)) {
                        return Error.DisplacementOverflow;
                    }
                    inst.patchRel32(block.bytes, @intCast(fixup_abs), 5, @intCast(disp_bytes));
                },
                else => @compileError("unsupported host arch for linker patch loop"),
            }
        }
    }`,
		},
	}
}

func prepareZwasmOverlay(zigBuildDir string, verbose bool) (string, error) {
	upstreamDir := filepath.Join(zigBuildDir, zwasmUpstreamDir)
	overlayDir := filepath.Join(zigBuildDir, zwasmOverlayDir)

	if _, err := os.Stat(filepath.Join(upstreamDir, "src", "zwasm.zig")); err != nil {
		_ = os.RemoveAll(upstreamDir)
		cmd := exec.Command("git", "clone", "--depth", "1", zwasmRepoURL, upstreamDir)
		if verbose {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("fetch zwasm into %s: %w\n%s", upstreamDir, err, string(out))
		}
		_ = os.RemoveAll(filepath.Join(upstreamDir, ".git"))
	}

	patchesByFile := make(map[string][]zwasmPatch)
	for _, p := range zwasmOverlayPatches() {
		patchesByFile[p.file] = append(patchesByFile[p.file], p)
	}

	upstreamSrc := filepath.Join(upstreamDir, "src")
	overlaySrc := filepath.Join(overlayDir, "src")
	err := filepath.WalkDir(upstreamSrc, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(upstreamSrc, path)
		if err != nil {
			return err
		}
		target := filepath.Join(overlaySrc, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relKey := "src/" + filepath.ToSlash(rel)
		if plist, ok := patchesByFile[relKey]; ok {
			content := strings.ReplaceAll(string(raw), "\r\n", "\n")
			for idx, p := range plist {
				if !strings.Contains(content, p.old) {
					return fmt.Errorf("zwasm overlay patch #%d target not found in %s:\n%s", idx, p.file, p.old)
				}
				content = strings.ReplaceAll(content, p.old, p.new)
			}
			raw = []byte(content)
		}
		return writeFileIfChanged(target, raw, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("prepare zwasm overlay %s: %w", overlayDir, err)
	}

	return filepath.Join(overlaySrc, "zwasm.zig"), nil
}
