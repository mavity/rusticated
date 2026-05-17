use crate::boxed::Box;
use crate::cell::RefCell;
use crate::collections::VecDeque;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll};
use crate::time::Duration;
use alloc::sync::Arc;
use core::sync::atomic::{AtomicBool, Ordering};

use super::{timers::next_deadline, waker::task_waker};

#[cfg(target_os = "linux")]
use super::linux_epoll::Driver;

#[cfg(windows)]
use super::windows::Driver;

#[cfg(any(
    target_os = "macos",
    target_os = "freebsd",
    target_os = "openbsd",
    target_os = "netbsd"
))]
use super::bsd::Driver;

/// A task in the per-thread run queue.
struct Task {
    /// The boxed, pinned future to drive.
    future: Pin<Box<dyn Future<Output = ()>>>,
    /// Set to `true` by the waker when the task should be re-polled.
    woken: Arc<AtomicBool>,
}

thread_local! {
    static TASKS: RefCell<VecDeque<Task>> = RefCell::new(VecDeque::new());
    static DRIVER: RefCell<Option<Driver>> = RefCell::new(None);
    /// Approximate depth of the local run queue. Future work-stealing logic
    /// can read peer depth counters (via a global thread registry) to pick
    /// steal targets without probing their queues directly.
    static TASK_DEPTH: RefCell<usize> = RefCell::new(0);
}

pub(crate) fn with_driver<R>(f: impl FnOnce(&mut Driver) -> R) -> io::Result<R> {
    DRIVER.with(|cell| {
        let mut borrow = cell.borrow_mut();
        if borrow.is_none() {
            *borrow = Some(Driver::new()?);
        }
        let Some(driver) = borrow.as_mut() else {
            return Err(io::Error::other("driver init race"));
        };
        Ok(f(driver))
    })
}

/// Submit a future to the per-thread task queue.
///
/// The future is polled on the current thread by each subsequent
/// [`poll_step`] call. Multiple tasks can be in flight concurrently; they
/// are polled round-robin within each step.
pub fn spawn<F>(future: F)
where
    F: Future<Output = ()> + 'static,
{
    // Mark woken=true so the task is polled on the very first poll_step.
    let woken = Arc::new(AtomicBool::new(true));
    TASKS.with(|q| {
        q.borrow_mut().push_back(Task {
            future: Box::pin(future),
            woken,
        })
    });
}

/// Submit a top-level future to the runtime.
///
/// Equivalent to [`spawn`]. Kept for backwards compatibility.
pub fn run<F>(future: F)
where
    F: Future<Output = ()> + 'static,
{
    spawn(future);
}

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
    // Drive the platform reactor with a zero timeout.
    let had_events = with_driver(|d| d.poll_nonblocking())??;

    // Only poll tasks whose waker flag was set since the last step.
    // Tasks spawned during polling are picked up on the next step.
    let n = TASKS.with(|q| q.borrow().len());
    let mut made_progress = false;
    let mut remaining = 0usize;

    for _ in 0..n {
        let task = TASKS.with(|q| q.borrow_mut().pop_front());
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
                    TASKS.with(|q| q.borrow_mut().push_back(task));
                    remaining += 1;
                }
            }
        } else {
            // Not yet woken — return to queue without polling.
            TASKS.with(|q| q.borrow_mut().push_back(task));
            remaining += 1;
        }
    }

    TASK_DEPTH.with(|d| *d.borrow_mut() = remaining);

    Ok(if remaining == 0 && !had_events {
        PollStatus::Done
    } else if made_progress || had_events {
        PollStatus::Ready
    } else {
        PollStatus::Idle {
            next_deadline: next_deadline(),
        }
    })
}
