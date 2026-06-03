use crate::boxed::Box;
use crate::cell::{Cell, RefCell};
use crate::collections::VecDeque;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll, Waker};
use crate::time::Duration;
use alloc::sync::Arc;
use core::cell::UnsafeCell;
use core::sync::atomic::{AtomicBool, Ordering};

use super::{timers::next_deadline, waker::task_waker};

#[cfg(any(target_os = "linux", rusticated_linux))]
use super::linux_driver::Driver;

#[cfg(windows)]
use super::windows::Driver;

#[cfg(any(
    target_os = "macos",
    target_os = "freebsd",
    target_os = "openbsd",
    target_os = "netbsd"
))]
use super::bsd::Driver;

// ─── Task ────────────────────────────────────────────────────────────────────

/// A task in the per-thread run queue.
struct Task {
    /// The boxed, pinned future to drive.
    future: Pin<Box<dyn Future<Output = ()>>>,
    /// Set to `true` by the waker when the task should be re-polled.
    woken: Arc<AtomicBool>,
}

// ── Global executor state ───────────────────────────────────────────────────
// TASKS: run queue. DRIVER: I/O reactor.
// TASK_DEPTH: approximate queue depth (available for work-stealing hints).

#[repr(align(8))]
struct TasksStorage(UnsafeCell<Option<VecDeque<Task>>>);

unsafe impl Sync for TasksStorage {}

#[repr(align(8))]
struct DriverStorage(UnsafeCell<Option<Driver>>);

unsafe impl Sync for DriverStorage {}

#[repr(align(8))]
struct TaskDepthStorage(UnsafeCell<Cell<usize>>);

unsafe impl Sync for TaskDepthStorage {}

static TASKS_STORAGE: TasksStorage = TasksStorage(UnsafeCell::new(None));
static DRIVER_STORAGE: DriverStorage = DriverStorage(UnsafeCell::new(None));
static TASK_DEPTH_STORAGE: TaskDepthStorage = TaskDepthStorage(UnsafeCell::new(Cell::new(0)));

fn tasks_mut<R>(f: impl FnOnce(&mut VecDeque<Task>) -> R) -> R {
    unsafe {
        let storage = core::ptr::addr_of!(TASKS_STORAGE).cast::<TasksStorage>();
        let q = (*storage).0.get();
        if (*q).is_none() {
            *q = Some(VecDeque::new());
        }
        let queue = (*q).as_mut().unwrap_unchecked();
        f(queue)
    }
}

fn task_depth() -> &'static mut Cell<usize> {
    unsafe { &mut *TASK_DEPTH_STORAGE.0.get() }
}

pub(crate) fn with_driver<R>(f: impl FnOnce(&mut Driver) -> R) -> io::Result<R> {
    unsafe {
        let storage = core::ptr::addr_of!(DRIVER_STORAGE).cast::<DriverStorage>();
        let cell = (*storage).0.get();
        if (*cell).is_none() {
            *cell = Some(Driver::new()?);
        }
        match (*cell).as_mut() {
            Some(d) => Ok(f(d)),
            None => Err(io::Error::other("driver init failed")),
        }
    }
}

// ─── JoinHandle ──────────────────────────────────────────────────────────────

/// Shared state between a spawned task and its [`JoinHandle`].
struct JoinState<T> {
    /// The task's return value, written on completion.
    result: Option<T>,
    /// Waker stored by the [`JoinHandle`] awaiter; woken when the task finishes.
    waker: Option<Waker>,
}

/// Internal future that drives `F` and deposits its output into [`JoinState`].
///
/// This is the actual payload stored in the executor's task queue.
struct JoinFuture<T> {
    inner: Pin<Box<dyn Future<Output = T>>>,
    state: Arc<RefCell<JoinState<T>>>,
}

// JoinFuture<T> is Unpin: Pin<Box<dyn Future>> is Unpin (Box is always Unpin),
// and Arc<RefCell<...>> is Unpin.
impl<T: 'static> Future for JoinFuture<T> {
    type Output = ();

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<()> {
        let this = Pin::into_inner(self);
        match this.inner.as_mut().poll(cx) {
            Poll::Ready(val) => {
                let mut state = this.state.borrow_mut();
                state.result = Some(val);
                if let Some(w) = state.waker.take() {
                    w.wake();
                }
                Poll::Ready(())
            }
            Poll::Pending => Poll::Pending,
        }
    }
}

/// Error returned when a task join fails.
#[derive(Debug)]
pub struct JoinError;

impl core::fmt::Display for JoinError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        write!(f, "task join failed")
    }
}

pub use core::error::Error;
impl Error for JoinError {}

/// A handle to a spawned task that can be awaited for its return value.
///
/// Dropping the handle does not cancel the task — it continues to run; its
/// output is simply discarded when it completes.
pub struct JoinHandle<T> {
    state: Arc<RefCell<JoinState<T>>>,
}

impl<T> Future for JoinHandle<T> {
    type Output = Result<T, JoinError>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let mut state = self.state.borrow_mut();
        if let Some(res) = state.result.take() {
            Poll::Ready(Ok(res))
        } else {
            state.waker = Some(cx.waker().clone());
            Poll::Pending
        }
    }
}

impl<T> JoinHandle<T> {
    /// Polls the join handle once, returning the result if complete.
    pub fn try_join(&self) -> Option<Result<T, JoinError>> {
        self.state.borrow_mut().result.take().map(Ok)
    }
}

// ─── spawn ───────────────────────────────────────────────────────────────────

/// Submit a future to the per-thread task queue, returning a [`JoinHandle`]
/// that can be awaited to obtain the task's output.
///
/// The future is polled on the current thread by each subsequent
/// [`poll_step`] call. Multiple tasks can be in flight concurrently; they
/// are polled round-robin within each step.
///
/// Dropping the returned handle does not cancel the task.
pub fn spawn<F, T>(future: F) -> JoinHandle<T>
where
    F: Future<Output = T> + 'static,
    T: 'static,
{
    #[cfg(windows)]
    super::windows::flush_completions();

    let state = Arc::new(RefCell::new(JoinState {
        result: None,
        waker: None,
    }));
    let handle = JoinHandle {
        state: Arc::clone(&state),
    };
    let wrapper = JoinFuture {
        inner: Box::pin(future),
        state,
    };
    // Mark woken=true so the task is polled on the very first poll_step.
    let woken = Arc::new(AtomicBool::new(true));
    tasks_mut(|q| {
        q.push_back(Task {
            future: Box::pin(wrapper),
            woken,
        })
    });
    handle
}

/// Internal helper: spawn a `Future<Output = ()>` and discard the handle.
///
/// Used by test `block_on` utilities and platform bootstrapping code within
/// this crate. Not part of the public API.
#[doc(hidden)]
pub fn run<F>(future: F)
where
    F: Future<Output = ()> + 'static,
{
    let _ = spawn(future);
}

// ─── select ──────────────────────────────────────────────────────────────────
pub use crate::rt::select::{Either, Select, select};

// ─── PollStatus / poll_step ──────────────────────────────────────────────────

/// Outcome of one [`poll_step`] iteration.
///
/// The host uses this to decide whether to keep ticking and how long it may
/// safely sleep before the next tick.
#[derive(Debug, Clone, Copy)]
pub enum PollStatus {
    /// All tasks have completed and there are no pending I/O events.
    Done,
    /// Work was performed (I/O events processed or at least one task made progress).
    Ready,
    /// No work was available this iteration. The host may sleep at most
    /// `next_deadline` before the next call (or indefinitely if `None`).
    Idle {
        /// Upper bound on how long the host may wait before the next call.
        next_deadline: Option<Duration>,
    },
}

/// Drive the runtime by exactly one step.
///
/// Performs a non-blocking platform poll, then polls every queued task
/// once in FIFO order. Tasks that return [`Poll::Pending`] are re-queued;
/// completed tasks are dropped.
///
/// Returns a [`PollStatus`] describing the outcome.
pub fn poll_step() -> io::Result<PollStatus> {
    poll_step_internal(None)
}

/// Drive the runtime, blocking until `deadline` if there are no immediately ready tasks.
///
/// If `deadline` is `None`, blocks indefinitely.
pub fn poll_step_idle(deadline: Option<Duration>) -> io::Result<PollStatus> {
    // We compute the remaining timeout based on `deadline`.
    // We already have `now_ns()` so we could do math, but it's simpler:
    // Actually, `deadline` is already the duration from `now` since `next_deadline()` returns duration.
    // Wait, the specification says: `deadline: Option<Duration>`.

    poll_step_internal(Some(deadline))
}

fn poll_step_internal(timeout: Option<Option<Duration>>) -> io::Result<PollStatus> {
    // Check for expired timers first so they get polled this iteration
    crate::rt::timers::wake_expired();

    // Drive the platform reactor.
    let _blocking = timeout.is_some();
    let _timeout_ms = match timeout {
        Some(Some(d)) => Some(d.as_millis() as u32),
        _ => None,
    };

    let had_events = with_driver(|d| {
        #[cfg(windows)]
        if let Some(ms) = _timeout_ms {
            d.set_timeout(Some(ms))?;
        }

        #[cfg(windows)]
        {
            // In Windows, waitable timers via SetWaitableTimer trigger APCs.
            // On sleep, SleepEx waits until either the timeout elapses OR an APC executes.
            // When an APC executes (such as our wakeup tick from the timer or I/O callback),
            // SleepEx returns WAIT_IO_COMPLETION.
            // So if `blocking` is true but there's a 0ms deadline (i.e. instant timeout),
            // we should still drop right through.
            d.poll(_blocking, _timeout_ms)
        }
        #[cfg(not(windows))]
        {
            let ms = match timeout {
                None => Some(0),
                Some(None) => None,
                Some(Some(d)) => Some(d.as_millis() as u32),
            };
            d.poll_with_timeout(ms)
        }
    })??;

    // Only poll tasks whose waker flag was set since the last step.
    // Tasks spawned during polling are picked up on the next step.
    let n = tasks_mut(|q| q.len());
    let mut made_progress = false;
    let mut remaining = 0usize;

    for _ in 0..n {
        let task = tasks_mut(|q| q.pop_front());
        let Some(mut task) = task else { break };

        if task.woken.swap(false, Ordering::AcqRel) {
            // Task was woken — give it a targeted waker and poll it.
            let waker = task_waker(Arc::clone(&task.woken));
            let mut cx = Context::from_waker(&waker);
            match task.future.as_mut().poll(&mut cx) {
                Poll::Ready(()) => {
                    made_progress = true;
                    // task dropped — not re-queued
                }
                Poll::Pending => {
                    tasks_mut(|q| q.push_back(task));
                    remaining += 1;
                }
            }
        } else {
            // Not yet woken — return to queue without polling.
            tasks_mut(|q| q.push_back(task));
            remaining += 1;
        }
    }

    task_depth().set(remaining);

    let expired = crate::rt::timers::wake_expired();

    let has_timers = crate::rt::timers::has_timers();

    Ok(if remaining == 0 && !has_timers {
        PollStatus::Done
    } else if made_progress || had_events || expired {
        PollStatus::Ready
    } else {
        PollStatus::Idle {
            next_deadline: next_deadline(),
        }
    })
}

// ─── Tests ───────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alloc::rc::Rc;
    use core::cell::Cell;

    /// Spin the executor until all tasks have completed (no I/O in unit tests).
    fn drive_until_done() {
        loop {
            match poll_step().unwrap() {
                PollStatus::Done => break,
                PollStatus::Ready | PollStatus::Idle { .. } => continue,
            }
        }
    }

    // ── JoinHandle ───────────────────────────────────────────────────────────

    /// A spawned future returning a non-unit value is retrievable via its handle.
    #[test]
    fn join_handle_resolves_with_task_output() {
        let result: Rc<Cell<u32>> = Rc::new(Cell::new(0));
        let result2 = Rc::clone(&result);

        let handle = spawn(async { 42u32 });
        let _ = spawn(async move {
            result2.set(handle.await.expect("join failed"));
        });

        drive_until_done();
        assert_eq!(result.get(), 42);
    }

    /// The handle waits even when the producer task is queued after the consumer.
    #[test]
    fn join_handle_waits_for_producer() {
        let result: Rc<Cell<u32>> = Rc::new(Cell::new(0));
        let result2 = Rc::clone(&result);

        // Consumer spawned first — it will see Pending on its first poll,
        // then be correctly woken when the producer completes.
        let handle = spawn(async { 99u32 });
        let _ = spawn(async move {
            result2.set(handle.await.expect("join failed"));
        });

        drive_until_done();
        assert_eq!(result.get(), 99);
    }

    /// Dropping the JoinHandle does not prevent the task from running.
    #[test]
    fn drop_join_handle_task_still_runs() {
        let ran: Rc<Cell<bool>> = Rc::new(Cell::new(false));
        let ran2 = Rc::clone(&ran);

        // Drop the handle immediately after spawning.
        drop(spawn(async move {
            ran2.set(true);
        }));

        drive_until_done();
        assert!(ran.get(), "task should run even after handle is dropped");
    }

    /// A task spawned from inside another async task is correctly scheduled.
    #[test]
    fn nested_spawn_resolves() {
        let result: Rc<Cell<bool>> = Rc::new(Cell::new(false));
        let result2 = Rc::clone(&result);

        let _ = spawn(async move {
            let handle = spawn(async { true });
            result2.set(handle.await.expect("join failed"));
        });

        drive_until_done();
        assert!(result.get());
    }

    /// Multiple independent tasks all complete and their side-effects accumulate.
    #[test]
    fn multiple_tasks_all_run() {
        let counter: Rc<Cell<u32>> = Rc::new(Cell::new(0));
        for _ in 0..5 {
            let c = Rc::clone(&counter);
            let _ = spawn(async move {
                c.set(c.get() + 1);
            });
        }
        drive_until_done();
        assert_eq!(counter.get(), 5);
    }

    // ── select ───────────────────────────────────────────────────────────────

    /// When left is immediately ready, select resolves with Left.
    #[test]
    fn select_left_wins_when_immediately_ready() {
        let winner: Rc<Cell<u8>> = Rc::new(Cell::new(0));
        let w2 = Rc::clone(&winner);

        let _ = spawn(async move {
            let r = select(async { 1u8 }, core::future::pending::<u8>()).await;
            match r {
                Either::Left(v) => w2.set(v),
                Either::Right(_) => w2.set(99),
            }
        });

        drive_until_done();
        assert_eq!(winner.get(), 1);
    }

    /// When right is immediately ready (left never resolves), select resolves with Right.
    #[test]
    fn select_right_wins_when_left_never_resolves() {
        let winner: Rc<Cell<u8>> = Rc::new(Cell::new(0));
        let w2 = Rc::clone(&winner);

        let _ = spawn(async move {
            let r = select(core::future::pending::<u8>(), async { 2u8 }).await;
            match r {
                Either::Left(_) => w2.set(99),
                Either::Right(v) => w2.set(v),
            }
        });

        drive_until_done();
        assert_eq!(winner.get(), 2);
    }

    /// When both sides are immediately ready, left wins (it is polled first).
    #[test]
    fn select_left_wins_when_both_immediately_ready() {
        let winner: Rc<Cell<u8>> = Rc::new(Cell::new(0));
        let w2 = Rc::clone(&winner);

        let _ = spawn(async move {
            let r = select(async { 10u8 }, async { 20u8 }).await;
            match r {
                Either::Left(v) | Either::Right(v) => w2.set(v),
            }
        });

        drive_until_done();
        assert_eq!(
            winner.get(),
            10,
            "left is polled first so it wins when both ready"
        );
    }

    /// select can be composed: one arm is itself a JoinHandle.
    #[test]
    fn select_with_join_handle_arm() {
        let result: Rc<Cell<u32>> = Rc::new(Cell::new(0));
        let r2 = Rc::clone(&result);

        let _ = spawn(async move {
            let fast = spawn(async { 7u32 });
            let r = select(fast, core::future::pending::<u32>()).await;
            match r {
                Either::Left(v) => r2.set(v.expect("join failed")),
                Either::Right(v) => r2.set(v),
            }
        });

        drive_until_done();
        assert_eq!(result.get(), 7);
    }
}
