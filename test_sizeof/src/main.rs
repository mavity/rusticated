use windows_sys::Win32::System::SystemServices::{IMAGE_IMPORT_BY_NAME, IMAGE_IMPORT_DESCRIPTOR};
fn main() {
    println!("IMAGE_IMPORT_BY_NAME size: {}", core::mem::size_of::<IMAGE_IMPORT_BY_NAME>());
    println!("IMAGE_IMPORT_DESCRIPTOR size: {}", core::mem::size_of::<IMAGE_IMPORT_DESCRIPTOR>());
}
