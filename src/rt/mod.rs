//! Runtime abstractions — proactor-backed executor and WASM completion registry.
//!
//! On native targets, [`block_on`] runs a future to completion using the
//! thread-local [`compio_driver::Proactor`].
//!
//! On WASM the host drives the proactor by calling `run()` (exported as a C
//! symbol).

#[cfg(not(target_family = "wasm"))]
pub mod native;

#[cfg(not(target_family = "wasm"))]
pub use native::block_on;

#[cfg(target_family = "wasm")]
pub mod wasm;

#[cfg(target_family = "wasm")]
pub use wasm::{OverlappedFuture, register_overlapped};
