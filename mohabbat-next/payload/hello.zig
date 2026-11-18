const builtin = @import("builtin");
const std = @import("std");
const core = @import("hello_core.zig");
const abi_types = core.abi_types;
const Overlapped = core.Overlapped;
const PalAbiTable = core.PalAbiTable;

const LINE_CAPACITY: usize = 256;
const OUTPUT_CAPACITY: usize = 512;
const PHASE_WRITING: u8 = 1;
const PHASE_READING: u8 = 2;

const GREETING = "Mohabbat-next ready (compiled zig-0.17.1). Enter an empty line to exit.\n> ";
const ECHO_PREFIX = "echo: ";
const NEWLINE = "\n";
const PROMPT = "> ";

pub const HelloGlobals = extern struct {
    initialized: bool = false,
    phase: u8 = 0,
    skip_lf: bool = false,
    line_len: usize = 0,
    output_len: usize = 0,
    output_offset: usize = 0,
    input_byte: u8 = 0,
    overlapped: Overlapped = .{},
    line: [LINE_CAPACITY]u8 = @splat(0),
    output: [OUTPUT_CAPACITY]u8 = @splat(0),
};

var test_globals: HelloGlobals = .{};
var test_abi_ptr: ?*const PalAbiTable = null;

inline fn getAbi() *const PalAbiTable {
    if (builtin.is_test) return test_abi_ptr.?;
    return core.abi();
}

inline fn getGlobals() *HelloGlobals {
    if (builtin.is_test) return &test_globals;
    return core.globalsPtr(HelloGlobals);
}

fn appendOutput(g: *HelloGlobals, bytes: []const u8) bool {
    if (g.output_len + bytes.len > OUTPUT_CAPACITY) return false;
    @memcpy(g.output[g.output_len..][0..bytes.len], bytes);
    g.output_len += bytes.len;
    return true;
}

fn scheduleWrite(g: *HelloGlobals) void {
    g.overlapped = .{};
    getAbi().write(
        &g.overlapped,
        1,
        @ptrCast(&g.output[g.output_offset]),
        g.output_len - g.output_offset,
    );
}

fn scheduleRead(g: *HelloGlobals) void {
    g.overlapped = .{};
    getAbi().read(&g.overlapped, 0, @ptrCast(&g.input_byte), 1);
}

fn prepareGreeting(g: *HelloGlobals) bool {
    g.output_len = 0;
    g.output_offset = 0;
    return appendOutput(g, GREETING);
}

fn prepareEcho(g: *HelloGlobals) bool {
    g.output_len = 0;
    g.output_offset = 0;
    if (!appendOutput(g, ECHO_PREFIX)) return false;
    if (g.output_len + g.line_len > OUTPUT_CAPACITY) return false;
    @memcpy(g.output[g.output_len..][0..g.line_len], g.line[0..g.line_len]);
    g.output_len += g.line_len;
    g.line_len = 0;
    return appendOutput(g, NEWLINE) and appendOutput(g, PROMPT);
}

fn finishLine(g: *HelloGlobals) void {
    if (g.line_len == 0) {
        getAbi().process_exit(0);
    }
    if (!prepareEcho(g)) {
        getAbi().process_exit(3);
    }
    g.phase = PHASE_WRITING;
    scheduleWrite(g);
}

pub fn resumeActivation() callconv(abi_types.pal_cc) void {
    const g = getGlobals();
    if (!g.initialized) {
        g.* = .{};
        g.initialized = true;
        if (!prepareGreeting(g)) {
            getAbi().process_exit(3);
        }
        g.phase = PHASE_WRITING;
        scheduleWrite(g);
    }

    while (true) {
        if ((g.overlapped.flags & abi_types.FLAG_COMPLETED) == 0) {
            return;
        }
        if (g.overlapped.@"error" != 0) {
            getAbi().process_exit(3);
        }

        switch (g.phase) {
            PHASE_WRITING => {
                const count: usize = @intCast(g.overlapped.result_ext);
                const remaining = g.output_len - g.output_offset;
                if (count == 0 or count > remaining) {
                    getAbi().process_exit(3);
                }
                g.output_offset += count;
                if (g.output_offset < g.output_len) {
                    scheduleWrite(g);
                    continue;
                }
                g.output_len = 0;
                g.output_offset = 0;
                g.phase = PHASE_READING;
                scheduleRead(g);
            },
            PHASE_READING => {
                switch (g.overlapped.result_ext) {
                    0 => getAbi().process_exit(0),
                    1 => {},
                    else => getAbi().process_exit(3),
                }
                const byte = g.input_byte;
                if (g.skip_lf) {
                    g.skip_lf = false;
                    if (byte == '\n') {
                        scheduleRead(g);
                        continue;
                    }
                }
                if (byte == '\r' or byte == '\n') {
                    g.skip_lf = (byte == '\r');
                    finishLine(g);
                } else {
                    if (g.line_len == LINE_CAPACITY) {
                        getAbi().process_exit(2);
                    }
                    g.line[g.line_len] = byte;
                    g.line_len += 1;
                    scheduleRead(g);
                }
            },
            else => getAbi().process_exit(3),
        }
    }
}

comptime {
    if (!builtin.is_test) {
        core.exportIntrinsics();
        core.exportEntry(resumeActivation);
        switch (builtin.cpu.arch) {
            .x86_64 => asm (
                \\.section .text.hello_payload_entry,"ax"
                \\.globl hello_payload_code_start
                \\hello_payload_code_start:
                \\jmp hello_payload_activation
            ),
            .aarch64 => asm (
                \\.section .text.hello_payload_entry,"ax"
                \\.globl hello_payload_code_start
                \\hello_payload_code_start:
                \\b hello_payload_activation
            ),
            else => @compileError("Unsupported architecture"),
        }
    }
}

test "hello greeting and echo state machine" {
    var g: HelloGlobals = .{};
    try std.testing.expect(prepareGreeting(&g));
    try std.testing.expectEqualStrings(GREETING, g.output[0..g.output_len]);

    const msg = "alpha";
    @memcpy(g.line[0..msg.len], msg);
    g.line_len = msg.len;
    try std.testing.expect(prepareEcho(&g));
    try std.testing.expectEqual(@as(usize, 0), g.line_len);
    try std.testing.expectEqualStrings("echo: alpha\n> ", g.output[0..g.output_len]);
}
