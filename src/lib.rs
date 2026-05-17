#![warn(missing_docs)]
//! Fast, standard-library-shaped async platform layer for brush-async

#![no_std]

extern crate alloc;

/// Shared ABI definitions
pub mod abi;
/// Collections
pub mod collections;
/// Environment evaluation
pub mod env;
/// Errors
pub mod error;
/// FFI and OS-string helpers
pub mod ffi;
/// File system abstractions
pub mod fs;
/// I/O operations
pub mod io;
/// Path extensions
pub mod path;
/// Process execution and management
pub mod process;
/// Runtime engine abstraction
pub mod rt;
/// OS signal abstractions
pub mod signal;
/// Synchronisation primitives
pub mod sync;
/// Time and async sleep utilities
pub mod time;
/// Terminal interface types
pub mod tty;

// ── std-shaped re-exports from core / alloc ─────────────────────────────────
/// Borrow utilities (`ToOwned`, `Cow`).
pub mod borrow {
    pub use alloc::borrow::*;
}
/// Heap-allocated pointer type.
pub mod boxed {
    pub use alloc::boxed::*;
}
/// Cell types (`RefCell`, `UnsafeCell`, `OnceCell`).
pub mod cell {
    pub use core::cell::*;
}
/// Async/future abstractions (`Future`, `poll_fn`).
pub mod future {
    pub use core::future::*;
}
/// CPU hints (`spin_loop`).
pub mod hint {
    pub use core::hint::*;
}
/// Operator traits (`Deref`, `DerefMut`, `Index`, `Add`, …).
pub mod ops {
    pub use core::ops::*;
}
/// Pinned-memory utilities.
pub mod pin {
    pub use core::pin::*;
}
/// Raw pointer utilities.
pub mod ptr {
    pub use core::ptr::*;
}
/// Reference-counted single-thread pointer.
pub mod rc {
    pub use alloc::rc::*;
}
/// Growable UTF-8 string type.
pub mod string {
    pub use alloc::string::*;
}
/// Async task types (`Context`, `Poll`, `Waker`).
pub mod task {
    pub use core::task::*;
}
/// Growable array type.
pub mod vec {
    pub use alloc::vec::*;
}

pub use error::{Error, Result};

#[cfg(not(target_family = "wasm"))]
#[macro_use]
extern crate std;
