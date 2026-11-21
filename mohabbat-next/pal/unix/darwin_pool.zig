const std = @import("std");

pub const DEFAULT_T_READY_NS: u64 = 50_000;
pub const DEFAULT_T_IDLE_NS: u64 = 5_000_000_000;
pub const MAX_POOL_WORKERS: usize = 64;
pub const WORKER_STACK_BYTES: usize = 64 * 1024;
pub const QUEUE_CAP: usize = 256;

/// Implements the Spec §7 / 10.9 Thread-Spawn Pacing Law and Linear Time-Based Retirement.
pub const PacedScalingPolicy = struct {
    active_workers: usize = 0,
    busy_workers: usize = 0,
    queued_items: usize = 0,
    spawning: bool = false,
    t_ready_ns: u64 = DEFAULT_T_READY_NS,
    t_idle_ns: u64 = DEFAULT_T_IDLE_NS,
    last_spawn_ns: u64 = 0,
    last_retire_ns: u64 = 0,
    min_workers: usize = 0,
    max_workers: usize = MAX_POOL_WORKERS,

    pub fn shouldSpawn(self: *const PacedScalingPolicy, now_ns: u64) bool {
        if (self.queued_items == 0 or self.spawning or self.active_workers >= self.max_workers) {
            return false;
        }
        if (self.active_workers == 0) {
            return true;
        }
        if (self.busy_workers < self.active_workers) {
            return false;
        }
        const elapsed: u64 = if (now_ns >= self.last_spawn_ns) now_ns - self.last_spawn_ns else 0;
        const required_dt: u64 = (self.t_ready_ns * @as(u64, @intCast(self.busy_workers))) / @as(u64, @intCast(self.queued_items));
        return elapsed >= required_dt;
    }

    pub fn onSpawnStarted(self: *PacedScalingPolicy, now_ns: u64) void {
        self.spawning = true;
        self.last_spawn_ns = now_ns;
    }

    pub fn onWorkerReady(self: *PacedScalingPolicy, now_ns: u64) void {
        self.spawning = false;
        self.active_workers += 1;
        const raw_measured: u64 = if (now_ns >= self.last_spawn_ns) now_ns - self.last_spawn_ns else 0;
        const measured = @max(@as(u64, 1_000), @min(raw_measured, @as(u64, 10_000_000)));
        self.t_ready_ns = (self.t_ready_ns * 3 + measured) / 4;
    }

    pub fn onSpawnFailed(self: *PacedScalingPolicy) void {
        self.spawning = false;
    }

    pub fn tryRetire(self: *PacedScalingPolicy, now_ns: u64) bool {
        if (self.queued_items > 0 or self.active_workers <= self.min_workers) {
            return false;
        }
        const elapsed: u64 = if (now_ns >= self.last_retire_ns) now_ns - self.last_retire_ns else 0;
        if (elapsed < self.t_idle_ns) {
            return false;
        }
        self.active_workers -= 1;
        self.last_retire_ns = now_ns;
        return true;
    }
};

pub const Timespec = extern struct {
    tv_sec: isize,
    tv_nsec: isize,
};

pub const WorkerSlot = struct {
    used: bool = false,
    pool_ptr: usize = 0,
    slot_idx: u16 = 0,
};

pub const ExecWorkFn = *const fn (*anyopaque, u16) void;

pub const DarwinWorkerPool = struct {
    policy: PacedScalingPolicy = .{},
    mutex: [64]u8 align(8) = @splat(0),
    cond: [48]u8 align(8) = @splat(0),
    stopping: u32 = 0,
    wake_wr_fd: i32 = -1,
    ctx: ?*anyopaque = null,
    exec_fn: ?ExecWorkFn = null,

    p_mutex_init: ?*const fn (*[64]u8, ?*anyopaque) callconv(.c) c_int = null,
    p_mutex_lock: ?*const fn (*[64]u8) callconv(.c) c_int = null,
    p_mutex_unlock: ?*const fn (*[64]u8) callconv(.c) c_int = null,
    p_cond_init: ?*const fn (*[48]u8, ?*anyopaque) callconv(.c) c_int = null,
    p_cond_signal: ?*const fn (*[48]u8) callconv(.c) c_int = null,
    p_cond_timedwait: ?*const fn (*[48]u8, *[64]u8, *const Timespec) callconv(.c) c_int = null,
    p_pthread_create: ?*const fn (*usize, ?*const [64]u8, *const fn (?*anyopaque) callconv(.c) ?*anyopaque, ?*anyopaque) callconv(.c) c_int = null,
    p_pthread_detach: ?*const fn (usize) callconv(.c) c_int = null,
    p_attr_init: ?*const fn (*[64]u8) callconv(.c) c_int = null,
    p_attr_setstacksize: ?*const fn (*[64]u8, usize) callconv(.c) c_int = null,
    p_attr_destroy: ?*const fn (*[64]u8) callconv(.c) c_int = null,
    p_write: ?*const fn (i32, [*]const u8, usize) callconv(.c) isize = null,
    p_clock_gettime: ?*const fn (c_int, *Timespec) callconv(.c) c_int = null,

    queue: [QUEUE_CAP]u16 = @splat(0),
    q_head: usize = 0,
    q_tail: usize = 0,
    done_queue: [QUEUE_CAP]u16 = @splat(0),
    done_head: usize = 0,
    done_tail: usize = 0,
    done_count: usize = 0,
    slots: [MAX_POOL_WORKERS]WorkerSlot = @splat(.{}),

    pub fn initSync(self: *DarwinWorkerPool) bool {
        const mi = self.p_mutex_init orelse return false;
        const ci = self.p_cond_init orelse return false;
        if (mi(&self.mutex, null) != 0) return false;
        if (ci(&self.cond, null) != 0) return false;
        return true;
    }

    fn nowNs(self: *const DarwinWorkerPool) u64 {
        const cg = self.p_clock_gettime orelse return 0;
        var ts: Timespec = .{ .tv_sec = 0, .tv_nsec = 0 };
        if (cg(6, &ts) == 0) { // CLOCK_MONOTONIC = 6 on Darwin
            return @as(u64, @intCast(@max(ts.tv_sec, 0))) * 1_000_000_000 + @as(u64, @intCast(@max(ts.tv_nsec, 0)));
        }
        return 0;
    }

    pub fn submit(self: *DarwinWorkerPool, op_idx: u16) bool {
        const lock = self.p_mutex_lock orelse return false;
        const unlock = self.p_mutex_unlock orelse return false;
        _ = lock(&self.mutex);
        if (self.policy.queued_items >= QUEUE_CAP) {
            _ = unlock(&self.mutex);
            return false;
        }
        self.queue[self.q_tail] = op_idx;
        self.q_tail = (self.q_tail + 1) % QUEUE_CAP;
        self.policy.queued_items += 1;

        const now = self.nowNs();
        const do_spawn = self.policy.shouldSpawn(now);
        if (do_spawn) {
            self.policy.onSpawnStarted(now);
        }
        _ = unlock(&self.mutex);

        if (self.p_cond_signal) |cs| _ = cs(&self.cond);

        if (do_spawn) {
            self.spawnOneWorker();
        }
        return true;
    }

    pub fn popDone(self: *DarwinWorkerPool) ?u16 {
        const lock = self.p_mutex_lock orelse return null;
        const unlock = self.p_mutex_unlock orelse return null;
        _ = lock(&self.mutex);
        defer _ = unlock(&self.mutex);
        if (self.done_count == 0) return null;
        const idx = self.done_queue[self.done_head];
        self.done_head = (self.done_head + 1) % QUEUE_CAP;
        self.done_count -= 1;
        return idx;
    }

    fn pushDoneAndWake(self: *DarwinWorkerPool, op_idx: u16) void {
        const lock = self.p_mutex_lock orelse return;
        const unlock = self.p_mutex_unlock orelse return;
        _ = lock(&self.mutex);
        if (self.done_count < QUEUE_CAP) {
            self.done_queue[self.done_tail] = op_idx;
            self.done_tail = (self.done_tail + 1) % QUEUE_CAP;
            self.done_count += 1;
        }
        _ = unlock(&self.mutex);

        if (self.wake_wr_fd >= 0) {
            if (self.p_write) |wr| {
                const b: [1]u8 = .{1};
                _ = wr(self.wake_wr_fd, &b, 1);
            }
        }
    }

    fn spawnOneWorker(self: *DarwinWorkerPool) void {
        const lock = self.p_mutex_lock orelse return;
        const unlock = self.p_mutex_unlock orelse return;
        const pc = self.p_pthread_create orelse return;

        _ = lock(&self.mutex);
        var chosen: ?usize = null;
        for (&self.slots, 0..) |*slot, idx| {
            if (!slot.used) {
                slot.used = true;
                slot.pool_ptr = @intFromPtr(self);
                slot.slot_idx = @intCast(idx);
                chosen = idx;
                break;
            }
        }
        _ = unlock(&self.mutex);

        const idx = chosen orelse {
            _ = lock(&self.mutex);
            self.policy.onSpawnFailed();
            _ = unlock(&self.mutex);
            return;
        };

        var attr: [64]u8 align(8) = @splat(0);
        var has_attr = false;
        if (self.p_attr_init) |ai| {
            if (ai(&attr) == 0) {
                has_attr = true;
                if (self.p_attr_setstacksize) |ss| {
                    _ = ss(&attr, WORKER_STACK_BYTES);
                }
            }
        }
        var tid: usize = 0;
        const rc = pc(&tid, if (has_attr) &attr else null, workerEntry, &self.slots[idx]);
        if (has_attr) {
            if (self.p_attr_destroy) |ad| _ = ad(&attr);
        }
        if (rc != 0) {
            _ = lock(&self.mutex);
            self.slots[idx].used = false;
            self.policy.onSpawnFailed();
            _ = unlock(&self.mutex);
            return;
        }
        if (self.p_pthread_detach) |pd| _ = pd(tid);
    }

    fn workerEntry(ctx: ?*anyopaque) callconv(.c) ?*anyopaque {
        const slot: *WorkerSlot = @ptrCast(@alignCast(ctx.?));
        const self: *DarwinWorkerPool = @ptrFromInt(slot.pool_ptr);
        self.workerLoop(slot);
        return null;
    }

    fn workerLoop(self: *DarwinWorkerPool, slot: *WorkerSlot) void {
        const lock = self.p_mutex_lock orelse return;
        const unlock = self.p_mutex_unlock orelse return;
        const tw = self.p_cond_timedwait orelse return;
        const cg = self.p_clock_gettime orelse return;

        _ = lock(&self.mutex);
        self.policy.onWorkerReady(self.nowNs());
        _ = unlock(&self.mutex);

        const idle_ns: u64 = self.policy.t_idle_ns + (@as(u64, slot.slot_idx & 0x7) * 50_000_000);

        while (@atomicLoad(u32, &self.stopping, .acquire) == 0) {
            _ = lock(&self.mutex);
            if (self.policy.queued_items > 0) {
                const op_idx = self.queue[self.q_head];
                self.q_head = (self.q_head + 1) % QUEUE_CAP;
                self.policy.queued_items -= 1;
                self.policy.busy_workers += 1;
                _ = unlock(&self.mutex);

                if (self.exec_fn) |ef| ef(self.ctx.?, op_idx);
                self.pushDoneAndWake(op_idx);

                _ = lock(&self.mutex);
                if (self.policy.busy_workers > 0) self.policy.busy_workers -= 1;
                _ = unlock(&self.mutex);
                continue;
            }

            // pthread_cond_timedwait uses CLOCK_REALTIME (0) on Darwin
            var abs: Timespec = .{ .tv_sec = 0, .tv_nsec = 0 };
            _ = cg(0, &abs);
            const total_ns = @as(u64, @intCast(@max(abs.tv_nsec, 0))) + idle_ns;
            abs.tv_sec += @intCast(total_ns / 1_000_000_000);
            abs.tv_nsec = @intCast(total_ns % 1_000_000_000);

            const wrc = tw(&self.cond, &self.mutex, &abs);
            if (wrc == 60) { // ETIMEDOUT = 60 on Darwin
                const retired = self.policy.tryRetire(self.nowNs());
                if (retired) {
                    slot.used = false;
                    _ = unlock(&self.mutex);
                    return;
                }
            }
            _ = unlock(&self.mutex);
        }
    }
};

test "Darwin PacedScalingPolicy enforces single-flight, pacing, and linear retirement" {
    var p: PacedScalingPolicy = .{};
    try std.testing.expect(!p.shouldSpawn(0));
    p.queued_items = 1;
    try std.testing.expect(p.shouldSpawn(1_000_000));
    p.onSpawnStarted(1_000_000);
    try std.testing.expect(!p.shouldSpawn(2_000_000));
    p.onWorkerReady(1_200_000);
    try std.testing.expectEqual(@as(usize, 1), p.active_workers);
    try std.testing.expectEqual(@as(u64, 87_500), p.t_ready_ns);
    p.queued_items = 2;
    p.busy_workers = 1;
    try std.testing.expect(!p.shouldSpawn(1_040_000));
    try std.testing.expect(p.shouldSpawn(1_045_000));
    p.queued_items = 0;
    p.busy_workers = 0;
    p.active_workers = 2;
    p.last_retire_ns = 10_000_000_000;
    try std.testing.expect(!p.tryRetire(14_999_999_999));
    try std.testing.expect(p.tryRetire(15_000_000_000));
    try std.testing.expectEqual(@as(usize, 1), p.active_workers);
}
