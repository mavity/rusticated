const std = @import("std");

pub const WasmLevel = enum { v1_0, v2_0, v3_0 };
pub const WasiLevel = enum { none, p1, p2, p3 };
pub const EngineMode = enum { interp, jit, both };

pub const wasm_level: WasmLevel = .v3_0;
pub const wasi_level: WasiLevel = .none;
pub const engine_mode: EngineMode = .jit;
pub const runner_engine_optimize: std.lang.Optimize = .fast;
pub const trace_ringbuffer: bool = false;
pub const trace_stackprobe: bool = false;
pub const enable_component: bool = false;
pub const enable_wasi_p3: bool = false;
pub const version: []const u8 = "2.12.0";
