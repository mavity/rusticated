//! OS string types and null-terminated C strings for `#![no_std]` use.

use crate::string::String;
use crate::vec::Vec;
use alloc::borrow::Cow;
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

/// Owned OS string type.
#[repr(transparent)]
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct OsString {
    inner: Vec<u8>,
}

/// Borrowed OS string slice.
#[repr(transparent)]
pub struct OsStr {
    inner: [u8],
}

impl OsString {
    /// Creates an empty OS string.
    pub fn new() -> Self {
        Self { inner: Vec::new() }
    }

    /// Creates an OS string from raw bytes.
    pub fn from_vec(vec: Vec<u8>) -> Self {
        Self { inner: vec }
    }

    /// Converts this owned OS string into its raw bytes.
    pub fn into_vec(self) -> Vec<u8> {
        self.inner
    }

    /// Attempts to convert this OS string into a UTF-8 string.
    pub fn into_string(self) -> Result<String, Self> {
        match String::from_utf8(self.inner) {
            Ok(s) => Ok(s),
            Err(err) => Err(Self {
                inner: err.into_bytes(),
            }),
        }
    }

    /// Borrows this value as an [`OsStr`].
    pub fn as_os_str(&self) -> &OsStr {
        unsafe { &*(self.inner.as_slice() as *const [u8] as *const OsStr) }
    }

    /// Returns a reference to the raw bytes of this string.
    pub fn as_bytes(&self) -> &[u8] {
        self.inner.as_slice()
    }

    /// Converts a wide UTF-16 vector into a platform `OsString` on Windows.
    #[cfg(windows)]
    pub fn from_wide(vec: Vec<u16>) -> Self {
        Self {
            inner: String::from_utf16_lossy(&vec).into_bytes(),
        }
    }
}

impl Default for OsString {
    fn default() -> Self {
        Self::new()
    }
}

impl AsRef<OsStr> for OsString {
    fn as_ref(&self) -> &OsStr {
        self.as_os_str()
    }
}

impl OsStr {
    /// Returns the underlying bytes of this OS string.
    pub fn as_bytes(&self) -> &[u8] {
        &self.inner
    }

    /// Returns `true` if this OS string is empty.
    pub fn is_empty(&self) -> bool {
        self.as_bytes().is_empty()
    }

    /// Returns a borrowed owned copy of this OS string.
    pub fn to_owned(&self) -> OsString {
        OsString::from_vec(self.as_bytes().to_vec())
    }

    /// Converts this OS string to a UTF-8 string, replacing invalid bytes.
    pub fn to_string_lossy(&self) -> Cow<'_, str> {
        String::from_utf8_lossy(self.as_bytes())
    }

    /// Returns the underlying WTF-8 bytes for Windows interop.
    #[cfg(windows)]
    pub fn as_encoded_bytes(&self) -> &[u8] {
        self.as_bytes()
    }
}

impl core::fmt::Debug for OsStr {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        write!(f, "{:?}", self.to_string_lossy())
    }
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
