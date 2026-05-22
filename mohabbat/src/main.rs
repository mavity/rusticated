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
#[unsafe(no_mangle)]
pub unsafe extern "Rust" fn guest_init() {
    std::rt::submit_main(async_main());
}

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
async fn err_print(s: &str) {
    use std::tty::stderr;
    write_all(&mut stderr(), s.as_bytes()).await;
}

#[cfg(target_arch = "wasm32")]
async fn read_all(path: &str) -> std::io::Result<Vec<u8>> {
    let mut file = File::open(path).await?;
    let mut buf = Vec::new();
    loop {
        let chunk = Vec::with_capacity(65536);
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
async fn write_file_all(file: &mut File, data: &[u8]) -> anyhow::Result<()> {
    let mut offset = 0;
    while offset < data.len() {
        let chunk_size = (data.len() - offset).min(65536);
        let chunk = data[offset..offset + chunk_size].to_vec();
        let (res, _) = file.write(chunk).await;
        let n = res.map_err(|e| anyhow::anyhow!("Write error: {}", e))?;
        if n == 0 {
            return Err(anyhow::anyhow!("Write returned 0 bytes"));
        }
        offset += n;
    }
    Ok(())
}

// Brotli decompression (no_std compatible via brotli-decompressor)
#[cfg(target_arch = "wasm32")]
fn decompress(input: &[u8]) -> Result<Vec<u8>, &'static str> {
    use alloc_no_stdlib::{Allocator, SliceWrapper, SliceWrapperMut};
    use brotli_decompressor::{BrotliDecompressStream, BrotliResult, HuffmanCode};

    struct Rebox<T> {
        b: alloc::boxed::Box<[T]>,
    }
    impl<T> Default for Rebox<T> {
        fn default() -> Self {
            Rebox {
                b: Vec::new().into_boxed_slice(),
            }
        }
    }
    impl<T> SliceWrapper<T> for Rebox<T> {
        fn slice(&self) -> &[T] {
            &self.b
        }
    }
    impl<T> SliceWrapperMut<T> for Rebox<T> {
        fn slice_mut(&mut self) -> &mut [T] {
            &mut self.b
        }
    }
    #[derive(Default, Clone, Copy)]
    struct HeapAllocator;
    impl Allocator<u8> for HeapAllocator {
        type AllocatedMemory = Rebox<u8>;
        fn alloc_cell(&mut self, size: usize) -> Rebox<u8> {
            Rebox {
                b: vec![0u8; size].into_boxed_slice(),
            }
        }
        fn free_cell(&mut self, _: Rebox<u8>) {}
    }
    impl Allocator<u32> for HeapAllocator {
        type AllocatedMemory = Rebox<u32>;
        fn alloc_cell(&mut self, size: usize) -> Rebox<u32> {
            Rebox {
                b: vec![0u32; size].into_boxed_slice(),
            }
        }
        fn free_cell(&mut self, _: Rebox<u32>) {}
    }
    impl Allocator<HuffmanCode> for HeapAllocator {
        type AllocatedMemory = Rebox<HuffmanCode>;
        fn alloc_cell(&mut self, size: usize) -> Rebox<HuffmanCode> {
            Rebox {
                b: vec![HuffmanCode::default(); size].into_boxed_slice(),
            }
        }
        fn free_cell(&mut self, _: Rebox<HuffmanCode>) {}
    }

    let mut state = alloc::boxed::Box::new(brotli_decompressor::BrotliState::new(
        HeapAllocator,
        HeapAllocator,
        HeapAllocator,
    ));
    let mut result_data = Vec::new();
    let mut available_in = input.len();
    let mut input_offset = 0;
    let mut output_buf = vec![0u8; 65536];

    loop {
        let mut available_out = output_buf.len();
        let mut output_offset = 0;
        let result = BrotliDecompressStream(
            &mut available_in,
            &mut input_offset,
            input,
            &mut available_out,
            &mut output_offset,
            &mut output_buf,
            &mut 0,
            &mut *state,
        );
        if output_offset > 0 {
            result_data.extend_from_slice(&output_buf[..output_offset]);
        }
        match result {
            BrotliResult::ResultSuccess => return Ok(result_data),
            BrotliResult::ResultFailure => return Err("brotli decompression failed"),
            BrotliResult::NeedsMoreInput => {
                if available_in == 0 {
                    return Err("brotli: unexpected end of input");
                }
            }
            BrotliResult::NeedsMoreOutput => {}
        }
    }
}

// Brotli compression (quality=1 for speed, no-std via BrotliCompressCustomIo)
#[cfg(target_arch = "wasm32")]
fn compress(data: &[u8]) -> anyhow::Result<Vec<u8>> {
    use brotli::enc::BrotliAlloc;
    use brotli::enc::backward_references::BrotliEncoderParams;
    use brotli::{
        Allocator, BrotliCompressCustomIo, CustomRead, CustomWrite, SliceWrapper, SliceWrapperMut,
    };

    // Box-backed memory cell (satisfies AllocatedSlice<T> via blanket impl)
    struct CBox<T> {
        b: alloc::boxed::Box<[T]>,
    }
    impl<T> Default for CBox<T> {
        fn default() -> Self {
            CBox {
                b: Vec::new().into_boxed_slice(),
            }
        }
    }
    impl<T> SliceWrapper<T> for CBox<T> {
        fn slice(&self) -> &[T] {
            &self.b
        }
    }
    impl<T> SliceWrapperMut<T> for CBox<T> {
        fn slice_mut(&mut self) -> &mut [T] {
            &mut self.b
        }
    }

    // Blanket allocator: works for every T: Default + Clone (covers all BrotliAlloc bounds)
    struct CAlloc;
    impl<T: Default + Clone> Allocator<T> for CAlloc {
        type AllocatedMemory = CBox<T>;
        fn alloc_cell(&mut self, len: usize) -> CBox<T> {
            CBox {
                b: vec![T::default(); len].into_boxed_slice(),
            }
        }
        fn free_cell(&mut self, _: CBox<T>) {}
    }
    impl BrotliAlloc for CAlloc {}

    struct SliceReader<'a> {
        data: &'a [u8],
        pos: usize,
    }
    impl<'a> CustomRead<&'static str> for SliceReader<'a> {
        fn read(&mut self, buf: &mut [u8]) -> Result<usize, &'static str> {
            let n = (self.data.len() - self.pos).min(buf.len());
            buf[..n].copy_from_slice(&self.data[self.pos..self.pos + n]);
            self.pos += n;
            Ok(n)
        }
    }

    struct VecWriter {
        vec: Vec<u8>,
    }
    impl CustomWrite<&'static str> for VecWriter {
        fn write(&mut self, buf: &[u8]) -> Result<usize, &'static str> {
            self.vec.extend_from_slice(buf);
            Ok(buf.len())
        }
        fn flush(&mut self) -> Result<(), &'static str> {
            Ok(())
        }
    }

    let mut input_buf = vec![0u8; 4096];
    let mut output_buf = vec![0u8; 4096];
    let mut params = BrotliEncoderParams::default();
    params.quality = 1;

    let mut reader = SliceReader { data, pos: 0 };
    let mut writer = VecWriter { vec: Vec::new() };

    BrotliCompressCustomIo(
        &mut reader,
        &mut writer,
        &mut input_buf,
        &mut output_buf,
        &params,
        CAlloc,
        &mut |_, _, _, _| {},
        "unexpected eof",
    )
    .map_err(|e| anyhow::anyhow!("brotli compress failed: {}", e))?;

    Ok(writer.vec)
}

// Find the first occurrence of needle in haystack
#[cfg(target_arch = "wasm32")]
fn find_subsequence(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack.windows(needle.len()).position(|w| w == needle)
}

// Parse the first MohabbatMeta found in buf.
// After "MOHABBAT" magic: pool_len(8), washmhost_offset(8), washmhost_len(8),
//                         payload_offset(8), payload_len(8), reserved(8)
#[cfg(target_arch = "wasm32")]
fn find_meta(buf: &[u8]) -> Option<(u64, u64, u64, u64, u64)> {
    let magic = b"MOHABBAT";
    for i in 0..buf.len().saturating_sub(magic.len() + 40) {
        if &buf[i..i + magic.len()] == magic {
            let p = i + magic.len();
            let pool_len = u64::from_le_bytes(buf[p..p + 8].try_into().ok()?);
            let washmhost_offset = u64::from_le_bytes(buf[p + 8..p + 16].try_into().ok()?);
            let washmhost_len = u64::from_le_bytes(buf[p + 16..p + 24].try_into().ok()?);
            let payload_offset = u64::from_le_bytes(buf[p + 24..p + 32].try_into().ok()?);
            let payload_len = u64::from_le_bytes(buf[p + 32..p + 40].try_into().ok()?);
            // Sanity check: pool_len must be non-zero.
            // Note: payload_offset is a decompressed offset, pool_len is the compressed
            // pool size, so they cannot be directly compared.
            if pool_len > 0 {
                return Some((
                    pool_len,
                    washmhost_offset,
                    washmhost_len,
                    payload_offset,
                    payload_len,
                ));
            }
        }
    }
    None
}

// Patch all MOHABBAT metas in buf with new pool_len and payload_len.
// washmhost_offset, washmhost_len and payload_offset are left unchanged.
#[cfg(target_arch = "wasm32")]
fn patch_metas(buf: &mut [u8], new_pool_len: u64, new_payload_len: u64) {
    let magic = b"MOHABBAT";
    let mut i = 0;
    while i + magic.len() + 48 <= buf.len() {
        if &buf[i..i + magic.len()] == magic {
            let p = i + magic.len();
            buf[p..p + 8].copy_from_slice(&new_pool_len.to_le_bytes());
            // washmhost_offset at p+8..p+16 — unchanged
            // washmhost_len   at p+16..p+24 — unchanged
            // payload_offset  at p+24..p+32 — unchanged
            buf[p + 32..p + 40].copy_from_slice(&new_payload_len.to_le_bytes());
            i += magic.len() + 48;
        } else {
            i += 1;
        }
    }
}

// Extract package name from raw Cargo.toml text
#[cfg(target_arch = "wasm32")]
fn extract_package_name(toml: &str) -> Option<String> {
    // Strip UTF-8 BOM if present (some editors write it)
    let toml = toml.trim_start_matches('\u{FEFF}');
    let mut in_package = false;
    for line in toml.lines() {
        let trimmed = line.trim();
        if trimmed == "[package]" {
            in_package = true;
            continue;
        }
        if trimmed.starts_with('[') {
            in_package = false;
        }
        if in_package && trimmed.starts_with("name") {
            if let Some(eq) = trimmed.find('=') {
                let val = trimmed[eq + 1..]
                    .trim()
                    .trim_matches('"')
                    .trim_matches('\'');
                if !val.is_empty() {
                    return Some(val.into());
                }
            }
        }
    }
    None
}

// Build a cargo project and return the path to the compiled .wasm file.
// Uses the workspace root derived from self_path (parent dir of mohab.bat).
#[cfg(target_arch = "wasm32")]
async fn build_project(project_dir: &str, self_path: &str) -> anyhow::Result<String> {
    // Derive workspace root from mohab.bat path
    let workspace_root: String = {
        let sep = self_path.rfind('/').or_else(|| self_path.rfind('\\'));
        match sep {
            Some(i) if i > 0 => self_path[..i].into(),
            _ => ".".into(),
        }
    };

    let cargo_toml_path = if project_dir.ends_with("Cargo.toml") {
        project_dir.into()
    } else {
        format!(
            "{}/Cargo.toml",
            project_dir.trim_end_matches('/').trim_end_matches('\\')
        )
    };

    let toml_data = read_all(&cargo_toml_path)
        .await
        .map_err(|e| anyhow::anyhow!("Cannot read {}: {}", cargo_toml_path, e))?;
    let toml_str = core::str::from_utf8(&toml_data)
        .map_err(|_| anyhow::anyhow!("Cargo.toml is not valid UTF-8"))?;
    let package_name = extract_package_name(toml_str)
        .ok_or_else(|| anyhow::anyhow!("Cannot find package name in Cargo.toml"))?;

    out_print(&format!("[mohabbat] Building package: {}\n", package_name)).await;

    let sysroot = format!("{}/target/sysroot-wasm32-unknown-unknown", workspace_root);
    let cargo_target_dir = format!("{}/target/tree", workspace_root);
    let rustflags = format!("--sysroot {}", sysroot);

    let mut cmd = std::process::Command::new("cargo");
    cmd.arg("build")
        .arg("--manifest-path")
        .arg(&cargo_toml_path)
        .arg("--release")
        .arg("--target")
        .arg("wasm32-unknown-unknown")
        .env("CARGO_TARGET_WASM32_UNKNOWN_UNKNOWN_RUSTFLAGS", &rustflags)
        .env("CARGO_TARGET_DIR", &cargo_target_dir);

    let mut child = cmd
        .spawn()
        .await
        .map_err(|e| anyhow::anyhow!("Failed to spawn cargo: {}", e))?;
    let status = child
        .wait()
        .await
        .map_err(|e| anyhow::anyhow!("cargo wait failed: {}", e))?;

    if !status.success() {
        return Err(anyhow::anyhow!(
            "cargo build failed (exit code {:?})",
            status.code()
        ));
    }

    // Locate the compiled wasm: binary name preserves hyphens (cargo output for [[bin]] targets)
    Ok(format!(
        "{}/wasm32-unknown-unknown/release/{}.wasm",
        cargo_target_dir, package_name
    ))
}

// The "juice bottle refill": read self (mohab.bat), swap payload WASM, write new vegetable.
#[cfg(target_arch = "wasm32")]
async fn juice_bottle_refill(
    self_path: &str,
    new_wasm_path: &str,
    output_path: &str,
) -> anyhow::Result<()> {
    out_print("[mohabbat] Reading self...\n").await;
    let self_data = read_all(self_path)
        .await
        .map_err(|e| anyhow::anyhow!("Cannot read self {}: {}", self_path, e))?;

    out_print("[mohabbat] Reading new payload...\n").await;
    let new_payload = read_all(new_wasm_path)
        .await
        .map_err(|e| anyhow::anyhow!("Cannot read wasm {}: {}", new_wasm_path, e))?;

    // Locate Zone A end: search for the sentinel that terminates the script header
    let sentinel = b"exit /b !RET!\r\n";
    let zone_a_end = find_subsequence(&self_data, sentinel)
        .map(|pos| pos + sentinel.len())
        .ok_or_else(|| anyhow::anyhow!("Cannot find Zone A end marker in self"))?;

    // Read pool_len and payload metadata from any brot in Zone B
    let (pool_len, _, _, payload_offset, _) = find_meta(&self_data[zone_a_end..])
        .ok_or_else(|| anyhow::anyhow!("Cannot find MOHABBAT meta in self"))?;

    let file_len = self_data.len();
    let pool_start = file_len
        .checked_sub(pool_len as usize)
        .ok_or_else(|| anyhow::anyhow!("pool_len {} > file_len {}", pool_len, file_len))?;

    if pool_start < zone_a_end {
        return Err(anyhow::anyhow!(
            "pool_start {} < zone_a_end {}: corrupted layout",
            pool_start,
            zone_a_end
        ));
    }

    // Decompress Zone C → pool_raw
    out_print("[mohabbat] Decompressing pool...\n").await;
    let pool_raw = decompress(&self_data[pool_start..])
        .map_err(|e| anyhow::anyhow!("Decompression failed: {}", e))?;

    // Extract washmhosts section (pool_raw[0..payload_offset])
    let washmhosts_raw = pool_raw.get(..payload_offset as usize).ok_or_else(|| {
        anyhow::anyhow!(
            "payload_offset {} > pool_raw.len() {}",
            payload_offset,
            pool_raw.len()
        )
    })?;

    // Build new pool: washmhosts unchanged + new payload
    let mut new_pool_raw = washmhosts_raw.to_vec();
    new_pool_raw.extend_from_slice(&new_payload);

    // Compress new pool
    out_print("[mohabbat] Compressing new pool...\n").await;
    let new_pool_compressed =
        compress(&new_pool_raw).map_err(|e| anyhow::anyhow!("Compression failed: {}", e))?;

    // Clone Zone B (brots) and patch all MOHABBAT metas
    let mut new_zone_b = self_data[zone_a_end..pool_start].to_vec();
    patch_metas(
        &mut new_zone_b,
        new_pool_compressed.len() as u64,
        new_payload.len() as u64,
    );

    // Write output vegetable: Zone A (unchanged) + Zone B (patched) + Zone C (new)
    out_print("[mohabbat] Writing output...\n").await;
    let mut out_file = File::create(output_path)
        .await
        .map_err(|e| anyhow::anyhow!("Cannot create {}: {}", output_path, e))?;

    write_file_all(&mut out_file, &self_data[..zone_a_end]).await?;
    write_file_all(&mut out_file, &new_zone_b).await?;
    write_file_all(&mut out_file, &new_pool_compressed).await?;

    Ok(())
}

#[cfg(target_arch = "wasm32")]
async fn async_main() {
    let args = std::env::get_args();
    if args.len() <= 1 || args[1] == "-h" || args[1] == "--help" {
        out_print("Usage: mohab.bat <input.wasm|project_dir> -o <output.bat>\n").await;
        return;
    }

    let self_path = &args[0];
    let input = &args[1];

    let mut output_path: Option<String> = None;
    let mut i = 2;
    while i < args.len() {
        if args[i] == "-o" && i + 1 < args.len() {
            output_path = Some(args[i + 1].clone());
            i += 2;
        } else {
            i += 1;
        }
    }

    let output = match output_path {
        Some(p) => p,
        None => {
            err_print("Error: no output path specified (use -o <output.bat>)\n").await;
            return;
        }
    };

    // Determine wasm path: either direct .wasm file or build from project dir
    let wasm_path: String = if input.ends_with(".wasm") {
        input.clone()
    } else {
        match build_project(input, self_path).await {
            Ok(p) => p,
            Err(e) => {
                err_print(&format!("[mohabbat] Build error: {}\n", e)).await;
                return;
            }
        }
    };

    out_print(&format!(
        "[mohabbat] Packaging {} -> {}\n",
        wasm_path, output
    ))
    .await;

    match juice_bottle_refill(self_path, &wasm_path, &output).await {
        Ok(()) => out_print(&format!("[mohabbat] Done: {}\n", output)).await,
        Err(e) => err_print(&format!("[mohabbat] Error: {}\n", e)).await,
    }
}

#[cfg(not(target_arch = "wasm32"))]
fn main() {
    println!("[mohabbat] Success: mohab.bat generated via build script.");
}
