#![allow(dead_code)]

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
use core::sync::atomic::{AtomicBool, AtomicI32, AtomicUsize, Ordering};

pub const DEFAULT_T_READY_NS: u64 = 50_000; // 50 us initial estimate
pub const DEFAULT_T_IDLE_NS: u64 = 5_000_000_000; // 5 s linear retirement step
pub const MAX_POOL_WORKERS: usize = 64;
pub const WORKER_STACK_BYTES: usize = 64 * 1024;
pub const GUARD_PAGE_BYTES: usize = 4096;

/// Deterministic state machine implementing the Spec §7 Thread-Spawn Pacing Law
/// and Linear Time-Based Retirement policy.
#[derive(Debug, Clone)]
pub struct PacedScalingPolicy {
    pub active_workers: usize,
    pub busy_workers: usize,
    pub queued_items: usize,
    pub spawning: bool,
    pub t_ready_ns: u64,
    pub t_idle_ns: u64,
    pub last_spawn_ns: u64,
    pub last_retire_ns: u64,
    pub min_workers: usize,
    pub max_workers: usize,
}

impl PacedScalingPolicy {
    pub const fn new(min_workers: usize, max_workers: usize, t_idle_ns: u64) -> Self {
        Self {
            active_workers: 0,
            busy_workers: 0,
            queued_items: 0,
            spawning: false,
            t_ready_ns: DEFAULT_T_READY_NS,
            t_idle_ns,
            last_spawn_ns: 0,
            last_retire_ns: 0,
            min_workers,
            max_workers,
        }
    }

    /// Returns `true` if a new worker thread should be spawned at `now_ns`.
    /// Enforces:
    /// - `Q > 0` and all active workers are busy (`B == W`)
    /// - `W < max_workers`
    /// - single-flight (`!spawning`)
    /// - Pacing law: `Δt >= T_ready * (B / Q)` when `W > 0`
    pub fn should_spawn(&self, now_ns: u64) -> bool {
        if self.queued_items == 0 || self.spawning || self.active_workers >= self.max_workers {
            return false;
        }
        if self.active_workers == 0 {
            return true;
        }
        if self.busy_workers < self.active_workers {
            return false;
        }
        let elapsed = now_ns.saturating_sub(self.last_spawn_ns) as u128;
        let required_dt = (self.t_ready_ns as u128)
            .saturating_mul(self.busy_workers as u128)
            / (self.queued_items as u128);
        elapsed >= required_dt
    }

    pub fn on_spawn_started(&mut self, now_ns: u64) {
        self.spawning = true;
        self.last_spawn_ns = now_ns;
    }

    pub fn on_worker_ready(&mut self, now_ns: u64) {
        self.spawning = false;
        self.active_workers = self.active_workers.saturating_add(1);
        let measured = now_ns
            .saturating_sub(self.last_spawn_ns)
            .clamp(1_000, 10_000_000);
        // EMA: T_ready <- (3 * T_ready + measured) / 4
        self.t_ready_ns = (self.t_ready_ns.saturating_mul(3).saturating_add(measured)) / 4;
    }

    pub fn on_spawn_failed(&mut self) {
        self.spawning = false;
    }

    /// Linear time-based retirement: when `Q == 0` and `W > min_workers`,
    /// at most one worker retires per `T_idle` interval.
    pub fn try_retire(&mut self, now_ns: u64) -> bool {
        if self.queued_items > 0 || self.active_workers <= self.min_workers {
            return false;
        }
        if now_ns.saturating_sub(self.last_retire_ns) < self.t_idle_ns {
            return false;
        }
        self.active_workers -= 1;
        self.last_retire_ns = now_ns;
        true
    }
}

/// Lightweight futex-backed mutex for `#![no_std]` Linux PAL.
#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
pub struct FutexMutex {
    state: AtomicI32,
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
impl FutexMutex {
    pub const fn new() -> Self {
        Self {
            state: AtomicI32::new(0),
        }
    }

    pub unsafe fn lock(&self) {
        if self
            .state
            .compare_exchange(0, 1, Ordering::Acquire, Ordering::Relaxed)
            .is_ok()
        {
            return;
        }
        loop {
            if self.state.swap(2, Ordering::Acquire) == 0 {
                return;
            }
            let _ = crate::linux_syscall::futex_wait(self.state.as_ptr(), 2);
        }
    }

    pub unsafe fn unlock(&self) {
        if self.state.swap(0, Ordering::Release) == 2 {
            let _ = crate::linux_syscall::futex_wake(self.state.as_ptr(), 1);
        }
    }
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
pub struct PoolWorkItem {
    pub op_id: usize,
    pub handle_key: usize,
    pub active: bool,
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
pub type ExecuteWorkFn = unsafe fn(op_id: usize);

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
#[repr(C)]
struct WorkerSlot {
    in_use: AtomicBool,
    child_tid: AtomicI32,
    stack_base: AtomicUsize,
    stack_total: AtomicUsize,
    pool_ptr: AtomicUsize,
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
impl WorkerSlot {
    const fn new() -> Self {
        Self {
            in_use: AtomicBool::new(false),
            child_tid: AtomicI32::new(0),
            stack_base: AtomicUsize::new(0),
            stack_total: AtomicUsize::new(0),
            pool_ptr: AtomicUsize::new(0),
        }
    }
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
const MAX_QUEUE_CAP: usize = 256;

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
pub struct LinuxWorkerPool {
    lock: FutexMutex,
    queue_wake: AtomicI32,
    shutdown: AtomicBool,
    policy: PacedScalingPolicy,
    queue_ops: [usize; MAX_QUEUE_CAP],
    queue_handles: [usize; MAX_QUEUE_CAP],
    queue_len: usize,
    in_flight_handles: [usize; MAX_POOL_WORKERS],
    in_flight_len: usize,
    slots: [WorkerSlot; MAX_POOL_WORKERS],
    notify_write_fd: AtomicI32,
    execute_fn: ExecuteWorkFn,
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
impl LinuxWorkerPool {
    pub const fn new(notify_write_fd: i32, execute_fn: ExecuteWorkFn) -> Self {
        Self {
            lock: FutexMutex::new(),
            queue_wake: AtomicI32::new(0),
            shutdown: AtomicBool::new(false),
            policy: PacedScalingPolicy::new(0, MAX_POOL_WORKERS, DEFAULT_T_IDLE_NS),
            queue_ops: [0; MAX_QUEUE_CAP],
            queue_handles: [0; MAX_QUEUE_CAP],
            queue_len: 0,
            in_flight_handles: [0; MAX_POOL_WORKERS],
            in_flight_len: 0,
            slots: [const { WorkerSlot::new() }; MAX_POOL_WORKERS],
            notify_write_fd: AtomicI32::new(notify_write_fd),
            execute_fn,
        }
    }

    pub fn set_notify_write_fd(&self, fd: i32) {
        self.notify_write_fd.store(fd, Ordering::Release);
    }

    /// Enqueues `op_id` with per-handle ordering key `handle_key` (0 = unordered),
    /// wakes waiting workers, and spawns a new worker if permitted by the pacing law.
    pub unsafe fn submit(&mut self, op_id: usize, handle_key: usize) -> bool {
        let now_ns = crate::linux_syscall::clock_gettime_ns(crate::linux_syscall::CLOCK_MONOTONIC);
        self.lock.lock();
        if self.queue_len >= MAX_QUEUE_CAP {
            self.lock.unlock();
            return false;
        }
        self.queue_ops[self.queue_len] = op_id;
        self.queue_handles[self.queue_len] = handle_key;
        self.queue_len += 1;
        self.policy.queued_items = self.queue_len;

        let need_spawn = self.policy.should_spawn(now_ns);
        if need_spawn {
            self.policy.on_spawn_started(now_ns);
        }
        self.lock.unlock();

        self.queue_wake.fetch_add(1, Ordering::Release);
        let _ = crate::linux_syscall::futex_wake(self.queue_wake.as_ptr(), 1);

        if need_spawn {
            self.spawn_one_worker();
        }
        true
    }

    unsafe fn reclaim_retired_stacks_locked(&mut self) {
        for slot in &self.slots {
            if !slot.in_use.load(Ordering::Acquire) {
                let base = slot.stack_base.load(Ordering::Acquire);
                if base != 0 && slot.child_tid.load(Ordering::Acquire) == 0 {
                    let total = slot.stack_total.swap(0, Ordering::AcqRel);
                    slot.stack_base.store(0, Ordering::Release);
                    if total > 0 {
                        let _ = crate::linux_syscall::munmap(base, total);
                    }
                }
            }
        }
    }

    unsafe fn spawn_one_worker(&mut self) {
        use crate::linux_syscall::*;

        self.lock.lock();
        self.reclaim_retired_stacks_locked();
        let mut chosen: Option<*mut WorkerSlot> = None;
        for slot in &mut self.slots {
            if !slot.in_use.load(Ordering::Acquire)
                && slot.stack_base.load(Ordering::Acquire) == 0
            {
                slot.in_use.store(true, Ordering::Release);
                chosen = Some(slot as *mut WorkerSlot);
                break;
            }
        }
        self.lock.unlock();

        let Some(slot_ptr) = chosen else {
            self.lock.lock();
            self.policy.on_spawn_failed();
            self.lock.unlock();
            return;
        };

        let total = GUARD_PAGE_BYTES + WORKER_STACK_BYTES;
        let base = mmap(
            0,
            total,
            PROT_READ | PROT_WRITE,
            MAP_PRIVATE | MAP_ANONYMOUS,
            -1,
            0,
        );
        if base < 0 && base >= -4095 {
            (*slot_ptr).in_use.store(false, Ordering::Release);
            self.lock.lock();
            self.policy.on_spawn_failed();
            self.lock.unlock();
            return;
        }
        // Guard page at the bottom of the stack.
        let _ = mprotect(base as usize, GUARD_PAGE_BYTES, PROT_NONE);

        (*slot_ptr).stack_base.store(base as usize, Ordering::Release);
        (*slot_ptr).stack_total.store(total, Ordering::Release);
        (*slot_ptr).pool_ptr.store(self as *mut Self as usize, Ordering::Release);
        (*slot_ptr).child_tid.store(1, Ordering::Release);

        let stack_top = (base as usize + total) as *mut u8;
        let flags = CLONE_VM
            | CLONE_FS
            | CLONE_FILES
            | CLONE_SIGHAND
            | CLONE_THREAD
            | CLONE_SYSVSEM
            | CLONE_PARENT_SETTID
            | CLONE_CHILD_CLEARTID;
        let ret = clone_thread(
            flags,
            stack_top,
            (*slot_ptr).child_tid.as_ptr(),
            worker_entry,
            slot_ptr.cast(),
        );
        if ret <= 0 {
            (*slot_ptr).child_tid.store(0, Ordering::Release);
            (*slot_ptr).stack_base.store(0, Ordering::Release);
            (*slot_ptr).stack_total.store(0, Ordering::Release);
            (*slot_ptr).in_use.store(false, Ordering::Release);
            let _ = munmap(base as usize, total);
            self.lock.lock();
            self.policy.on_spawn_failed();
            self.lock.unlock();
        }
    }

    unsafe fn worker_loop(&mut self, slot: &WorkerSlot, worker_index_hint: u64) {
        let now_ns = crate::linux_syscall::clock_gettime_ns(crate::linux_syscall::CLOCK_MONOTONIC);
        self.lock.lock();
        self.policy.on_worker_ready(now_ns);
        self.lock.unlock();

        // Staggered idle wait: T_idle + small per-slot jitter to avoid thundering herd.
        let wait_ns = self
            .policy
            .t_idle_ns
            .saturating_add((worker_index_hint & 0x7) * 50_000_000);
        let timeout = crate::linux_syscall::Timespec {
            tv_sec: (wait_ns / 1_000_000_000) as i64,
            tv_nsec: (wait_ns % 1_000_000_000) as i64,
        };

        loop {
            if self.shutdown.load(Ordering::Acquire) {
                break;
            }

            let mut popped: Option<(usize, usize)> = None;
            let observed_seq = self.queue_wake.load(Ordering::Acquire);

            self.lock.lock();
            for idx in 0..self.queue_len {
                let h = self.queue_handles[idx];
                let handle_busy = h != 0
                    && self.in_flight_handles[..self.in_flight_len]
                        .iter()
                        .any(|&x| x == h);
                if !handle_busy {
                    let op_id = self.queue_ops[idx];
                    // Shift queue preserving FIFO order per handle.
                    for j in idx + 1..self.queue_len {
                        self.queue_ops[j - 1] = self.queue_ops[j];
                        self.queue_handles[j - 1] = self.queue_handles[j];
                    }
                    self.queue_len -= 1;
                    self.policy.queued_items = self.queue_len;
                    self.policy.busy_workers += 1;
                    if h != 0 && self.in_flight_len < MAX_POOL_WORKERS {
                        self.in_flight_handles[self.in_flight_len] = h;
                        self.in_flight_len += 1;
                    }
                    popped = Some((op_id, h));
                    break;
                }
            }
            self.lock.unlock();

            if let Some((op_id, h)) = popped {
                (self.execute_fn)(op_id);

                self.lock.lock();
                self.policy.busy_workers = self.policy.busy_workers.saturating_sub(1);
                if h != 0 {
                    if let Some(pos) = self.in_flight_handles[..self.in_flight_len]
                        .iter()
                        .position(|&x| x == h)
                    {
                        self.in_flight_handles[pos] =
                            self.in_flight_handles[self.in_flight_len - 1];
                        self.in_flight_len -= 1;
                    }
                }
                self.lock.unlock();

                // Wake self-pipe for poll() loop and wake any worker waiting on handle ordering.
                let wfd = self.notify_write_fd.load(Ordering::Acquire);
                if wfd >= 0 {
                    let byte: u8 = 1;
                    let _ = crate::linux_syscall::syscall3(
                        crate::linux_syscall::nr::WRITE,
                        wfd as usize,
                        (&byte as *const u8) as usize,
                        1,
                    );
                }
                self.queue_wake.fetch_add(1, Ordering::Release);
                let _ = crate::linux_syscall::futex_wake(self.queue_wake.as_ptr(), 1);
                continue;
            }

            let ret = crate::linux_syscall::futex_wait_timeout(
                self.queue_wake.as_ptr(),
                observed_seq,
                &timeout,
            );
            if ret == -(crate::linux_syscall::ETIMEDOUT as isize) {
                let now =
                    crate::linux_syscall::clock_gettime_ns(crate::linux_syscall::CLOCK_MONOTONIC);
                self.lock.lock();
                let retire = self.policy.try_retire(now);
                self.lock.unlock();
                if retire {
                    break;
                }
            }
        }

        slot.in_use.store(false, Ordering::Release);
    }

    pub unsafe fn shutdown(&mut self) {
        self.shutdown.store(true, Ordering::Release);
        self.queue_wake.fetch_add(1, Ordering::Release);
        let _ = crate::linux_syscall::futex_wake(self.queue_wake.as_ptr(), i32::MAX);
        for slot in &self.slots {
            loop {
                let tid = slot.child_tid.load(Ordering::Acquire);
                if tid == 0 {
                    break;
                }
                let _ = crate::linux_syscall::futex_wait(slot.child_tid.as_ptr(), tid);
            }
        }
        self.lock.lock();
        self.reclaim_retired_stacks_locked();
        self.policy.active_workers = 0;
        self.policy.busy_workers = 0;
        self.policy.spawning = false;
        self.lock.unlock();
    }
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
unsafe extern "C" fn worker_entry(arg: *mut core::ffi::c_void) -> ! {
    let slot = &*arg.cast::<WorkerSlot>();
    let pool = &mut *(slot.pool_ptr.load(Ordering::Acquire) as *mut LinuxWorkerPool);
    let hint = (arg as usize) as u64;
    pool.worker_loop(slot, hint);
    crate::linux_syscall::exit_thread(0);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pacing_law_allows_immediate_cold_start_and_paces_subsequent_spawns() {
        let mut p = PacedScalingPolicy::new(0, 16, 5_000_000_000);
        p.queued_items = 1;
        // Cold start (W = 0) spawns immediately.
        assert!(p.should_spawn(1_000_000));
        p.on_spawn_started(1_000_000);
        // Single-flight prevents concurrent spawn while spawning == true.
        assert!(!p.should_spawn(2_000_000));

        // Worker ready 40 us later; T_ready EMA updates from 50 us -> (150 + 40)/4 = 47.5 us.
        p.on_worker_ready(1_040_000);
        assert_eq!(p.active_workers, 1);
        assert_eq!(p.t_ready_ns, 47_500);

        // Now B = 1, Q = 1 => required dt = 47_500 * (1/1) = 47_500 ns from last_spawn (1_000_000).
        p.busy_workers = 1;
        p.queued_items = 1;
        assert!(!p.should_spawn(1_047_499));
        assert!(p.should_spawn(1_047_500));
    }

    #[test]
    fn pacing_law_scales_faster_when_queue_depth_grows() {
        let mut p = PacedScalingPolicy::new(0, 16, 5_000_000_000);
        p.active_workers = 4;
        p.busy_workers = 4;
        p.t_ready_ns = 100_000;
        p.last_spawn_ns = 1_000_000;

        // With Q = 1, B/Q = 4 => required dt = 400_000 ns.
        p.queued_items = 1;
        assert!(!p.should_spawn(1_150_000));
        assert!(p.should_spawn(1_400_000));

        // With Q = 8, B/Q = 0.5 => required dt = 50_000 ns.
        p.queued_items = 8;
        assert!(!p.should_spawn(1_049_999));
        assert!(p.should_spawn(1_050_000));
    }

    #[test]
    fn linear_retirement_retires_at_most_one_worker_per_t_idle() {
        let mut p = PacedScalingPolicy::new(0, 16, 5_000_000_000);
        p.active_workers = 3;
        p.queued_items = 0;
        p.last_retire_ns = 10_000_000_000;

        // Before T_idle elapses, nobody retires.
        assert!(!p.try_retire(14_999_999_999));
        assert_eq!(p.active_workers, 3);

        // At +5s, exactly one worker retires.
        assert!(p.try_retire(15_000_000_000));
        assert_eq!(p.active_workers, 2);

        // Immediately afterwards at the same timestamp, a second worker cannot retire.
        assert!(!p.try_retire(15_000_000_000));
        assert_eq!(p.active_workers, 2);

        // At +10s, the second worker retires; at +15s, the third retires down to 0.
        assert!(p.try_retire(20_000_000_000));
        assert!(p.try_retire(25_000_000_000));
        assert_eq!(p.active_workers, 0);
        assert!(!p.try_retire(30_000_000_000));
    }
}
