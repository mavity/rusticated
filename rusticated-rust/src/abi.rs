//! WASM host ABI definitions: `Overlapped` struct and host imports.

#![allow(clippy::doc_markdown, clippy::missing_safety_doc)]

/// Overlapped I/O structure for host-guest WASM communication.
///
/// This struct is passed to host ABI calls to represent an async operation
/// in progress. The host fills in `flags`, `error`, and `result_ext` before
/// waking the guest. The positioned-read extension is specified in
/// `mohabbat/docs/9-positioned-read.md`.
#[repr(C)]
#[derive(Debug, Default, Clone, Copy)]
pub struct Overlapped {
    /// Completion and operation-specific request flags.
    ///
    /// Bit 0 is `FLAG_COMPLETED`; bits 1-23 are shared-reserved; bits 24-31
    /// are operation-specific. Undefined bits must be zero.
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

const _: [(); 24] = [(); core::mem::size_of::<Overlapped>()];

impl Overlapped {
    /// Flag indicating the host has completed the requested operation.
    pub const FLAG_COMPLETED: u32 = 1;

    /// Returns `true` when the host has finished the operation.
    pub const fn is_complete(&self) -> bool {
        (self.flags & Self::FLAG_COMPLETED) != 0
    }
}

/// Stable 112-byte stat payload written by host `path_stat` into guest memory.
/// Optional metadata is indicated by `valid_mask`.
#[repr(C)]
#[derive(Debug, Default, Clone, Copy)]
pub struct AbiStat {
    /// One of the `STAT_KIND_*` constants describing the filesystem object type.
    pub kind: u32,
    /// Unix-compatible file type and permission bits, synthesized where needed.
    pub mode: u32,
    /// Owner user identifier.
    pub uid: u32,
    /// Owner group identifier.
    pub gid: u32,
    /// File size in bytes.
    pub size: u64,
    /// Last modification time in signed nanoseconds since the Unix epoch.
    pub modified_ns: i64,
    /// Last access time in signed nanoseconds since the Unix epoch.
    pub accessed_ns: i64,
    /// Last metadata-change time in signed nanoseconds since the Unix epoch.
    pub changed_ns: i64,
    /// Birth/creation time in signed nanoseconds since the Unix epoch.
    pub created_ns: i64,
    /// Number of hard links pointing at the file.
    pub nlink: u64,
    /// Filesystem object identifier, typically an inode.
    pub inode: u64,
    /// Device containing the file.
    pub dev: u64,
    /// Device identifier for special files.
    pub rdev: u64,
    /// Preferred block size for filesystem I/O, or a documented estimate.
    pub blksize: i64,
    /// Allocated 512-byte blocks, or an estimate where the host lacks this data.
    pub blocks: i64,
    /// `STAT_VALID_*` flags indicating available metadata.
    pub valid_mask: u64,
}

const _: [(); 112] = [(); core::mem::size_of::<AbiStat>()];

/// `valid_mask` bit indicating that the user identifier is available.
pub const STAT_VALID_UID: u64 = 1 << 0;
/// `valid_mask` bit indicating that the group identifier is available.
pub const STAT_VALID_GID: u64 = 1 << 1;
/// `valid_mask` bit indicating that the access time is available.
pub const STAT_VALID_ATIME: u64 = 1 << 2;
/// `valid_mask` bit indicating that the modification time is available.
pub const STAT_VALID_MTIME: u64 = 1 << 3;
/// `valid_mask` bit indicating that the metadata-change time is available.
pub const STAT_VALID_CTIME: u64 = 1 << 4;
/// `valid_mask` bit indicating that the birth/creation time is available.
pub const STAT_VALID_BIRTHTIME: u64 = 1 << 5;
/// `valid_mask` bit indicating that the hard-link count is available.
pub const STAT_VALID_NLINK: u64 = 1 << 6;
/// `valid_mask` bit indicating that the filesystem object identifier is available.
pub const STAT_VALID_INODE: u64 = 1 << 7;
/// `valid_mask` bit indicating that the containing device is available.
pub const STAT_VALID_DEV: u64 = 1 << 8;
/// `valid_mask` bit indicating that the special-file device identifier is available.
pub const STAT_VALID_RDEV: u64 = 1 << 9;
/// `valid_mask` bit indicating that the preferred block size is available.
pub const STAT_VALID_BLKSIZE: u64 = 1 << 10;
/// `valid_mask` bit indicating that the allocated block count is available.
pub const STAT_VALID_BLOCKS: u64 = 1 << 11;

/// Do not follow symbolic links when statting a path.
///
/// This flag instructs the host to return metadata for the symlink itself
/// rather than the target it points to.
pub const STAT_FLAG_NOFOLLOW: u32 = 1;

/// Follow the final source symlink when creating a hard link.
pub const LINK_FLAG_FOLLOW_SOURCE: u32 = 1;

/// The guest does not specify a symbolic-link target kind.
pub const SYMLINK_TARGET_UNKNOWN: u32 = 0;
/// The symbolic-link target is intended to be a file.
pub const SYMLINK_TARGET_FILE: u32 = 1;
/// The symbolic-link target is intended to be a directory.
pub const SYMLINK_TARGET_DIRECTORY: u32 = 2;

/// Platform information structure populated by the host.
#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct AbiPlatformInfo {
    /// Flags (bit 0: case sensitive)
    pub flags: u32,
    /// Path separator character.
    pub path_sep: u8,
    /// Path list separator character.
    pub list_sep: u8,
    /// Operating system kind (1=Windows, 2=Linux, 3=Darwin, 4=Bsd).
    pub os_kind: u16,
    /// Operating system version (4x u16).
    pub os_version: [u16; 4],
    /// CPU Architecture type (1=x86_64, 2=arm64).
    pub cpu_type: u16,
    /// CPU Biteness (e.g. 64).
    pub cpu_bitness: u8,
    /// padding
    pub _reserved: u8,
    /// OS name string (64 bytes).
    pub os_name: [u8; 64],
    /// WASM runtime category/platform name (64 bytes).
    pub wasm_platform: [u8; 64],
    /// WASM platform version.
    pub wasm_version: [u16; 4],
    /// WASM platform version string (64 bytes).
    pub wasm_version_str: [u8; 64],
    /// Rusticated name string (64 bytes).
    pub rusticated_name: [u8; 64],
    /// Rusticated version.
    pub rusticated_version: [u16; 4],
    /// Rusticated version string (64 bytes).
    pub rusticated_version_str: [u8; 64],
    /// Build version string (64 bytes).
    pub build_version: [u8; 64],
    /// Build time string (64 bytes).
    pub build_time: [u8; 64],
    /// Build platform string (64 bytes).
    pub build_platform: [u8; 64],
    /// Host user home directory string (64 bytes).
    pub user_home_dir: [u8; 64],
    /// Host temporary directory string (64 bytes).
    pub temp_dir: [u8; 64],
}

impl Default for AbiPlatformInfo {
    fn default() -> Self {
        unsafe { core::mem::zeroed() }
    }
}

/// Unknown filesystem object kind returned by `path_stat`.
pub const STAT_KIND_UNKNOWN: u32 = 0;

/// Regular file kind returned by `path_stat`.
pub const STAT_KIND_FILE: u32 = 1;

/// Directory kind returned by `path_stat`.
pub const STAT_KIND_DIR: u32 = 2;

/// Symbolic link kind returned by `path_stat`.
pub const STAT_KIND_SYMLINK: u32 = 3;

/// Character device kind returned by `path_stat`.
pub const STAT_KIND_CHAR: u32 = 4;

/// Block device kind returned by `path_stat`.
pub const STAT_KIND_BLOCK: u32 = 5;

/// FIFO kind returned by `path_stat`.
pub const STAT_KIND_FIFO: u32 = 6;

/// Socket kind returned by `path_stat`.
pub const STAT_KIND_SOCKET: u32 = 7;

/// WASM Host Imports
#[cfg(target_family = "wasm")]
pub mod imports {
    /// Request a positional read using the `read` overlapped extension.
    pub const READ_FLAG_POSITIONAL: u32 = 1 << 24;

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
        /// Cancel any pending overlapped operation (Sync).
        pub fn cancel(target_overlapped: *mut Overlapped);

        /// Unified read. `READ_FLAG_POSITIONAL` adds a u64 offset after the
        /// standard 24-byte header. Result_ext = bytes transferred.
        pub fn read(overlapped: *mut Overlapped, handle: u64, buffer_ptr: *mut u8, buffer_len: u32);
        /// Unified write. Result_ext = bytes transferred.
        pub fn write(
            overlapped: *mut Overlapped,
            handle: u64,
            buffer_ptr: *const u8,
            buffer_len: u32,
        );
        /// Set a file handle's logical length in bytes. Result_ext = 0.
        pub fn handle_truncate(overlapped: *mut Overlapped, handle: u64, length: i64);
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
        /// Create a hard link. `flags` accepts `LINK_FLAG_FOLLOW_SOURCE`.
        pub fn path_link(
            overlapped: *mut Overlapped,
            source_ptr: *const u8,
            source_len: u32,
            new_path_ptr: *const u8,
            new_path_len: u32,
            flags: u32,
        );
        /// Create a symbolic link containing `target_ptr` as link data.
        pub fn path_symlink(
            overlapped: *mut Overlapped,
            target_ptr: *const u8,
            target_len: u32,
            link_path_ptr: *const u8,
            link_path_len: u32,
            target_kind: u32,
        );
        /// Read the target stored in the final symbolic-link path component.
        pub fn path_readlink(
            overlapped: *mut Overlapped,
            path_ptr: *const u8,
            path_len: u32,
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
        /// Set path access and modification times.
        ///
        /// Timestamp flag bits are defined by Rusticated's `path_set_times` contract.
        pub fn path_set_times(
            overlapped: *mut Overlapped,
            path_ptr: *const u8,
            path_len: u32,
            atime_sec: i64,
            atime_nsec: u32,
            mtime_sec: i64,
            mtime_nsec: u32,
            flags: u32,
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

        /// Get detailed host platform and build information.
        pub fn get_platform_info(out_ptr: *mut u8, out_len: u32) -> u32;
    }
}
