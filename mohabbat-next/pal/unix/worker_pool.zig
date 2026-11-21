const std = @import("std");
const sys = @import("linux_sys.zig");

pub const DEFAULT_T_READY_NS: u64 = 50_000; // 50 us initial estimate
pub const DEFAULT_T_IDLE_NS: u64 = 5_000_000_000; // 5 s linear retirement step
pub const MAX_POOL_WORKERS: usize = 64;
pub const WORKER_STACK_BYTES: usize = 64 * 1024;
pub const GUARD_PAGE_BYTES: usize = 4096;
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

pub const FutexMutex = struct {
    state: u32 = 0, // 0 = unlocked, 1 = locked uncontended, 2 = locked contended

    pub fn lock(self: *FutexMutex) void {
        if (@cmpxchgWeak(u32, &self.state, 0, 1, .acquire, .monotonic) == null) return;
        while (true) {
            const cur = @atomicLoad(u32, &self.state, .monotonic);
            if (cur == 2 or @cmpxchgWeak(u32, &self.state, 1, 2, .monotonic, .monotonic) == null) {
                _ = sys.sys4(sys.NR.futex, @intFromPtr(&self.state), sys.FUTEX_WAIT | sys.FUTEX_PRIVATE_FLAG, 2, 0);
            }
            if (@cmpxchgWeak(u32, &self.state, 0, 2, .acquire, .monotonic) == null) return;
        }
    }

    pub fn unlock(self: *FutexMutex) void {
        if (@atomicRmw(u32, &self.state, .Sub, 1, .release) != 1) {
            @atomicStore(u32, &self.state, 0, .release);
            _ = sys.sys3(sys.NR.futex, @intFromPtr(&self.state), sys.FUTEX_WAKE | sys.FUTEX_PRIVATE_FLAG, 1);
        }
    }
};

pub const WorkerSlot = struct {
    used: bool = false,
    stack_base: usize = 0,
    stack_len: usize = 0,
    pool_ptr: usize = 0,
    slot_idx: u16 = 0,
};

pub const ExecWorkFn = *const fn (*anyopaque, u16) void;

pub const LinuxWorkerPool = struct {
    policy: PacedScalingPolicy = .{},
    mutex: FutexMutex = .{},
    wake_seq: u32 = 0,
    stopping: u32 = 0,
    event_fd: i32 = -1,
    exec_fn: ExecWorkFn,
    ctx: *anyopaque,
    queue: [QUEUE_CAP]u16 = @splat(0),
    q_head: usize = 0,
    q_tail: usize = 0,
    slots: [MAX_POOL_WORKERS]WorkerSlot = @splat(.{}),

    pub fn init(event_fd: i32, ctx: *anyopaque, exec_fn: ExecWorkFn) LinuxWorkerPool {
        return .{
            .event_fd = event_fd,
            .ctx = ctx,
            .exec_fn = exec_fn,
        };
    }

    fn monotonicNs() u64 {
        var ts: sys.Timespec = .{ .sec = 0, .nsec = 0 };
        _ = sys.sys2(sys.NR.clock_gettime, 1, @intFromPtr(&ts));
        return @as(u64, @intCast(@max(ts.sec, 0))) * 1_000_000_000 + @as(u64, @intCast(@max(ts.nsec, 0)));
    }

    pub fn submit(self: *LinuxWorkerPool, op_idx: u16) bool {
        self.mutex.lock();
        if (self.policy.queued_items >= QUEUE_CAP) {
            self.mutex.unlock();
            return false;
        }
        self.queue[self.q_tail] = op_idx;
        self.q_tail = (self.q_tail + 1) % QUEUE_CAP;
        self.policy.queued_items += 1;

        const now_ns = monotonicNs();
        const do_spawn = self.policy.shouldSpawn(now_ns);
        if (do_spawn) {
            self.policy.onSpawnStarted(now_ns);
        }
        _ = @atomicRmw(u32, &self.wake_seq, .Add, 1, .release);
        self.mutex.unlock();

        _ = sys.sys3(sys.NR.futex, @intFromPtr(&self.wake_seq), sys.FUTEX_WAKE | sys.FUTEX_PRIVATE_FLAG, 1);

        if (do_spawn) {
            self.spawnOneWorker();
        }
        return true;
    }

    fn spawnOneWorker(self: *LinuxWorkerPool) void {
        self.mutex.lock();
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
        self.mutex.unlock();

        const idx = chosen orelse {
            self.mutex.lock();
            self.policy.onSpawnFailed();
            self.mutex.unlock();
            return;
        };

        const total_stack = GUARD_PAGE_BYTES + WORKER_STACK_BYTES;
        const mres = sys.sys6(sys.NR.mmap, 0, total_stack, sys.PROT_READ | sys.PROT_WRITE, sys.MAP_PRIVATE | sys.MAP_ANONYMOUS, @bitCast(@as(isize, -1)), 0);
        if (sys.isErr(mres) != null) {
            self.mutex.lock();
            self.slots[idx].used = false;
            self.policy.onSpawnFailed();
            self.mutex.unlock();
            return;
        }
        _ = sys.sys3(sys.NR.mprotect, mres, GUARD_PAGE_BYTES, 0); // PROT_NONE guard page

        self.slots[idx].stack_base = mres;
        self.slots[idx].stack_len = total_stack;
        const stack_top = (mres + total_stack) & ~@as(usize, 15);
        const slot_ptr = @intFromPtr(&self.slots[idx]);

        const cres = sys.cloneThread(stack_top, workerEntry, slot_ptr);
        if (cres < 0) {
            _ = sys.sys2(sys.NR.munmap, mres, total_stack);
            self.mutex.lock();
            self.slots[idx].used = false;
            self.policy.onSpawnFailed();
            self.mutex.unlock();
        }
    }

    fn workerEntry(slot_addr: usize) callconv(.c) void {
        const slot: *WorkerSlot = @ptrFromInt(slot_addr);
        const self: *LinuxWorkerPool = @ptrFromInt(slot.pool_ptr);
        self.workerLoop(slot);
    }

    fn workerLoop(self: *LinuxWorkerPool, slot: *WorkerSlot) void {
        self.mutex.lock();
        self.policy.onWorkerReady(monotonicNs());
        self.mutex.unlock();

        const idle_ns = self.policy.t_idle_ns + (@as(u64, slot.slot_idx & 0x7) * 50_000_000);

        while (@atomicLoad(u32, &self.stopping, .acquire) == 0) {
            self.mutex.lock();
            if (self.policy.queued_items > 0) {
                const op_idx = self.queue[self.q_head];
                self.q_head = (self.q_head + 1) % QUEUE_CAP;
                self.policy.queued_items -= 1;
                self.policy.busy_workers += 1;
                self.mutex.unlock();

                self.exec_fn(self.ctx, op_idx);
                if (self.event_fd >= 0) {
                    var inc: u64 = 1;
                    _ = sys.sys3(sys.NR.write, @intCast(self.event_fd), @intFromPtr(&inc), @sizeOf(u64));
                }

                self.mutex.lock();
                if (self.policy.busy_workers > 0) self.policy.busy_workers -= 1;
                self.mutex.unlock();
                continue;
            }

            const seq = @atomicLoad(u32, &self.wake_seq, .acquire);
            self.mutex.unlock();

            var ts: sys.Timespec = .{
                .sec = @intCast(idle_ns / 1_000_000_000),
                .nsec = @intCast(idle_ns % 1_000_000_000),
            };
            _ = sys.sys4(sys.NR.futex, @intFromPtr(&self.wake_seq), sys.FUTEX_WAIT | sys.FUTEX_PRIVATE_FLAG, seq, @intFromPtr(&ts));

            self.mutex.lock();
            const retired = self.policy.tryRetire(monotonicNs());
            if (retired) {
                slot.used = false;
                const base = slot.stack_base;
                const len = slot.stack_len;
                self.mutex.unlock();
                _ = base;
                _ = len;
                // Kernel reclaims thread on exit; keep stack clean or exit directly.
                return;
            }
            self.mutex.unlock();
        }
    }
};

test "PacedScalingPolicy enforces single-flight, pacing law, EMA, and linear retirement" {
    var p: PacedScalingPolicy = .{};

    // Empty queue never spawns.
    try std.testing.expect(!p.shouldSpawn(0));

    // Cold start (W == 0, Q > 0) spawns immediately.
    p.queued_items = 1;
    try std.testing.expect(p.shouldSpawn(1_000_000));
    p.onSpawnStarted(1_000_000);
    // Single-flight blocks concurrent spawn while spawning == true.
    try std.testing.expect(!p.shouldSpawn(2_000_000));

    // Worker becomes ready 200 us later -> EMA: (3*50_000 + 200_000) / 4 = 87_500 ns.
    p.onWorkerReady(1_200_000);
    try std.testing.expectEqual(@as(usize, 1), p.active_workers);
    try std.testing.expectEqual(@as(u64, 87_500), p.t_ready_ns);

    // W == 1, B == 0 -> idle worker exists, do not spawn.
    p.queued_items = 2;
    try std.testing.expect(!p.shouldSpawn(1_500_000));

    // All workers busy (B == W == 1), Q == 2 -> required_dt = 87_500 * 1 / 2 = 43_750 ns.
    p.busy_workers = 1;
    try std.testing.expect(!p.shouldSpawn(1_040_000)); // elapsed 40_000 < 43_750
    try std.testing.expect(p.shouldSpawn(1_045_000)); // elapsed 45_000 >= 43_750

    // Retirement requires Q == 0 and T_idle elapsed, retiring at most 1 per T_idle.
    p.queued_items = 0;
    p.busy_workers = 0;
    p.active_workers = 2;
    p.last_retire_ns = 10_000_000_000;
    try std.testing.expect(!p.tryRetire(14_999_999_999));
    try std.testing.expect(p.tryRetire(15_000_000_000));
    try std.testing.expectEqual(@as(usize, 1), p.active_workers);
    try std.testing.expect(!p.tryRetire(15_100_000_000));
}
