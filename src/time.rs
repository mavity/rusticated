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

/// Re-export `core::time::Duration` as the canonical duration type.
pub use core::time::Duration;

// â”€â”€â”€ Native Instant
// â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

#[cfg(not(target_family = "wasm"))]
mod native_instant {
    use core::time::Duration;

    // â”€â”€ Unix: clock_gettime(CLOCK_MONOTONIC)
    // â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

    #[cfg(unix)]
    #[repr(C)]
    struct Timespec {
        tv_sec: i64,
        tv_nsec: i64,
    }

    #[cfg(unix)]
    unsafe extern "C" {
        fn clock_gettime(clk_id: i32, tp: *mut Timespec) -> i32;
    }

    #[cfg(unix)]
    const CLOCK_MONOTONIC: i32 = 1;

    // â”€â”€ Windows: QueryPerformanceCounter
    // â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

    #[cfg(windows)]
    unsafe extern "system" {
        fn QueryPerformanceCounter(lp_performance_count: *mut i64) -> i32;
        fn QueryPerformanceFrequency(lp_frequency: *mut i64) -> i32;
    }

    #[cfg(windows)]
    static QPC_FREQ: core::sync::atomic::AtomicI64 = core::sync::atomic::AtomicI64::new(0);

    #[cfg(windows)]
    fn qpc_freq() -> i64 {
        use core::sync::atomic::Ordering;
        let cached = QPC_FREQ.load(Ordering::Relaxed);
        if cached != 0 {
            return cached;
        }
        let mut f = 0i64;
        // SAFETY: pointer is valid for the call duration.
        unsafe { QueryPerformanceFrequency(&mut f) };
        QPC_FREQ.store(f, Ordering::Relaxed);
        f
    }

    /// A measurement of a monotonic clock, in nanoseconds since an
    /// unspecified epoch.
    #[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
    pub struct Instant(u64);

    impl Instant {
        /// Returns the current instant.
        pub fn now() -> Self {
            #[cfg(unix)]
            {
                let mut ts = Timespec {
                    tv_sec: 0,
                    tv_nsec: 0,
                };
                // SAFETY: ts is valid for the call duration.
                unsafe { clock_gettime(CLOCK_MONOTONIC, &mut ts) };
                let nanos = (ts.tv_sec as u64)
                    .wrapping_mul(1_000_000_000)
                    .wrapping_add(ts.tv_nsec as u64);
                Self(nanos)
            }
            #[cfg(windows)]
            {
                let mut count = 0i64;
                // SAFETY: pointer is valid for the call duration.
                unsafe { QueryPerformanceCounter(&mut count) };
                let freq = qpc_freq();
                // Convert ticks â†’ nanoseconds.
                let nanos = if freq > 0 {
                    (count as u128).wrapping_mul(1_000_000_000) / freq as u128
                } else {
                    0
                };
                Self(nanos as u64)
            }
            #[cfg(not(any(unix, windows)))]
            {
                Self(0)
            }
        }

        /// Returns the amount of time elapsed from `earlier` to `self`.
        #[must_use]
        pub fn duration_since(&self, earlier: Self) -> Duration {
            Duration::from_nanos(self.0.saturating_sub(earlier.0))
        }

        /// Returns the time elapsed since this instant.
        #[must_use]
        pub fn elapsed(&self) -> Duration {
            Self::now().duration_since(*self)
        }
    }

    impl core::ops::Sub for Instant {
        type Output = Duration;
        fn sub(self, rhs: Self) -> Duration {
            self.duration_since(rhs)
        }
    }

    impl core::ops::Add<Duration> for Instant {
        type Output = Self;
        fn add(self, rhs: Duration) -> Self {
            Self(self.0.saturating_add(rhs.as_nanos() as u64))
        }
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_instant::Instant;

// â”€â”€â”€ Native SystemTime
// â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

/// A simple wall-clock timestamp (nanoseconds since the Unix epoch).
#[cfg(not(target_family = "wasm"))]
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct SystemTime(pub u64);

/// Error returned when a `SystemTime` subtraction underflows.
#[cfg(not(target_family = "wasm"))]
#[derive(Debug)]
pub struct SystemTimeError;

#[cfg(not(target_family = "wasm"))]
impl core::fmt::Display for SystemTimeError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str("system time error")
    }
}

// â”€â”€â”€ Native Sleep future
// â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

#[cfg(not(target_family = "wasm"))]
mod native_time {
    use super::Instant;
    use core::future::Future;
    use core::pin::Pin;
    use core::task::{Context, Poll};
    use core::time::Duration;

    /// Future that resolves once a target [`Instant`] has been reached.
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
    pub fn sleep(duration: Duration) -> Sleep {
        Sleep::new(duration)
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_time::{Sleep, sleep};

// â”€â”€â”€ WASM
// â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedFuture;

/// Sleep asynchronously for `duration` (WASM host-scheduled timer).
#[cfg(target_family = "wasm")]
pub async fn sleep(duration: Duration) {
    let delay_ms = duration.as_millis() as u32;
    let _ = OverlappedFuture::new(move |ov| {
        // SAFETY: `ov` is a valid overlapped pointer supplied by the runtime.
        unsafe { imports::timer_set(ov, delay_ms) };
    })
    .await;
}

/// A measurement of the host's monotonic clock (milliseconds since an epoch).
#[cfg(target_family = "wasm")]
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Instant(u64);

#[cfg(target_family = "wasm")]
impl Instant {
    /// Returns the current instant from the WASM host clock.
    pub fn now() -> Self {
        // SAFETY: `get_time` is a side-effect-free host import.
        Self(unsafe { imports::get_time() as u64 })
    }

    /// Returns the duration elapsed from `earlier` to `self`.
    #[must_use]
    pub fn duration_since(&self, earlier: Self) -> Duration {
        Duration::from_millis(self.0.saturating_sub(earlier.0))
    }

    /// Returns the time elapsed since this instant was created.
    #[must_use]
    pub fn elapsed(&self) -> Duration {
        Self::now().duration_since(*self)
    }
}

#[cfg(target_family = "wasm")]
impl core::ops::Add<Duration> for Instant {
    type Output = Self;
    fn add(self, rhs: Duration) -> Self {
        Self(self.0.saturating_add(rhs.as_millis() as u64))
    }
}

#[cfg(target_family = "wasm")]
impl core::ops::Sub for Instant {
    type Output = Duration;
    fn sub(self, rhs: Self) -> Duration {
        self.duration_since(rhs)
    }
}

/// Wall-clock timestamp (nanoseconds since Unix epoch).
#[cfg(target_family = "wasm")]
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct SystemTime(pub u64);

/// Error returned when a `SystemTime` subtraction underflows.
#[cfg(target_family = "wasm")]
#[derive(Debug)]
pub struct SystemTimeError;

#[cfg(target_family = "wasm")]
impl core::fmt::Display for SystemTimeError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str("system time error")
    }
}
