#![warn(missing_docs)]
//! Fast, standard-library-shaped async platform layer for brush-async

#![no_std]
#![feature(thread_local)]

extern crate alloc;
// The test harness is std-based; bring std in for test builds only.
#[cfg(test)]
extern crate std;

/// Declares one or more thread-local values, initialised lazily on first access.
///
/// Works identically to `std::thread_local!` but is implemented using the nightly
/// `#[thread_local]` attribute. Values are not dropped on thread exit.
///
/// # Example
///
/// ```rust,ignore
/// thread_local! {
///     static COUNTER: crate::cell::Cell<u32> = crate::cell::Cell::new(0);
/// }
/// COUNTER.with(|c| c.set(c.get() + 1));
/// ```
#[macro_export]
macro_rules! thread_local {
    () => {};
    ($(#[$attr:meta])* static $name:ident: $t:ty = $init:expr; $($rest:tt)*) => {
        $(#[$attr])*
        static $name: $crate::thread::LocalKey<$t> = {
            #[thread_local]
            static STORAGE: $crate::cell::UnsafeCell<::core::option::Option<$t>> =
                $crate::cell::UnsafeCell::new(::core::option::Option::None);

            fn __get() -> *const $t {
                // SAFETY: `#[thread_local]` guarantees exclusive single-thread
                // access; this function must not be called re-entrantly.
                unsafe {
                    let ptr = STORAGE.get();
                    if (*ptr).is_none() {
                        *ptr = ::core::option::Option::Some($init);
                    }
                    // SAFETY: initialised just above.
                    (*ptr).as_ref().unwrap_unchecked() as *const _
                }
            }

            $crate::thread::LocalKey::new(__get)
        };
        $crate::thread_local!($($rest)*);
    };
}

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
/// Thread-local storage key type.
pub mod thread;
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
