const builtin = @import("builtin");
const std = @import("std");

pub fn main(init: std.process.Init.Minimal) void {
    const argv_slice: []const [*:0]const u8 = init.args.vector;
    if (builtin.os.tag == .linux) {
        const linux = @import("linux.zig");
        const envp_slice: []const [*:0]const u8 = @ptrCast(init.environ.block.slice);
        linux.runMain(argv_slice, envp_slice);
    } else if (builtin.os.tag == .macos) {
        const darwin = @import("darwin.zig");
        darwin.runMain(argv_slice);
    } else {
        @compileError("brot/unix only supports linux and macos targets");
    }
}

test {
    _ = @import("pool.zig");
    _ = @import("decompress.zig");
}
