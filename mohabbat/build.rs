use std::env;
use std::fs::File;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::process::Command;

/// Six output slots (x86_64-linux, aarch64-linux, x86_64-win, aarch64-win, x86_64-darwin, aarch64-darwin).
/// Each inner slice lists candidates in preference order; the first buildable one wins.
const TARGET_SLOTS: &[&[&str]] = &[
    &["x86_64-unknown-linux-musl", "x86_64-unknown-linux-gnu"],
    &["aarch64-unknown-linux-musl", "aarch64-unknown-linux-gnu"],
    &["x86_64-pc-windows-gnu", "x86_64-pc-windows-msvc"],
    &["aarch64-pc-windows-gnu", "aarch64-pc-windows-msvc"],
    &["x86_64-apple-darwin"],
    &["aarch64-apple-darwin"],
];

#[repr(C, packed)]
pub struct MohabbatMeta {
    pub magic: [u8; 8],
    pub pool_len: u64,
    pub washmhost_offset: u64,
    pub washmhost_len: u64,
    pub payload_offset: u64,
    pub payload_len: u64,
    pub reserved: u64,
}

pub fn patch_meta_buf(buf: &mut [u8], meta: &MohabbatMeta) -> Result<(), std::io::Error> {
    let magic = b"MOHABBAT";
    let mut matches = 0;
    let mut pos = 0;

    for (i, window) in buf.windows(magic.len()).enumerate() {
        if window == magic {
            matches += 1;
            pos = i;
        }
    }

    if matches == 1 {
        let p = pos + magic.len();
        buf[p..p + 8].copy_from_slice(&meta.pool_len.to_le_bytes());
        buf[p + 8..p + 16].copy_from_slice(&meta.washmhost_offset.to_le_bytes());
        buf[p + 16..p + 24].copy_from_slice(&meta.washmhost_len.to_le_bytes());
        buf[p + 24..p + 32].copy_from_slice(&meta.payload_offset.to_le_bytes());
        buf[p + 32..p + 40].copy_from_slice(&meta.payload_len.to_le_bytes());
        buf[p + 40..p + 48].copy_from_slice(&meta.reserved.to_le_bytes());
        Ok(())
    } else if matches == 0 {
        Err(std::io::Error::new(
            std::io::ErrorKind::NotFound,
            "Magic not found",
        ))
    } else {
        Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "Magic found multiple times",
        ))
    }
}

pub const ZONE_A_TEMPLATE: &str = "::; echo \"[mohabbat] starting (sh)...\" >&2; \
case \"$(uname -m)-$(uname -s)\" in \
  x86_64-Linux)   S_OFF={{X86_64_LINUX_OFF}}; S_LEN={{X86_64_LINUX_LEN}} ;; \
  aarch64-Linux)  S_OFF={{AARCH64_LINUX_OFF}}; S_LEN={{AARCH64_LINUX_LEN}} ;; \
  x86_64-Darwin)  S_OFF={{X86_64_DARWIN_OFF}}; S_LEN={{X86_64_DARWIN_LEN}} ;; \
  aarch64-Darwin) S_OFF={{AARCH64_DARWIN_OFF}}; S_LEN={{AARCH64_DARWIN_LEN}} ;; \
esac; \
[ \"$S_LEN\" = \"0\" ] && { echo \"[mohabbat] Unsupported arch/os\"; exit 1; }; \
TMP_EXE=\"/tmp/moh-$$-$(date +%s)\"; \
dd if=\"$0\" bs=1 skip=\"$S_OFF\" count=\"$S_LEN\" of=\"$TMP_EXE\" 2>/dev/null; \
chmod +x \"$TMP_EXE\"; \
\"$TMP_EXE\" \"$0\" \"$@\"; \
RET=$?; rm \"$TMP_EXE\"; exit $RET\r\n\
@echo off\r\n\
setlocal enabledelayedexpansion\r\n\
set \"ME=%~f0\"\r\n\
set \"TMP_EXE=%TEMP%\\moh-!RANDOM!.exe\"\r\n\
set \"ARCH=%PROCESSOR_ARCHITECTURE%\"\r\n\
if \"!PROCESSOR_ARCHITEW6432!\" neq \"\" set \"ARCH=!PROCESSOR_ARCHITEW6432!\"\r\n\
set \"S_OFF=0\"\r\n\
set \"S_LEN=0\"\r\n\
if \"!ARCH!\"==\"AMD64\" (\r\n\
    set \"S_OFF={{X86_64_WIN_OFF}}\"\r\n\
    set \"S_LEN={{X86_64_WIN_LEN}}\"\r\n\
) else if \"!ARCH!\"==\"ARM64\" (\r\n\
    set \"S_OFF={{AARCH64_WIN_OFF}}\"\r\n\
    set \"S_LEN={{AARCH64_WIN_LEN}}\"\r\n\
)\r\n\
if \"!S_LEN!\"==\"0\" (\r\n\
    echo [mohabbat] This vegetable does not support !ARCH! on Windows.\r\n\
    exit /b 1\r\n\
)\r\n\
powershell -NoProfile -ExecutionPolicy Bypass -Command \"$f=[IO.File]::OpenRead($env:ME); [void]$f.Seek(!S_OFF!,[IO.SeekOrigin]::Begin); $b=New-Object byte[] !S_LEN!; [void]$f.Read($b,0,!S_LEN!); [IO.File]::WriteAllBytes($env:TMP_EXE,$b); $f.Close()\"\r\n\
\"!TMP_EXE!\" \"!ME!\" %*\r\n\
set \"RET=%ERRORLEVEL%\"\r\n\
del \"!TMP_EXE!\"\r\n\
exit /b !RET!\r\n\
";

fn main() {
    let manifest_dir = env::var("CARGO_MANIFEST_DIR").unwrap();
    let workspace_dir = Path::new(&manifest_dir).parent().unwrap();

    // Skip building if we are targeting wasm32 (the brain itself being compiled)
    if env::var("CARGO_CFG_TARGET_ARCH").unwrap_or_default() == "wasm32" {
        return;
    }

    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-changed=../brot/src");
    println!("cargo:rerun-if-changed=../washmhost/src");

    let target_tree = workspace_dir.join("target").join("tree");

    // Phase 1: For each slot pick the first available target and build brot + washmhost.
    let mut slot_targets: Vec<Option<&str>> = Vec::new();
    for candidates in TARGET_SLOTS {
        let resolved = candidates.iter().copied().find(|t| can_build_target(t));
        match resolved {
            Some(target) => {
                println!("cargo:warning=Slot resolved to target {}", target);
                let b1 = build_component(workspace_dir, &target_tree, "brot", target);
                let b2 = build_component(workspace_dir, &target_tree, "washmhost", target);
                slot_targets.push(if b1 && b2 { Some(target) } else { None });
            }
            None => {
                println!(
                    "cargo:warning=No available target for slot {:?}",
                    candidates
                );
                slot_targets.push(None);
            }
        }
    }

    // Phase 2: Build the brain
    build_component(
        workspace_dir,
        &target_tree,
        "mohabbat",
        "wasm32-unknown-unknown",
    );

    // Phase 3: Stitching
    stitch(workspace_dir, &target_tree, &slot_targets);
}

fn can_build_target(target: &str) -> bool {
    let output = Command::new("rustup")
        .args(&["target", "list", "--installed"])
        .env_remove("RUSTUP_TOOLCHAIN")
        .output();

    match output {
        Ok(out) => {
            let stdout = String::from_utf8_lossy(&out.stdout);
            if stdout.contains(target) {
                return true;
            }
            println!(
                "cargo:warning=rustup stdout did not contain {}: {:?}",
                target, stdout
            );
        }
        Err(e) => {
            println!("cargo:warning=rustup failed to run: {}", e);
        }
    }

    if let Ok(host) = env::var("TARGET") {
        if host == target {
            return true;
        }
    }
    false
}

fn build_sysroot(workspace_dir: &Path, target: &str) -> bool {
    let sysroot_dir = workspace_dir
        .join("target")
        .join(format!("sysroot-{}", target));
    let sysroot_lib_dir = sysroot_dir
        .join("lib")
        .join("rustlib")
        .join(target)
        .join("lib");

    // If the sysroot already contains a libstd.rlib, skip rebuilding to
    // preserve the cargo cache for heavy deps like wasmtime/serde.
    // The sysroot will be rebuilt automatically if it is deleted manually.
    let sysroot_exists = sysroot_lib_dir.join("libstd.rlib").exists()
        || std::fs::read_dir(&sysroot_lib_dir)
            .map(|mut d| {
                d.any(|e| {
                    e.ok().map_or(false, |e| {
                        e.file_name().to_string_lossy().starts_with("libstd-")
                            && e.file_name().to_string_lossy().ends_with(".rlib")
                    })
                })
            })
            .unwrap_or(false);

    if sysroot_exists {
        return true;
    }

    let _ = std::fs::create_dir_all(&sysroot_lib_dir);

    let mut cmd = Command::new("cargo");
    cmd.current_dir(workspace_dir)
        .env_remove("CARGO_MAKEFLAGS")
        .env("RUSTC", "rustc")
        .args(&[
            "build",
            "-Z",
            "build-std=core,alloc,compiler_builtins",
            "-Z",
            "build-std-features=compiler-builtins-mem",
            "-p",
            "rusticated",
            "--target",
            target,
            "--release",
            "--message-format=json",
        ]);

    let build_dir = workspace_dir
        .join("target")
        .join(format!("build-std-{}", target));
    cmd.env("CARGO_TARGET_DIR", &build_dir);

    let output = match cmd.output() {
        Ok(o) => o,
        Err(e) => {
            println!(
                "cargo:warning=Failed to spawn sysroot cargo for {}: {}",
                target, e
            );
            return false;
        }
    };

    if !output.status.success() {
        println!(
            "cargo:warning=Failed sysroot build for {} (exit code {:?}), stderr: {}",
            target,
            output.status.code(),
            String::from_utf8_lossy(&output.stderr)
        );
        return false;
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    let mut success = false;
    for line in stdout.lines() {
        if line.contains("\"reason\":\"compiler-artifact\"") && line.contains(".rlib") {
            // Very simple JSON parsing just to grab the filenames without depending on serde_json in build.rs
            let parts: Vec<&str> = line.split("\"filenames\":[").collect();
            if parts.len() > 1 {
                let file_part = parts[1].split(']').next().unwrap_or("");
                let files: Vec<&str> = file_part.split(',').collect();
                for f in files {
                    let cleaned = f.trim_matches('"');
                    if cleaned.ends_with(".rlib") || cleaned.ends_with(".rmeta") {
                        let src_path = Path::new(cleaned);
                        if let Some(file_name_os) = src_path.file_name() {
                            let file_name = file_name_os.to_string_lossy();
                            let dest_path = if file_name.starts_with("librusticated") {
                                sysroot_lib_dir.join(file_name.replace("librusticated", "libstd"))
                            } else {
                                sysroot_lib_dir.join(&*file_name)
                            };
                            if std::fs::copy(src_path, dest_path).is_ok() {
                                success = true;
                            }
                        }
                    }
                }
            }
        }
    }
    success
}

fn build_component(workspace_dir: &Path, target_tree: &Path, package: &str, target: &str) -> bool {
    // If the output asset already exists, skip the build to preserve the cargo
    // cache for heavy deps like wasmtime/serde. Delete the asset manually to
    // force a rebuild (e.g. after making washmhost source changes).
    let existing = find_asset(target_tree, target, package);
    if existing.exists() {
        return true;
    }

    // washmhost uses real std (wasmtime depends on it), so skip the custom
    // rusticated sysroot for washmhost builds.  brot and mohabbat (brain) do
    // need the sysroot when cross-compiling, but when building for the host
    // gnu target the sysroot rename trick doesn't work (the rlib crate-name
    // stays "rusticated", not "std"), so just build natively against normal std.
    let host = env::var("HOST").unwrap_or_default();
    let needs_sysroot = package != "washmhost" && target != host;

    let target_env = target.to_uppercase().replace("-", "_");
    let rustflags_env = format!("CARGO_TARGET_{}_RUSTFLAGS", target_env);
    let mut rustflags = env::var("RUSTFLAGS").unwrap_or_default();

    if needs_sysroot {
        // Ensure sysroot is built for this target first
        if !build_sysroot(workspace_dir, target) {
            println!("cargo:warning=Failed to build sysroot for {}", target);
            return false;
        }

        let sysroot_path = workspace_dir
            .join("target")
            .join(format!("sysroot-{}", target));
        rustflags.push_str(&format!(" --sysroot {}", sysroot_path.display()));
    }

    // washmhost has a no_std bin target (main.rs) that fails to compile without
    // the sysroot, but the cdylib lib target compiles fine against system std.
    // Build only the lib to skip the failing bin.
    let lib_only = package == "washmhost";

    let mut cmd = Command::new("cargo");
    cmd.current_dir(workspace_dir)
        .env("CARGO_TARGET_DIR", target_tree)
        .env(rustflags_env, rustflags)
        .env_remove("CARGO_MAKEFLAGS")
        .args(&["build", "-p", package, "--release", "--target", target]);
    if lib_only {
        cmd.arg("--lib");
    }

    let output = cmd.output();
    match output {
        Ok(out) if out.status.success() => true,
        Ok(out) => {
            // Even on partial failure the lib artifact may have been produced.
            let existing = find_asset(target_tree, target, package);
            if existing.exists() {
                return true;
            }
            println!(
                "cargo:warning=Failed to build {} for {} with status {}. stderr: {}",
                package,
                target,
                out.status,
                String::from_utf8_lossy(&out.stderr)
            );
            false
        }
        Err(e) => {
            println!(
                "cargo:warning=Failed to execute build for {}: {}",
                package, e
            );
            false
        }
    }
}

fn find_asset(target_tree: &Path, target: &str, name: &str) -> PathBuf {
    let (prefix, ext) = if name == "washmhost" && target.contains("windows") {
        ("", ".dll")
    } else if name == "washmhost" && target.contains("darwin") {
        ("lib", ".dylib")
    } else if name == "washmhost" {
        ("lib", ".so")
    } else if target.contains("windows") {
        ("", ".exe")
    } else if target.contains("wasm32") {
        ("", ".wasm")
    } else {
        ("", "")
    };
    let mut path = target_tree
        .join(target)
        .join("release")
        .join(format!("{}{}{}", prefix, name, ext));
    if !path.exists() {
        path = target_tree
            .join(target)
            .join("release")
            .join("deps")
            .join(format!("{}{}{}", prefix, name, ext));
    }
    path
}

fn stitch(workspace_dir: &Path, target_tree: &Path, slot_targets: &[Option<&str>]) {
    let brain_path = find_asset(target_tree, "wasm32-unknown-unknown", "mohabbat");
    if !brain_path.exists() {
        println!("cargo:warning=Brain not found, skipping stitch");
        return;
    }
    let mut brain_data = Vec::new();
    File::open(&brain_path)
        .unwrap()
        .read_to_end(&mut brain_data)
        .unwrap();

    let mut washmhosts = Vec::new();
    let mut brots = Vec::new();

    for opt_target in slot_targets {
        if let Some(target) = opt_target {
            let host_path = find_asset(target_tree, target, "washmhost");
            let mut data = Vec::new();
            File::open(host_path)
                .unwrap()
                .read_to_end(&mut data)
                .unwrap();
            washmhosts.push(Some(data));

            let brot_path = find_asset(target_tree, target, "brot");
            let mut data = Vec::new();
            File::open(brot_path)
                .unwrap()
                .read_to_end(&mut data)
                .unwrap();
            brots.push(Some(data));
        } else {
            washmhosts.push(None);
            brots.push(None);
        }
    }

    // Create Pool
    let mut pool_raw = Vec::new();
    let mut washmhost_metadata = Vec::new();
    for washmhost in &washmhosts {
        if let Some(data) = washmhost {
            let offset = pool_raw.len() as u64;
            let len = data.len() as u64;
            pool_raw.extend_from_slice(data);
            washmhost_metadata.push((offset, len));
        } else {
            washmhost_metadata.push((0, 0));
        }
    }

    let payload_offset = pool_raw.len() as u64;
    let payload_len = brain_data.len() as u64;
    pool_raw.extend_from_slice(&brain_data);

    // Compress Pool
    let mut pool_compressed = Vec::new();
    let mut params = brotli::enc::backward_references::BrotliEncoderParams::default();
    params.quality = 1; // reduced from 11 to avoiding hanging
    brotli::BrotliCompress(&mut &pool_raw[..], &mut pool_compressed, &params).unwrap();

    // Patch Brots
    let mut patched_brots = Vec::new();
    for (i, brot) in brots.into_iter().enumerate() {
        if let Some(mut data) = brot {
            let meta = MohabbatMeta {
                magic: *b"MOHABBAT",
                pool_len: pool_compressed.len() as u64,
                washmhost_offset: washmhost_metadata[i].0,
                washmhost_len: washmhost_metadata[i].1,
                payload_offset,
                payload_len,
                reserved: 0,
            };
            patch_meta_buf(&mut data, &meta).unwrap();
            patched_brots.push(Some(data));
        } else {
            patched_brots.push(None);
        }
    }

    // Generate Zone A
    let mut zone_a = ZONE_A_TEMPLATE.to_string();

    // Compute offsets
    let mut current_offset = 0; // We'll pre-calculate Zone A length
    // zone_a is approximately constant in length if we use padded numbers or just use placeholders
    // For now, let's just generate it once, get length, then generate again with real offsets.

    for _ in 0..2 {
        let mut zone_b_table = Vec::new();
        let mut offset = zone_a.len();
        for brot in &patched_brots {
            if let Some(data) = brot {
                zone_b_table.push((offset, data.len()));
                offset += data.len();
            } else {
                zone_b_table.push((0, 0));
            }
        }

        zone_a = ZONE_A_TEMPLATE.to_string();
        for _ in 0..10 {
            if zone_b_table.len() < 10 {
                zone_b_table.push((0, 0));
            }
        }
        zone_a = zone_a.replace("{{X86_64_LINUX_OFF}}", &zone_b_table[0].0.to_string());
        zone_a = zone_a.replace("{{X86_64_LINUX_LEN}}", &zone_b_table[0].1.to_string());
        zone_a = zone_a.replace("{{AARCH64_LINUX_OFF}}", &zone_b_table[1].0.to_string());
        zone_a = zone_a.replace("{{AARCH64_LINUX_LEN}}", &zone_b_table[1].1.to_string());
        zone_a = zone_a.replace("{{X86_64_WIN_OFF}}", &zone_b_table[2].0.to_string());
        zone_a = zone_a.replace("{{X86_64_WIN_LEN}}", &zone_b_table[2].1.to_string());
        zone_a = zone_a.replace("{{AARCH64_WIN_OFF}}", &zone_b_table[3].0.to_string());
        zone_a = zone_a.replace("{{AARCH64_WIN_LEN}}", &zone_b_table[3].1.to_string());
        zone_a = zone_a.replace("{{X86_64_DARWIN_OFF}}", &zone_b_table[4].0.to_string());
        zone_a = zone_a.replace("{{X86_64_DARWIN_LEN}}", &zone_b_table[4].1.to_string());
        zone_a = zone_a.replace("{{AARCH64_DARWIN_OFF}}", &zone_b_table[5].0.to_string());
        zone_a = zone_a.replace("{{AARCH64_DARWIN_LEN}}", &zone_b_table[5].1.to_string());
    }

    let bat_path = workspace_dir.join("mohab.bat");
    let mut out = File::create(bat_path).unwrap();
    out.write_all(zone_a.as_bytes()).unwrap();
    for brot in patched_brots {
        if let Some(data) = brot {
            out.write_all(&data).unwrap();
        }
    }
    out.write_all(&pool_compressed).unwrap();
}
