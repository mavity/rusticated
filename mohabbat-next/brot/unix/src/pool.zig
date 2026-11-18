const std = @import("std");

pub fn findEntry(pool: []const u8, name: []const u8) ?[]const u8 {
    if (pool.len < 16 or !std.mem.eql(u8, pool[0..8], "MHBTPOOL")) {
        return null;
    }
    const version = std.mem.readInt(u32, pool[8..12], .little);
    const count: usize = std.mem.readInt(u32, pool[12..16], .little);
    const toc_bytes = std.math.mul(usize, count, 40) catch return null;
    const header_end = std.math.add(usize, 16, toc_bytes) catch return null;
    if (version != 1 or header_end > pool.len) {
        return null;
    }
    for (0..count) |i| {
        const toc_off = 16 + i * 40;
        const name_bytes = pool[toc_off .. toc_off + 32];
        var nlen: usize = 0;
        while (nlen < 32 and name_bytes[nlen] != 0) : (nlen += 1) {}
        if (std.mem.eql(u8, name_bytes[0..nlen], name)) {
            const off: usize = std.mem.readInt(u32, pool[toc_off + 32 ..][0..4], .little);
            const len: usize = std.mem.readInt(u32, pool[toc_off + 36 ..][0..4], .little);
            const end = std.math.add(usize, off, len) catch return null;
            if (len == 0 or end > pool.len) {
                return null;
            }
            return pool[off..end];
        }
    }
    return null;
}

pub fn parseU64(s: []const u8) ?u64 {
    if (s.len == 0) return null;
    var val: u64 = 0;
    for (s) |b| {
        if (b < '0' or b > '9') return null;
        val = std.math.mul(u64, val, 10) catch return null;
        val = std.math.add(u64, val, b - '0') catch return null;
    }
    return val;
}

pub fn getBasename(path: []const u8) []const u8 {
    if (path.len == 0) return path;
    var i = path.len;
    while (i > 0) {
        i -= 1;
        if (path[i] == '/') return path[i + 1 ..];
    }
    return path;
}

test "pool lookup and helpers" {
    var raw: [16 + 40 + 5]u8 = @splat(0);
    @memcpy(raw[0..8], "MHBTPOOL");
    std.mem.writeInt(u32, raw[8..12], 1, .little);
    std.mem.writeInt(u32, raw[12..16], 1, .little);
    @memcpy(raw[16..28], "sample-arm64");
    std.mem.writeInt(u32, raw[16 + 32 ..][0..4], 56, .little);
    std.mem.writeInt(u32, raw[16 + 36 ..][0..4], 5, .little);
    @memcpy(raw[56..61], "abcde");

    try std.testing.expectEqualStrings("abcde", findEntry(&raw, "sample-arm64").?);
    try std.testing.expect(findEntry(&raw, "missing") == null);
    try std.testing.expectEqual(@as(?u64, 12345), parseU64("12345"));
    try std.testing.expect(parseU64("12x") == null);
    try std.testing.expectEqualStrings("🐚︎abc123", getBasename("/tmp/🐚︎abc123"));
    try std.testing.expectEqualStrings("plain", getBasename("plain"));
}
