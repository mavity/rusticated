#![warn(missing_docs)]
//! Fast, standard-library-shaped async platform layer for brush-async

/// Shared ABI definitions
pub mod abi;
/// Environment evaluation
pub mod env;
/// Errors
pub mod error;
/// File system abstractions
pub mod fs;
/// I/O operations
pub mod io;
/// Path extensions
pub mod path;
/// Runtime engine abstraction
pub mod rt;
/// OS signal abstractions
pub mod signal;
/// Terminal interface types
pub mod tty;

pub use error::{Error, Result};
