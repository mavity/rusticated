mod logic;

use anyhow::Context;
use std::fs;
use std::io::{Cursor, Read, Write};
use std::path::{Path, PathBuf};

fn compress(data: &[u8]) -> anyhow::Result<Vec<u8>> {
    let mut result = Vec::new();
    let mut params = brotli::enc::backward_references::BrotliEncoderParams::default();
    params.quality = 9;
    brotli::BrotliCompress(&mut Cursor::new(data), &mut result, &params)
        .map_err(|e| anyhow::anyhow!("Brotli error: {:?}", e))?;
    Ok(result)
}

fn main() -> anyhow::Result<()> {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 2 {
        println!("Usage: mohabbat <input.wasm> [output.exe]");
        return Ok(());
    }

    let input_wasm_path = &args[1];
    let output_exe_path = args.get(2).map(|s| s.as_str()).unwrap_or("rusticated.exe");

    let workspace_dir = Path::new(env!("CARGO_MANIFEST_DIR")).parent().unwrap();
    let target_tree = workspace_dir.join("target").join("tree");

    // Attempt to find built components in known locations
    let mut brot_path = None;
    let mut washmhost_path = None;

    let targets = [
        "aarch64-pc-windows-msvc",
        "x86_64-pc-windows-msvc",
        "x86_64-unknown-linux-gnu",
    ];
    for t in targets {
        let bp = target_tree.join(t).join("release").join("brot.exe");
        let wp = target_tree.join(t).join("release").join("washmhost.exe");
        if bp.exists() && wp.exists() {
            brot_path = Some(bp);
            washmhost_path = Some(wp);
            break;
        }
    }

    let brot_path = brot_path.context("Failed to find brot.exe in target/tree (not built?)")?;
    let washmhost_path =
        washmhost_path.context("Failed to find washmhost.exe in target/tree (not built?)")?;

    println!("[mohabbat] Reading components...");
    let brot_data = fs::read(&brot_path).context("Failed to read brot.exe (not built?)")?;
    let washmhost_data =
        fs::read(&washmhost_path).context("Failed to read washmhost.exe (not built?)")?;
    let wasm_data = fs::read(input_wasm_path).context("Failed to read input wasm")?;

    println!("[mohabbat] Compressing payload...");
    let c_washmhost = compress(&washmhost_data)?;
    let c_wasm = compress(&wasm_data)?;

    println!("[mohabbat] Stitching...");

    let mut final_data = brot_data.clone();
    let washmhost_offset = final_data.len() as u64;
    final_data.extend_from_slice(&c_washmhost);

    let payload_offset = final_data.len() as u64;
    final_data.extend_from_slice(&c_wasm);

    let pool_len = (final_data.len() - brot_data.len()) as u64;

    // Patch meta in final_data
    let mut meta = logic::MohabbatMeta {
        magic: *b"MOHABBAT",
        pool_len,
        washmhost_offset,
        washmhost_len: c_washmhost.len() as u64,
        payload_offset,
        payload_len: c_wasm.len() as u64,
        reserved: 0,
    };

    logic::patch_meta_buf(&mut final_data, &meta)?;

    // Write final output
    fs::write(output_exe_path, &final_data)?;
    println!("[mohabbat] Success: {} generated", output_exe_path);

    Ok(())
}
