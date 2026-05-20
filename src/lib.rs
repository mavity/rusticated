#![warn(missing_docs)]
//! Fast, standard-library-shaped async platform layer for brush-async

#![no_std]
#![feature(thread_local)]

extern crate alloc;
// The test harness is std-based; bring std in for test builds only.
// On native targets the final binary always links std; expose it here so that
// platform-specific modules (fs, time, …) can use std::fs, std::time, etc.
// without duplicating raw-syscall struct definitions for every architecture.

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
    ($(#[$attr:meta])* $vis:vis static $name:ident: $t:ty = $init:expr; $($rest:tt)*) => {
        $(#[$attr])*
        $vis static $name: $crate::thread::LocalKey<$t> = {
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

#[cfg(not(test))]
#[panic_handler]
#[allow(unused_variables, unused_assignments, clashing_extern_declarations)]
fn panic(info: &core::panic::PanicInfo<'_>) -> ! {
    #[cfg(windows)]
    unsafe {
        let mut handle: core::primitive::usize = 0;
        unsafe extern "system" {
            fn GetStdHandle(nStdHandle: u32) -> core::primitive::usize;
        }
        handle = GetStdHandle(0xFFFFFFF5); // STD_ERROR_HANDLE is -11

        let msg = b"RUSTICATED PANIC! ";
        unsafe extern "system" {
            #[link_name = "WriteFile"]
            fn WriteFileLibRs(
                hFile: core::primitive::usize,
                lpBuffer: *const u8,
                nNumberOfBytesToWrite: u32,
                lpNumberOfBytesWritten: *mut u32,
                lpOverlapped: *mut crate::rt::windows::Overlapped,
            ) -> i32;
            fn ExitProcess(uExitCode: u32) -> !;
        }
        let mut written = 0;
        WriteFileLibRs(
            handle,
            msg.as_ptr(),
            msg.len() as u32,
            &mut written,
            core::ptr::null_mut(),
        );

        let crash = crate::rt::windows::CRASH_REASON.load(core::sync::atomic::Ordering::SeqCst);
        let mut buf = [b'0'; 11];
        let mut n = crash.abs() as u32;
        let mut i = 10;
        if n == 0 {
            buf[i] = b'0';
            i -= 1;
        } else {
            while n > 0 {
                buf[i] = b'0' + (n % 10) as u8;
                n /= 10;
                i -= 1;
            }
        }
        if crash < 0 {
            buf[i] = b'-';
            i -= 1;
        }
        let reason_msg = &buf[i + 1..11];
        WriteFileLibRs(
            handle,
            reason_msg.as_ptr(),
            reason_msg.len() as u32,
            &mut written,
            core::ptr::null_mut(),
        );

        let msg3 = b"\n";
        WriteFileLibRs(
            handle,
            msg3.as_ptr(),
            msg3.len() as u32,
            &mut written,
            core::ptr::null_mut(),
        );

        if let Some(loc) = info.location() {
            let file = loc.file();
            WriteFileLibRs(
                handle,
                file.as_ptr(),
                file.len() as u32,
                &mut written,
                core::ptr::null_mut(),
            );
            let line = loc.line();
            // simple u32 to string
            let mut line_buf = [b'0'; 11];
            let mut line_n = line;
            let mut line_i = 10;
            if line_n == 0 {
                line_buf[line_i] = b'0';
                line_i -= 1;
            } else {
                while line_n > 0 {
                    line_buf[line_i] = b'0' + (line_n % 10) as u8;
                    line_n /= 10;
                    line_i -= 1;
                }
            }
            WriteFileLibRs(
                handle,
                b":".as_ptr(),
                1,
                &mut written,
                core::ptr::null_mut(),
            );
            let lmsg = &line_buf[line_i + 1..11];
            WriteFileLibRs(
                handle,
                lmsg.as_ptr(),
                lmsg.len() as u32,
                &mut written,
                core::ptr::null_mut(),
            );
        }
        ExitProcess(1)
    }

    #[cfg(not(windows))]
    loop {}
}

#[cfg(not(any(test, target_family = "wasm")))]
struct SystemAllocator;

#[cfg(not(any(test, target_family = "wasm")))]
unsafe impl core::alloc::GlobalAlloc for SystemAllocator {
    unsafe fn alloc(&self, layout: core::alloc::Layout) -> *mut u8 {
        #[cfg(unix)]
        {
            unsafe extern "C" {
                fn malloc(size: core::primitive::usize) -> *mut u8;
            }
            unsafe { malloc(layout.size()) }
        }
        #[cfg(windows)]
        {
            unsafe extern "system" {
                fn GetProcessHeap() -> core::primitive::usize;
                fn HeapAlloc(
                    hHeap: core::primitive::usize,
                    dwFlags: u32,
                    dwBytes: core::primitive::usize,
                ) -> *mut u8;
            }
            unsafe { HeapAlloc(GetProcessHeap(), 0, layout.size()) }
        }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, _layout: core::alloc::Layout) {
        #[cfg(unix)]
        {
            unsafe extern "C" {
                fn free(p: *mut u8);
            }
            unsafe { free(ptr) }
        }
        #[cfg(windows)]
        {
            unsafe extern "system" {
                fn GetProcessHeap() -> core::primitive::usize;
                fn HeapFree(hHeap: core::primitive::usize, dwFlags: u32, lpMem: *mut u8) -> i32;
            }
            unsafe {
                HeapFree(GetProcessHeap(), 0, ptr);
            }
        }
    }
}

#[cfg(not(any(test, target_family = "wasm")))]
#[global_allocator]
static ALLOCATOR: SystemAllocator = SystemAllocator;

#[cfg(all(not(test), target_family = "wasm"))]
#[global_allocator]
static ALLOCATOR: dlmalloc::GlobalDlmalloc = dlmalloc::GlobalDlmalloc;

// On Unix without std, the linker does not automatically pull in libc.
// We declare it explicitly so that all extern "C" syscall stubs resolve.
#[cfg(all(unix, not(test), not(target_family = "wasm")))]
#[link(name = "c")]
unsafe extern "C" {}

// libgcc_s provides _Unwind_Resume on Linux, referenced by sysroot alloc.
#[cfg(all(target_os = "linux", not(test)))]
#[link(name = "gcc_s")]
unsafe extern "C" {}

// rust_eh_personality is the DWARF exception-handling personality function.
// The sysroot alloc (compiled without panic=abort) emits a DW.ref reference to
// it.  With panic=abort the function is never invoked; we define a harmless stub
// so the linker is satisfied.
#[cfg(all(target_os = "linux", not(test), not(target_family = "wasm")))]
#[unsafe(no_mangle)]
#[allow(missing_docs)]
pub unsafe extern "C" fn rust_eh_personality() {}

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
