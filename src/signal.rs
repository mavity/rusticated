//! OS signal abstractions

#[cfg(not(target_family = "wasm"))]
pub use compio::signal::ctrl_c;

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedFuture;

#[cfg(target_family = "wasm")]
/// Wait for a signal on WASM
pub async fn signal_wait(signum: u32) -> std::io::Result<()> {
    let (err, _, _) = OverlappedFuture::new(move |ov| {
        unsafe { imports::signal_wait(ov, signum) };
    }).await;

    if err != 0 {
        return Err(std::io::Error::from_raw_os_error(err as i32));
    }

    Ok(())
}

#[cfg(target_family = "wasm")]
/// Dummy ctrl_c for WASM (SIGINT is usually 2)
pub async fn ctrl_c() -> std::io::Result<()> {
    signal_wait(2).await
}
