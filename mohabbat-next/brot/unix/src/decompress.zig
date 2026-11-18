const std = @import("std");

pub fn decompressZstd(allocator: std.mem.Allocator, compressed: []const u8) ![]u8 {
    var reader = std.Io.Reader.fixed(compressed);
    var decomp = std.compress.zstd.Decompress.init(&reader, &.{}, .{});
    var out = std.Io.Writer.Allocating.init(allocator);
    errdefer out.deinit();
    _ = try decomp.reader.streamRemaining(&out.writer);
    return try out.toOwnedSlice();
}

test "zstd decompress round-trip" {
    const alloc = std.testing.allocator;
    // Truncated zstd frame returns error cleanly
    try std.testing.expectError(error.ReadFailed, decompressZstd(alloc, &[_]u8{ 0x28, 0xb5, 0x2f, 0xfd }));
    // Valid zstd frame for "hello" (uncompressed block, 5 bytes)
    const zstd_hello = [_]u8{
        0x28, 0xb5, 0x2f, 0xfd, // magic
        0x20, 0x05,             // frame header: single segment, 1-byte FCS = 5
        0x29, 0x00, 0x00,       // block header: last=1, type=raw(0), size=5 ((5 << 3) | 1 = 0x29)
        'h', 'e', 'l', 'l', 'o',
    };
    const out = try decompressZstd(alloc, &zstd_hello);
    defer alloc.free(out);
    try std.testing.expectEqualStrings("hello", out);
}
