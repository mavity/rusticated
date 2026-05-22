//! OS-specific extensions, shaped after `std::os`.

/// Raw FFI types, shaped after `std::os::raw`.
#[allow(non_camel_case_types)]
pub mod raw {
    /// C `int` type.
    pub type c_int = i32;
    /// C `char` type.
    pub type c_char = i8;
}

/// Unix-specific extensions.
#[cfg(unix)]
pub mod unix {
    /// Unix file-descriptor I/O extensions.
    pub mod io {
        /// Raw Unix file descriptor number.
        pub type RawFd = i32;

        /// Objects that expose a raw Unix file descriptor.
        pub trait AsRawFd {
            /// Returns the raw file descriptor.
            fn as_raw_fd(&self) -> RawFd;
        }

        /// Construct from a raw file descriptor.
        pub trait FromRawFd {
            /// Creates a new instance wrapping `fd`.
            ///
            /// # Safety
            ///
            /// `fd` must be a valid, open file descriptor that is not owned by
            /// any other Rust value. The created object takes exclusive
            /// ownership of `fd` and will close it on drop.
            unsafe fn from_raw_fd(fd: RawFd) -> Self;
        }

        /// Convert to a raw file descriptor, consuming the owning type.
        pub trait IntoRawFd {
            /// Consumes `self` and returns the underlying raw file descriptor.
            ///
            /// The caller is responsible for closing the descriptor.
            fn into_raw_fd(self) -> RawFd;
        }
    }
}

/// Windows-specific extensions.
#[cfg(windows)]
pub mod windows {
    /// Windows HANDLE I/O extensions.
    pub mod io {
        /// Raw Windows HANDLE value.
        pub type RawHandle = *mut core::ffi::c_void;

        /// Objects that expose a raw Windows HANDLE.
        pub trait AsRawHandle {
            /// Returns the raw handle.
            fn as_raw_handle(&self) -> RawHandle;
        }

        /// Construct from a raw Windows HANDLE.
        pub trait FromRawHandle {
            /// Creates a new instance wrapping `handle`.
            ///
            /// # Safety
            ///
            /// `handle` must be a valid, open handle not owned by any other
            /// Rust value. The created object takes exclusive ownership.
            unsafe fn from_raw_handle(handle: RawHandle) -> Self;
        }

        /// Convert to a raw Windows HANDLE, consuming the owning type.
        pub trait IntoRawHandle {
            /// Consumes `self` and returns the underlying raw handle.
            fn into_raw_handle(self) -> RawHandle;
        }
    }
}
