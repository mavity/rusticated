//! OS-specific extensions, shaped after `std::os`.

/// Raw FFI types, shaped after `std::os::raw`.
#[allow(non_camel_case_types)]
pub mod raw {
    /// C `int` type.
    pub type c_int = i32;
    /// C `char` type.
    pub type c_char = i8;
}

#[cfg(any(target_os = "linux", rusticated_linux))]
pub mod linux;

/// Unix-specific extensions.
#[cfg(any(unix, rusticated_linux))]
pub mod unix {
    /// Unix-specific OS string extension traits and helpers.
    pub mod ffi {
        pub use crate::ffi::{OsStr, OsString};

        /// Extension methods for borrowed Unix OS strings.
        pub trait OsStrExt {
            /// Returns the underlying OS string bytes.
            fn as_bytes(&self) -> &[u8];
            /// Converts to an owned `OsString`.
            fn to_os_string(&self) -> OsString;
        }

        /// Extension methods for owned Unix OS strings.
        pub trait OsStringExt {
            /// Constructs an owned `OsString` from raw bytes.
            fn from_vec(vec: alloc::vec::Vec<u8>) -> OsString;
            /// Consumes this value and returns the raw bytes.
            fn into_vec(self) -> alloc::vec::Vec<u8>;
        }

        impl OsStrExt for OsStr {
            fn as_bytes(&self) -> &[u8] {
                self.as_bytes()
            }

            fn to_os_string(&self) -> OsString {
                self.to_owned()
            }
        }

        impl OsStringExt for OsString {
            fn from_vec(vec: alloc::vec::Vec<u8>) -> OsString {
                OsString::from_vec(vec)
            }

            fn into_vec(self) -> alloc::vec::Vec<u8> {
                self.into_vec()
            }
        }
    }

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

    /// Windows-specific OS string extension traits and helpers.
    pub mod ffi {
        pub use crate::ffi::{OsStr, OsString};

        /// Extension methods for borrowed Windows OS strings.
        pub trait OsStrExt {
            /// Returns the WTF-8-encoded bytes of this string.
            fn as_encoded_bytes(&self) -> &[u8];
            /// Converts to an owned `OsString`.
            fn to_os_string(&self) -> OsString;
        }

        /// Extension methods for owned Windows OS strings.
        pub trait OsStringExt {
            /// Constructs an owned `OsString` from raw UTF-16 code units.
            fn from_wide(vec: alloc::vec::Vec<u16>) -> OsString;
        }

        impl OsStrExt for OsStr {
            fn as_encoded_bytes(&self) -> &[u8] {
                self.as_encoded_bytes()
            }

            fn to_os_string(&self) -> OsString {
                self.to_owned()
            }
        }

        impl OsStringExt for OsString {
            fn from_wide(vec: alloc::vec::Vec<u16>) -> OsString {
                OsString::from_wide(vec)
            }
        }
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
