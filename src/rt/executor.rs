use crate::boxed::Box;
use crate::cell::RefCell;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll};
use crate::time::Duration;

use super::{timers::next_deadline, waker::noop_waker};

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

thread_local! {
    static MAIN_FUTURE: RefCell<Option<Pin<Box<dyn Future<Output = ()>>>>> =
        RefCell::new(None);
    static DRIVER: RefCell<Option<Driver>> = RefCell::new(None);
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

/// Submit a top-level future to the runtime.
///
/// The future is stored on the current thread and will be polled by each
/// subsequent [`poll_step`] call. A second call to `run` while a future is
/// already in flight replaces it.
pub fn run<F>(future: F)
where
    F: Future<Output = ()> + 'static,
{
    MAIN_FUTURE.with(|main| {
        *main.borrow_mut() = Some(Box::pin(future));
    });
}

/// Outcome of one [`poll_step`] iteration.
///
/// The host uses this to decide whether to keep ticking and how long it may
/// safely sleep before the next tick.
#[derive(Debug, Clone, Copy)]
pub enum PollStatus {
    /// The top-level future is complete; no further work is required.
    Done,
    /// Work was performed (I/O events processed or future polled).
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
/// Performs a non-blocking platform poll, then polls the main future once.
/// Returns a [`PollStatus`] describing the outcome.
pub fn poll_step() -> io::Result<PollStatus> {
    // Drive the platform reactor with a zero timeout.
    let had_events = with_driver(|d| d.poll_nonblocking())??;

    // Poll the main future once.
    let waker = noop_waker();
    let mut cx = Context::from_waker(&waker);
    let main_status = MAIN_FUTURE.with(|main_fut| {
        let mut borrow = main_fut.borrow_mut();
        if let Some(fut) = borrow.as_mut() {
            match fut.as_mut().poll(&mut cx) {
                Poll::Ready(()) => {
                    *borrow = None;
                    Some(true)
                }
                Poll::Pending => Some(false),
            }
        } else {
            None
        }
    });

    Ok(match main_status {
        Some(true) => PollStatus::Done,
        Some(false) if had_events => PollStatus::Ready,
        Some(false) => PollStatus::Idle {
            next_deadline: next_deadline(),
        },
        None => PollStatus::Done,
    })
}
