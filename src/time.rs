//! Time handling and async sleeps
#[cfg(not(target_family = "wasm"))]
pub use compio::time::sleep;

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
pub struct Instant(u64);

#[cfg(target_family = "wasm")]
impl Instant {
    pub fn now() -> Self {
        Self(unsafe { imports::get_time() as u64 })
    }

    pub fn duration_since(&self, earlier: Instant) -> std::time::Duration {
        std::time::Duration::from_millis(self.0.saturating_sub(earlier.0))
    }

    pub fn elapsed(&self) -> std::time::Duration {
        Self::now().duration_since(*self)
    }
}

#[cfg(target_family = "wasm")]
pub type SystemTime = std::time::SystemTime;
#[cfg(target_family = "wasm")]
pub type SystemTimeError = std::time::SystemTimeError;
