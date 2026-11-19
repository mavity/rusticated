const builtin = @import("builtin");
const std = @import("std");
const Io = std.Io;
const testing = std.testing;

pub const std_options: std.Options = .{
    .logFn = log,
};

var log_err_count: usize = 0;
var fba_buffer: [16384]u8 = undefined;
var fba: std.heap.FixedBufferAllocator = .init(&fba_buffer);
var stdout_buffer: [4096]u8 = undefined;
var stdout_writer: Io.File.Writer = undefined;
const runner_threaded_io: Io = Io.Threaded.global_single_threaded.io();

pub fn log(
    comptime message_level: std.log.Level,
    comptime scope: @EnumLiteral(),
    comptime format: []const u8,
    args: anytype,
) void {
    @disableInstrumentation();
    if (@backingInt(message_level) <= @backingInt(std.log.Level.err)) {
        log_err_count +|= 1;
    }
    if (@backingInt(message_level) <= @backingInt(testing.log_level)) {
        std.debug.print(
            "[" ++ @tagName(scope) ++ "] (" ++ @tagName(message_level) ++ "): " ++ format ++ "\n",
            args,
        );
    }
}

fn writeJsonString(w: *Io.Writer, s: []const u8) !void {
    try w.writeByte('"');
    for (s) |c| {
        switch (c) {
            '"' => try w.writeAll("\\\""),
            '\\' => try w.writeAll("\\\\"),
            '\n' => try w.writeAll("\\n"),
            '\r' => try w.writeAll("\\r"),
            '\t' => try w.writeAll("\\t"),
            else => {
                if (c < 0x20) {
                    try w.print("\\u{x:0>4}", .{c});
                } else {
                    try w.writeByte(c);
                }
            },
        }
    }
    try w.writeByte('"');
}

fn emitEvent(w: *Io.Writer, action: []const u8, test_name: []const u8, output: ?[]const u8) void {
    w.writeAll("{\"Action\":") catch return;
    writeJsonString(w, action) catch return;
    w.writeAll(",\"Test\":") catch return;
    writeJsonString(w, test_name) catch return;
    if (output) |out| {
        w.writeAll(",\"Output\":") catch return;
        writeJsonString(w, out) catch return;
    }
    w.writeAll("}\n") catch return;
    w.flush() catch {};
}

fn matchesFilter(name: []const u8, filters: []const []const u8) bool {
    if (filters.len == 0) return true;
    for (filters) |f| {
        if (std.mem.indexOf(u8, name, f) != null) return true;
    }
    return false;
}

pub fn main(init: std.process.Init.Minimal) void {
    @disableInstrumentation();

    const args = init.args.toSlice(fba.allocator()) catch |err| {
        std.debug.panic("unable to parse command line args: {t}", .{err});
    };

    var filter_buf: [64][]const u8 = undefined;
    var filter_count: usize = 0;

    var idx: usize = 1;
    while (idx < args.len) : (idx += 1) {
        const arg = args[idx];
        if (std.mem.eql(u8, arg, "--test-filter")) {
            if (idx + 1 < args.len) {
                idx += 1;
                if (filter_count < filter_buf.len) {
                    filter_buf[filter_count] = args[idx];
                    filter_count += 1;
                }
            }
        } else if (std.mem.startsWith(u8, arg, "--test-filter=")) {
            if (filter_count < filter_buf.len) {
                filter_buf[filter_count] = arg["--test-filter=".len..];
                filter_count += 1;
            }
        } else if (std.mem.startsWith(u8, arg, "--seed=")) {
            testing.random_seed = std.fmt.parseUnsigned(u32, arg["--seed=".len..], 0) catch
                @panic("unable to parse --seed command line argument");
        }
    }

    const filters = filter_buf[0..filter_count];

    stdout_writer = .initStreaming(.stdout(), runner_threaded_io, &stdout_buffer);
    const w = &stdout_writer.interface;

    var fail_count: usize = 0;

    for (builtin.test_functions) |test_fn| {
        if (!matchesFilter(test_fn.name, filters)) {
            continue;
        }

        emitEvent(w, "run", test_fn.name, null);

        testing.allocator_instance = .init(std.heap.page_allocator, .{
            .canary = 0xc3a701ba,
            .check_write_after_free = true,
        });
        testing.io_instance = .init(testing.allocator, .{
            .argv0 = .init(init.args),
            .environ = init.environ,
        });
        testing.log_level = .warn;
        testing.environ = init.environ;
        log_err_count = 0;

        if (test_fn.func()) |_| {
            testing.io_instance.deinit();
            const leak_count = testing.allocator_instance.deinit();
            if (leak_count != 0) {
                fail_count += 1;
                emitEvent(w, "fail", test_fn.name, "memory leak detected");
            } else if (log_err_count != 0) {
                fail_count += 1;
                emitEvent(w, "fail", test_fn.name, "error logged during test");
            } else {
                emitEvent(w, "pass", test_fn.name, null);
            }
        } else |err| switch (err) {
            error.SkipZigTest => {
                testing.io_instance.deinit();
                _ = testing.allocator_instance.deinit();
                emitEvent(w, "skip", test_fn.name, null);
            },
            else => {
                if (@errorReturnTrace()) |trace| {
                    std.debug.dumpErrorReturnTrace(trace);
                }
                testing.io_instance.deinit();
                _ = testing.allocator_instance.deinit();
                fail_count += 1;
                emitEvent(w, "fail", test_fn.name, @errorName(err));
            },
        }
    }

    w.flush() catch {};
    if (fail_count != 0) {
        std.process.exit(1);
    }
}
