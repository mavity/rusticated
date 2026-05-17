#![allow(missing_docs, dead_code)]

#[cfg(not(target_family = "wasm"))]
pub mod bsd;
#[cfg(not(target_family = "wasm"))]
pub mod executor;
#[cfg(not(target_family = "wasm"))]
pub mod linux_epoll;
#[cfg(not(target_family = "wasm"))]
pub mod ready;
#[cfg(not(target_family = "wasm"))]
pub mod timers;
#[cfg(not(target_family = "wasm"))]
pub mod waker;
#[cfg(not(target_family = "wasm"))]
pub mod windows;

#[cfg(not(target_family = "wasm"))]
pub use executor::{PollStatus, poll_step, run, spawn};

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

#[cfg(target_family = "wasm")]
pub use wasm::*;
