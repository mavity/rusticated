const builtin = @import("builtin");
pub const abi_types = @import("pal_abi");

pub const PalAbiTable = abi_types.PalAbiTable;
pub const Overlapped = abi_types.Overlapped;

pub inline fn sliceOrigin() [*]u8 {
    if (builtin.is_test) {
        @compileError("sliceOrigin() must not be called in unit tests");
    }
    const addr = switch (builtin.cpu.arch) {
        .x86_64 => asm ("lea hello_payload_code_start(%rip), %[ret]"
            : [ret] "=r" (-> usize),
        ),
        .aarch64 => asm ("adr %[ret], hello_payload_code_start"
            : [ret] "=r" (-> usize),
        ),
        else => @compileError("Unsupported architecture"),
    };
    return @ptrFromInt(addr);
}

pub inline fn abi() *const PalAbiTable {
    return @ptrCast(@alignCast(sliceOrigin() - abi_types.PREFIX_ABI_OFFSET));
}

pub inline fn globalsPtr(comptime T: type) *T {
    return @ptrCast(@alignCast(sliceOrigin() - abi_types.PREFIX_GLOBALS_OFFSET));
}

pub fn exportEntry(comptime activation_fn: fn () callconv(abi_types.pal_cc) void) void {
    const S = struct {
        fn activation() callconv(abi_types.pal_cc) void {
            activation_fn();
        }
    };
    @export(&S.activation, .{ .name = "hello_payload_activation" });
}

pub fn exportIntrinsics() void {
    const S = struct {
        fn _memcpy(dest: [*]u8, src: [*]const u8, len: usize) callconv(.c) [*]u8 {
            for (0..len) |i| dest[i] = src[i];
            return dest;
        }
        fn _memset(dest: [*]u8, value: c_int, len: usize) callconv(.c) [*]u8 {
            const b: u8 = @truncate(@as(c_uint, @bitCast(value)));
            for (0..len) |i| dest[i] = b;
            return dest;
        }
        fn _memmove(dest: [*]u8, src: [*]const u8, len: usize) callconv(.c) [*]u8 {
            const d = @intFromPtr(dest);
            const s = @intFromPtr(src);
            if (d <= s or d >= s + len) {
                for (0..len) |i| dest[i] = src[i];
            } else {
                var i = len;
                while (i > 0) {
                    i -= 1;
                    dest[i] = src[i];
                }
            }
            return dest;
        }
        fn _memcmp(left: [*]const u8, right: [*]const u8, len: usize) callconv(.c) c_int {
            for (0..len) |i| {
                if (left[i] != right[i]) {
                    return @as(c_int, left[i]) - @as(c_int, right[i]);
                }
            }
            return 0;
        }
    };
    @export(&S._memcpy, .{ .name = "memcpy" });
    @export(&S._memset, .{ .name = "memset" });
    @export(&S._memmove, .{ .name = "memmove" });
    @export(&S._memcmp, .{ .name = "memcmp" });
    @export(&S._memcmp, .{ .name = "bcmp" });
}
