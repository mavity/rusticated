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
    &["x86_64-pc-windows-msvc", "x86_64-pc-windows-gnullvm", "x86_64-pc-windows-gnu"],
    &["aarch64-pc-windows-msvc", "aarch64-pc-windows-gnullvm", "aarch64-pc-windows-gnu"],
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

    // Read any existing mohab.bat BEFORE building so we can retain embedded
    // binaries for slots we cannot build this session.
    let bat_path = workspace_dir.join("mohab.bat");
    let old_slot_data = parse_existing_mohab(&bat_path);

    // Phase 1: For each slot pick the first available target and build brot + washmhost.
    // Any build failure is a hard stop.
    let mut slot_targets: Vec<Option<&str>> = Vec::new();
    for candidates in TARGET_SLOTS {
        let resolved = candidates.iter().copied().find(|t| can_build_target(t));
        match resolved {
            Some(target) => {
                println!("cargo:warning=Slot resolved to target {}", target);
                let b1 = build_component(workspace_dir, &target_tree, "brot", target);
                let b2 = build_component(workspace_dir, &target_tree, "washmhost", target);
                if !(b1 && b2) {
                    panic!(
                        "Build failed for slot target {} (brot: {}, washmhost: {})",
                        target, b1, b2
                    );
                }
                slot_targets.push(Some(target));
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
    let brain_ok = build_component(
        workspace_dir,
        &target_tree,
        "mohabbat",
        "wasm32-unknown-unknown",
    );
    if !brain_ok {
        panic!("Build failed for mohabbat wasm32-unknown-unknown");
    }

    // Phase 3: Stitching
    stitch(workspace_dir, &target_tree, &slot_targets, &old_slot_data);
}

fn can_build_target(target: &str) -> bool {
    // This slot model requires both `brot` and `washmhost` on the same target.
    // `washmhost` is built as cdylib, and rustc currently rejects cdylib for
    // x86_64-unknown-linux-musl in this pipeline.
    // Therefore musl linux targets are not buildable here.
    if target.contains("unknown-linux-musl") {
        return false;
    }

    // Temporarily disable cross-compilation in mohabbat. We only want to build
    // the actual host target in this pipeline and accumulate engines across
    // separate runs on different real targets later.
    if let Ok(host) = env::var("HOST") {
        if host != target {
            println!(
                "cargo:warning=Skipping {}: cross-compilation disabled for mohabbat (host is {})",
                target, host
            );
            return false;
        }
        return true;
    }

    // Fallback: if HOST is unavailable, only accept targets explicitly installed.
    let installed = Command::new("rustup")
        .args(&["target", "list", "--installed"])
        .env_remove("RUSTUP_TOOLCHAIN")
        .output()
        .map(|o| String::from_utf8_lossy(&o.stdout).contains(target))
        .unwrap_or(false);
    if installed {
        return true;
    }
    false
}

fn build_sysroot(workspace_dir: &Path, target: &str, target_name: &str) -> bool {
    let sysroot_dir = workspace_dir
        .join("target")
        .join(format!("sysroot-{}", target_name));
    let sysroot_lib_dir = sysroot_dir
        .join("lib")
        .join("rustlib")
        .join(target_name)
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
            "-Z",
            "json-target-spec",
            "-p",
            "rusticated",
            "--target",
            target,
            "--release",
            "--message-format=json",
        ]);

    let build_dir = workspace_dir
        .join("target")
        .join(format!("build-std-{}", target_name));
    cmd.env("CARGO_TARGET_DIR", &build_dir);
    let existing_rustflags = env::var("RUSTFLAGS").unwrap_or_default();
    let sysroot_rustflags = if existing_rustflags.is_empty() {
        "--cfg backtrace_in_libstd".to_string()
    } else {
        format!("{} --cfg backtrace_in_libstd", existing_rustflags)
    };
    cmd.env("RUSTFLAGS", sysroot_rustflags);
    cmd.env("CARGO_CFG_BACKTRACE_IN_LIBSTD", "");

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
                            let dest_path = sysroot_lib_dir.join(&*file_name);
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
    // washmhost is compiled for the custom rusticated target and needs our
    // custom std.  We build a proper sysroot (rlibs stored under the custom
    // target triple name) and pass --sysroot; no explicit --extern std/core/…
    // flags are needed, which avoids the duplicate-CrateNum ICE that occurs
    // when both --sysroot and --extern point at the same crates.
    //
    // brot is compiled for the standard host triple and uses the system std;
    // no custom sysroot is required (the custom sysroot dir uses a different
    // target-triple name and rustc would not find std there anyway).
    let needs_sysroot = package == "washmhost";
    let rusticated_spec_dir = workspace_dir.join("target").join("rusticated-spec");

    let custom_target = if target.starts_with("x86_64-") {
        if target.contains("windows") {
            rusticated_spec_dir.join("x86_64-windows-rusticated.json")
        } else if target.contains("linux") {
            rusticated_spec_dir.join("x86_64-linux-rusticated.json")
        } else {
            rusticated_spec_dir.join("x86_64-rusticated.json")
        }
    } else if target.starts_with("aarch64-") {
        if target.contains("windows") {
            rusticated_spec_dir.join("aarch64-windows-rusticated.json")
        } else if target.contains("linux") {
            rusticated_spec_dir.join("aarch64-linux-rusticated.json")
        } else {
            rusticated_spec_dir.join("aarch64-rusticated.json")
        }
    } else if target.starts_with("wasm32-") {
        rusticated_spec_dir.join("wasm32-rusticated.json")
    } else {
        PathBuf::from(target)
    };
    
    let target_name = custom_target.file_stem().unwrap().to_string_lossy();

    let target_env = target.to_uppercase().replace("-", "_");
    let rustflags_env = format!("CARGO_TARGET_{}_RUSTFLAGS", target_env);
    // Start from a clean slate — do NOT inherit RUSTFLAGS from the parent
    // cargo invocation.  The parent is building mohabbat for the host target
    // (via sysroot.toml which injects --sysroot <rusticated-sysroot>), and
    // inheriting those flags into sub-builds for the STANDARD host triple
    // (brot) or for different custom targets (washmhost) causes rustc to look
    // for core/alloc in the wrong sysroot and fail with "can't find crate for
    // `core`".
    let mut rustflags = String::new();

    // brot defines its own Windows entry point (mainCRTStartup) via rusticated's
    // runtime.  Without -nostartfiles, mingw's crt2.o also defines mainCRTStartup
    // causing a duplicate-symbol link error.  -C panic=abort drops the unwinding
    // machinery so rust_eh_personality doesn't need to be resolved.
    if package == "brot" && target.contains("windows") {
        rustflags.push_str(" -C panic=abort -C link-arg=-nostartfiles");
    }

    if package == "washmhost" && target.contains("linux-musl") {
        rustflags.push_str(" -C target-feature=-crt-static");
    }

    println!(
        "cargo:warning=Building {} for {} with rustflags: {}",
        package, target, rustflags
    );

    if needs_sysroot {
        // Ensure sysroot is built for this target first
        let sysroot_target_str = custom_target.to_string_lossy();
        if !build_sysroot(workspace_dir, &sysroot_target_str, &target_name) {
            println!("cargo:warning=Failed to build sysroot for {}", target);
            return false;
        }

        let sysroot_path = workspace_dir
            .join("target")
            .join(format!("sysroot-{}", target_name));
        rustflags.push_str(&format!(" --sysroot {}", sysroot_path.display()));
    }

    // washmhost's cdylib is the artifact embedded by mohabbat; keep the build
    // scoped to the library target here.
    // Build only the lib to skip the failing bin.
    let lib_only = package == "washmhost";

    let mut cmd = Command::new("cargo");

    cmd.current_dir(workspace_dir)
        .env("CARGO_TARGET_DIR", target_tree)
        .env("RUSTFLAGS", &rustflags)
        .env(rustflags_env, rustflags)
        // CARGO_ENCODED_RUSTFLAGS is an internal cargo env var set in the
        // parent build-script environment.  It encodes the rustflags that the
        // parent cargo is using (including --sysroot for the rusticated target).
        // If it leaks into sub-cargo invocations it overrides our explicit
        // RUSTFLAGS, causing "can't find crate for `core`" when brot or
        // washmhost is compiled for a different target than the parent.
        .env_remove("CARGO_ENCODED_RUSTFLAGS")
        .env_remove("CARGO_MAKEFLAGS")
        .args(&["build", "-p", package, "--release"]);

    if package == "washmhost" {
        // Do NOT pass --config config.toml explicitly.  When --config is
        // passed as a CLI flag it has HIGHER priority than the RUSTFLAGS
        // environment variable, which would prevent the --sysroot flag we set
        // via RUSTFLAGS from overriding the explicit --extern std/core/… flags
        // that used to live in config.toml.  Now that config.toml uses
        // --sysroot instead of explicit externs this is less of a concern, but
        // keeping the approach clean avoids future surprises.
        //
        // washmhost/.cargo/config.toml is NOT loaded by cargo when the current
        // working directory is the workspace root (cargo only searches parent
        // dirs, not subdirs), so we must provide the required settings here:
        //   • json-target-spec → inline --config flag (doesn't affect rustflags)
        //   • RUST_TARGET_PATH → explicit env var
        //   • custom std sysroot → RUSTFLAGS (set above by needs_sysroot path)
        cmd.env("RUST_TARGET_PATH", &rusticated_spec_dir);
        cmd.arg("--config").arg("unstable.json-target-spec=true");
        cmd.arg("--target").arg(&custom_target);
    } else {
        cmd.arg("--target").arg(target);
    }

    if lib_only {
        cmd.arg("--lib");
    }

    let output = cmd.output();
    match output {
        Ok(out) if out.status.success() => true,
        Ok(out) => {
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

fn stitch(
    workspace_dir: &Path,
    target_tree: &Path,
    slot_targets: &[Option<&str>],
    old_slot_data: &[Option<(Vec<u8>, Vec<u8>)>],
) {
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

    for (i, opt_target) in slot_targets.iter().enumerate() {
        if let Some(target) = opt_target {
            // Freshly built this session — read from target/tree.
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
        } else if let Some((old_brot, old_washmhost)) =
            old_slot_data.get(i).and_then(|x| x.as_ref())
        {
            // Not built this session — retain the pair from the existing mohab.bat.
            println!(
                "cargo:warning=Slot {} retaining binary pair from existing mohab.bat",
                i
            );
            washmhosts.push(Some(old_washmhost.clone()));
            brots.push(Some(old_brot.clone()));
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
    let mut out = File::create(&bat_path).unwrap();
    out.write_all(zone_a.as_bytes()).unwrap();
    for brot in patched_brots {
        if let Some(data) = brot {
            out.write_all(&data).unwrap();
        }
    }
    out.write_all(&pool_compressed).unwrap();
}

/// Parses an existing mohab.bat and extracts (raw_brot, raw_washmhost) for each
/// TARGET_SLOTS slot. Slots not embedded in the file get None.
fn parse_existing_mohab(mohab_path: &Path) -> Vec<Option<(Vec<u8>, Vec<u8>)>> {
    let n = TARGET_SLOTS.len();
    let empty = vec![None; n];

    let mut file_data = Vec::new();
    match File::open(mohab_path).and_then(|mut f| f.read_to_end(&mut file_data)) {
        Ok(_) => {}
        Err(e) => {
            println!("cargo:warning=No existing mohab.bat to read: {}", e);
            return empty;
        }
    }

    // Zone A is the text header — search in the first 8 KiB.
    let search_limit = file_data.len().min(8192);
    let zone_a = String::from_utf8_lossy(&file_data[..search_limit]);

    // Slot order matches TARGET_SLOTS:
    // 0=x86_64-linux  1=aarch64-linux  2=x86_64-win  3=aarch64-win
    // 4=x86_64-darwin 5=aarch64-darwin
    let slot_offsets: [(usize, usize); 6] = [
        parse_shell_slot(&zone_a, "x86_64-Linux"),
        parse_shell_slot(&zone_a, "aarch64-Linux"),
        parse_win_slot(&zone_a, "AMD64"),
        parse_win_slot(&zone_a, "ARM64"),
        parse_shell_slot(&zone_a, "x86_64-Darwin"),
        parse_shell_slot(&zone_a, "aarch64-Darwin"),
    ];

    // Decompress the shared pool using the MOHABBAT meta from any non-empty brot.
    let mut decompressed_pool: Option<Vec<u8>> = None;
    for &(off, len) in &slot_offsets {
        if len == 0 || off + len > file_data.len() {
            continue;
        }
        if let Some((pool_len, _, _, _, _)) = read_mohabbat_meta(&file_data[off..off + len]) {
            let pool_start = file_data.len().saturating_sub(pool_len as usize);
            let mut pool_out = Vec::new();
            if brotli::BrotliDecompress(&mut &file_data[pool_start..], &mut pool_out).is_ok() {
                decompressed_pool = Some(pool_out);
            }
            break;
        }
    }

    let pool = match decompressed_pool {
        Some(p) => p,
        None => {
            if slot_offsets.iter().any(|&(_, len)| len > 0) {
                println!(
                    "cargo:warning=Could not decompress pool from existing mohab.bat"
                );
            }
            return empty;
        }
    };

    let mut result = vec![None; n];
    for (i, &(off, len)) in slot_offsets.iter().enumerate() {
        if len == 0 || off + len > file_data.len() {
            continue;
        }
        let brot = file_data[off..off + len].to_vec();
        if let Some((_, woff, wlen, _, _)) = read_mohabbat_meta(&brot) {
            let (woff, wlen) = (woff as usize, wlen as usize);
            if wlen > 0 && woff + wlen <= pool.len() {
                result[i] = Some((brot, pool[woff..woff + wlen].to_vec()));
            }
        }
    }
    result
}

fn parse_shell_slot(zone_a: &str, name: &str) -> (usize, usize) {
    let marker = format!("{}) ", name);
    let start = match zone_a.find(&marker) {
        Some(p) => p,
        None => return (0, 0),
    };
    let end = zone_a[start..]
        .find(";;")
        .map(|p| p + start)
        .unwrap_or(zone_a.len());
    let branch = &zone_a[start..end];
    (
        parse_num_after(branch, "S_OFF="),
        parse_num_after(branch, "S_LEN="),
    )
}

fn parse_win_slot(zone_a: &str, arch: &str) -> (usize, usize) {
    // Match e.g. `"AMD64" (` then grab the first S_OFF= and S_LEN= inside that block.
    let marker = format!("\"{arch}\" (");
    let start = match zone_a.find(&marker) {
        Some(p) => p + marker.len(),
        None => return (0, 0),
    };
    let section = &zone_a[start..];
    (
        parse_num_after(section, "S_OFF="),
        parse_num_after(section, "S_LEN="),
    )
}

fn parse_num_after(text: &str, prefix: &str) -> usize {
    match text.find(prefix) {
        Some(p) => text[p + prefix.len()..]
            .chars()
            .take_while(|c| c.is_ascii_digit())
            .collect::<String>()
            .parse()
            .unwrap_or(0),
        None => 0,
    }
}

/// Locate the MOHABBAT magic in a brot binary and return its metadata fields:
/// (pool_len, washmhost_offset, washmhost_len, payload_offset, payload_len).
fn read_mohabbat_meta(data: &[u8]) -> Option<(u64, u64, u64, u64, u64)> {
    let magic = b"MOHABBAT";
    for i in 0..data.len().saturating_sub(magic.len() + 40) {
        if &data[i..i + magic.len()] == magic {
            let p = i + magic.len();
            let r = |s: usize| -> Option<u64> {
                data.get(p + s..p + s + 8)
                    .and_then(|b| b.try_into().ok())
                    .map(u64::from_le_bytes)
            };
            return Some((r(0)?, r(8)?, r(16)?, r(24)?, r(32)?));
        }
    }
    None
}
