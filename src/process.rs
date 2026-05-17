//! Process execution and management

#[cfg(not(target_family = "wasm"))]
mod native_process {
    use std::{ffi::OsStr, io, process};

    /// A running child process.
    ///
    /// Created by [`Command::spawn`].
    pub struct Child(Option<process::Child>);

    impl Child {
        /// Wait asynchronously for the child process to exit.
        ///
        /// Consumes the underlying child handle.  Further calls to `wait`
        /// return an error.
        pub async fn wait(&mut self) -> io::Result<process::ExitStatus> {
            let Some(child) = self.0.take() else {
                return Err(io::Error::other("child process already waited"));
            };
            // Run the blocking `Child::wait()` call on a background thread.
            crate::rt::native::spawn_blocking(move || {
                let mut c = child;
                c.wait()
            })?
            .await?
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
use crate::rt::wasm::OverlappedFuture;

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

        let cfg_ptr = config.as_ptr();
        let cfg_len = config.len() as u32;

        let (err, handle, _) = OverlappedFuture::new(move |ov| {
            unsafe { imports::process_spawn(ov, cfg_ptr, cfg_len) };
        })
        .await;

        if err != 0 {
            return Err(std::io::Error::from_raw_os_error(err as i32));
        }

        Ok(Child { handle })
    }
}
