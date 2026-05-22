#![allow(missing_docs, dead_code)]

pub mod select;
pub use select::{Either, Select, select};

#[cfg(not(target_family = "wasm"))]
pub mod bsd;
#[cfg(not(target_family = "wasm"))]
pub mod executor;
#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub mod linux_driver;
#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub mod linux_epoll;
#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub mod linux_state;
#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub mod linux_uring;
#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub mod linux_op;
#[cfg(not(target_family = "wasm"))]
pub mod ready;
#[cfg(not(target_family = "wasm"))]
pub mod timers;
#[cfg(not(target_family = "wasm"))]
pub mod waker;
#[cfg(not(target_family = "wasm"))]
pub mod windows;

#[cfg(not(target_family = "wasm"))]
pub use executor::{JoinHandle, PollStatus, poll_step, spawn, spawn_blocking};

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub use linux_epoll::{WaitReadable, WaitWritable};
#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub fn wait_readable(fd: i32) -> WaitReadable {
    WaitReadable::new(fd)
}
#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub fn wait_writable(fd: i32) -> WaitWritable {
    WaitWritable::new(fd)
}

#[cfg(all(not(target_family = "wasm"), windows))]
pub use windows::{WaitReadable, WaitWritable};
#[cfg(all(not(target_family = "wasm"), windows))]
pub fn wait_readable(h: u64) -> WaitReadable {
    WaitReadable::new(h)
}
#[cfg(all(not(target_family = "wasm"), windows))]
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
pub fn wait_writable(fd: i32) -> WaitWritable {
    WaitWritable::new(fd)
}

#[cfg(target_family = "wasm")]
pub mod wasm;

// Export the WASM Rust consumer API explicitly, deliberately excluding the
// host-ABI `run()` symbol (which stays as `#[no_mangle] pub extern "C"` in
// wasm.rs for the host linker, but is not a Rust-level public API).
#[cfg(target_family = "wasm")]
pub use wasm::{OverlappedBufferFuture, OverlappedFuture, poll_step, submit_main};

/// Lang item for program start.
#[cfg(not(any(test, feature = "std")))]
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
