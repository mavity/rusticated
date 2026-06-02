//! WASM host ABI definitions: `Overlapped` struct and host imports.

#![allow(clippy::doc_markdown, clippy::missing_safety_doc)]

/// Overlapped I/O structure for host-guest WASM communication.
///
/// This struct is passed to host ABI calls to represent an async operation
/// in progress. The host fills in `flags`, `error`, and `result_ext` before
/// waking the guest.
#[repr(C)]
#[derive(Debug, Default, Clone, Copy)]
pub struct Overlapped {
    /// Completion and status flags set by the host.
    ///
    /// `FLAG_COMPLETED` is used by the runtime to know when the operation has
    /// finished, and other bits may encode additional host-specific state.
    pub flags: u32,
    /// Host error code returned by the operation.
    ///
    /// A value of `0` indicates success; nonzero values are host-defined error
    /// codes that should be translated by the guest runtime.
    pub error: u32,
    /// Continuation value provided by the host.
    ///
    /// This opaque field is generally used to keep operation-specific state
    /// such as file offsets, buffer positions, or other host-managed data.
    pub continued: u64,
    /// Operation-specific result payload.
    ///
    /// For reads/writes it is typically the byte count transferred; for open
    /// it can be the returned handle, and for other operations it is an
    /// additional result value defined by the host ABI.
    pub result_ext: u64,
}

impl Overlapped {
    /// Flag indicating the host has completed the requested operation.
    pub const FLAG_COMPLETED: u32 = 1;

    /// Returns `true` when the host has finished the operation.
    pub const fn is_complete(&self) -> bool {
        (self.flags & Self::FLAG_COMPLETED) != 0
    }
}

/// Stable stat payload written by host `path_stat` into guest memory.
///
/// The host fills this structure for filesystem metadata requests made by the
/// WASM guest. All fields are stable in layout but may be platform-dependent
/// in exact semantics.
#[repr(C)]
#[derive(Debug, Default, Clone, Copy)]
pub struct AbiStat {
    /// One of the `STAT_KIND_*` constants describing the filesystem object type.
    pub kind: u32,
    /// Platform-specific mode bits.
    ///
    /// On Unix this includes permission and file type flags; on other targets it
    /// is interpreted by the host ABI implementation.
    pub mode: u32,
    /// Owner user identifier, if provided by the host.
    pub uid: u32,
    /// Owner group identifier, if provided by the host.
    pub gid: u32,
    /// File size in bytes.
    ///
    /// For directories or special files this may be zero depending on the host.
    pub size: u64,
    /// Last modification time in nanoseconds since the Unix epoch.
    pub modified_ns: u64,
    /// Last access time in nanoseconds since the Unix epoch.
    pub accessed_ns: u64,
    /// Creation time in nanoseconds since the Unix epoch.
    pub created_ns: u64,
    /// Number of hard links pointing at the file.
    pub nlink: u64,
    /// Filesystem object identifier.
    ///
    /// This is typically an inode or equivalent identifier; it is opaque and
    /// should be used for equality or uniqueness checks only.
    pub inode: u64,
}

/// Do not follow symbolic links when statting a path.
///
/// This flag instructs the host to return metadata for the symlink itself
/// rather than the target it points to.
pub const STAT_FLAG_NOFOLLOW: u32 = 1;

/// Unknown filesystem object kind returned by `path_stat`.
pub const STAT_KIND_UNKNOWN: u32 = 0;

/// Regular file kind returned by `path_stat`.
pub const STAT_KIND_FILE: u32 = 1;

/// Directory kind returned by `path_stat`.
pub const STAT_KIND_DIR: u32 = 2;

/// Symbolic link kind returned by `path_stat`.
pub const STAT_KIND_SYMLINK: u32 = 3;

/// WASM Host Imports
#[cfg(target_family = "wasm")]
pub mod imports {
    use super::Overlapped;

    #[link(wasm_import_module = "env")]
    unsafe extern "C" {
        /// Ask for the system time (returns nanos since epoch).
        pub fn get_time() -> u64;

        /// One-shot retrieval for argv/env.
        /// Returns: (Count of items in high 32 bits | Total bytes written in low 32 bits).
        pub fn get_args(strings_ptr: *mut u8, strings_len: u32) -> u64;
        /// One-shot retrieval for argv/env.
        pub fn get_env(strings_ptr: *mut u8, strings_len: u32) -> u64;

        /// One-shot retrieval for current working directory.
        /// Returns: (Error code in high 32 bits | Required bytes in low 32 bits).
        pub fn get_cwd(path_ptr: *mut u8, path_len: u32) -> u64;
        /// Set current working directory.
        /// Returns: host OS-style error code, 0 on success.
        pub fn set_cwd(path_ptr: *const u8, path_len: u32) -> u32;

        /// Request a wakeup after `delay_ms`.
        pub fn timer_set(overlapped: *mut Overlapped, delay_ms: u32);
        /// Cancel a pending timer (Sync).
        pub fn timer_cancel(target_overlapped: *const Overlapped);

        /// Unified read. Result_ext = bytes transferred.
        pub fn read(overlapped: *mut Overlapped, handle: u64, buffer_ptr: *mut u8, buffer_len: u32);
        /// Unified write. Result_ext = bytes transferred.
        pub fn write(
            overlapped: *mut Overlapped,
            handle: u64,
            buffer_ptr: *const u8,
            buffer_len: u32,
        );
        /// Close handle (Sync)
        pub fn handle_close(handle: u64);

        /// Open a file or directory. Result_ext = Handle.
        pub fn path_open(
            overlapped: *mut Overlapped,
            path_ptr: *const u8,
            path_len: u32,
            flags: u32,
        );
        /// Read directory entries into buffer (linear names, null-separated).
        pub fn dir_read(
            overlapped: *mut Overlapped,
            handle: u64,
            buffer_ptr: *mut u8,
            buffer_len: u32,
        );
        /// Query metadata for a path and write a full `AbiStat` payload into guest memory.
        ///
        /// `flags`: use `STAT_FLAG_NOFOLLOW` to request symlink metadata.
        /// `result_ext`: bytes written (or required bytes if `out_len` is too small).
        pub fn path_stat(
            overlapped: *mut Overlapped,
            path_ptr: *const u8,
            path_len: u32,
            flags: u32,
            out_ptr: *mut u8,
            out_len: u32,
        );
        /// Update path permissions / mode bits.
        pub fn path_chmod(
            overlapped: *mut Overlapped,
            path_ptr: *const u8,
            path_len: u32,
            mode: u32,
        );

        /// Create listener or connection. Result_ext = Socket Handle.
        pub fn net_open(
            overlapped: *mut Overlapped,
            addr_ptr: *const u8,
            addr_len: u32,
            port: u16,
            flags: u32,
        );
        /// Await a new connection. Result_ext = Client Handle.
        pub fn net_accept(overlapped: *mut Overlapped, listen_handle: u64);

        /// Spawn process. Maps guest handles to child's 0,1,2. Result_ext = Process Handle.
        pub fn process_spawn(overlapped: *mut Overlapped, cfg_ptr: *const u8, cfg_len: u32);
        /// Await process exit. Result_ext = Exit Code (high 32) | Status (low 32).
        pub fn process_wait(overlapped: *mut Overlapped, process_handle: u64);
        /// Terminate the current WASM guest immediately with the given exit code.
        pub fn process_exit(code: i32) -> !;
        /// Send signal (Sync).
        pub fn process_signal(process_handle: u64, signum: u32);

        /// Await a specific system signal (SIGINT, etc.).
        pub fn signal_wait(overlapped: *mut Overlapped, signum: u32);

        /// Set terminal mode (Raw/Cooked/Echo).
        pub fn tty_set_mode(handle: u64, mode: u32);
        /// Get window size (Sync). Returns columns << 16 | rows.
        pub fn tty_get_size(handle: u64) -> u32;

        /// Fill buffer with random bytes.
        pub fn get_random(buffer_ptr: *mut u8, buffer_len: u32);
    }
}
