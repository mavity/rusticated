//! TTY management

#[cfg(not(target_family = "wasm"))]
pub use native::*;

#[cfg(target_family = "wasm")]
pub use wasm::*;

#[cfg(not(target_family = "wasm"))]
mod native {
    /// Get terminal size
    pub const fn get_size(_handle: u64) -> crate::io::Result<(u16, u16)> {
        // Implementation for native (likely using crossterm or nix)
        // For now dummy or placeholder if not using full compio-tty
        Ok((80, 24))
    }

    /// Set terminal mode
    pub const fn set_mode(_handle: u64, _mode: u32) -> crate::io::Result<()> {
        Ok(())
    }
}

#[cfg(target_family = "wasm")]
#[allow(clippy::cast_possible_truncation, clippy::unnecessary_wraps)]
mod wasm {
    use crate::abi::imports;

    /// Get terminal size for WASM
    pub fn get_size(handle: u64) -> crate::io::Result<(u16, u16)> {
        // SAFETY: `tty_get_size` is a side-effect-free host import that
        // accepts any handle value.
        let res = unsafe { imports::tty_get_size(handle) };
        let cols = (res >> 16) as u16;
        let rows = (res & 0xFFFF) as u16;
        Ok((cols, rows))
    }

    /// Set terminal mode for WASM
    pub fn set_mode(handle: u64, mode: u32) -> crate::io::Result<()> {
        // SAFETY: `tty_set_mode` is a host import that accepts any handle
        // and mode value.
        unsafe { imports::tty_set_mode(handle, mode) };
        Ok(())
    }
}
