//! OS string types and null-terminated C strings for `#![no_std]` use.

use crate::vec::Vec;
pub use core::ffi::c_char;
pub use core::ffi::c_double;
pub use core::ffi::c_float;
pub use core::ffi::c_int;
pub use core::ffi::c_long;
pub use core::ffi::c_longlong;
pub use core::ffi::c_schar;
pub use core::ffi::c_short;
pub use core::ffi::c_uchar;
pub use core::ffi::c_uint;
pub use core::ffi::c_ulong;
pub use core::ffi::c_ulonglong;
pub use core::ffi::c_ushort;
pub use core::ffi::c_void;

/// Re-export of `core::any` so `std::any` works in the rusticated compatibility layer.
pub mod any {
    pub use core::any::*;
}


/// Error returned when a string contains an interior null byte.
#[derive(Debug)]
pub struct NulError;

impl core::fmt::Display for NulError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str("interior null byte in string")
    }
}

impl core::error::Error for NulError {}

/// A heap-allocated, null-terminated byte string suitable for C APIs.
pub struct CString(Vec<u8>);

impl CString {
    /// Construct a `CString` from a UTF-8 string.
    ///
    /// Returns [`NulError`] if `s` contains any null bytes.
    pub fn new(s: &str) -> core::result::Result<Self, NulError> {
        if s.bytes().any(|b| b == 0) {
            return Err(NulError);
        }
        let mut v = Vec::with_capacity(s.len() + 1);
        v.extend_from_slice(s.as_bytes());
        v.push(0);
        Ok(Self(v))
    }

    /// Returns a pointer to the start of the null-terminated byte string.
    #[inline]
    pub fn as_ptr(&self) -> *const u8 {
        self.0.as_ptr()
    }
}


/// Iterator that yields UTF-16 code units for a `str`.
pub struct EncodeWide<'a> {
    inner: core::str::EncodeUtf16<'a>,
}

impl Iterator for EncodeWide<'_> {
    type Item = u16;

    #[inline]
    fn next(&mut self) -> Option<u16> {
        self.inner.next()
    }
}

/// UTF-16 encoder for UTF-8 path strings.
pub trait EncodeWideExt {
    /// Returns an iterator over the UTF-16 code units of this string.
    fn encode_wide(&self) -> EncodeWide<'_>;
}

impl EncodeWideExt for str {
    fn encode_wide(&self) -> EncodeWide<'_> {
        EncodeWide {
            inner: self.encode_utf16(),
        }
    }
}

impl EncodeWideExt for alloc::string::String {
    fn encode_wide(&self) -> EncodeWide<'_> {
        self.as_str().encode_wide()
    }
}
