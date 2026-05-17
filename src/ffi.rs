//! OS string types and null-terminated C strings for `#![no_std]` use.

use crate::string::String;
use crate::vec::Vec;

// ─── OsStr / OsString ────────────────────────────────────────────────────────

/// OS string slice — an alias for [`str`].
///
/// All supported targets work with UTF-8 exclusively; this alias makes
/// call sites that import `crate::ffi::OsStr` compatible with code originally
/// written against `std::ffi::OsStr`.
pub type OsStr = str;

/// Owned OS string — an alias for [`String`].
pub type OsString = String;

// ─── CString ─────────────────────────────────────────────────────────────────

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

// ─── OsStrExt ────────────────────────────────────────────────────────────────

/// Extension methods on [`OsStr`] (`str`) providing byte-level access
/// and Windows UTF-16 encoding.
pub trait OsStrExt {
    /// Returns the underlying byte representation.
    fn as_bytes(&self) -> &[u8];

    /// Encodes the string as UTF-16 code units.
    ///
    /// Used on Windows to build `LPCWSTR` arguments for Win32 APIs.
    fn encode_wide(&self) -> EncodeWide<'_>;
}

impl OsStrExt for str {
    #[inline]
    fn as_bytes(&self) -> &[u8] {
        str::as_bytes(self)
    }

    #[inline]
    fn encode_wide(&self) -> EncodeWide<'_> {
        EncodeWide {
            inner: self.encode_utf16(),
        }
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
