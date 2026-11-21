const std = @import("std");
const win = @import("win_ffi.zig");

pub const DEFAULT_T_READY_NS: u64 = 50_000; // 50 us initial estimate
pub const DEFAULT_T_IDLE_NS: u64 = 5_000_000_000; // 5 s linear retirement step
pub const MAX_POOL_WORKERS: usize = 64;
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

pub const WorkerSlot = struct {
    used: bool = false,
    thread_h: win.HANDLE = null,
    pool_ptr: usize = 0,
    slot_idx: u16 = 0,
};

pub const ExecWorkFn = *const fn (*anyopaque, u16) void;
pub const PostDoneFn = *const fn (*anyopaque, u16) void;

pub const WinWorkerPool = struct {
    policy: PacedScalingPolicy = .{},
    srw_lock: usize = 0,
    semaphore: win.HANDLE = null,
    stopping: u32 = 0,
    ctx: ?*anyopaque = null,
    exec_fn: ?ExecWorkFn = null,
    post_fn: ?PostDoneFn = null,
    p_acquire_srw: ?*const fn (*usize) callconv(win.win_cc) void = null,
    p_release_srw: ?*const fn (*usize) callconv(win.win_cc) void = null,
    p_release_sem: ?*const fn (win.HANDLE, i32, ?*i32) callconv(win.win_cc) i32 = null,
    p_wait_single: ?*const fn (win.HANDLE, u32) callconv(win.win_cc) u32 = null,
    p_create_thread: ?*const fn (?*anyopaque, usize, *const fn (?*anyopaque) callconv(win.win_cc) u32, ?*anyopaque, u32, ?*u32) callconv(win.win_cc) win.HANDLE = null,
    p_close_handle: ?*const fn (win.HANDLE) callconv(win.win_cc) i32 = null,
    p_get_tick_count64: ?*const fn () callconv(win.win_cc) u64 = null,
    queue: [QUEUE_CAP]u16 = @splat(0),
    q_head: usize = 0,
    q_tail: usize = 0,
    slots: [MAX_POOL_WORKERS]WorkerSlot = @splat(.{}),

    fn nowNs(self: *const WinWorkerPool) u64 {
        if (self.p_get_tick_count64) |gtc| return gtc() * 1_000_000;
        return 0;
    }

    pub fn submit(self: *WinWorkerPool, op_idx: u16) bool {
        const acq = self.p_acquire_srw orelse return false;
        const rel = self.p_release_srw orelse return false;
        const rsem = self.p_release_sem orelse return false;
        acq(&self.srw_lock);
        if (self.policy.queued_items >= QUEUE_CAP) {
            rel(&self.srw_lock);
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
        rel(&self.srw_lock);

        _ = rsem(self.semaphore, 1, null);

        if (do_spawn) {
            self.spawnOneWorker();
        }
        return true;
    }

    fn spawnOneWorker(self: *WinWorkerPool) void {
        const acq = self.p_acquire_srw orelse return;
        const rel = self.p_release_srw orelse return;
        const cth = self.p_create_thread orelse return;
        acq(&self.srw_lock);
        var chosen: ?usize = null;
        for (&self.slots, 0..) |*slot, idx| {
            if (!slot.used) {
                if (slot.thread_h != null) {
                    if (self.p_close_handle) |ch| _ = ch(slot.thread_h);
                    slot.thread_h = null;
                }
                slot.used = true;
                slot.pool_ptr = @intFromPtr(self);
                slot.slot_idx = @intCast(idx);
                chosen = idx;
                break;
            }
        }
        rel(&self.srw_lock);

        const idx = chosen orelse {
            acq(&self.srw_lock);
            self.policy.onSpawnFailed();
            rel(&self.srw_lock);
            return;
        };

        const th = cth(null, 64 * 1024, workerEntry, &self.slots[idx], 0, null);
        if (th == null) {
            acq(&self.srw_lock);
            self.slots[idx].used = false;
            self.policy.onSpawnFailed();
            rel(&self.srw_lock);
            return;
        }
        self.slots[idx].thread_h = th;
    }

    fn workerEntry(ctx: ?*anyopaque) callconv(win.win_cc) u32 {
        const slot: *WorkerSlot = @ptrCast(@alignCast(ctx.?));
        const self: *WinWorkerPool = @ptrFromInt(slot.pool_ptr);
        self.workerLoop(slot);
        return 0;
    }

    fn workerLoop(self: *WinWorkerPool, slot: *WorkerSlot) void {
        const acq = self.p_acquire_srw orelse return;
        const rel = self.p_release_srw orelse return;
        const wait = self.p_wait_single orelse return;
        acq(&self.srw_lock);
        self.policy.onWorkerReady(self.nowNs());
        rel(&self.srw_lock);

        const idle_ms: u32 = @intCast((self.policy.t_idle_ns / 1_000_000) + (@as(u64, slot.slot_idx & 0x7) * 50));

        while (@atomicLoad(u32, &self.stopping, .acquire) == 0) {
            acq(&self.srw_lock);
            if (self.policy.queued_items > 0) {
                const op_idx = self.queue[self.q_head];
                self.q_head = (self.q_head + 1) % QUEUE_CAP;
                self.policy.queued_items -= 1;
                self.policy.busy_workers += 1;
                rel(&self.srw_lock);

                if (self.exec_fn) |ef| ef(self.ctx.?, op_idx);
                if (self.post_fn) |pf| pf(self.ctx.?, op_idx);

                acq(&self.srw_lock);
                if (self.policy.busy_workers > 0) self.policy.busy_workers -= 1;
                rel(&self.srw_lock);
                continue;
            }
            rel(&self.srw_lock);

            const wres = wait(self.semaphore, idle_ms);
            if (wres == 258) { // WAIT_TIMEOUT
                acq(&self.srw_lock);
                const retired = self.policy.tryRetire(self.nowNs());
                if (retired) {
                    slot.used = false;
                    rel(&self.srw_lock);
                    return;
                }
                rel(&self.srw_lock);
            }
        }
    }
};

test "Windows PacedScalingPolicy enforces single-flight, pacing, and linear retirement" {
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
