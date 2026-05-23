#![warn(missing_docs)]
//! Fast, standard-library-shaped async platform layer for brush-async

#![no_std]
#![allow(unused_features)]
#![feature(thread_local)]
#![allow(stable_features)]
#![feature(async_fn_in_trait)]
#![feature(lang_items)]
#![allow(internal_features)]
#![allow(missing_docs)]
#![feature(alloc_error_handler)]

// No prelude_import here, as this IS the std library providing the prelude.

pub extern crate alloc;
// The test harness is std-based; bring std in for test builds only.
// On native targets the final binary always links std; expose it here so that
// platform-specific modules (fs, time, â€¦) can use std::fs, std::time, etc.
// without duplicating raw-syscall struct definitions for every architecture.

#[cfg(windows)]
mod msvc_stubs;

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
/// Prints to the standard output, with a newline.
#[macro_export]
macro_rules! println {
    () => {
        $crate::print!("\n")
    };
    ($($arg:tt)*) => {
        $crate::print!("{}\n", $crate::format!($($arg)*))
    };
}

/// Prints to the standard output.
#[macro_export]
macro_rules! print {
    ($($arg:tt)*) => {
        {
            use $crate::io::Write;
            let mut out = $crate::io::stdout();
            let _ = out.write_all($crate::format!($($arg)*).as_bytes());
        }
    };
}

/// Prints to the standard error, with a newline.
#[macro_export]
macro_rules! eprintln {
    () => {
        $crate::eprint!("\n")
    };
    ($($arg:tt)*) => {
        $crate::eprint!("{}\n", $crate::format!($($arg)*))
    };
}

/// Prints to the standard error.
#[macro_export]
macro_rules! eprint {
    ($($arg:tt)*) => {
        {
            use $crate::io::Write;
            let mut out = $crate::io::stderr();
            let _ = out.write_all($crate::format!($($arg)*).as_bytes());
        }
    };
}

/// Creates a String using interpolation of runtime expressions.
#[macro_export]
macro_rules! format {
    ($($arg:tt)*) => {
        $crate::alloc::format!($($arg)*)
    };
}
/// Prelude for the standard library.
pub mod prelude {
    /// Rust edition-independent prelude v1.
    #[allow(unused_imports)]
    pub mod v1 {
        // alloc types that are normally injected by the std prelude
        pub use alloc::borrow::ToOwned;
        pub use alloc::boxed::Box;
        pub use alloc::string::{String, ToString};
        pub use alloc::vec::Vec;
        // Macros
        pub use crate::{eprint, eprintln, format, print, println, spawn, thread_local};
        pub use alloc::vec;
        pub use core::{
            assert, assert_eq, assert_ne, debug_assert, debug_assert_eq, debug_assert_ne, matches,
            panic, todo, unimplemented, unreachable, write, writeln,
        };
        // Core traits already in scope via core::prelude, but re-export
        // them here for completeness so a wildcard import is sufficient.
        pub use core::clone::Clone;
        pub use core::cmp::{Eq, Ord, PartialEq, PartialOrd};
        pub use core::convert::{AsMut, AsRef, From, Into, TryFrom, TryInto};
        pub use core::default::Default;
        pub use core::fmt::{Debug, Display};
        pub use core::iter::{
            DoubleEndedIterator, ExactSizeIterator, Extend, FromIterator, IntoIterator, Iterator,
        };
        pub use core::marker::{Copy, Send, Sized, Sync};
        pub use core::mem::drop;
        pub use core::ops::{Drop, Fn, FnMut, FnOnce};
        pub use core::option::Option::{self, None, Some};
        pub use core::result::Result::{self, Err, Ok};
    }

    /// Prelude for Edition 2024.
    pub mod rust_2024 {
        pub use super::v1::*;
        pub use core::prelude::rust_2024::*;
    }
}

// NO prelude_import here! We want libstd to use the default core prelude.

/// Shared ABI definitions
pub mod abi;

#[cfg(all(feature = "panic-handler", not(test)))]
#[panic_handler]
#[allow(unused_variables, unused_assignments, clashing_extern_declarations)]
fn panic(info: &core::panic::PanicInfo<'_>) -> ! {
    #[cfg(windows)]
    unsafe {
        let mut handle: core::primitive::usize = 0;
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetStdHandle(nStdHandle: u32) -> core::primitive::usize;
        }
        handle = GetStdHandle(0xFFFFFFF5); // STD_ERROR_HANDLE is -11

        let msg = b"RUSTICATED PANIC! ";
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            #[link_name = "WriteFile"]
            fn WriteFileLibRs(
                hFile: core::primitive::usize,
                lpBuffer: *const u8,
                nNumberOfBytesToWrite: u32,
                lpNumberOfBytesWritten: *mut u32,
                lpOverlapped: *mut core::ffi::c_void,
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

        let text = format!("{}", info);
        WriteFileLibRs(
            handle,
            text.as_ptr(),
            text.len() as u32,
            &mut written,
            core::ptr::null_mut(),
        );
        WriteFileLibRs(
            handle,
            b"\n".as_ptr(),
            1,
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

#[cfg(all(
    not(any(test, target_family = "wasm", feature = "std")),
    any(target_os = "none", windows, target_os = "linux")
))]
struct SystemAllocator;

#[cfg(all(
    not(any(test, target_family = "wasm", feature = "std")),
    any(target_os = "none", windows, target_os = "linux")
))]
#[allow(missing_docs)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn __rust_alloc(size: usize, align: usize) -> *mut u8 {
    use core::alloc::GlobalAlloc;
    unsafe { ALLOCATOR.alloc(core::alloc::Layout::from_size_align_unchecked(size, align)) }
}

#[cfg(all(
    not(any(test, target_family = "wasm", feature = "std")),
    any(target_os = "none", windows, target_os = "linux")
))]
#[allow(missing_docs)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn __rust_dealloc(ptr: *mut u8, size: usize, align: usize) {
    use core::alloc::GlobalAlloc;
    unsafe {
        ALLOCATOR.dealloc(
            ptr,
            core::alloc::Layout::from_size_align_unchecked(size, align),
        )
    }
}

#[cfg(all(
    not(any(test, target_family = "wasm", feature = "std")),
    any(target_os = "none", windows, target_os = "linux")
))]
#[allow(missing_docs)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn __rust_realloc(
    ptr: *mut u8,
    old_size: usize,
    align: usize,
    new_size: usize,
) -> *mut u8 {
    use core::alloc::GlobalAlloc;
    unsafe {
        ALLOCATOR.realloc(
            ptr,
            core::alloc::Layout::from_size_align_unchecked(old_size, align),
            new_size,
        )
    }
}

#[cfg(all(
    not(any(test, target_family = "wasm", feature = "std")),
    any(target_os = "none", windows, target_os = "linux")
))]
#[allow(missing_docs)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn __rust_alloc_zeroed(size: usize, align: usize) -> *mut u8 {
    use core::alloc::GlobalAlloc;
    unsafe { ALLOCATOR.alloc_zeroed(core::alloc::Layout::from_size_align_unchecked(size, align)) }
}

#[cfg(all(
    not(any(test, target_family = "wasm", feature = "std")),
    any(target_os = "none", windows, target_os = "linux")
))]
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
            #[link(name = "kernel32", kind = "raw-dylib")]
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
            #[link(name = "kernel32", kind = "raw-dylib")]
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

#[cfg(all(
    not(any(test, target_family = "wasm", feature = "std")),
    any(target_os = "none", windows, target_os = "linux")
))]
#[global_allocator]
static ALLOCATOR: SystemAllocator = SystemAllocator;

#[cfg(all(not(test), target_family = "wasm"))]
#[global_allocator]
static ALLOCATOR: dlmalloc::GlobalDlmalloc = dlmalloc::GlobalDlmalloc;

#[cfg(not(any(test, feature = "std")))]
#[alloc_error_handler]
fn oom(_: core::alloc::Layout) -> ! {
    panic!("out of memory");
}

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
#[cfg(all(not(test), not(target_family = "wasm")))]
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
/// OS-specific extensions
pub mod os;
/// Path extensions
pub mod path;
/// Process execution and management
pub mod process;
/// Runtime engine abstraction
pub mod rt;
/// Executor
#[cfg(not(target_family = "wasm"))]
pub use rt::executor;
/// OS signal abstractions
pub mod signal;
/// Synchronisation primitives
pub mod sync;
/// Thread-local storage key type.
pub mod thread;
/// Time and async sleep utilities
pub mod time;
/// Core traits
pub mod traits;
/// Terminal interface types
pub mod tty;

// â”€â”€ std-shaped re-exports from core / alloc â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
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
/// Character utilities.
pub mod char {
    pub use core::char::*;
}
/// Option types (`Option`, `Some`, `None`).
pub mod option {
    pub use core::option::*;
}
/// Type comparison and ordering.
pub mod cmp {
    pub use core::cmp::*;
}
/// Conversion traits (`From`, `Into`, `TryFrom`, `TryInto`).
pub mod convert {
    pub use core::convert::*;
}
/// Formatting utilities (`Display`, `Debug`, `Formatter`, `Write`, â€¦).
pub mod fmt {
    pub use core::fmt::*;
}
/// Result types (`Result`, `Ok`, `Err`).
pub mod result {
    pub use core::result::*;
}
/// Async/future abstractions (`Future`, `poll_fn`).
pub mod future {
    pub use core::future::*;
}
/// Hashing traits.
pub mod hash {
    pub use core::hash::*;
}
/// CPU hints (`spin_loop`).
pub mod hint {
    pub use core::hint::*;
}
/// Iterator adaptors and traits.
pub mod iter {
    pub use core::iter::*;
}
/// Marker traits (`Send`, `Sync`, `Copy`, `PhantomData`, â€¦).
pub mod marker {
    pub use core::marker::*;
}
/// Memory utilities (`size_of`, `align_of`, `take`, `swap`, â€¦).
pub mod mem {
    pub use core::mem::*;
}
/// Operator traits (`Deref`, `DerefMut`, `Index`, `Add`, â€¦).
pub mod ops {
    pub use core::ops::*;
}
/// Numeric traits and types.
pub mod num {
    pub use core::num::*;
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
/// Slice utilities and iterators.
pub mod slice {
    pub use core::slice::*;
}
/// String utilities and iterators.
pub mod str {
    pub use core::str::*;
}
/// Growable UTF-8 string type.
pub mod string {
    pub use alloc::string::*;
}

/// Async task types (`Context`, `Poll`, `Waker`).
pub mod task {
    #[cfg(not(target_family = "wasm"))]
    pub use crate::rt::executor::{JoinError, JoinHandle};
    pub use core::task::*;
}
/// Growable array type.
#[macro_use]
pub mod vec {
    pub use alloc::vec;
    pub use alloc::vec::*;
}

/// Re-exports from the runtime engine.
#[cfg(not(target_family = "wasm"))]
pub use crate::rt::executor::{JoinHandle, spawn, spawn_blocking};

pub use crate::error::{Result, SystemError as Error};
