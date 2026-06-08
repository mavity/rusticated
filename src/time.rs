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

/// Returns the current wall-clock time as nanoseconds since the UNIX epoch.
///
/// On WASM delegates to the host via `get_time()`. On native reads the system
/// clock via `std::time::SystemTime`.
pub fn now_ns() -> u64 {
    #[cfg(any(unix, rusticated_linux))]
    {
        #[repr(C)]
        struct Timespec {
            tv_sec: i64,
            tv_nsec: i64,
        }

        let mut ts = Timespec {
            tv_sec: 0,
            tv_nsec: 0,
        };

        #[cfg(any(target_os = "linux", rusticated_linux))]
        crate::syscall!(
            crate::os::linux::syscall::nr::CLOCK_GETTIME,
            0usize, // CLOCK_REALTIME
            &mut ts as *mut _ as usize
        );
        #[cfg(not(any(target_os = "linux", rusticated_linux)))]
        unsafe {
            unsafe extern "C" {
                fn clock_gettime(clk_id: i32, tp: *mut Timespec) -> i32;
            }
            clock_gettime(0, &mut ts); // CLOCK_REALTIME is 0
        }

        (ts.tv_sec as u64) * 1_000_000_000 + (ts.tv_nsec as u64)
    }
    #[cfg(windows)]
    {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetSystemTimeAsFileTime(lpSystemTimeAsFileTime: *mut u64);
        }
        let mut ft = 0u64;
        unsafe { GetSystemTimeAsFileTime(&mut ft) };
        let unix_epoch = 116444736000000000u64;
        if ft >= unix_epoch {
            (ft - unix_epoch) * 100
        } else {
            0
        }
    }
    #[cfg(target_family = "wasm")]
    {
        unsafe { crate::abi::imports::get_time() }
    }
}

/// Chrono-compatible UTC module for shell history timestamping.
pub mod chrono {
    /// UTC time source.
    pub struct Utc;

    impl Utc {
        /// Returns the current time in nanoseconds since epoch.
        pub fn now() -> u64 {
            crate::time::now_ns()
        }
    }
}

/// A measurement of the system clock.
#[cfg(not(target_family = "wasm"))]
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub struct SystemTime(Duration);

#[cfg(not(target_family = "wasm"))]
impl SystemTime {
    /// The UNIX epoch (January 1, 1970 00:00:00 UTC).
    pub const UNIX_EPOCH: Self = Self(Duration::ZERO);

    /// Returns the system time corresponding to "now".
    pub fn now() -> Self {
        Self(Duration::from_nanos(now_ns()))
    }

    /// Constructs a `SystemTime` from nanoseconds since the Unix epoch.
    pub(crate) fn from_nanos(ns: u64) -> Self {
        Self(Duration::from_nanos(ns))
    }

    /// Returns the amount of time elapsed since this system time was created.
    pub fn duration_since(&self, earlier: SystemTime) -> Result<Duration, Error> {
        if self.0 >= earlier.0 {
            Ok(self.0 - earlier.0)
        } else {
            // Ideally we'd have a specific error for this, but SystemTimeError is in std.
            Err(Error)
        }
    }
}

/// An error returned from system time calculations.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Error;

impl core::fmt::Display for Error {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        write!(f, "second time provided was later than self")
    }
}

impl core::error::Error for Error {}

/// The UNIX epoch (January 1, 1970 00:00:00 UTC).
#[cfg(not(target_family = "wasm"))]
pub const UNIX_EPOCH: SystemTime = SystemTime(Duration::ZERO);

/// Error returned when a `SystemTime` subtraction underflows.
#[cfg(not(target_family = "wasm"))]
pub type SystemTimeError = Error;

// ——— Native Instant
// -----------------------------------------------------------

#[cfg(not(target_family = "wasm"))]
mod native_instant {
    use core::time::Duration;

    // -- Unix: clock_gettime(CLOCK_MONOTONIC)
    // ---------------------------------

    #[cfg(any(unix, rusticated_linux))]
    #[repr(C)]
    struct Timespec {
        tv_sec: i64,
        tv_nsec: i64,
    }

    #[cfg(all(any(unix), not(any(target_os = "linux", rusticated_linux))))]
    unsafe extern "C" {
        fn clock_gettime(clk_id: i32, tp: *mut Timespec) -> i32;
    }

    #[cfg(all(any(unix), not(any(target_os = "linux", rusticated_linux))))]
    const CLOCK_MONOTONIC: i32 = 1;

    // -- Windows: QueryPerformanceCounter
    // ------------------------------------

    #[cfg(windows)]
    #[link(name = "kernel32", kind = "raw-dylib")]
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
            #[cfg(any(target_os = "linux", rusticated_linux))]
            {
                let mut ts = Timespec {
                    tv_sec: 0,
                    tv_nsec: 0,
                };
                crate::syscall!(
                    crate::os::linux::syscall::nr::CLOCK_GETTIME,
                    1usize, // CLOCK_MONOTONIC
                    &mut ts as *mut _ as usize
                );
                let nanos = (ts.tv_sec as u64)
                    .wrapping_mul(1_000_000_000)
                    .wrapping_add(ts.tv_nsec as u64);
                Self(nanos)
            }
            #[cfg(all(any(unix), not(any(target_os = "linux", rusticated_linux))))]
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

// ——— Native Sleep future
// ————————————————————————————————————————————————————————

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

        fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<()> {
            if Instant::now() >= self.deadline {
                if let Some(id) = self.timer_id.take() {
                    crate::rt::timers::cancel_timer(id);
                }
                return Poll::Ready(());
            }
            if self.timer_id.is_none() {
                self.timer_id = Some(crate::rt::timers::register_timer(
                    self.deadline,
                    cx.waker().clone(),
                ));
            }
            // Overwrite waker if already registered
            crate::rt::timers::cancel_timer(self.timer_id.unwrap());
            self.timer_id = Some(crate::rt::timers::register_timer(
                self.deadline,
                cx.waker().clone(),
            ));
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

// --- WASM
// ---------------------------------------------------------------------

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedFuture;

/// A future that resolves after a specified duration.
#[cfg(target_family = "wasm")]
pub struct Sleep {
    inner: OverlappedFuture,
}

#[cfg(target_family = "wasm")]
impl core::future::Future for Sleep {
    type Output = ();

    fn poll(
        mut self: core::pin::Pin<&mut Self>,
        cx: &mut core::task::Context<'_>,
    ) -> core::task::Poll<Self::Output> {
        match core::pin::Pin::new(&mut self.inner).poll(cx) {
            core::task::Poll::Ready(_) => core::task::Poll::Ready(()),
            core::task::Poll::Pending => core::task::Poll::Pending,
        }
    }
}

/// Sleep asynchronously for `duration` (WASM host-scheduled timer).
#[cfg(target_family = "wasm")]
pub fn sleep(duration: Duration) -> Sleep {
    let delay_ms = duration.as_millis() as u32;
    Sleep {
        inner: OverlappedFuture::new(move |ov| {
            // SAFETY: `ov` is a valid overlapped pointer supplied by the runtime.
            unsafe { imports::timer_set(ov, delay_ms) };
        }),
    }
}

/// A future that times out after a certain duration.
#[must_use = "futures do nothing unless you `.await` them"]
pub struct Timeout<F> {
    future: F,
    delay: Sleep,
}

impl<F: core::future::Future> core::future::Future for Timeout<F> {
    type Output = Result<F::Output, ()>;

    fn poll(
        self: core::pin::Pin<&mut Self>,
        cx: &mut core::task::Context<'_>,
    ) -> core::task::Poll<Self::Output> {
        // SAFETY: We are implementing a standard adapter and ensuring we don't
        // move the inner futures.
        let this = unsafe { self.get_unchecked_mut() };
        let future = unsafe { core::pin::Pin::new_unchecked(&mut this.future) };
        let delay = unsafe { core::pin::Pin::new_unchecked(&mut this.delay) };

        if let core::task::Poll::Ready(output) = future.poll(cx) {
            return core::task::Poll::Ready(Ok(output));
        }

        if let core::task::Poll::Ready(()) = delay.poll(cx) {
            return core::task::Poll::Ready(Err(()));
        }

        core::task::Poll::Pending
    }
}

/// Await `future` for at most `duration`.
pub fn timeout<F: core::future::Future>(duration: Duration, future: F) -> Timeout<F> {
    Timeout {
        future,
        delay: sleep(duration),
    }
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
