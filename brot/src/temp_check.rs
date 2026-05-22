pub fn check() {
    #[cfg(windows)]
    let _ = windows_sys::Win32::Storage::FileSystem::ReadFile;
}
