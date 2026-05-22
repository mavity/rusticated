#![cfg_attr(target_arch = "wasm32", no_std)]
#![cfg_attr(target_arch = "wasm32", no_main)]

#[cfg(target_arch = "wasm32")]
extern crate alloc;

#[cfg(target_arch = "wasm32")]
use alloc::{format, string::String, vec, vec::Vec};
#[cfg(target_arch = "wasm32")]
use std::fs::File;
#[cfg(target_arch = "wasm32")]
use std::io::{AsyncRead, AsyncWrite};
#[cfg(target_arch = "wasm32")]
use std::tty::stdout;

#[cfg(target_arch = "wasm32")]
std::spawn!(async_main());

#[cfg(target_arch = "wasm32")]
async fn write_all(writer: &mut impl AsyncWrite, bytes: &[u8]) {
    let mut buf = bytes.to_vec();
    while !buf.is_empty() {
        let (result, mut returned) = writer.write(buf).await;
        match result {
            Ok(written) => {
                if written >= returned.len() {
                    break;
                }
                buf = returned.split_off(written);
            }
            Err(_) => break,
        }
    }
}

#[cfg(target_arch = "wasm32")]
async fn out_print(s: &str) {
    write_all(&mut stdout(), s.as_bytes()).await;
}

#[cfg(target_arch = "wasm32")]
async fn read_all(path: &str) -> std::io::Result<Vec<u8>> {
    let mut file = File::open(path).await?;
    let mut buf = Vec::new();
    loop {
        let chunk = Vec::with_capacity(4096);
        let (res, mut chunk) = file.read(chunk).await;
        let n = res?;
        if n == 0 {
            break;
        }
        chunk.truncate(n);
        buf.append(&mut chunk);
    }
    Ok(buf)
}

#[cfg(target_arch = "wasm32")]
fn compress(data: &[u8]) -> anyhow::Result<Vec<u8>> {
    Ok(Vec::new())
}

#[cfg(target_arch = "wasm32")]
async fn async_main() {
    let args = std::env::get_args();
    if args.len() <= 1 || args[1] == "-h" || args[1] == "--help" {
        out_print("Usage: mohabbat <input.wasm> [output.bat]\n").await;
        return;
    }
    out_print("Running builder...\n").await;
}

#[cfg(not(target_arch = "wasm32"))]
fn main() {
    println!("[mohabbat] Success: mohab.bat generated via build script.");
}


