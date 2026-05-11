//! Runtime abstractions — WASM completion registry and host entry point.
//!
//! On native targets, callers use `compio` directly.
//! On WASM the host drives the proactor by calling `run()` (exported as a C symbol).

#[cfg(target_family = "wasm")]
pub mod wasm;

#[cfg(target_family = "wasm")]
pub use wasm::{OverlappedFuture, register_overlapped};
