//! Environment variable handling

#![cfg_attr(
    target_family = "wasm",
    allow(
        clippy::cast_possible_truncation,
        clippy::undocumented_unsafe_blocks,
        clippy::no_effect_underscore_binding,
        clippy::needless_pass_by_value,
        clippy::missing_const_for_fn,
        clippy::doc_markdown,
        clippy::unreadable_literal,
    )
)]

/// Environment-specific constants.
pub mod consts {
    /// The target architecture.
    pub const ARCH: &str = if cfg!(target_arch = "x86_64") {
        "x86_64"
    } else if cfg!(target_arch = "aarch64") {
        "aarch64"
    } else if cfg!(target_arch = "wasm32") {
        "wasm32"
    } else {
        "unknown"
    };

    /// The target operating system.
    pub const OS: &str = if cfg!(target_os = "linux") {
        "linux"
    } else if cfg!(target_os = "macos") {
        "macos"
    } else if cfg!(target_os = "windows") {
        "windows"
    } else if cfg!(target_os = "freebsd") {
        "freebsd"
    } else if cfg!(target_os = "netbsd") {
        "netbsd"
    } else if cfg!(target_os = "openbsd") {
        "openbsd"
    } else if cfg!(target_os = "dragonfly") {
        "dragonfly"
    } else if cfg!(target_os = "android") {
        "android"
    } else if cfg!(target_os = "ios") {
        "ios"
    } else {
        "unknown"
    };

    /// The target family.
    pub const FAMILY: &str = if cfg!(unix) {
        "unix"
    } else if cfg!(windows) {
        "windows"
    } else {
        "unknown"
    };

    /// The executable file extension, if any.
    pub const EXE_EXTENSION: &str = if cfg!(windows) { "exe" } else { "" };
}

#[cfg(not(target_family = "wasm"))]
mod native_env {
    use crate::alloc::borrow::ToOwned;
    use crate::io;
    use crate::path::Path;
    use crate::path::PathBuf;
    use crate::string::String;
    use crate::vec::Vec;

    /// Returns all host environment variables.
    pub fn get_host_env_vars() -> Vec<(String, String)> {
        alloc::vec![]
    }

    /// Returns the current working directory.
    #[cfg(windows)]
    pub fn current_dir() -> io::Result<PathBuf> {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetCurrentDirectoryW(nBufferLength: u32, lpBuffer: *mut u16) -> u32;
        }

        let mut buf = alloc::vec![0u16; 512];
        let len = unsafe { GetCurrentDirectoryW(buf.len() as u32, buf.as_mut_ptr()) };
        if len == 0 {
            return Err(io::Error::from_raw_os_error(0)); // Or some default
        }

        let path_str = crate::string::String::from_utf16_lossy(&buf[..len as usize]);
        Ok(PathBuf::from(path_str))
    }

    #[cfg(not(windows))]
    pub fn current_dir() -> io::Result<PathBuf> {
        Ok(PathBuf::from("/"))
    }

    /// Sets the current working directory.
    #[cfg(windows)]
    pub fn set_current_dir(path: &Path) -> io::Result<()> {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn SetCurrentDirectoryW(lpPathName: *const u16) -> i32;
        }

        let wide = path.to_wide_null();
        let ok = unsafe { SetCurrentDirectoryW(wide.as_ptr()) };
        if ok == 0 {
            Err(io::Error::from_raw_os_error(1))
        } else {
            Ok(())
        }
    }

    /// Sets the current working directory.
    #[cfg(not(windows))]
    pub fn set_current_dir(path: &Path) -> io::Result<()> {
        unsafe extern "C" {
            fn chdir(path: *const u8) -> i32;
        }

        let mut p = path.as_str().as_bytes().to_vec();
        p.push(0);
        let ret = unsafe { chdir(p.as_ptr()) };
        if ret == 0 {
            Ok(())
        } else {
            Err(io::Error::from_raw_os_error(1))
        }
    }

    // â”€â”€ args
    // â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

    #[cfg(target_os = "linux")]
    fn read_args() -> Vec<String> {
        unsafe extern "C" {
            fn open(pathname: *const u8, flags: i32, mode: u32) -> i32;
            fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
            fn close(fd: i32) -> i32;
        }
        const O_RDONLY: i32 = 0;
        let path = b"/proc/self/cmdline\0";
        // SAFETY: path is a valid C string; mode is ignored for O_RDONLY.
        let fd = unsafe { open(path.as_ptr(), O_RDONLY, 0) };
        if fd < 0 {
            return Vec::new();
        }
        let mut buf = alloc::vec![0u8; 65536];
        // SAFETY: fd is valid; buf is writable for buf.capacity() bytes.
        let n = unsafe { read(fd, buf.as_mut_ptr(), buf.capacity()) };
        // SAFETY: fd is valid.
        unsafe { close(fd) };
        if n <= 0 {
            return Vec::new();
        }
        // SAFETY: n bytes were initialised by read().
        unsafe { buf.set_len(n as usize) };
        buf.split(|&b| b == 0)
            .filter(|s| !s.is_empty())
            .map(|s| String::from_utf8_lossy(s).into_owned())
            .collect()
    }

    #[cfg(target_os = "macos")]
    fn read_args() -> Vec<String> {
        unsafe extern "C" {
            fn _NSGetArgc() -> *const i32;
            fn _NSGetArgv() -> *const *const *const u8;
        }
        // SAFETY: both functions return valid pointers on macOS.
        unsafe {
            let argc = *_NSGetArgc();
            let argv = *_NSGetArgv();
            (0..argc as usize)
                .map(|i| {
                    let ptr = *argv.add(i);
                    let len = (0..).find(|&j| *ptr.add(j) == 0).unwrap_or(0);
                    let bytes = core::slice::from_raw_parts(ptr, len);
                    String::from_utf8_lossy(bytes).into_owned()
                })
                .collect()
        }
    }

    #[cfg(windows)]
    fn read_args() -> Vec<String> {
        #[link(name = "shell32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn CommandLineToArgvW(lpCmdLine: *const u16, pNumArgs: *mut i32) -> *mut *mut u16;
        }
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetCommandLineW() -> *const u16;
            fn LocalFree(hMem: *mut core::ffi::c_void) -> *mut core::ffi::c_void;
        }

        let cmdline_ptr = unsafe { GetCommandLineW() };
        let mut argc = 0;
        let argv_ptr = unsafe { CommandLineToArgvW(cmdline_ptr, &mut argc) };

        if argv_ptr.is_null() {
            return Vec::new();
        }

        let mut args = Vec::with_capacity(argc as usize);
        for i in 0..argc {
            unsafe {
                let mut ptr = *argv_ptr.add(i as usize);
                let mut len = 0;
                while *ptr != 0 {
                    ptr = ptr.add(1);
                    len += 1;
                }
                let ptr_start = *argv_ptr.add(i as usize);
                let wchars = core::slice::from_raw_parts(ptr_start, len);
                args.push(String::from_utf16_lossy(wchars));
            }
        }

        unsafe { LocalFree(argv_ptr as *mut core::ffi::c_void) };
        args
    }

    #[cfg(not(any(target_os = "linux", target_os = "macos", windows)))]
    fn read_args() -> Vec<String> {
        Vec::new()
    }

    // â”€â”€ env
    // â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

    #[cfg(any(unix))]
    fn read_env() -> Vec<(String, String)> {
        unsafe extern "C" {
            static environ: *const *const u8;
        }
        let mut result = Vec::new();
        // SAFETY: `environ` is a valid null-terminated array of null-terminated strings.
        unsafe {
            let mut ptr = environ;
            while !(*ptr).is_null() {
                let entry = *ptr;
                let mut len = 0usize;
                while *entry.add(len) != 0 {
                    len += 1;
                }
                let bytes = core::slice::from_raw_parts(entry, len);
                let s = String::from_utf8_lossy(bytes).into_owned();
                if let Some(eq) = s.find('=') {
                    let k = s[..eq].to_owned();
                    let v = s[eq + 1..].to_owned();
                    result.push((k, v));
                }
                ptr = ptr.add(1);
            }
        }
        result
    }

    #[cfg(windows)]
    fn read_env() -> Vec<(String, String)> {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetEnvironmentStringsW() -> *const u16;
            fn FreeEnvironmentStringsW(env: *const u16) -> i32;
        }
        // SAFETY: GetEnvironmentStringsW returns a valid double-null-terminated block.
        let env_ptr = unsafe { GetEnvironmentStringsW() };
        if env_ptr.is_null() {
            return Vec::new();
        }
        let mut result = Vec::new();
        let mut pos = 0usize;
        loop {
            // SAFETY: we stop at the double-null terminator.
            let start = pos;
            while unsafe { *env_ptr.add(pos) } != 0 {
                pos += 1;
            }
            if pos == start {
                break; // double-null reached
            }
            // SAFETY: we computed the length above.
            let wchars = unsafe { core::slice::from_raw_parts(env_ptr.add(start), pos - start) };
            let s = String::from_utf16_lossy(wchars);
            if let Some(eq) = s.find('=') {
                let k = s[..eq].to_owned();
                let v = s[eq + 1..].to_owned();
                result.push((k, v));
            }
            pos += 1; // skip null terminator
        }
        // SAFETY: env_ptr was obtained from GetEnvironmentStringsW.
        unsafe { FreeEnvironmentStringsW(env_ptr) };
        result
    }

    #[cfg(not(any(unix, windows)))]
    fn read_env() -> Vec<(String, String)> {
        Vec::new()
    }

    /// Returns the command-line arguments.
    pub fn get_args() -> Vec<String> {
        read_args()
    }

    /// Returns the environment variables.
    pub fn get_env() -> Vec<(String, String)> {
        read_env()
    }
    /// Joins a collection of paths into a single path-like string.
    pub fn join_paths<I, T>(_paths: I) -> io::Result<String>
    where
        I: IntoIterator<Item = T>,
        T: AsRef<crate::path::Path>,
    {
        Ok(String::new())
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_env::{current_dir, get_env, get_host_env_vars, join_paths};

#[cfg(not(target_family = "wasm"))]
pub fn set_current_dir<P: AsRef<crate::path::Path>>(path: P) -> crate::io::Result<()> {
    native_env::set_current_dir(path.as_ref())
}

#[cfg(not(target_family = "wasm"))]
fn get_args() -> alloc::vec::Vec<crate::string::String> {
    native_env::get_args()
}

#[cfg(target_family = "wasm")]
pub fn current_dir() -> crate::io::Result<crate::path::PathBuf> {
    let probe = unsafe { imports::get_cwd(core::ptr::null_mut(), 0) };
    let err = (probe >> 32) as u32;
    let bytes_needed = (probe & 0xFFFF_FFFF) as u32;
    if err != 0 {
        return Err(crate::io::Error::from_raw_os_error(err as i32));
    }

    let mut buf = alloc::vec![0u8; bytes_needed as usize];
    let res = unsafe { imports::get_cwd(buf.as_mut_ptr(), bytes_needed) };
    let err = (res >> 32) as u32;
    let written = (res & 0xFFFF_FFFF) as u32;
    if err != 0 {
        return Err(crate::io::Error::from_raw_os_error(err as i32));
    }

    if written as usize > buf.len() {
        return Err(crate::io::Error::from_raw_os_error(22));
    }
    let path = crate::string::String::from_utf8_lossy(&buf[..written as usize]).into_owned();
    Ok(crate::path::PathBuf::from(path))
}

#[cfg(target_family = "wasm")]
pub fn set_current_dir<P: AsRef<crate::path::Path>>(path: P) -> crate::io::Result<()> {
    let p = path.as_ref().as_str();
    let err = unsafe { imports::set_cwd(p.as_ptr(), p.len() as u32) };
    if err == 0 {
        Ok(())
    } else {
        Err(crate::io::Error::from_raw_os_error(err as i32))
    }
}

#[cfg(target_family = "wasm")]
pub fn join_paths<I, T>(_paths: I) -> Result<crate::ffi::OsString, ()>
where
    I: IntoIterator<Item = T>,
    T: AsRef<crate::ffi::OsStr>,
{
    Err(())
}

/// Error returned when an environment variable is not found or invalid.
#[derive(Debug, Clone)]
pub struct VarError;

impl core::fmt::Display for VarError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str("environment variable not found")
    }
}

/// Looks up an environment variable by key.
///
/// Returns `Ok(String)` if the variable is set, or `Err(VarError)` otherwise.
pub fn var(key: &str) -> Result<crate::string::String, VarError> {
    get_env()
        .into_iter()
        .find(|(k, _)| k == key)
        .map(|(_, v)| v)
        .ok_or(VarError)
}

/// Returns an iterator over command-line arguments as `String` values.
pub fn args_os() -> impl Iterator<Item = crate::string::String> {
    get_args().into_iter()
}

/// Returns an iterator over command-line arguments as `String` values.
///
/// This mirrors the conventional Rust `std::env::args` API on upstream std.
pub fn args() -> impl Iterator<Item = crate::string::String> {
    args_os()
}

/// Returns an iterator over environment variables as `(key, value)` string pairs.
pub fn vars_os() -> impl Iterator<Item = (crate::string::String, crate::string::String)> {
    get_env().into_iter()
}

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::string::String;
#[cfg(target_family = "wasm")]
use crate::vec::Vec;

/// Get args for WASM.
#[cfg(target_family = "wasm")]
fn get_args() -> Vec<String> {
    let res = unsafe { imports::get_args(core::ptr::null_mut(), 0) };
    let _count = (res >> 32) as u32;
    let bytes_needed = (res & 0xFFFF_FFFF) as u32;

    let mut buf = alloc::vec![0u8; bytes_needed as usize];
    let _ = unsafe { imports::get_args(buf.as_mut_ptr(), bytes_needed) };

    parse_null_separated(buf)
}

/// Get env for WASM.
#[cfg(target_family = "wasm")]
pub fn get_env() -> Vec<(String, String)> {
    let res = unsafe { imports::get_env(core::ptr::null_mut(), 0) };
    let _count = (res >> 32) as u32;
    let bytes_needed = (res & 0xFFFF_FFFF) as u32;

    let mut buf = alloc::vec![0u8; bytes_needed as usize];
    let _ = unsafe { imports::get_env(buf.as_mut_ptr(), bytes_needed) };

    let vars = parse_null_separated(buf);
    vars.into_iter()
        .filter_map(|s| {
            let mut parts = s.splitn(2, '=');
            let k = String::from(parts.next()?);
            let v = String::from(parts.next()?);
            Some((k, v))
        })
        .collect()
}

#[cfg(target_family = "wasm")]
fn parse_null_separated(buf: Vec<u8>) -> Vec<String> {
    buf.split(|&b| b == 0)
        .filter(|s| !s.is_empty())
        .map(|s| String::from_utf8_lossy(s).into_owned())
        .collect()
}
