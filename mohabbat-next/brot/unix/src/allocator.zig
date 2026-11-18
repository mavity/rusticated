const builtin = @import("builtin");
const std = @import("std");

const PAGE_SIZE: usize = 4096;

extern "c" fn mmap(addr: ?*anyopaque, len: usize, prot: c_int, flags: c_int, fd: c_int, offset: i64) ?*anyopaque;
extern "c" fn munmap(addr: ?*anyopaque, len: usize) c_int;

fn pageAlign(n: usize) usize {
    return (n + PAGE_SIZE - 1) & ~(PAGE_SIZE - 1);
}

fn osMmap(len: usize, prot_rw: bool) ?[*]u8 {
    if (builtin.os.tag == .linux) {
        const prot: std.os.linux.PROT = .{ .READ = true, .WRITE = prot_rw };
        const res = std.os.linux.mmap(null, len, prot, .{ .TYPE = .PRIVATE, .ANONYMOUS = true }, -1, 0);
        const signed: isize = @bitCast(res);
        if (signed < 0) return null;
        return @ptrFromInt(res);
    } else if (builtin.os.tag == .macos) {
        const prot: c_int = if (prot_rw) 0x3 else 0x1;
        const ptr = mmap(null, len, prot, 0x1002, -1, 0);
        if (ptr == null or @intFromPtr(ptr) == std.math.maxInt(usize)) return null;
        return @ptrCast(ptr.?);
    } else {
        @compileError("Unsupported OS in brot/unix allocator");
    }
}

fn osMunmap(ptr: [*]u8, len: usize) void {
    if (builtin.os.tag == .linux) {
        _ = std.os.linux.munmap(ptr, len);
    } else if (builtin.os.tag == .macos) {
        _ = munmap(ptr, len);
    }
}

const HEADER_SIZE: usize = 16;

fn mmapAlloc(_: *anyopaque, len: usize, _: std.mem.Alignment, _: usize) ?[*]u8 {
    const total = pageAlign(len + HEADER_SIZE);
    const base = osMmap(total, true) orelse return null;
    const hdr: *usize = @ptrCast(@alignCast(base));
    hdr.* = total;
    return base + HEADER_SIZE;
}

fn mmapResize(_: *anyopaque, memory: []u8, _: std.mem.Alignment, new_len: usize, _: usize) bool {
    const base = memory.ptr - HEADER_SIZE;
    const hdr: *usize = @ptrCast(@alignCast(base));
    return new_len + HEADER_SIZE <= hdr.*;
}

fn mmapRemap(ctx: *anyopaque, memory: []u8, alignment: std.mem.Alignment, new_len: usize, ret_addr: usize) ?[*]u8 {
    if (mmapResize(ctx, memory, alignment, new_len, ret_addr)) return memory.ptr;
    const new_ptr = mmapAlloc(ctx, new_len, alignment, ret_addr) orelse return null;
    const copy_len = @min(memory.len, new_len);
    @memcpy(new_ptr[0..copy_len], memory[0..copy_len]);
    mmapFree(ctx, memory, alignment, ret_addr);
    return new_ptr;
}

fn mmapFree(_: *anyopaque, memory: []u8, _: std.mem.Alignment, _: usize) void {
    const base = memory.ptr - HEADER_SIZE;
    const hdr: *usize = @ptrCast(@alignCast(base));
    osMunmap(base, hdr.*);
}

var dummy_ctx: u8 = 0;

pub const os_allocator: std.mem.Allocator = .{
    .ptr = &dummy_ctx,
    .vtable = &.{
        .alloc = mmapAlloc,
        .resize = mmapResize,
        .remap = mmapRemap,
        .free = mmapFree,
    },
};
