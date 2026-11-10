//! Host-reported system context and change notifications for WASM guests.

use crate::abi::AbiPlatformInfo;

/// Read the host's current platform and system-context snapshot.
#[cfg(target_family = "wasm")]
pub fn get_platform_info() -> crate::io::Result<AbiPlatformInfo> {
    let mut info = AbiPlatformInfo::default();
    let error = unsafe {
        crate::abi::imports::get_platform_info(
            (&mut info as *mut AbiPlatformInfo).cast(),
            core::mem::size_of::<AbiPlatformInfo>() as u32,
        )
    };
    if error != 0 {
        return Err(crate::io::Error::from_raw_os_error(error as i32));
    }
    Ok(info)
}

/// Wait asynchronously until a host-reported system-context field changes.
///
/// Poll this future in a task before reading the initial snapshot, then repeat
/// that order after each completion to avoid missing a change.
#[cfg(target_family = "wasm")]
pub async fn wait_for_change() -> crate::io::Result<()> {
    let (error, _, _) = crate::rt::wasm::OverlappedFuture::new(|overlapped| unsafe {
        crate::abi::imports::system_info_wait(overlapped)
    })
    .await;
    if error != 0 {
        return Err(crate::io::Error::from_raw_os_error(error as i32));
    }
    Ok(())
}
