const builtin = @import("builtin");
const std = @import("std");
const pool = @import("pool.zig");
const alloc_mod = @import("allocator.zig");
const decompress = @import("decompress.zig");

const PREFIX_ABI_OFFSET: usize = 0x20000;

extern "c" fn open(path: [*:0]const u8, flags: c_int, mode: u32) c_int;
extern "c" fn lseek(fd: c_int, offset: i64, whence: c_int) i64;
extern "c" fn read(fd: c_int, buf: [*]u8, count: usize) isize;
extern "c" fn close(fd: c_int) c_int;
extern "c" fn unlink(path: [*:0]const u8) c_int;
extern "c" fn write(fd: c_int, buf: [*]const u8, count: usize) isize;
extern "c" fn exit(code: c_int) noreturn;
extern "c" fn dlopen(path: ?[*:0]const u8, flags: c_int) ?*anyopaque;
extern "c" fn dlsym(handle: ?*anyopaque, symbol: [*:0]const u8) ?*anyopaque;
extern "c" fn sys_icache_invalidate(start: ?*const anyopaque, len: usize) void;
extern "c" fn pthread_jit_write_protect_np(enabled: c_int) void;
extern "c" fn _NSGetExecutablePath(buf: [*]u8, bufsize: *u32) c_int;
extern "c" fn mmap(addr: ?*anyopaque, len: usize, prot: c_int, flags: c_int, fd: c_int, offset: i64) ?*anyopaque;
extern "c" fn munmap(addr: ?*anyopaque, len: usize) c_int;
extern "c" fn mprotect(addr: ?*anyopaque, len: usize, prot: c_int) c_int;

pub const UnixPlatformHost = extern struct {
    load_library: *const fn ([*:0]const u8, c_int) callconv(.c) ?*anyopaque,
    get_proc_address: *const fn (?*anyopaque, [*:0]const u8) callconv(.c) ?*anyopaque,
    alloc: *const fn (usize) callconv(.c) ?*anyopaque,
    free: *const fn (?*anyopaque, usize) callconv(.c) void,
    flush_cache: *const fn (?*anyopaque, usize) callconv(.c) void,
    set_jit_write_protect: *const fn (c_int) callconv(.c) void,
    runtime_slice: [*]const u8,
    runtime_slice_size: usize,
    argv: [*]const [*:0]const u8,
    argc: usize,
    wasm_bytes: [*]const u8,
    wasm_len: usize,
};

fn loadLibCb(name: [*:0]const u8, flags: c_int) callconv(.c) ?*anyopaque {
    return dlopen(name, flags);
}

fn getProcCb(module: ?*anyopaque, name: [*:0]const u8) callconv(.c) ?*anyopaque {
    return dlsym(module, name);
}

fn allocCb(size: usize) callconv(.c) ?*anyopaque {
    const aligned = (size + 4095) & ~@as(usize, 4095);
    // PROT_READ | PROT_WRITE | PROT_EXEC = 7, MAP_PRIVATE | MAP_ANON | MAP_JIT = 0x1802
    const ptr = mmap(null, aligned, 7, 0x1802, -1, 0);
    if (ptr == null or @intFromPtr(ptr) == std.math.maxInt(usize)) return null;
    return ptr;
}

fn freeCb(ptr: ?*anyopaque, size: usize) callconv(.c) void {
    if (ptr) |p| {
        const aligned = (size + 4095) & ~@as(usize, 4095);
        _ = munmap(p, aligned);
    }
}

fn flushCb(ptr: ?*anyopaque, size: usize) callconv(.c) void {
    sys_icache_invalidate(ptr, size);
}

fn jitProtectCb(enabled: c_int) callconv(.c) void {
    pthread_jit_write_protect_np(enabled);
}

fn cstrSlice(p: [*:0]const u8) []const u8 {
    return std.mem.sliceTo(p, 0);
}

fn die(msg: []const u8, code: c_int) noreturn {
    _ = write(2, msg.ptr, msg.len);
    exit(code);
}

pub fn runMain(argv: []const [*:0]const u8) noreturn {
    const allocator = alloc_mod.os_allocator;

    var self_buf: [4096]u8 = undefined;
    var buf_len: u32 = self_buf.len - 1;
    if (_NSGetExecutablePath(&self_buf, &buf_len) != 0) die("brot: _NSGetExecutablePath failed\n", 10);
    const self_slice = cstrSlice(@ptrCast(&self_buf));
    _ = unlink(@ptrCast(&self_buf));

    const own_base = pool.getBasename(self_slice);
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
    const tail_argv = argv[base_idx + 4 ..];
    const user_argv = allocator.alloc([*:0]const u8, 1 + tail_argv.len) catch die("brot: alloc argv failed\n", 18);
    user_argv[0] = me_path_z;
    @memcpy(user_argv[1..], tail_argv);

    if (pool_len_u64 == 0 or pool_len_u64 > 128 * 1024 * 1024) die("brot: invalid pool_len\n", 15);
    const pool_len: usize = @intCast(pool_len_u64);

    const fd = open(me_path_z, 0, 0);
    if (fd < 0) die("brot: open bundle failed\n", 16);
    if (lseek(fd, @intCast(pool_off), 0) < 0) die("brot: lseek failed\n", 17);

    const comp_buf = allocator.alloc(u8, pool_len) catch die("brot: alloc compressed failed\n", 18);
    var total_read: usize = 0;
    while (total_read < pool_len) {
        const r = read(fd, comp_buf.ptr + total_read, pool_len - total_read);
        if (r <= 0) die("brot: read pool failed\n", 19);
        total_read += @intCast(r);
    }
    _ = close(fd);

    const raw_pool = decompress.decompressZstd(allocator, comp_buf) catch die("brot: zstd decompress failed\n", 20);
    allocator.free(comp_buf);

    const pal_name = if (builtin.cpu.arch == .x86_64) "platform-darwin-x86_64" else "platform-darwin-aarch64";
    const pal_slice = pool.findEntry(raw_pool, pal_name) orelse die("brot: PAL not found in pool\n", 21);

    const runtime_name = if (builtin.cpu.arch == .x86_64) "runtime-x86_64" else "runtime-aarch64";
    const runtime_slice = pool.findEntry(raw_pool, runtime_name) orelse die("brot: runtime not found in pool\n", 22);
    const wasm_slice = pool.findEntry(raw_pool, "brain.wasm") orelse die("brot: application brain not found in pool\n", 28);

    const prefix_raw = mmap(null, PREFIX_ABI_OFFSET, 0x3, 0x1002, -1, 0);
    if (prefix_raw == null or @intFromPtr(prefix_raw) == std.math.maxInt(usize)) die("brot: mmap prefix failed\n", 23);

    // Map contiguous prefix + code via single MAP_JIT or separate prefix/code;
    // since PAL uses PC-relative addressing from code_base - 0x20000, allocate one contiguous region.
    _ = munmap(prefix_raw.?, PREFIX_ABI_OFFSET);
    const code_aligned = (pal_slice.len + 16383) & ~@as(usize, 16383);
    const total_map = PREFIX_ABI_OFFSET + code_aligned;
    const pal_raw = mmap(null, total_map, 0x7, 0x1802, -1, 0);
    if (pal_raw == null or @intFromPtr(pal_raw) == std.math.maxInt(usize)) die("brot: mmap PAL failed\n", 24);
    const pal_base: [*]u8 = @ptrCast(pal_raw.?);

    pthread_jit_write_protect_np(0);
    const host: *UnixPlatformHost = @ptrCast(@alignCast(pal_base));
    host.* = .{
        .load_library = loadLibCb,
        .get_proc_address = getProcCb,
        .alloc = allocCb,
        .free = freeCb,
        .flush_cache = flushCb,
        .set_jit_write_protect = jitProtectCb,
        .runtime_slice = runtime_slice.ptr,
        .runtime_slice_size = runtime_slice.len,
        .argv = user_argv.ptr,
        .argc = user_argv.len,
        .wasm_bytes = wasm_slice.ptr,
        .wasm_len = wasm_slice.len,
    };
    const code_ptr = pal_base + PREFIX_ABI_OFFSET;
    @memcpy(code_ptr[0..pal_slice.len], pal_slice);
    pthread_jit_write_protect_np(1);
    sys_icache_invalidate(code_ptr, pal_slice.len);

    const EntryFn = *const fn () callconv(.c) i32;
    const entry: EntryFn = @ptrCast(@alignCast(code_ptr));
    exit(entry());
}
