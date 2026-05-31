#[cfg(windows)]
pub fn test() {
    let _ = windows_sys::Win32::Foundation::CloseHandle;
}

#[cfg(not(windows))]
pub fn test() {}
