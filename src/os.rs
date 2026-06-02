//! OS-specific extensions, shaped after `std::os`.

/// Raw FFI types, shaped after `std::os::raw`.
#[allow(non_camel_case_types)]
pub mod raw {
    /// C `int` type.
    pub type c_int = i32;
    /// C `char` type.
    pub type c_char = i8;
}

#[cfg(target_os = "linux")]
pub mod linux;

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

    /// Unix file system extensions.
    pub mod fs {
        /// Metadata extensions for Unix file systems.
        pub trait MetadataExt {
            /// Returns the raw permission bits for this file metadata.
            fn mode(&self) -> u32;
            /// Returns the number of hard links to this file.
            fn nlink(&self) -> u64;
            /// Returns the owning user ID.
            fn uid(&self) -> u32;
            /// Returns the owning group ID.
            fn gid(&self) -> u32;
            /// Returns the inode number.
            fn ino(&self) -> u64;
        }

        impl MetadataExt for crate::fs::Metadata {
            fn mode(&self) -> u32 {
                crate::fs::Metadata::mode(self)
            }
            fn nlink(&self) -> u64 {
                crate::fs::Metadata::nlink(self)
            }
            fn uid(&self) -> u32 {
                crate::fs::Metadata::uid(self)
            }
            fn gid(&self) -> u32 {
                crate::fs::Metadata::gid(self)
            }
            fn ino(&self) -> u64 {
                crate::fs::Metadata::inode(self)
            }
        }
    }
}

/// Windows-specific extensions.
#[cfg(windows)]
pub mod windows {
    /// Prelude for Windows.
    pub mod prelude {
        pub use super::io::*;
    }

    /// Windows HANDLE I/O extensions.
    pub mod io {
        /// Raw Windows HANDLE value.
        pub type RawHandle = *mut core::ffi::c_void;

        /// Raw Windows SOCKET value.
        pub type RawSocket = u64;

        /// A borrowed Windows HANDLE.
        #[derive(Copy, Clone, Debug, Eq, PartialEq, Hash)]
        #[repr(transparent)]
        pub struct BorrowedHandle<'handle> {
            handle: RawHandle,
            _marker: core::marker::PhantomData<&'handle ()>,
        }

        impl<'handle> BorrowedHandle<'handle> {
            /// Return the raw handle.
            pub fn as_raw_handle(&self) -> RawHandle {
                self.handle
            }
        }

        /// A borrowed Windows SOCKET.
        #[derive(Copy, Clone, Debug, Eq, PartialEq, Hash)]
        #[repr(transparent)]
        pub struct BorrowedSocket<'socket> {
            socket: RawSocket,
            _marker: core::marker::PhantomData<&'socket ()>,
        }

        impl<'socket> BorrowedSocket<'socket> {
            /// Return the raw socket.
            pub fn as_raw_socket(&self) -> RawSocket {
                self.socket
            }
        }

        /// Extract a borrowed handle.
        pub trait AsHandle {
            /// Returns a borrowed Windows `HANDLE` wrapper.
            fn as_handle(&self) -> BorrowedHandle<'_>;
        }

        /// Extract a borrowed socket.
        pub trait AsSocket {
            /// Returns a borrowed Windows `SOCKET` wrapper.
            fn as_socket(&self) -> BorrowedSocket<'_>;
        }

        /// Objects that expose a raw Windows HANDLE.
        pub trait AsRawHandle {
            /// Returns the raw handle.
            fn as_raw_handle(&self) -> RawHandle;
        }

        /// Objects that expose a raw Windows SOCKET.
        pub trait AsRawSocket {
            /// Returns the raw socket.
            fn as_raw_socket(&self) -> RawSocket;
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

    /// Windows file system extensions.
    pub mod fs {
        /// Metadata extensions for Windows file systems.
        pub trait MetadataExt {
            /// Returns the raw Win32 file attribute DWORD.
            fn file_attributes(&self) -> u32;
        }

        impl MetadataExt for crate::fs::Metadata {
            fn file_attributes(&self) -> u32 {
                crate::fs::Metadata::mode(self)
            }
        }
    }
}
