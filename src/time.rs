//! Time handling and async sleeps

#[cfg(not(target_family = "wasm"))]
/// Sleep asynchronously for `duration`.
///
/// Runs [`std::thread::sleep`] on a background thread so that other futures
/// can make progress while waiting.
pub async fn sleep(duration: std::time::Duration) {
    // Ignore errors — if the driver is unavailable, sleep still occurs on the
    // background thread; the future just never wakes up cleanly.
    if let Ok(handle) = crate::rt::native::spawn_blocking(move || std::thread::sleep(duration)) {
        let _ = handle.await;
    }
}

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedFuture;

#[cfg(target_family = "wasm")]
/// Async sleep for WASM
pub async fn sleep(duration: std::time::Duration) {
    let delay_ms = duration.as_millis() as u32;
    let _ = OverlappedFuture::new(move |ov| {
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
        Self(unsafe { imports::get_time() as u64 })
    }

    /// Returns the duration elapsed from `earlier` to `self`.
    pub fn duration_since(&self, earlier: Instant) -> std::time::Duration {
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
