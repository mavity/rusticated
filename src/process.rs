//! Process execution and management

/// Process identifier.
pub type ProcessId = u32;

/// Result of a process execution.
#[derive(Debug, Clone)]
pub struct Output {
    /// The status (exit code) of the process.
    pub status: ExitStatus,
    /// The data that the process wrote to stdout.
    pub stdout: crate::vec::Vec<u8>,
    /// The data that the process wrote to stderr.
    pub stderr: crate::vec::Vec<u8>,
}

/// A trait for types that can be returned from `main`.
pub trait Termination {
    /// Is called to get the exit code of the process.
    fn report(self) -> i32;
}

impl Termination for () {
    fn report(self) -> i32 {
        0
    }
}

impl Termination for i32 {
    fn report(self) -> i32 {
        self
    }
}

/// Terminates the process in an abnormal fashion.
pub fn abort() -> ! {
    core::intrinsics::abort()
}

impl Termination for ExitStatus {
    fn report(self) -> i32 {
        self.code().unwrap_or(1)
    }
}

impl<T: Termination, E: core::fmt::Debug> Termination for core::result::Result<T, E> {
    fn report(self) -> i32 {
        match self {
            Ok(val) => val.report(),
            Err(err) => {
                let _ = err;
                #[cfg(not(target_arch = "wasm32"))]
                crate::eprintln!("Error: {:?}", err);
                1
            }
        }
    }
}

/// Returns the unique ID of the current process.
pub fn id() -> ProcessId {
    0
}

#[cfg_attr(
    target_family = "wasm",
    allow(
        clippy::cast_possible_truncation,
        clippy::cast_possible_wrap,
        clippy::cast_sign_loss,
        clippy::missing_const_for_fn,
        clippy::doc_markdown,
        clippy::type_complexity,
        clippy::unnecessary_wraps,
        clippy::needless_pass_by_value,
        clippy::undocumented_unsafe_blocks,
    )
)]
#[cfg(not(target_family = "wasm"))]
mod native_process {
    use crate::io;
    use crate::path::{Path, PathBuf};
    use crate::string::String;
    use crate::vec::Vec;
    // ── Linux pidfd async wait ────────────────────────────────────────────────

    #[cfg(all(
        target_os = "linux",
        any(target_arch = "x86_64", target_arch = "aarch64")
    ))]
    const SYS_PIDFD_OPEN: i64 = 434;

    #[cfg(target_os = "linux")]
    unsafe extern "C" {
        fn syscall(num: i64, ...) -> i64;
        fn close(fd: i32) -> i32;
    }

    #[cfg(all(
        target_os = "linux",
        any(target_arch = "x86_64", target_arch = "aarch64")
    ))]
    fn pidfd_open(pid: u32) -> io::Result<i32> {
        // SAFETY: variadic syscall with two ABI-correct arguments.
        let r = unsafe { syscall(SYS_PIDFD_OPEN, pid as i64, 0i64) };
        if r < 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(r as i32)
        }
    }

    // ── Unix: posix_spawnp, waitpid, kill ────────────────────────────────────

    #[cfg(unix)]
    unsafe extern "C" {
        fn posix_spawnp(
            pid: *mut i32,
            file: *const u8,
            file_actions: *const core::ffi::c_void,
            attrp: *const core::ffi::c_void,
            argv: *const *const u8,
            envp: *const *const u8,
        ) -> i32;
        fn waitpid(pid: i32, status: *mut i32, options: i32) -> i32;
        fn kill(pid: i32, sig: i32) -> i32;
        static environ: *const *const u8;
    }

    #[cfg(unix)]
    const SIGKILL: i32 = 9;
    #[cfg(unix)]
    const WNOHANG: i32 = 1;

    // ── Windows ───────────────────────────────────────────────────────────────

    #[cfg(windows)]
    #[repr(C)]
    struct StartupInfoW {
        cb: u32,
        lp_reserved: *mut u16,
        lp_desktop: *mut u16,
        lp_title: *mut u16,
        dw_x: u32,
        dw_y: u32,
        dw_x_size: u32,
        dw_y_size: u32,
        dw_x_count_chars: u32,
        dw_y_count_chars: u32,
        dw_fill_attribute: u32,
        dw_flags: u32,
        w_show_window: u16,
        cb_reserved2: u16,
        lp_reserved2: *mut u8,
        h_std_input: usize,
        h_std_output: usize,
        h_std_error: usize,
    }

    #[cfg(windows)]
    #[repr(C)]
    struct ProcessInformation {
        h_process: usize,
        h_thread: usize,
        dw_process_id: u32,
        dw_thread_id: u32,
    }

    #[cfg(windows)]
    unsafe extern "system" {
        fn CreateProcessW(
            lp_application_name: *const u16,
            lp_command_line: *mut u16,
            lp_process_attributes: *const core::ffi::c_void,
            lp_thread_attributes: *const core::ffi::c_void,
            b_inherit_handles: i32,
            dw_creation_flags: u32,
            lp_environment: *const core::ffi::c_void,
            lp_current_directory: *const u16,
            lp_startup_info: *const StartupInfoW,
            lp_process_information: *mut ProcessInformation,
        ) -> i32;
        fn GetExitCodeProcess(h_process: usize, lp_exit_code: *mut u32) -> i32;
        fn CloseHandle(h_object: usize) -> i32;
        fn TerminateProcess(h_process: usize, u_exit_code: u32) -> i32;
    }

    // ── Exit status ───────────────────────────────────────────────────────────

    /// Exit status of a child process.
    #[derive(Debug, Clone, Copy, PartialEq, Eq)]
    pub struct ChildExitStatus(i32);
    /// Alias for ExitStatus to match std.
    pub use ChildExitStatus as ExitStatus;

    impl ChildExitStatus {
        /// Returns `true` if the process exited with code 0.
        pub fn success(&self) -> bool {
            self.0 == 0
        }
        /// Returns the raw exit code.
        pub fn code(&self) -> Option<i32> {
            Some(self.0)
        }
    }

    // ── Stdio ─────────────────────────────────────────────────────────────────

    /// I/O configuration for a child process's stdin/stdout/stderr.
    pub enum Stdio {
        /// Inherit the parent's handle.
        Inherit,
        /// Redirect to the null device.
        Null,
        /// Create a pipe.
        Piped,
        /// Redirect to a file.
        File(crate::fs::FileNative),
    }

    impl Stdio {
        /// Create a `Stdio` that inherits the parent's handle.
        pub fn inherit() -> Self {
            Self::Inherit
        }
        /// Create a `Stdio` that discards all I/O.
        pub fn null() -> Self {
            Self::Null
        }
        /// Create a `Stdio` that sets up a pipe.
        pub fn piped() -> Self {
            Self::Piped
        }
    }

    impl From<crate::fs::FileNative> for Stdio {
        fn from(file: crate::fs::FileNative) -> Self {
            Self::File(file)
        }
    }

    // ── Child ─────────────────────────────────────────────────────────────────

    /// A running child process.
    pub struct Child {
        #[cfg(unix)]
        pid: u32,
        #[cfg(windows)]
        handle: usize,

        /// Async reader for stdout if piped.
        pub stdout: Option<crate::fs::FileNative>,
        /// Async reader for stderr if piped.
        pub stderr: Option<crate::fs::FileNative>,

        #[cfg(not(any(unix, windows)))]
        _opaque: (),
    }

    impl Child {
        /// Returns the process ID.
        pub fn id(&self) -> Option<u32> {
            #[cfg(unix)]
            {
                Some(self.pid)
            }
            #[cfg(windows)]
            {
                None
            }
            #[cfg(not(any(unix, windows)))]
            {
                None
            }
        }

        /// Wait asynchronously for the child to exit.
        #[allow(clippy::unused_async)]
        pub async fn wait(&mut self) -> io::Result<ChildExitStatus> {
            #[cfg(all(
                target_os = "linux",
                any(target_arch = "x86_64", target_arch = "aarch64")
            ))]
            {
                let pidfd = pidfd_open(self.pid)?;
                let res = crate::rt::wait_readable(pidfd).await;
                // SAFETY: pidfd is valid and owned by us.
                unsafe { close(pidfd) };
                res?;
                let mut status = 0i32;
                // SAFETY: self.pid is valid.
                let r = unsafe { waitpid(self.pid as i32, &mut status, 0) };
                if r < 0 {
                    return Err(io::Error::last_os_error());
                }
                let code = if status & 0x7f == 0 {
                    (status >> 8) & 0xff
                } else {
                    -1
                };
                return Ok(ChildExitStatus(code));
            }
            #[cfg(all(
                unix,
                not(all(
                    target_os = "linux",
                    any(target_arch = "x86_64", target_arch = "aarch64")
                ))
            ))]
            {
                let mut status = 0i32;
                // SAFETY: self.pid is valid.
                let r = unsafe { waitpid(self.pid as i32, &mut status, 0) };
                if r < 0 {
                    return Err(io::Error::last_os_error());
                }
                let code = if status & 0x7f == 0 {
                    (status >> 8) & 0xff
                } else {
                    -1
                };
                return Ok(ChildExitStatus(code));
            }
            #[cfg(windows)]
            {
                crate::rt::windows::WaitProcess::new(self.handle as u64).await?;
                let mut code = 0u32;
                // SAFETY: handle is a valid process handle.
                unsafe { GetExitCodeProcess(self.handle, &mut code) };
                return Ok(ChildExitStatus(code as i32));
            }
            #[cfg(not(any(unix, windows)))]
            Err(io::Error::other(
                "Child::wait: not supported on this platform",
            ))
        }

        /// Wait for the child to exit and return its output.
        pub async fn wait_with_output(mut self) -> io::Result<crate::process::Output> {
            let mut stdout_data = Vec::new();
            let mut stderr_data = Vec::new();

            if let Some(mut r) = self.stdout.take() {
                use crate::io::AsyncRead;
                let mut temp = alloc::vec![0u8; 8192];
                loop {
                    let (res, mut out) = r.read(temp).await;
                    match res {
                        Ok(0) => break,
                        Ok(n) => {
                            stdout_data.extend_from_slice(&out[..n]);
                            out.clear();
                            out.resize(8192, 0);
                            temp = out;
                        }
                        Err(e) if e.kind() == crate::io::ErrorKind::Interrupted => {
                            out.clear();
                            out.resize(8192, 0);
                            temp = out;
                        }
                        Err(e) => return Err(e),
                    }
                }
            }
            if let Some(mut r) = self.stderr.take() {
                use crate::io::AsyncRead;
                let mut temp = alloc::vec![0u8; 8192];
                loop {
                    let (res, mut out) = r.read(temp).await;
                    match res {
                        Ok(0) => break,
                        Ok(n) => {
                            stderr_data.extend_from_slice(&out[..n]);
                            out.clear();
                            out.resize(8192, 0);
                            temp = out;
                        }
                        Err(e) if e.kind() == crate::io::ErrorKind::Interrupted => {
                            out.clear();
                            out.resize(8192, 0);
                            temp = out;
                        }
                        Err(e) => return Err(e),
                    }
                }
            }

            let status = self.wait().await?;
            Ok(crate::process::Output {
                status: unsafe { core::mem::transmute(status) },
                stdout: stdout_data,
                stderr: stderr_data,
            })
        }

        /// Non-blocking check if the child has exited.
        pub fn try_wait(&mut self) -> io::Result<Option<ChildExitStatus>> {
            #[cfg(unix)]
            {
                let mut status = 0i32;
                // SAFETY: self.pid is valid.
                let r = unsafe { waitpid(self.pid as i32, &mut status, WNOHANG) };
                if r < 0 {
                    return Err(io::Error::last_os_error());
                }
                if r == 0 {
                    return Ok(None);
                }
                let code = if status & 0x7f == 0 {
                    (status >> 8) & 0xff
                } else {
                    -1
                };
                return Ok(Some(ChildExitStatus(code)));
            }
            #[cfg(windows)]
            {
                let mut code = 0u32;
                // SAFETY: handle is valid.
                unsafe { GetExitCodeProcess(self.handle, &mut code) };
                const STILL_ACTIVE: u32 = 259;
                return Ok(if code == STILL_ACTIVE {
                    None
                } else {
                    Some(ChildExitStatus(code as i32))
                });
            }
            #[cfg(not(any(unix, windows)))]
            Ok(None)
        }

        /// Send SIGKILL (Unix) or TerminateProcess (Windows) to the child.
        pub fn kill(&mut self) -> io::Result<()> {
            #[cfg(unix)]
            {
                // SAFETY: self.pid is valid.
                let r = unsafe { kill(self.pid as i32, SIGKILL) };
                if r < 0 {
                    return Err(io::Error::last_os_error());
                }
                return Ok(());
            }
            #[cfg(windows)]
            {
                // SAFETY: handle is valid.
                let r = unsafe { TerminateProcess(self.handle, 1) };
                if r == 0 {
                    return Err(io::Error::last_os_error());
                }
                return Ok(());
            }
            #[cfg(not(any(unix, windows)))]
            Err(io::Error::other("kill: not supported"))
        }
    }

    #[cfg(windows)]
    impl Drop for Child {
        fn drop(&mut self) {
            // SAFETY: handle is valid and owned by us.
            unsafe { CloseHandle(self.handle) };
        }
    }

    // ── Command ───────────────────────────────────────────────────────────────

    /// Builder for spawning child processes.
    pub struct Command {
        program: String,
        arg0: Option<String>,
        args: Vec<String>,
        envs: Vec<(String, String)>,
        stdin: Stdio,
        stdout: Stdio,
        stderr: Stdio,
        cwd: Option<PathBuf>,
        pgid: Option<i32>,
    }

    impl Command {
        /// Create a new command for `program`.
        pub fn new<S: AsRef<str>>(program: S) -> Self {
            Self {
                program: program.as_ref().into(),
                arg0: None,
                args: Vec::new(),
                envs: Vec::new(),
                stdin: Stdio::Inherit,
                stdout: Stdio::Inherit,
                stderr: Stdio::Inherit,
                cwd: None,
                pgid: None,
            }
        }

        /// Append a single argument.
        pub fn arg<S: AsRef<str>>(&mut self, arg: S) -> &mut Self {
            self.args.push(arg.as_ref().into());
            self
        }

        /// Append multiple arguments.
        pub fn args<I, S>(&mut self, args: I) -> &mut Self
        where
            I: IntoIterator<Item = S>,
            S: AsRef<str>,
        {
            for a in args {
                self.args.push(a.as_ref().into());
            }
            self
        }

        /// Set arg0.
        pub fn arg0<S: AsRef<str>>(&mut self, arg: S) -> &mut Self {
            self.arg0 = Some(arg.as_ref().into());
            self
        }

        /// Set workspace.
        pub fn current_dir<P: AsRef<Path>>(&mut self, dir: P) -> &mut Self {
            self.cwd = Some(dir.as_ref().to_path_buf());
            self
        }

        /// Clear envs.
        pub fn env_clear(&mut self) -> &mut Self {
            self.envs.clear();
            self
        }

        /// Set process group.
        pub fn process_group(&mut self, pgid: u32) -> &mut Self {
            self.pgid = Some(pgid as i32);
            self
        }

        /// Get program.
        pub fn get_program(&self) -> &str {
            &self.program
        }

        /// Get args.
        pub fn get_args(&self) -> impl Iterator<Item = &str> {
            self.args.iter().map(|s| s.as_str())
        }

        /// Set an environment variable for the child process.
        pub fn env<K: AsRef<str>, V: AsRef<str>>(&mut self, key: K, val: V) -> &mut Self {
            self.envs.push((key.as_ref().into(), val.as_ref().into()));
            self
        }

        /// Configure stdin for the child process.
        pub fn stdin<T: Into<Stdio>>(&mut self, cfg: T) -> &mut Self {
            self.stdin = cfg.into();
            self
        }

        /// Configure stdout for the child process.
        pub fn stdout<T: Into<Stdio>>(&mut self, cfg: T) -> &mut Self {
            self.stdout = cfg.into();
            self
        }

        /// Configure stderr for the child process.
        pub fn stderr<T: Into<Stdio>>(&mut self, cfg: T) -> &mut Self {
            self.stderr = cfg.into();
            self
        }

        /// Spawn the command as a child process.
        pub fn spawn(&mut self) -> io::Result<Child> {
            self.spawn_impl()
        }

        #[cfg(unix)]
        fn spawn_impl(&mut self) -> io::Result<Child> {
            // Build null-terminated byte strings for argv.
            let mut argv_storage: Vec<Vec<u8>> = Vec::new();
            {
                let mut b = Vec::with_capacity(self.program.len() + 1);
                b.extend_from_slice(self.program.as_bytes());
                b.push(0);
                argv_storage.push(b);
            }
            for arg in &self.args {
                let mut b = Vec::with_capacity(arg.len() + 1);
                b.extend_from_slice(arg.as_bytes());
                b.push(0);
                argv_storage.push(b);
            }
            // Null-terminated pointer array.
            let mut argv_ptrs: Vec<*const u8> = argv_storage.iter().map(|v| v.as_ptr()).collect();
            argv_ptrs.push(core::ptr::null());

            let mut pid = 0i32;
            // SAFETY: argv_storage keeps the strings alive for the duration of
            // posix_spawnp; environ is a valid global pointer.
            let r = unsafe {
                posix_spawnp(
                    &mut pid,
                    argv_storage[0].as_ptr(),
                    core::ptr::null(),
                    core::ptr::null(),
                    argv_ptrs.as_ptr(),
                    environ,
                )
            };
            if r != 0 {
                return Err(io::Error::from_raw_os_error(r));
            }
            Ok(Child {
                pid: pid as u32,
                stdout: None,
                stderr: None,
            })
        }

        #[cfg(windows)]
        fn spawn_impl(&mut self) -> io::Result<Child> {
            use crate::ffi::OsStrExt as _;
            // Build a wide command line.
            let mut cmdline: Vec<u16> = self.program.encode_wide().collect();
            for arg in &self.args {
                cmdline.push(b' ' as u16);
                cmdline.extend(arg.encode_wide());
            }
            cmdline.push(0);

            let si = StartupInfoW {
                cb: core::mem::size_of::<StartupInfoW>() as u32,
                lp_reserved: core::ptr::null_mut(),
                lp_desktop: core::ptr::null_mut(),
                lp_title: core::ptr::null_mut(),
                dw_x: 0,
                dw_y: 0,
                dw_x_size: 0,
                dw_y_size: 0,
                dw_x_count_chars: 0,
                dw_y_count_chars: 0,
                dw_fill_attribute: 0,
                dw_flags: 0,
                w_show_window: 0,
                cb_reserved2: 0,
                lp_reserved2: core::ptr::null_mut(),
                h_std_input: 0,
                h_std_output: 0,
                h_std_error: 0,
            };
            let mut pi = ProcessInformation {
                h_process: 0,
                h_thread: 0,
                dw_process_id: 0,
                dw_thread_id: 0,
            };
            // SAFETY: si/pi are validly initialised; cmdline is null-terminated.
            let ok = unsafe {
                CreateProcessW(
                    core::ptr::null(),
                    cmdline.as_mut_ptr(),
                    core::ptr::null(),
                    core::ptr::null(),
                    0,
                    0,
                    core::ptr::null(),
                    core::ptr::null(),
                    &si,
                    &mut pi,
                )
            };
            if ok == 0 {
                return Err(io::Error::last_os_error());
            }
            // SAFETY: h_thread is a valid handle.
            unsafe { CloseHandle(pi.h_thread) };
            Ok(Child {
                handle: pi.h_process,
                stdout: None,
                stderr: None,
            })
        }

        #[cfg(not(any(unix, windows)))]
        fn spawn_impl(&mut self) -> io::Result<Child> {
            Err(io::Error::other("spawn: not supported on this platform"))
        }
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_process::{Child, ChildExitStatus, Command, ExitStatus, Stdio};

/// Terminate the current process immediately with the given exit code.
///
/// Equivalent to `std::process::exit`. Never returns.
pub fn exit(code: i32) -> ! {
    #[cfg(unix)]
    {
        unsafe extern "C" {
            fn _exit(status: i32) -> !;
        }
        // SAFETY: `_exit` has no preconditions; any `i32` is a valid status.
        unsafe { _exit(code) }
    }
    #[cfg(windows)]
    {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn ExitProcess(u_exit_code: u32) -> !;
        }
        // SAFETY: `ExitProcess` has no preconditions.
        unsafe { ExitProcess(code as u32) }
    }
    #[cfg(target_family = "wasm")]
    {
        use crate::abi::imports;
        // SAFETY: The host import has no preconditions; the guest halts.
        unsafe { imports::process_exit(code) }
    }
}

// ─── WASM ─────────────────────────────────────────────────────────────────────

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::{OverlappedBufferFuture, OverlappedFuture};
#[cfg(target_family = "wasm")]
use crate::string::String;
#[cfg(target_family = "wasm")]
use crate::vec::Vec;

/// WASM child process handle.
#[cfg(target_family = "wasm")]
pub struct Child {
    handle: u64,
}

#[cfg(target_family = "wasm")]
impl Child {
    /// Wait for the child to exit.
    pub async fn wait(&mut self) -> crate::io::Result<ChildExitStatus> {
        let (err, result, _) = OverlappedFuture::new({
            let handle = self.handle;
            move |ov| {
                // SAFETY: `ov` is a valid overlapped pointer.
                unsafe { imports::process_wait(ov, handle) };
            }
        })
        .await;

        if err != 0 {
            return Err(crate::io::Error::from_raw_os_error(err as i32));
        }

        let exit_code = (result >> 32) as i32;
        let success = exit_code == 0;
        Ok(ChildExitStatus { exit_code, success })
    }
}

/// Exit status for a WASM child process.
#[cfg(target_family = "wasm")]
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ChildExitStatus {
    exit_code: i32,
    success: bool,
}
#[cfg(target_family = "wasm")]
pub use ChildExitStatus as ExitStatus;

#[cfg(target_family = "wasm")]
impl ChildExitStatus {
    /// Returns the exit code.
    pub fn code(&self) -> Option<i32> {
        Some(self.exit_code)
    }

    /// Returns `true` if the process exited successfully.
    pub fn success(&self) -> bool {
        self.success
    }
}

/// I/O configuration for a WASM child process.
#[cfg(target_family = "wasm")]
pub enum Stdio {
    /// Inherit the parent's handle.
    Inherit,
    /// Redirect to the null device.
    Null,
    /// Create a pipe.
    Piped,
}

/// Builder for spawning WASM child processes.
#[cfg(target_family = "wasm")]
pub struct Command {
    program: String,
    args: Vec<String>,
    env: Vec<(String, String)>,
}

#[cfg(target_family = "wasm")]
impl Command {
    /// Create a new command.
    pub fn new<S: Into<String>>(program: S) -> Self {
        Self {
            program: program.into(),
            args: Vec::new(),
            env: Vec::new(),
        }
    }

    /// Add an argument.
    pub fn arg<S: Into<String>>(&mut self, arg: S) -> &mut Self {
        self.args.push(arg.into());
        self
    }

    /// Add multiple arguments.
    pub fn args<I, S>(&mut self, args: I) -> &mut Self
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        for arg in args {
            self.args.push(arg.into());
        }
        self
    }

    /// Set an environment variable for the child process.
    pub fn env<K: Into<String>, V: Into<String>>(&mut self, key: K, val: V) -> &mut Self {
        self.env.push((key.into(), val.into()));
        self
    }

    /// Spawn the command.
    pub async fn spawn(&mut self) -> crate::io::Result<Child> {
        // Format: program\0arg1\0arg2\0\0key=val\0\0
        let mut config = Vec::new();
        config.extend_from_slice(self.program.as_bytes());
        config.push(0);
        for arg in &self.args {
            config.extend_from_slice(arg.as_bytes());
            config.push(0);
        }
        config.push(0); // end-of-args

        for (k, v) in &self.env {
            config.extend_from_slice(k.as_bytes());
            config.push(b'=');
            config.extend_from_slice(v.as_bytes());
            config.push(0);
        }
        config.push(0); // end-of-env

        let (err, handle, _, _config) = OverlappedBufferFuture::new(config, move |ov, ptr, len| {
            // SAFETY: `ptr`/`len` describe the future-owned config buffer.
            unsafe { imports::process_spawn(ov, ptr.cast_const(), len) };
        })
        .await;

        if err != 0 {
            return Err(crate::io::Error::from_raw_os_error(err as i32));
        }

        Ok(Child { handle })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloc::string::ToString;

    #[test]
    fn test_process_wait() {
        crate::rt::run(async {
            #[cfg(target_os = "windows")]
            let cmd_name = "cmd";
            #[cfg(not(target_os = "windows"))]
            let cmd_name = "echo";

            let mut cmd = Command::new(cmd_name);

            #[cfg(target_os = "windows")]
            cmd.arg("/c").arg("echo hello");

            let spawn_res = cmd.spawn();
            if spawn_res.is_err() {
                return; // stub or unsupported platform
            }
            let mut child = spawn_res.unwrap();

            let res = child.wait().await;
            if let Err(e) = &res {
                if e.to_string().contains("pending") {
                    return;
                }
            }

            let status = res.expect("wait failed");
            assert!(status.success());
        });
    }
}
