const std = @import("std");

pub fn slice(memory: ?[]u8, ptr_i32: i32, len_i32: i32) ?[]u8 {
    const mem = memory orelse return null;
    if (ptr_i32 < 0 or len_i32 < 0) return null;
    const off: usize = @intCast(ptr_i32);
    const len: usize = @intCast(len_i32);
    const end = std.math.add(usize, off, len) catch return null;
    if (end > mem.len) return null;
    return mem[off..end];
}

test "guest span validation accepts bounded spans and rejects invalid ones" {
    const SpanCase = struct {
        ptr: i32,
        len: i32,
        valid: bool,
        start: usize = 0,
        length: usize = 0,
    };
    const cases = [_]SpanCase{
        .{ .ptr = 2, .len = 3, .valid = true, .start = 2, .length = 3 },
        .{ .ptr = 12, .len = 4, .valid = true, .start = 12, .length = 4 },
        .{ .ptr = 16, .len = 0, .valid = true, .start = 16 },
        .{ .ptr = -1, .len = 1, .valid = false },
        .{ .ptr = 0, .len = -1, .valid = false },
        .{ .ptr = 15, .len = 2, .valid = false },
        .{ .ptr = 17, .len = 0, .valid = false },
    };
    var memory = [_]u8{ 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };

    for (cases) |case| {
        const span = slice(memory[0..], case.ptr, case.len);
        try std.testing.expectEqual(case.valid, span != null);
        if (span) |bytes| {
            try std.testing.expectEqualSlices(
                u8,
                memory[case.start..][0..case.length],
                bytes,
            );
        }
    }

    try std.testing.expect(slice(null, 0, 0) == null);
}
