//! Error types.

use crate::string::String;
use core::fmt;

/// Result alias.
pub type Result<T> = core::result::Result<T, SystemError>;

/// Errors from I/O and system calls.
#[derive(Debug)]
#[non_exhaustive]
pub enum SystemError {
    /// The requested operation is not supported on this target.
    Unsupported(&'static str),

    /// A path-bound operation failed.
    Io {
        /// Path that the failing operation referred to.
        path: String,
        /// Underlying I/O error.
        source: crate::io::Error,
    },

    /// A path-free I/O failure.
    PlainIo(crate::io::Error),

    /// A name could not be resolved.
    NotFound(String),

    /// Implementation-specific failure.
    Other(String),
}

impl fmt::Display for SystemError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Unsupported(s) => write!(f, "operation not supported: {}", s),
            Self::Io { path, source } => write!(f, "i/o error on {}: {}", path, source),
            Self::PlainIo(e) => write!(f, "i/o error: {}", e),
            Self::NotFound(s) => write!(f, "not found: {}", s),
            Self::Other(s) => write!(f, "error: {}", s),
        }
    }
}

impl core::error::Error for SystemError {
    fn source(&self) -> Option<&(dyn core::error::Error + 'static)> {
        match self {
            Self::Io { source, .. } => Some(source),
            Self::PlainIo(source) => Some(source),
            _ => None,
        }
    }
}

impl From<crate::io::Error> for SystemError {
    fn from(e: crate::io::Error) -> Self {
        Self::PlainIo(e)
    }
}

impl SystemError {
    /// Convenience constructor for a path-bound I/O error.
    pub fn io(path: impl Into<String>, source: crate::io::Error) -> Self {
        Self::Io {
            path: path.into(),
            source,
        }
    }
}

#[allow(unused_imports)]
pub use core::error::*;
