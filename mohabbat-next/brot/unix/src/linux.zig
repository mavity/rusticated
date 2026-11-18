const builtin = @import("builtin");
const std = @import("std");
const linux = std.os.linux;
const pool = @import("pool.zig");
const alloc_mod = @import("allocator.zig");
const decompress = @import("decompress.zig");

const PREFIX_ABI_OFFSET: usize = 0x20000;

pub const UnixPlatformHost = extern struct {
    hello_payload: [*]const u8,
    hello_payload_size: usize,
    argv: [*]const [*:0]const u8,
    argc: usize,
    envp: [*]const [*:0]const u8,
    envc: usize,
    pid: u32,
    wasm_bytes: ?[*]const u8,
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
    if (base_idx + 4 >= argv.len) die("brot: missing bundle arguments\n", 12);

    const me_path_z = argv[base_idx + 1];
    const pool_off = pool.parseU64(cstrSlice(argv[base_idx + 2])) orelse die("brot: bad pool_off\n", 13);
    const pool_len_u64 = pool.parseU64(cstrSlice(argv[base_idx + 3])) orelse die("brot: bad pool_len\n", 14);
    const mode = cstrSlice(argv[base_idx + 4]);
    var user_argv = argv[base_idx + 5 ..];

    var override_wasm: ?[]const u8 = null;
    if (user_argv.len >= 2 and std.mem.eql(u8, cstrSlice(user_argv[0]), "--wasm")) {
        const wpath_z = user_argv[1];
        const wfd_res = linux.openat(linux.AT.FDCWD, wpath_z, .{}, 0);
        const wfd_signed: isize = @bitCast(wfd_res);
        if (wfd_signed < 0) die("brot: open --wasm file failed\n", 24);
        const wfd: i32 = @intCast(wfd_signed);
        const wsize_res = linux.lseek(wfd, 0, linux.SEEK.END);
        const wsize_signed: isize = @bitCast(wsize_res);
        if (wsize_signed <= 0 or wsize_signed > 128 * 1024 * 1024) die("brot: invalid --wasm size\n", 25);
        _ = linux.lseek(wfd, 0, linux.SEEK.SET);
        const wlen: usize = @intCast(wsize_signed);
        const wbuf = allocator.alloc(u8, wlen) catch die("brot: alloc --wasm failed\n", 26);
        var wread: usize = 0;
        while (wread < wlen) {
            const r_res = linux.read(wfd, wbuf.ptr + wread, wlen - wread);
            const r_signed: isize = @bitCast(r_res);
            if (r_signed <= 0) die("brot: read --wasm failed\n", 27);
            wread += @intCast(r_signed);
        }
        _ = linux.close(wfd);
        override_wasm = wbuf;
        if (user_argv.len >= 3 and std.mem.eql(u8, cstrSlice(user_argv[2]), "--")) {
            user_argv = user_argv[2..]; // keep argv[0] slot (overwritten or skipped) -> shift past "--"
            user_argv = argv[base_idx + 7 ..];
        } else {
            user_argv = user_argv[1..];
        }
    }

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

    const arch_str = switch (builtin.cpu.arch) {
        .x86_64 => "x86_64",
        .aarch64 => "aarch64",
        else => @compileError("Unsupported arch"),
    };
    const pal_name = if (builtin.cpu.arch == .x86_64) "platform-linux-x86_64" else "platform-linux-aarch64";
    const pal_slice = pool.findEntry(raw_pool, pal_name) orelse die("brot: PAL not found in pool\n", 21);

    const wasm_slice = override_wasm orelse pool.findEntry(raw_pool, "brain.wasm");
    const payload_name = if (wasm_slice != null)
        (if (builtin.cpu.arch == .x86_64) "runtime-x86_64" else "runtime-aarch64")
    else if (std.mem.eql(u8, mode, "fileio"))
        (if (builtin.cpu.arch == .x86_64) "fileio-x86_64" else "fileio-aarch64")
    else if (std.mem.eql(u8, mode, "abi"))
        (if (builtin.cpu.arch == .x86_64) "abi-conformance-x86_64" else "abi-conformance-aarch64")
    else
        (if (builtin.cpu.arch == .x86_64) "hello-x86_64" else "hello-aarch64");
    _ = arch_str;

    const payload_slice = pool.findEntry(raw_pool, payload_name) orelse die("brot: payload not found in pool\n", 22);

    const code_aligned = (pal_slice.len + 4095) & ~@as(usize, 4095);
    const total_map = PREFIX_ABI_OFFSET + code_aligned;
    const map_res = linux.mmap(null, total_map, .{ .READ = true, .WRITE = true }, .{ .TYPE = .PRIVATE, .ANONYMOUS = true }, -1, 0);
    const map_signed: isize = @bitCast(map_res);
    if (map_signed < 0) die("brot: mmap PAL failed\n", 23);
    const pal_base: [*]u8 = @ptrFromInt(map_res);

    const host: *UnixPlatformHost = @ptrCast(@alignCast(pal_base));
    const pid_res = linux.getpid();
    host.* = .{
        .hello_payload = payload_slice.ptr,
        .hello_payload_size = payload_slice.len,
        .argv = user_argv.ptr,
        .argc = user_argv.len,
        .envp = envp.ptr,
        .envc = envp.len,
        .pid = @intCast(pid_res),
        .wasm_bytes = if (wasm_slice) |w| w.ptr else null,
        .wasm_len = if (wasm_slice) |w| w.len else 0,
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
