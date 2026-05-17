//! Error types.

use crate::string::String;
use thiserror::Error;

/// Result alias.
pub type Result<T> = core::result::Result<T, Error>;

/// Errors from I/O and system calls.
#[derive(Debug, Error)]
#[non_exhaustive]
pub enum Error {
    /// The requested operation is not supported on this target.
    #[error("operation not supported: {0}")]
    Unsupported(&'static str),

    /// A path-bound operation failed.
    #[error("i/o error on {path}: {source}")]
    Io {
        /// Path that the failing operation referred to.
        path: String,
        /// Underlying I/O error.
        #[source]
        source: crate::io::Error,
    },

    /// A path-free I/O failure.
    #[error("i/o error: {0}")]
    PlainIo(#[from] crate::io::Error),

    /// A name could not be resolved.
    #[error("not found: {0}")]
    NotFound(String),

    /// Implementation-specific failure.
    #[error("error: {0}")]
    Other(String),
}

impl Error {
    /// Convenience constructor for a path-bound I/O error.
    pub fn io(path: impl Into<String>, source: crate::io::Error) -> Self {
        Self::Io {
            path: path.into(),
            source,
        }
    }
}
