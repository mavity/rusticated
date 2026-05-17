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

#[cfg(not(target_family = "wasm"))]
/// Get args for native
pub fn get_args() -> Vec<String> {
    std::env::args().collect()
}

#[cfg(not(target_family = "wasm"))]
/// Get env for native
pub fn get_env() -> Vec<(String, String)> {
    std::env::vars().collect()
}

#[cfg(target_family = "wasm")]
use crate::abi::imports;

#[cfg(target_family = "wasm")]
/// Get args for WASM
pub fn get_args() -> Vec<String> {
    let res = unsafe { imports::get_args(std::ptr::null_mut(), 0) };
    let _count = (res >> 32) as u32;
    let bytes_needed = (res & 0xFFFFFFFF) as u32;

    let mut buf = vec![0u8; bytes_needed as usize];
    let _ = unsafe { imports::get_args(buf.as_mut_ptr(), bytes_needed) };

    parse_null_separated(buf)
}

#[cfg(target_family = "wasm")]
/// Get env for WASM
pub fn get_env() -> Vec<(String, String)> {
    let res = unsafe { imports::get_env(std::ptr::null_mut(), 0) };
    let _count = (res >> 32) as u32;
    let bytes_needed = (res & 0xFFFFFFFF) as u32;

    let mut buf = vec![0u8; bytes_needed as usize];
    let _ = unsafe { imports::get_env(buf.as_mut_ptr(), bytes_needed) };

    let vars = parse_null_separated(buf);
    vars.into_iter()
        .filter_map(|s| {
            let mut parts = s.splitn(2, '=');
            let k = parts.next()?.to_string();
            let v = parts.next()?.to_string();
            Some((k, v))
        })
        .collect()
}

#[cfg(target_family = "wasm")]
fn parse_null_separated(buf: Vec<u8>) -> Vec<String> {
    buf.split(|&b| b == 0)
        .filter(|s| !s.is_empty())
        .map(|s| String::from_utf8_lossy(s).to_string())
        .collect()
}
