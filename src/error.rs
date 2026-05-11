//! Error types.

use std::path::PathBuf;

use thiserror::Error;

/// Result alias.
pub type Result<T> = std::result::Result<T, Error>;

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
        path: PathBuf,
        /// Underlying I/O error.
        #[source]
        source: std::io::Error,
    },

    /// A path-free I/O failure.
    #[error("i/o error: {0}")]
    PlainIo(#[from] std::io::Error),

    /// A name could not be resolved.
    #[error("not found: {0}")]
    NotFound(String),

    /// Implementation-specific failure.
    #[error("error: {0}")]
    Other(String),
}

impl Error {
    /// Convenience constructor for a path-bound I/O error.
    pub fn io(path: impl Into<PathBuf>, source: std::io::Error) -> Self {
        Self::Io {
            path: path.into(),
            source,
        }
    }
}
