//! Stack backtraces.
//!
//! Provides [`Backtrace`] and [`BacktraceStatus`], backed by the `backtrace`
//! crate running in `backtrace_in_libstd` mode. In that mode the crate does
//! NOT expose its own `Backtrace` capture type; instead it provides the
//! low-level primitives (`trace_unsynchronized`, `resolve`, `SymbolName`, …)
//! that this module uses to build a real, non-stub implementation.

#![allow(clippy::module_name_repetitions)]

extern crate backtrace as backtrace_sys;

use alloc::vec::Vec;
use core::ffi::c_void;
use core::fmt;

/// The status of a [`Backtrace`].
#[non_exhaustive]
#[derive(Debug, PartialEq, Eq)]
pub enum BacktraceStatus {
    /// Backtraces are not supported on the current platform.
    Unsupported,
    /// Backtrace capture has been disabled via the `RUST_BACKTRACE` or
    /// `RUST_LIB_BACKTRACE` environment variable.
    Disabled,
    /// A backtrace has been captured and contains meaningful frame data.
    Captured,
}

/// Captured frame instruction pointers stored as plain `usize` values so the
/// entire [`Backtrace`] is unconditionally `Send + Sync`.
struct RawFrames {
    ips: Vec<usize>,
}

enum Inner {
    Disabled,
    Captured(RawFrames),
}

/// A captured OS thread stack backtrace.
///
/// Capture is controlled by the `RUST_BACKTRACE` / `RUST_LIB_BACKTRACE`
/// environment variables, mirroring the behaviour of the standard library.
pub struct Backtrace {
    inner: Inner,
}

impl Backtrace {
    fn capture_enabled() -> bool {
        match crate::env::var("RUST_LIB_BACKTRACE").or_else(|_| crate::env::var("RUST_BACKTRACE")) {
            Ok(s) => s == "1" || s == "full",
            Err(_) => false,
        }
    }

    /// Capture a stack backtrace of the current thread.
    ///
    /// Returns a [`Backtrace`] with status [`BacktraceStatus::Disabled`] if
    /// `RUST_BACKTRACE` or `RUST_LIB_BACKTRACE` is not set to `1` or `full`.
    pub fn capture() -> Backtrace {
        if !Self::capture_enabled() {
            return Backtrace {
                inner: Inner::Disabled,
            };
        }
        Self::force_capture()
    }

    /// Capture a full backtrace, regardless of environment variable
    /// configuration.
    pub fn force_capture() -> Backtrace {
        let mut ips: Vec<usize> = Vec::new();
        // SAFETY: The closure only appends `frame.ip()` (a plain integer cast)
        // to a Vec and never calls back into the backtrace machinery, so there
        // are no reentrancy hazards.
        unsafe {
            backtrace_sys::trace_unsynchronized(|frame| {
                ips.push(frame.ip() as usize);
                true
            });
        }
        Backtrace {
            inner: Inner::Captured(RawFrames { ips }),
        }
    }

    /// Return a disabled, empty [`Backtrace`].
    pub const fn disabled() -> Backtrace {
        Backtrace {
            inner: Inner::Disabled,
        }
    }

    /// Returns the status of this backtrace.
    pub fn status(&self) -> BacktraceStatus {
        match &self.inner {
            Inner::Disabled => BacktraceStatus::Disabled,
            Inner::Captured(_) => BacktraceStatus::Captured,
        }
    }
}

impl fmt::Debug for Backtrace {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Display::fmt(self, f)
    }
}

impl fmt::Display for Backtrace {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match &self.inner {
            Inner::Disabled => f.write_str("disabled backtrace"),
            Inner::Captured(raw) => {
                for (idx, &ip_usize) in raw.ips.iter().enumerate() {
                    let ip = ip_usize as *mut c_void;
                    let mut found = false;
                    // SAFETY: `ip` is a valid instruction pointer obtained from
                    // `trace_unsynchronized`. The callback does not re-enter the
                    // backtrace machinery, so reentrancy is not a concern.
                    unsafe {
                        backtrace_sys::resolve_unsynchronized(ip, |symbol| {
                            if !found {
                                found = true;
                                if let Some(name) = symbol.name() {
                                    let _ = write!(f, "\n{idx:>4}: {name}");
                                } else {
                                    let _ = write!(f, "\n{idx:>4}: <unknown>");
                                }
                                if let Some(lineno) = symbol.lineno() {
                                    if let Some(path) = symbol.filename_raw() {
                                        match path {
                                            backtrace_sys::BytesOrWideString::Bytes(b) => {
                                                if let Ok(s) = core::str::from_utf8(b) {
                                                    let _ =
                                                        write!(f, "\n             at {s}:{lineno}");
                                                }
                                            }
                                            backtrace_sys::BytesOrWideString::Wide(_) => {}
                                        }
                                    }
                                }
                            }
                        });
                    }
                    if !found {
                        write!(f, "\n{idx:>4}: <unknown> ({ip:p})")?;
                    }
                }
                Ok(())
            }
        }
    }
}
