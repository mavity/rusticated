//! Runtime abstractions.
//!
//! `fast-std` follows a **host-driven** model on every platform: the library
//! never owns the thread or blocks. The host (the caller of `fast-std`) hands
//! work to the runtime via [`run`] and pumps it via [`poll_step`].
//!
//! On native targets, [`native`] implements `poll_step` using a non-blocking
//! reactor poll (epoll/kqueue/IOCP). On WASM the [`wasm`] module exposes the
//! same surface via a host import boundary.
//!
//! Blocking primitives (`block_on`, `spawn_blocking`) are intentionally absent
//! from the default build. Thread spawning is available only with the
//! `threads` feature, and never on WASM.

#[cfg(not(target_family = "wasm"))]
pub mod native;

#[cfg(not(target_family = "wasm"))]
pub use native::{poll_step, run};

#[cfg(target_family = "wasm")]
pub mod wasm;

#[cfg(target_family = "wasm")]
pub use wasm::{OverlappedBufferFuture, OverlappedFuture, poll_step, run, submit_main};
