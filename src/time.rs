//! Time handling and async sleeps.
//!
//! `sleep` is implemented without threads or platform timer backends: on
//! native targets a [`Sleep`] future registers a deadline in a thread-local
//! timer list. Each call to [`crate::rt::poll_step`] reports the earliest
//! deadline back to the host as `PollStatus::Idle::next_deadline`, so the
//! host can sleep at most that long before driving the runtime again. On
//! WASM the host's `timer_set` import schedules the wake-up directly.

#![cfg_attr(
    target_family = "wasm",
    allow(
        clippy::cast_possible_truncation,
        clippy::cast_possible_wrap,
        clippy::cast_sign_loss,
        clippy::missing_const_for_fn,
    )
)]

#[cfg(not(target_family = "wasm"))]
mod native_time {
    use std::{
        future::Future,
        pin::Pin,
        task::{Context, Poll},
        time::{Duration, Instant},
    };

    /// Future that resolves once a target [`Instant`] has been reached.
    ///
    /// The future registers its deadline with the runtime on first poll and
    /// re-checks `Instant::now()` on every subsequent poll. The runtime
    /// reports the soonest pending deadline back to the host so the host can
    /// schedule the next [`crate::rt::poll_step`] accordingly.
    pub struct Sleep {
        deadline: Instant,
        timer_id: Option<u64>,
    }

    impl Sleep {
        fn new(duration: Duration) -> Self {
            Self {
                deadline: Instant::now() + duration,
                timer_id: None,
            }
        }
    }

    impl Future for Sleep {
        type Output = ();

        fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<()> {
            if Instant::now() >= self.deadline {
                if let Some(id) = self.timer_id.take() {
                    crate::rt::timers::cancel_timer(id);
                }
                return Poll::Ready(());
            }
            if self.timer_id.is_none() {
                self.timer_id = Some(crate::rt::timers::register_timer(self.deadline));
            }
            Poll::Pending
        }
    }

    impl Drop for Sleep {
        fn drop(&mut self) {
            if let Some(id) = self.timer_id.take() {
                crate::rt::timers::cancel_timer(id);
            }
        }
    }

    /// Sleep asynchronously for `duration`.
    ///
    /// The current task yields until `duration` has elapsed. No thread is
    /// blocked; the host schedules wake-ups via the deadline reported by
    /// [`crate::rt::poll_step`].
    pub fn sleep(duration: Duration) -> Sleep {
        Sleep::new(duration)
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_time::{Sleep, sleep};

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedFuture;

#[cfg(target_family = "wasm")]
/// Sleep asynchronously for `duration` (WASM host-scheduled timer).
pub async fn sleep(duration: std::time::Duration) {
    let delay_ms = duration.as_millis() as u32;
    let _ = OverlappedFuture::new(move |ov| {
        // SAFETY: `ov` is a valid overlapped pointer supplied by the runtime.
        unsafe { imports::timer_set(ov, delay_ms) };
    })
    .await;
}

#[cfg(target_family = "wasm")]
#[derive(Clone, Copy, Debug)]
/// A measurement of the host's monotonic clock (milliseconds since epoch).
pub struct Instant(u64);

#[cfg(target_family = "wasm")]
impl Instant {
    /// Returns the current instant from the WASM host clock.
    pub fn now() -> Self {
        // SAFETY: `get_time` is a side-effect-free host import.
        Self(unsafe { imports::get_time() as u64 })
    }

    /// Returns the duration elapsed from `earlier` to `self`.
    pub fn duration_since(&self, earlier: Self) -> std::time::Duration {
        std::time::Duration::from_millis(self.0.saturating_sub(earlier.0))
    }

    /// Returns the time elapsed since this instant was created.
    pub fn elapsed(&self) -> std::time::Duration {
        Self::now().duration_since(*self)
    }
}

#[cfg(target_family = "wasm")]
/// System time type (re-export of [`std::time::SystemTime`]).
pub type SystemTime = std::time::SystemTime;
#[cfg(target_family = "wasm")]
/// System time error type (re-export of [`std::time::SystemTimeError`]).
pub type SystemTimeError = std::time::SystemTimeError;
