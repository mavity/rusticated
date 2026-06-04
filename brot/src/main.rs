#![windows_subsystem = "console"]
#![allow(dead_code)]
#![allow(non_snake_case)]

mod decompress;

#[cfg(windows)]
mod win32;

#[cfg(windows)]
fn print_err(msg: &str) {
    unsafe {
        let h_err = crate::win32::Win32::System::Console::GetStdHandle(
            crate::win32::Win32::System::Console::STD_ERROR_HANDLE,
        );
        if h_err != core::ptr::null_mut() && h_err as isize != -1 {
            let mut written = 0;
            crate::win32::Win32::Storage::FileSystem::WriteFile(
                h_err,
                msg.as_ptr() as *const _,
                msg.len() as u32,
                &mut written,
                core::ptr::null_mut(),
            );
        }
    }
}

#[repr(C, packed)]
#[derive(Clone, Copy)]
pub struct MohabbatMeta {
    pub magic: [u8; 8],
    pub pool_len: u64,
    pub washmhost_offset: u64,
    pub washmhost_len: u64,
    pub payload_offset: u64,
    pub payload_len: u64,
    pub reserved: u64,
}

#[cfg_attr(windows, unsafe(link_section = ".mohmeta"))]
#[cfg_attr(
    any(target_os = "linux", target_vendor = "apple"),
    unsafe(link_section = ".mohabbat_meta")
)]
#[used]
pub static mut META: MohabbatMeta = MohabbatMeta {
    magic: *b"MOHABBAT",
    pool_len: 0,
    washmhost_offset: 0,
    washmhost_len: 0,
    payload_offset: 0,
    payload_len: 0,
    reserved: 0,
};

#[cfg(windows)]
mod load_win;

#[cfg(windows)]
mod windows;

#[cfg(target_os = "linux")]
mod linux;

#[cfg(target_os = "macos")]
mod darwin;

fn main() {
    #[cfg(windows)]
    unsafe {
        print_err("brot: starting (windows)\n");
        windows::run()
    }

    #[cfg(target_os = "linux")]
    unsafe {
        linux::run()
    }

    #[cfg(target_os = "macos")]
    unsafe {
        darwin::run()
    }
}
pub mod test_symbols;
