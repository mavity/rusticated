#[cfg(windows)]
pub fn test() {
    let _ = crate::win32::Win32::Foundation::CloseHandle;
}

#[cfg(not(windows))]
pub fn test() {}
