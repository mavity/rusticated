const builtin = @import("builtin");
const std = @import("std");
const pal_abi = @import("pal_abi");

pub inline fn sliceOrigin() [*]u8 {
    if (builtin.is_test) {
        @compileError("sliceOrigin() must not be called in unit tests");
    }
    const addr = switch (builtin.cpu.arch) {
        .x86_64 => asm ("lea runtime_code_start(%%rip), %[ret]"
            : [ret] "=r" (-> usize),
        ),
        .aarch64 => asm (
            \\adrp %[ret], runtime_code_start
            \\add %[ret], %[ret], :lo12:runtime_code_start
            : [ret] "=r" (-> usize),
        ),
        else => @compileError("Unsupported arch"),
    };
    return @ptrFromInt(addr);
}

pub inline fn abi() *const pal_abi.PalAbiTable {
    return @ptrCast(@alignCast(sliceOrigin() - pal_abi.PREFIX_ABI_OFFSET));
}

// ─── Slab / Arena Allocator over PalAbiTable ─────────────────────────────────

const BlockHeader = struct {
    chunk_ptr: [*]u8,
    chunk_size: usize,
    user_len: usize,
};

const MIN_CHUNK_SIZE: usize = 64 * 1024;

pub const PalArenaState = struct {
    cur_ptr: ?[*]u8 = null,
    cur_pos: usize = 0,
    cur_cap: usize = 0,
};

pub fn makePalAllocator(state: *PalArenaState) std.mem.Allocator {
    return .{
        .ptr = state,
        .vtable = &pal_alloc_vtable,
    };
}

const pal_alloc_vtable: std.mem.Allocator.VTable = .{
    .alloc = palAlloc,
    .resize = palResize,
    .remap = palRemap,
    .free = palFree,
};

fn alignUp(v: usize, a: usize) usize {
    return (v + a - 1) & ~(a - 1);
}

fn palAlloc(ctx: *anyopaque, len: usize, alignment: std.mem.Alignment, _: usize) ?[*]u8 {
    const s: *PalArenaState = @ptrCast(@alignCast(ctx));
    const align_bytes = @max(@as(usize, 16), alignment.toByteUnits());

    // Large allocations go directly to PalAbiTable.alloc_mem with a 16-byte header
    if (len >= MIN_CHUNK_SIZE / 2) {
        const hdr_pad = alignUp(@sizeOf(BlockHeader), align_bytes);
        const total = hdr_pad + len;
        const raw = abi().alloc_mem(total, align_bytes, pal_abi.MEM_READWRITE) orelse return null;
        const user_ptr = raw + hdr_pad;
        const hdr: *BlockHeader = @ptrCast(@alignCast(user_ptr - @sizeOf(BlockHeader)));
        hdr.* = .{ .chunk_ptr = raw, .chunk_size = total, .user_len = len };
        return user_ptr;
    }

    // Bump-allocate from current 64 KiB+ chunk
    if (s.cur_ptr) |cp| {
        const base_addr = @intFromPtr(cp);
        const hdr_end = alignUp(base_addr + s.cur_pos + @sizeOf(BlockHeader), align_bytes);
        const end_off = (hdr_end - base_addr) + len;
        if (end_off <= s.cur_cap) {
            s.cur_pos = end_off;
            const user_ptr: [*]u8 = @ptrFromInt(hdr_end);
            const hdr: *BlockHeader = @ptrCast(@alignCast(user_ptr - @sizeOf(BlockHeader)));
            hdr.* = .{ .chunk_ptr = cp, .chunk_size = 0, .user_len = len };
            return user_ptr;
        }
    }

    const chunk_size = @max(MIN_CHUNK_SIZE, len + align_bytes + @sizeOf(BlockHeader));
    const new_chunk = abi().alloc_mem(chunk_size, 4096, pal_abi.MEM_READWRITE) orelse return null;
    s.cur_ptr = new_chunk;
    s.cur_cap = chunk_size;
    const base_addr = @intFromPtr(new_chunk);
    const user_addr = alignUp(base_addr + @sizeOf(BlockHeader), align_bytes);
    s.cur_pos = (user_addr - base_addr) + len;
    const user_ptr: [*]u8 = @ptrFromInt(user_addr);
    const hdr: *BlockHeader = @ptrCast(@alignCast(user_ptr - @sizeOf(BlockHeader)));
    hdr.* = .{ .chunk_ptr = new_chunk, .chunk_size = 0, .user_len = len };
    return user_ptr;
}

fn palResize(_: *anyopaque, memory: []u8, _: std.mem.Alignment, new_len: usize, _: usize) bool {
    return new_len <= memory.len;
}

fn palRemap(ctx: *anyopaque, memory: []u8, alignment: std.mem.Alignment, new_len: usize, ret_addr: usize) ?[*]u8 {
    if (new_len <= memory.len) return memory.ptr;
    const new_ptr = palAlloc(ctx, new_len, alignment, ret_addr) orelse return null;
    @memcpy(new_ptr[0..memory.len], memory);
    palFree(ctx, memory, alignment, ret_addr);
    return new_ptr;
}

fn palFree(_: *anyopaque, memory: []u8, _: std.mem.Alignment, _: usize) void {
    if (memory.len == 0) return;
    const hdr: *const BlockHeader = @ptrCast(@alignCast(memory.ptr - @sizeOf(BlockHeader)));
    if (hdr.chunk_size != 0) {
        abi().free_mem(hdr.chunk_ptr, hdr.chunk_size);
    }
}

// ─── JIT Memory Hooks for zwasm/src/platform/jit_mem.zig ─────────────────────

pub fn exportJitHooks() void {
    const Hooks = struct {
        fn jitAlloc(size: usize) callconv(.c) ?[*]u8 {
            return abi().alloc_mem(size, 4096, pal_abi.MEM_CODE_JIT_RW);
        }
        fn jitFree(ptr: [*]u8, size: usize) callconv(.c) void {
            abi().free_mem(ptr, size);
        }
        fn jitSetExec(ptr: [*]u8, size: usize) callconv(.c) i32 {
            const rc = abi().protect_mem(ptr, size, pal_abi.MEM_CODE_EXEC_RX);
            if (rc == 0) abi().flush_icache(ptr, size);
            return rc;
        }
        fn jitSetWrite(ptr: [*]u8, size: usize) callconv(.c) i32 {
            return abi().protect_mem(ptr, size, pal_abi.MEM_CODE_JIT_RW);
        }
        fn rawAlloc(size: usize, align_bytes: usize) callconv(.c) ?[*]u8 {
            return abi().alloc_mem(size, align_bytes, pal_abi.MEM_READWRITE);
        }
        fn rawFree(ptr: [*]u8, size: usize) callconv(.c) void {
            abi().free_mem(ptr, size);
        }
    };
    @export(&Hooks.jitAlloc, .{ .name = "mohabbat_jit_alloc" });
    @export(&Hooks.jitFree, .{ .name = "mohabbat_jit_free" });
    @export(&Hooks.jitSetExec, .{ .name = "mohabbat_jit_set_executable" });
    @export(&Hooks.jitSetWrite, .{ .name = "mohabbat_jit_set_writable" });
    @export(&Hooks.rawAlloc, .{ .name = "mohabbat_raw_alloc" });
    @export(&Hooks.rawFree, .{ .name = "mohabbat_raw_free" });
}

// ─── Word-Width Freestanding Memory Intrinsics ───────────────────────────────

pub fn exportIntrinsics() void {
    const Intrinsics = struct {
        fn _memcpy(dest: [*]u8, src: [*]const u8, len: usize) callconv(.c) [*]u8 {
            var i: usize = 0;
            while (i + 8 <= len) : (i += 8) {
                const v = @as(*align(1) const volatile u64, @ptrCast(src + i)).*;
                @as(*align(1) volatile u64, @ptrCast(dest + i)).* = v;
            }
            while (i < len) : (i += 1) {
                @as(*volatile u8, @ptrCast(dest + i)).* = @as(*const volatile u8, @ptrCast(src + i)).*;
            }
            return dest;
        }
        fn _memset(dest: [*]u8, value: c_int, len: usize) callconv(.c) [*]u8 {
            const b: u8 = @truncate(@as(c_uint, @bitCast(value)));
            const word: u64 = @as(u64, b) * 0x0101_0101_0101_0101;
            var i: usize = 0;
            while (i + 8 <= len) : (i += 8) {
                @as(*align(1) volatile u64, @ptrCast(dest + i)).* = word;
            }
            while (i < len) : (i += 1) {
                @as(*volatile u8, @ptrCast(dest + i)).* = b;
            }
            return dest;
        }
        fn _memmove(dest: [*]u8, src: [*]const u8, len: usize) callconv(.c) [*]u8 {
            const d = @intFromPtr(dest);
            const s = @intFromPtr(src);
            if (d <= s or d >= s + len) {
                return _memcpy(dest, src, len);
            }
            var i = len;
            while (i >= 8) {
                i -= 8;
                const v = @as(*align(1) const volatile u64, @ptrCast(src + i)).*;
                @as(*align(1) volatile u64, @ptrCast(dest + i)).* = v;
            }
            while (i > 0) {
                i -= 1;
                @as(*volatile u8, @ptrCast(dest + i)).* = @as(*const volatile u8, @ptrCast(src + i)).*;
            }
            return dest;
        }
        fn _memcmp(left: [*]const u8, right: [*]const u8, len: usize) callconv(.c) c_int {
            for (0..len) |i| {
                if (left[i] != right[i]) return @as(c_int, left[i]) - @as(c_int, right[i]);
            }
            return 0;
        }
        fn _strlen(str: [*:0]const u8) callconv(.c) usize {
            var i: usize = 0;
            while (str[i] != 0) : (i += 1) {}
            return i;
        }
    };
    @export(&Intrinsics._memcpy, .{ .name = "memcpy" });
    @export(&Intrinsics._memset, .{ .name = "memset" });
    @export(&Intrinsics._memmove, .{ .name = "memmove" });
    @export(&Intrinsics._memcmp, .{ .name = "memcmp" });
    @export(&Intrinsics._memcmp, .{ .name = "bcmp" });
    @export(&Intrinsics._strlen, .{ .name = "strlen" });

    const Math = struct {
        fn _trunc(x: f64) callconv(.c) f64 {
            const bits: u64 = @bitCast(x);
            const exp: i32 = @as(i32, @intCast((bits >> 52) & 0x7ff)) - 1023;
            if (exp < 0) return if ((bits >> 63) != 0) -0.0 else 0.0;
            if (exp >= 52) return x;
            const mask = (@as(u64, 1) << @intCast(52 - exp)) - 1;
            return @bitCast(bits & ~mask);
        }
        fn _truncf(x: f32) callconv(.c) f32 {
            const bits: u32 = @bitCast(x);
            const exp: i32 = @as(i32, @intCast((bits >> 23) & 0xff)) - 127;
            if (exp < 0) return if ((bits >> 31) != 0) -0.0 else 0.0;
            if (exp >= 23) return x;
            const mask = (@as(u32, 1) << @intCast(23 - exp)) - 1;
            return @bitCast(bits & ~mask);
        }
        fn _floor(x: f64) callconv(.c) f64 {
            const t = _trunc(x);
            return if (x < t) t - 1.0 else t;
        }
        fn _floorf(x: f32) callconv(.c) f32 {
            const t = _truncf(x);
            return if (x < t) t - 1.0 else t;
        }
        fn _ceil(x: f64) callconv(.c) f64 {
            const t = _trunc(x);
            return if (x > t) t + 1.0 else t;
        }
        fn _ceilf(x: f32) callconv(.c) f32 {
            const t = _truncf(x);
            return if (x > t) t + 1.0 else t;
        }
        fn _round(x: f64) callconv(.c) f64 {
            return if (x >= 0.0) _floor(x + 0.5) else _ceil(x - 0.5);
        }
        fn _roundf(x: f32) callconv(.c) f32 {
            return if (x >= 0.0) _floorf(x + 0.5) else _ceilf(x - 0.5);
        }
        fn _fmod(x: f64, y: f64) callconv(.c) f64 {
            return x - _trunc(x / y) * y;
        }
        fn _fmodf(x: f32, y: f32) callconv(.c) f32 {
            return x - _truncf(x / y) * y;
        }
    };
    @export(&Math._trunc, .{ .name = "trunc" });
    @export(&Math._truncf, .{ .name = "truncf" });
    @export(&Math._floor, .{ .name = "floor" });
    @export(&Math._floorf, .{ .name = "floorf" });
    @export(&Math._ceil, .{ .name = "ceil" });
    @export(&Math._ceilf, .{ .name = "ceilf" });
    @export(&Math._round, .{ .name = "round" });
    @export(&Math._roundf, .{ .name = "roundf" });
    @export(&Math._fmod, .{ .name = "fmod" });
    @export(&Math._fmodf, .{ .name = "fmodf" });
}

test "word-width memcpy and memset" {
    var src: [37]u8 = undefined;
    var dst: [37]u8 = @splat(0);
    for (&src, 0..) |*b, i| b.* = @intCast(i + 1);
    for (0..src.len) |i| dst[i] = src[i];
    try std.testing.expectEqualSlices(u8, &src, &dst);
}
