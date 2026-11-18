const builtin = @import("builtin");
const std = @import("std");
const linux = std.os.linux;
const pool = @import("pool.zig");
const alloc_mod = @import("allocator.zig");
const decompress = @import("decompress.zig");

const PREFIX_ABI_OFFSET: usize = 0x20000;

pub const UnixPlatformHost = extern struct {
    runtime_slice: [*]const u8,
    runtime_slice_size: usize,
    argv: [*]const [*:0]const u8,
    argc: usize,
    envp: [*]const [*:0]const u8,
    envc: usize,
    pid: u32,
    wasm_bytes: [*]const u8,
    wasm_len: usize,
};

fn flushIcache(ptr: [*]const u8, len: usize) void {
    if (builtin.cpu.arch == .aarch64) {
        const start = @intFromPtr(ptr) & ~@as(usize, 63);
        const end = (@intFromPtr(ptr) + len + 63) & ~@as(usize, 63);
        var addr = start;
        while (addr < end) : (addr += 64) {
            asm volatile ("dc cvau, %[addr]"
                :
                : [addr] "r" (addr),
                : .{ .memory = true });
        }
        asm volatile ("dsb ish" ::: .{ .memory = true });
        addr = start;
        while (addr < end) : (addr += 64) {
            asm volatile ("ic ivau, %[addr]"
                :
                : [addr] "r" (addr),
                : .{ .memory = true });
        }
        asm volatile ("dsb ish\nisb" ::: .{ .memory = true });
    }
}

fn cstrSlice(p: [*:0]const u8) []const u8 {
    return std.mem.sliceTo(p, 0);
}

fn writeErr(msg: []const u8) void {
    _ = linux.write(2, msg.ptr, msg.len);
}

fn die(msg: []const u8, code: u8) noreturn {
    writeErr(msg);
    linux.exit_group(code);
}

pub fn runMain(argv: []const [*:0]const u8, envp: []const [*:0]const u8) noreturn {
    const allocator = alloc_mod.os_allocator;

    var self_buf: [4096]u8 = undefined;
    const rl_res = linux.readlinkat(linux.AT.FDCWD, "/proc/self/exe", &self_buf, self_buf.len - 1);
    const rl_signed: isize = @bitCast(rl_res);
    if (rl_signed <= 0) die("brot: readlinkat /proc/self/exe failed\n", 10);
    const self_len: usize = @intCast(rl_signed);
    self_buf[self_len] = 0;
    const self_path_z: [*:0]const u8 = @ptrCast(&self_buf);

    // Unlink temporary launcher immediately.
    _ = linux.unlinkat(linux.AT.FDCWD, self_path_z, 0);

    const own_base = pool.getBasename(self_buf[0..self_len]);
    var match_idx: ?usize = null;
    for (argv, 0..) |arg, i| {
        if (std.mem.eql(u8, pool.getBasename(cstrSlice(arg)), own_base)) {
            match_idx = i;
        }
    }
    const base_idx = match_idx orelse die("brot: argv demarcation failed\n", 11);
    if (base_idx + 3 >= argv.len) die("brot: missing bundle arguments\n", 12);

    const me_path_z = argv[base_idx + 1];
    const pool_off = pool.parseU64(cstrSlice(argv[base_idx + 2])) orelse die("brot: bad pool_off\n", 13);
    const pool_len_u64 = pool.parseU64(cstrSlice(argv[base_idx + 3])) orelse die("brot: bad pool_len\n", 14);
    const user_argv = argv[base_idx + 4 ..];

    if (pool_len_u64 == 0 or pool_len_u64 > 128 * 1024 * 1024) die("brot: invalid pool_len\n", 15);
    const pool_len: usize = @intCast(pool_len_u64);

    const fd_res = linux.openat(linux.AT.FDCWD, me_path_z, .{}, 0);
    const fd_signed: isize = @bitCast(fd_res);
    if (fd_signed < 0) die("brot: open bundle failed\n", 16);
    const fd: i32 = @intCast(fd_signed);

    const seek_res = linux.lseek(fd, @intCast(pool_off), linux.SEEK.SET);
    const seek_signed: isize = @bitCast(seek_res);
    if (seek_signed < 0) die("brot: lseek failed\n", 17);

    const comp_buf = allocator.alloc(u8, pool_len) catch die("brot: alloc compressed failed\n", 18);
    var total_read: usize = 0;
    while (total_read < pool_len) {
        const r_res = linux.read(fd, comp_buf.ptr + total_read, pool_len - total_read);
        const r_signed: isize = @bitCast(r_res);
        if (r_signed <= 0) die("brot: read pool failed\n", 19);
        total_read += @intCast(r_signed);
    }
    _ = linux.close(fd);

    const raw_pool = decompress.decompressZstd(allocator, comp_buf) catch die("brot: zstd decompress failed\n", 20);
    allocator.free(comp_buf);

    const pal_name = if (builtin.cpu.arch == .x86_64) "platform-linux-x86_64" else "platform-linux-aarch64";
    const pal_slice = pool.findEntry(raw_pool, pal_name) orelse die("brot: PAL not found in pool\n", 21);
    const runtime_name = if (builtin.cpu.arch == .x86_64) "runtime-x86_64" else "runtime-aarch64";
    const runtime_slice = pool.findEntry(raw_pool, runtime_name) orelse die("brot: runtime not found in pool\n", 22);
    const wasm_slice = pool.findEntry(raw_pool, "brain.wasm") orelse die("brot: application brain not found in pool\n", 28);

    const code_aligned = (pal_slice.len + 4095) & ~@as(usize, 4095);
    const total_map = PREFIX_ABI_OFFSET + code_aligned;
    const map_res = linux.mmap(null, total_map, .{ .READ = true, .WRITE = true }, .{ .TYPE = .PRIVATE, .ANONYMOUS = true }, -1, 0);
    const map_signed: isize = @bitCast(map_res);
    if (map_signed < 0) die("brot: mmap PAL failed\n", 23);
    const pal_base: [*]u8 = @ptrFromInt(map_res);

    const host: *UnixPlatformHost = @ptrCast(@alignCast(pal_base));
    const pid_res = linux.getpid();
    host.* = .{
        .runtime_slice = runtime_slice.ptr,
        .runtime_slice_size = runtime_slice.len,
        .argv = user_argv.ptr,
        .argc = user_argv.len,
        .envp = envp.ptr,
        .envc = envp.len,
        .pid = @intCast(pid_res),
        .wasm_bytes = wasm_slice.ptr,
        .wasm_len = wasm_slice.len,
    };

    const code_ptr = pal_base + PREFIX_ABI_OFFSET;
    @memcpy(code_ptr[0..pal_slice.len], pal_slice);
    _ = linux.mprotect(code_ptr, code_aligned, @bitCast(linux.PROT{ .READ = true, .EXEC = true }));
    flushIcache(code_ptr, pal_slice.len);

    const EntryFn = *const fn () callconv(.c) i32;
    const entry: EntryFn = @ptrCast(@alignCast(code_ptr));
    const exit_code = entry();
    linux.exit_group(@intCast(@as(u32, @bitCast(exit_code)) & 0xff));
}
