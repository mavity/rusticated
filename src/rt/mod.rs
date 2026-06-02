/// Combinators for selecting between futures.
pub mod select;
pub use select::{Either, Select, select};

#[cfg(not(target_family = "wasm"))]
/// Blocking support for offloading work to threads.
pub mod blocking;

#[cfg(not(target_family = "wasm"))]
/// BSD-compatible event polling implementation.
pub mod bsd;

#[cfg(not(target_family = "wasm"))]
/// Executor and task scheduling primitives.
pub mod executor;

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
/// Linux-specific driver integration.
pub mod linux_driver;

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
/// Epoll-based readiness polling for Linux.
pub mod linux_epoll;

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
/// Linux asynchronous operation wrappers.
pub mod linux_op;

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
/// Linux-specific runtime state handling.
pub mod linux_state;

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
/// io_uring integration for Linux.
pub mod linux_uring;

#[cfg(not(target_family = "wasm"))]
/// Ready queue management for futures.
pub mod ready;

#[cfg(not(target_family = "wasm"))]
/// Timer and deadline support.
pub mod timers;

#[cfg(not(target_family = "wasm"))]
/// Task wake-up primitives.
pub mod waker;

#[cfg(not(target_family = "wasm"))]
/// Windows-specific runtime support.
pub mod windows;

#[cfg(not(target_family = "wasm"))]
pub use executor::{JoinHandle, PollStatus, poll_step, spawn};

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub use linux_epoll::{WaitReadable, WaitWritable};

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
/// Returns a future that waits for the file descriptor to become readable.
pub fn wait_readable(fd: i32) -> WaitReadable {
    WaitReadable::new(fd)
}

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
/// Returns a future that waits for the file descriptor to become writable.
pub fn wait_writable(fd: i32) -> WaitWritable {
    WaitWritable::new(fd)
}

#[cfg(all(not(target_family = "wasm"), windows))]
pub use windows::{WaitReadable, WaitWritable};

#[cfg(all(not(target_family = "wasm"), windows))]
/// Returns a future that waits for the Windows handle to become readable.
pub fn wait_readable(h: u64) -> WaitReadable {
    WaitReadable::new(h)
}

#[cfg(all(not(target_family = "wasm"), windows))]
/// Returns a future that waits for the Windows handle to become writable.
pub fn wait_writable(h: u64) -> WaitWritable {
    WaitWritable::new(h)
}

#[cfg(all(
    not(target_family = "wasm"),
    any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd"
    )
))]
pub use bsd::{WaitReadable, WaitWritable};
#[cfg(all(
    not(target_family = "wasm"),
    any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd"
    )
))]
/// Returns a future that waits for the BSD-style file descriptor to become readable.
pub fn wait_readable(fd: i32) -> WaitReadable {
    WaitReadable::new(fd)
}

#[cfg(all(
    not(target_family = "wasm"),
    any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd"
    )
))]
/// Returns a future that waits for the BSD-style file descriptor to become writable.
pub fn wait_writable(fd: i32) -> WaitWritable {
    WaitWritable::new(fd)
}

#[cfg(target_family = "wasm")]
/// WASM runtime helpers and host ABI integration.
pub mod wasm;

// Export the WASM Rust consumer API explicitly, deliberately excluding the
// host-ABI `run()` symbol (which stays as `#[no_mangle] pub extern "C"` in
// wasm.rs for the host linker, but is not a Rust-level public API).
#[cfg(target_family = "wasm")]
pub use wasm::{OverlappedBufferFuture, OverlappedFuture, poll_step, submit_main};

/// Lang item for program start.
#[cfg(not(test))]
#[lang = "start"]
fn lang_start<T: crate::process::Termination + 'static>(
    main: fn() -> T,
    _argc: isize,
    _argv: *const *const u8,
    _sigpipe: u8,
) -> isize {
    let res = main().report() as isize;

    #[cfg(not(target_family = "wasm"))]
    {
        use crate::rt::{PollStatus, poll_step};
        loop {
            match poll_step() {
                Ok(PollStatus::Done) => break,
                Ok(PollStatus::Ready) => continue,
                Ok(PollStatus::Idle { next_deadline }) => {
                    if let Err(_e) = crate::rt::executor::poll_step_idle(next_deadline) {
                        #[cfg(windows)]
                        crate::rt::windows::CRASH_REASON.store(
                            _e.raw_os_error().unwrap_or(999999) as i32,
                            core::sync::atomic::Ordering::SeqCst,
                        );
                        panic!("idle_err");
                    }
                }
                Err(_e) => {
                    #[cfg(windows)]
                    crate::rt::windows::CRASH_REASON.store(
                        _e.raw_os_error().unwrap_or(999999) as i32,
                        core::sync::atomic::Ordering::SeqCst,
                    );
                    panic!("err");
                }
            }
        }
    }

    res
}

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
#[unsafe(no_mangle)]
/// Global environment pointer used by the Unix environment variable lookup.
pub static mut environ: *const *const u8 = core::ptr::null();

/// Entrypoint for Linux targets.
#[cfg(all(not(test), not(target_family = "wasm"), target_os = "linux"))]
#[unsafe(no_mangle)]
#[unsafe(naked)]
pub unsafe extern "C" fn _start() -> ! {
    #[cfg(target_arch = "x86_64")]
    core::arch::naked_asm!(
        "xor rbp, rbp",       // Clear RBP per ABI
        "mov rdi, [rsp]",     // argc
        "lea rsi, [rsp + 8]", // argv
        "call __rust_start"
    );
    #[cfg(target_arch = "aarch64")]
    core::arch::naked_asm!(
        "mov x29, #0",    // Clear X29 (frame pointer)
        "mov x30, #0",    // Clear X30 (link register)
        "ldr x0, [sp]",   // argc
        "add x1, sp, #8", // argv
        "bl __rust_start"
    );
}

#[cfg(all(not(test), not(target_family = "wasm"), target_os = "linux"))]
#[unsafe(no_mangle)]
unsafe extern "C" fn __rust_start(argc: isize, argv: *const *const u8) -> ! {
    // envp is argv + argc + 1
    unsafe {
        environ = argv.add(argc as usize + 1);
    }

    unsafe extern "Rust" {
        fn main() -> i32;
    }

    fn safe_main() -> i32 {
        unsafe { main() }
    }

    let res = lang_start(safe_main, argc, argv, 0);

    crate::syscall!(crate::os::linux::syscall::nr::EXIT, res as usize);
    unsafe {
        core::hint::unreachable_unchecked();
    }
}

#[cfg(test)]
#[cfg(not(target_family = "wasm"))]
pub(crate) fn run<F: core::future::Future<Output = ()> + 'static>(future: F) {
    use crate::rt::{PollStatus, poll_step};
    crate::rt::executor::run(future);
    loop {
        match poll_step() {
            Ok(PollStatus::Done) => break,
            Ok(PollStatus::Ready) => continue,
            Ok(PollStatus::Idle { next_deadline }) => {
                if let Err(_e) = crate::rt::executor::poll_step_idle(next_deadline) {
                    #[cfg(windows)]
                    crate::rt::windows::CRASH_REASON.store(
                        _e.raw_os_error().unwrap_or(999999) as i32,
                        core::sync::atomic::Ordering::SeqCst,
                    );
                    panic!("idle_err");
                }
            }
            Err(_e) => {
                #[cfg(windows)]
                crate::rt::windows::CRASH_REASON.store(
                    _e.raw_os_error().unwrap_or(999999) as i32,
                    core::sync::atomic::Ordering::SeqCst,
                );
                panic!("err");
            }
        }
    }
}

/// Declare the program entry-point in a platform-agnostic way.
///
/// On native targets expands to a `fn main()` that drives the rusticated
/// async runtime. On WASM expands to an empty `fn main()` plus the
/// `guest_init` export expected by the rusticated WASM host.
///
/// # Example
/// Defines the entry point for the application.
///
/// ```rust,ignore
/// std::main!(my_async_fn());
/// ```
#[macro_export]
macro_rules! main {
    ($future:expr) => {
        #[unsafe(no_mangle)]
        pub extern "Rust" fn main() {
            $crate::spawn!($future);
        }
    };
}

/// Spawn a future on the rusticated runtime.
///
/// On native targets this runs the future immediately on the runtime executor.
/// On WASM it exports `guest_init` for the host to invoke.
#[macro_export]
macro_rules! spawn {
    ($future:expr) => {
        #[cfg(not(target_family = "wasm"))]
        {
            $crate::rt::executor::run($future);
        }

        #[cfg(target_family = "wasm")]
        #[unsafe(no_mangle)]
        pub unsafe extern "Rust" fn guest_init() {
            $crate::rt::submit_main($future);
        }
    };
}
