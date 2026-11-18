const builtin = @import("builtin");
const pal_abi = @import("pal_abi");

pub const win_cc = pal_abi.pal_cc;

pub const LoadLibraryFn = *const fn ([*:0]const u8) callconv(win_cc) ?*anyopaque;
pub const GetProcAddressFn = *const fn (?*anyopaque, [*:0]const u8) callconv(win_cc) ?*anyopaque;
pub const AllocFn = *const fn (usize) callconv(win_cc) ?*anyopaque;
pub const FreeFn = *const fn (?*anyopaque) callconv(win_cc) void;

pub const WindowsPlatformHost = extern struct {
    load_library: ?LoadLibraryFn,
    get_proc_address: ?GetProcAddressFn,
    alloc: ?AllocFn,
    free: ?FreeFn,
    runtime_slice: ?[*]const u8,
    runtime_slice_size: usize,
    argv: ?[*]const [*:0]const u8,
    argc: usize,
    wasm_payload: ?[*]const u8,
    wasm_payload_size: usize,
};

pub const HANDLE = ?*anyopaque;
pub const INVALID_HANDLE_VALUE: HANDLE = @ptrFromInt(0xffff_ffff_ffff_ffff);

pub const WinOverlapped = extern struct {
    internal: usize = 0,
    internal_high: usize = 0,
    offset: u32 = 0,
    offset_high: u32 = 0,
    event: HANDLE = null,
};

pub const OverlappedEntry = extern struct {
    completion_key: usize = 0,
    overlapped: ?*WinOverlapped = null,
    internal: usize = 0,
    bytes_transferred: u32 = 0,
};

pub const FileTime = extern struct {
    low: u32 = 0,
    high: u32 = 0,
};

pub const Win32FileAttributeData = extern struct {
    attributes: u32 = 0,
    creation_time: FileTime = .{},
    last_access_time: FileTime = .{},
    last_write_time: FileTime = .{},
    size_high: u32 = 0,
    size_low: u32 = 0,
};

pub const Win32FindDataW = extern struct {
    attributes: u32 = 0,
    creation_time: FileTime = .{},
    last_access_time: FileTime = .{},
    last_write_time: FileTime = .{},
    size_high: u32 = 0,
    size_low: u32 = 0,
    reserved0: u32 = 0,
    reserved1: u32 = 0,
    file_name: [260]u16 = @splat(0),
    alt_name: [14]u16 = @splat(0),
};

pub const SecurityAttributes = extern struct {
    length: u32 = @sizeOf(SecurityAttributes),
    security_descriptor: ?*anyopaque = null,
    inherit_handle: i32 = 0,
};

pub const StartupInfoW = extern struct {
    cb: u32 = @sizeOf(StartupInfoW),
    reserved: ?[*:0]u16 = null,
    desktop: ?[*:0]u16 = null,
    title: ?[*:0]u16 = null,
    x: u32 = 0,
    y: u32 = 0,
    x_size: u32 = 0,
    y_size: u32 = 0,
    x_count_chars: u32 = 0,
    y_count_chars: u32 = 0,
    fill_attribute: u32 = 0,
    flags: u32 = 0,
    show_window: u16 = 0,
    cb_reserved2: u16 = 0,
    lp_reserved2: ?[*]u8 = null,
    std_input: HANDLE = null,
    std_output: HANDLE = null,
    std_error: HANDLE = null,
};

pub const ProcessInformation = extern struct {
    process: HANDLE = null,
    thread: HANDLE = null,
    process_id: u32 = 0,
    thread_id: u32 = 0,
};

pub const Coord = extern struct {
    x: i16 = 0,
    y: i16 = 0,
};

pub const SmallRect = extern struct {
    left: i16 = 0,
    top: i16 = 0,
    right: i16 = 0,
    bottom: i16 = 0,
};

pub const ConsoleScreenBufferInfo = extern struct {
    size: Coord = .{},
    cursor_position: Coord = .{},
    attributes: u16 = 0,
    window: SmallRect = .{},
    maximum_window_size: Coord = .{},
};

pub fn mapWinError(win_err: u32) u32 {
    return switch (win_err) {
        0 => 0,
        2, 3 => pal_abi.ERROR_NOENT,
        5 => pal_abi.ERROR_ACCES,
        6 => pal_abi.ERROR_BADF,
        8, 14 => pal_abi.ERROR_NOMEM,
        32, 33, 183 => pal_abi.ERROR_EXIST,
        80 => pal_abi.ERROR_EXIST,
        87, 123, 131, 161 => pal_abi.ERROR_INVALID,
        109, 232 => pal_abi.ERROR_PIPE,
        145 => pal_abi.ERROR_ISDIR,
        267 => pal_abi.ERROR_NOTDIR,
        995 => pal_abi.ERROR_CANCELED,
        997 => pal_abi.ERROR_AGAIN,
        10035 => pal_abi.ERROR_AGAIN,
        10060 => pal_abi.ERROR_TIMEDOUT,
        10061 => pal_abi.ERROR_CONNREFUSED,
        else => pal_abi.ERROR_IO,
    };
}

pub fn fileTimeToUnixNs(ft: FileTime) i64 {
    const raw: u64 = (@as(u64, ft.high) << 32) | ft.low;
    const EPOCH_DIFF_100NS: u64 = 116_444_736_000_000_000;
    if (raw < EPOCH_DIFF_100NS) return 0;
    const rel = raw - EPOCH_DIFF_100NS;
    const ns = rel *| 100;
    return @intCast(@min(ns, @as(u64, @intCast(0x7fff_ffff_ffff_ffff))));
}
