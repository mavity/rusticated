//! Process execution and management

#![cfg_attr(
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
    use std::{ffi::OsStr, io, process};

    // ─── Linux pidfd async wait ──────────────────────────────────────────────

    /// Linux syscall number for `pidfd_open` on x86_64 / aarch64 / riscv64.
    #[cfg(all(
        target_os = "linux",
        any(target_arch = "x86_64", target_arch = "aarch64")
    ))]
    const SYS_PIDFD_OPEN: i64 = 434;

    #[cfg(target_os = "linux")]
    extern "C" {
        fn syscall(num: i64, ...) -> i64;
        fn close(fd: std::os::raw::c_int) -> std::os::raw::c_int;
    }

    /// Open a pidfd referring to the given pid.
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

    /// A running child process.
    ///
    /// Created by [`Command::spawn`].
    pub struct Child(Option<process::Child>);

    impl Child {
        /// Wait asynchronously for the child process to exit.
        ///
        /// On Linux this opens a `pidfd` and awaits readability through the
        /// runtime's epoll driver — no thread, no polling.
        ///
        /// Consumes the underlying child handle. Further calls to `wait`
        /// return an error.
        #[cfg_attr(
            not(all(
                target_os = "linux",
                any(target_arch = "x86_64", target_arch = "aarch64")
            )),
            allow(clippy::unused_async)
        )]
        pub async fn wait(&mut self) -> io::Result<process::ExitStatus> {
            #[cfg(all(
                target_os = "linux",
                any(target_arch = "x86_64", target_arch = "aarch64")
            ))]
            {
                let Some(mut child) = self.0.take() else {
                    return Err(io::Error::other("child process already waited"));
                };
                let pid = child.id();
                let pidfd = pidfd_open(pid)?;
                let res = crate::rt::wait_readable(pidfd).await;
                // SAFETY: pidfd was obtained from `pidfd_open` and is owned by us.
                unsafe { close(pidfd) };
                res?;
                // After the pidfd reports readable, `wait()` returns immediately.
                child.wait()
            }

            #[cfg(not(all(
                target_os = "linux",
                any(target_arch = "x86_64", target_arch = "aarch64")
            )))]
            {
                let Some(_child) = self.0.take() else {
                    return Err(io::Error::other("child process already waited"));
                };
                Err(io::Error::other(
                    "Child::wait: async backend pending on this platform",
                ))
            }
        }

        /// Non-blocking poll to check if the child has exited.
        pub fn try_wait(&mut self) -> io::Result<Option<process::ExitStatus>> {
            self.0.as_mut().map_or(Ok(None), |child| child.try_wait())
        }

        /// Send a kill signal to the child process.
        pub fn kill(&mut self) -> io::Result<()> {
            self.0.as_mut().map_or_else(
                || Err(io::Error::other("child process already waited")),
                |child| child.kill(),
            )
        }
    }

    /// A builder for spawning child processes.
    ///
    /// Wraps [`std::process::Command`] to add an async-friendly `spawn`.
    pub struct Command(process::Command);

    impl Command {
        /// Create a new command for `program`.
        pub fn new<S: AsRef<OsStr>>(program: S) -> Self {
            Self(process::Command::new(program))
        }

        /// Append a single argument.
        pub fn arg<S: AsRef<OsStr>>(&mut self, arg: S) -> &mut Self {
            self.0.arg(arg);
            self
        }

        /// Append multiple arguments.
        pub fn args<I, S>(&mut self, args: I) -> &mut Self
        where
            I: IntoIterator<Item = S>,
            S: AsRef<OsStr>,
        {
            self.0.args(args);
            self
        }

        /// Set an environment variable for the child process.
        pub fn env<K, V>(&mut self, key: K, val: V) -> &mut Self
        where
            K: AsRef<OsStr>,
            V: AsRef<OsStr>,
        {
            self.0.env(key, val);
            self
        }

        /// Configure stdin for the child process.
        pub fn stdin<T: Into<process::Stdio>>(&mut self, cfg: T) -> &mut Self {
            self.0.stdin(cfg);
            self
        }

        /// Configure stdout for the child process.
        pub fn stdout<T: Into<process::Stdio>>(&mut self, cfg: T) -> &mut Self {
            self.0.stdout(cfg);
            self
        }

        /// Configure stderr for the child process.
        pub fn stderr<T: Into<process::Stdio>>(&mut self, cfg: T) -> &mut Self {
            self.0.stderr(cfg);
            self
        }

        /// Spawn the command as a child process.
        ///
        /// The underlying `fork`/`exec` (or `CreateProcess` on Windows) is
        /// synchronous but completes almost immediately.
        pub fn spawn(&mut self) -> io::Result<Child> {
            self.0.spawn().map(|c| Child(Some(c)))
        }
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_process::{Child, Command};
#[cfg(not(target_family = "wasm"))]
pub use std::process::{ExitStatus as ChildExitStatus, Stdio};

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::{OverlappedBufferFuture, OverlappedFuture};

#[cfg(target_family = "wasm")]
/// WASM Child process
pub struct Child {
    handle: u64,
}

#[cfg(target_family = "wasm")]
impl Child {
    /// Wait for the child to exit
    pub async fn wait(&mut self) -> std::io::Result<ChildExitStatus> {
        let (err, result, _) = OverlappedFuture::new({
            let handle = self.handle;
            move |ov| {
                unsafe { imports::process_wait(ov, handle) };
            }
        })
        .await;

        if err != 0 {
            return Err(std::io::Error::from_raw_os_error(err as i32));
        }

        let exit_code = (result >> 32) as i32;
        let success = exit_code == 0;

        Ok(ChildExitStatus { exit_code, success })
    }
}

#[cfg(target_family = "wasm")]
/// WASM ChildExitStatus
pub struct ChildExitStatus {
    exit_code: i32,
    success: bool,
}

#[cfg(target_family = "wasm")]
impl ChildExitStatus {
    /// Returns the exit code
    pub fn code(&self) -> Option<i32> {
        Some(self.exit_code)
    }

    /// Returns true if success
    pub fn success(&self) -> bool {
        self.success
    }
}

#[cfg(target_family = "wasm")]
/// WASM Stdio
pub enum Stdio {
    /// Inherit
    Inherit,
    /// Null
    Null,
    /// Pipe
    Piped,
}

#[cfg(target_family = "wasm")]
/// WASM Command
pub struct Command {
    program: String,
    args: Vec<String>,
    env: Vec<(String, String)>,
}

#[cfg(target_family = "wasm")]
impl Command {
    /// Create a new command
    pub fn new<S: Into<String>>(program: S) -> Self {
        Self {
            program: program.into(),
            args: Vec::new(),
            env: Vec::new(),
        }
    }

    /// Add an argument
    pub fn arg<S: Into<String>>(&mut self, arg: S) -> &mut Self {
        self.args.push(arg.into());
        self
    }

    /// Add multiple arguments
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

    /// Spawn the command
    pub async fn spawn(&mut self) -> std::io::Result<Child> {
        // Build a linear buffer for SpawnConfig
        // Format: program\0arg1\0arg2\0\0env1=val1\0env2=val2\0\0
        let mut config = Vec::new();
        config.extend_from_slice(self.program.as_bytes());
        config.push(0);
        for arg in &self.args {
            config.extend_from_slice(arg.as_bytes());
            config.push(0);
        }
        config.push(0); // End of args

        for (k, v) in &self.env {
            config.extend_from_slice(k.as_bytes());
            config.push(b'=');
            config.extend_from_slice(v.as_bytes());
            config.push(0);
        }
        config.push(0); // End of env

        let (err, handle, _, _config) = OverlappedBufferFuture::new(config, move |ov, ptr, len| {
            // SAFETY: `ptr`/`len` describe the future-owned config buffer;
            // the completion registry's `Rc` clone keeps it alive across
            // any cancellation.
            unsafe { imports::process_spawn(ov, ptr.cast_const(), len) };
        })
        .await;

        if err != 0 {
            return Err(std::io::Error::from_raw_os_error(err as i32));
        }

        Ok(Child { handle })
    }
}
