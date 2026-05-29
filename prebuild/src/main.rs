use std::fs;
use std::path::PathBuf;
use std::process::Command;

fn main() {
    let output = Command::new("rustc").arg("-vV").output().expect("Failed to run rustc");
    let rustc_out = String::from_utf8_lossy(&output.stdout);
    let host = rustc_out
        .lines()
        .find(|l| l.starts_with("host: "))
        .expect("No host line in rustc -vV")
        .trim_start_matches("host: ")
        .trim();
    
    let base_targets = [
        ("x86_64-pc-windows-msvc", "x86_64-windows-rusticated"),
        ("x86_64-unknown-linux-gnu", "x86_64-linux-rusticated"),
        ("aarch64-pc-windows-msvc", "aarch64-windows-rusticated"),
        ("aarch64-unknown-linux-gnu", "aarch64-linux-rusticated"),
        ("wasm32-unknown-unknown", "wasm32-rusticated"),
    ];

    let host_rusticated_target = base_targets
        .iter()
        .find(|(t, _)| *t == host)
        .map(|(_, r)| *r)
        .unwrap_or("x86_64-windows-rusticated"); // fallback

    let target_dir = PathBuf::from("target");
    let spec_dir = target_dir.join("rusticated-spec");
    fs::create_dir_all(&spec_dir).expect("Failed to create spec dir");

    // Important: Clear out the config in case it taints the build-std below
    // Write an empty string so Cargo doesn't fail on a missing include file.
    let _ = fs::write(spec_dir.join("config.toml"), "");

    let mut config_toml = String::new();
    
    // Set RUST_TARGET_PATH so all workspace crates can find target specs by name without .json
    let rust_target_path = spec_dir.display().to_string().replace('\\', "/");
    config_toml.push_str(&format!("[env]\nRUST_TARGET_PATH = \"{}\"\n\n", rust_target_path));
    
    let abs_json = spec_dir.join(format!("{}.json", host_rusticated_target))
        .canonicalize().unwrap_or_else(|_| spec_dir.join(format!("{}.json", host_rusticated_target)))
        .to_string_lossy().replace("\\\\?\\", "").replace('\\', "/");
    config_toml.push_str(&format!("[build]\ntarget = \"{}\"\n\n", abs_json));
    
    config_toml.push_str("[unstable]\njson-target-spec = true\n\n");


    for (base_target, custom_name) in base_targets {
        let output = Command::new("rustc")
            .arg("-Z").arg("unstable-options")
            .arg("--print").arg("target-spec-json")
            .arg("--target").arg(base_target)
            .output().expect("Failed to invoke rustc");

        if !output.status.success() {
            println!("Skipping {} (rustc error or missing component)", base_target);
            continue;
        }

        let mut spec: serde_json::Value =
            serde_json::from_slice(&output.stdout).expect("Failed to parse JSON");

        let obj = spec.as_object_mut().unwrap();
        obj.insert("panic-strategy".to_string(), serde_json::json!("abort"));
        if base_target.contains("-windows") {
            obj.insert("crt-static-default".to_string(), serde_json::json!(true));
            let pre_link_args = serde_json::json!({
                "msvc": ["/NOLOGO", "/NXCOMPAT", "/DYNAMICBASE", "/ENTRY:mainCRTStartup", "/SUBSYSTEM:CONSOLE", "/FORCE:MULTIPLE"],
                "lld-link": ["/NOLOGO", "/NXCOMPAT", "/DYNAMICBASE", "/ENTRY:mainCRTStartup", "/SUBSYSTEM:CONSOLE", "/FORCE:MULTIPLE"]
            });
            obj.insert("pre-link-args".to_string(), pre_link_args);
        }
        obj.insert("crt-static-respected".to_string(), serde_json::json!(true));
        obj.insert("no-default-libraries".to_string(), serde_json::json!(true));
        if let Some(metadata) = obj.get_mut("metadata") {
            if let Some(meta_obj) = metadata.as_object_mut() {
                meta_obj.insert("std".to_string(), serde_json::json!(false));
            }
        } else {
            obj.insert("metadata".to_string(), serde_json::json!({ "std": false }));
        }

        let spec_json = serde_json::to_string_pretty(&spec).unwrap();
        let json_path = spec_dir.join(format!("{}.json", custom_name));
        fs::write(&json_path, spec_json).unwrap();

        // Now build rusticated for this target!
        let existing_rustflags = std::env::var("RUSTFLAGS").unwrap_or_default();
        let rustflags = if existing_rustflags.is_empty() {
            "--cfg backtrace_in_libstd".to_string()
        } else {
            format!("{} --cfg backtrace_in_libstd", existing_rustflags)
        };

        // Single build with --message-format=json captures both success/failure
        // and the artifact paths in one pass (cargo's incremental cache means
        // a repeated build without --message-format is almost free, but one
        // call is cleaner).
        //
        // --release so the sysroot rlibs match washmhost's release build.
        let build_output = Command::new("cargo")
            .env("RUSTFLAGS", &rustflags)
            .arg("build")
            .arg("-p").arg("rusticated")
            .arg("-Z").arg("build-std=core,alloc,compiler_builtins")
            .arg("--config").arg("unstable.json-target-spec=true")
            .arg("--target").arg(json_path.to_string_lossy().to_string())
            .arg("--release")
            .arg("--message-format=json")
            .output().expect("cargo build failed");

        if build_output.status.success() {
            // Build sysroot directory structure:
            //   target/sysroot-<custom_name>/lib/rustlib/<custom_name>/lib/*.rlib
            //
            // Using --sysroot <path> in config.toml instead of explicit --extern
            // flags avoids two problems:
            //   1. Explicit externs for the custom target leak into HOST
            //      compilations (build deps), causing target-triple mismatches.
            //   2. Explicit externs can cause the same crate (core, alloc) to be
            //      registered twice under different CrateNum indices, triggering an
            //      ICE in the metadata encoder when zerocopy/simd is compiled.
            let sysroot_dir = target_dir.join(format!("sysroot-{}", custom_name));
            let sysroot_lib_dir = sysroot_dir
                .join("lib")
                .join("rustlib")
                .join(custom_name)
                .join("lib");
            // Clear the lib dir so stale rlibs from earlier debug/release builds
            // don't coexist with the fresh ones — rustc would be confused by
            // multiple libstd-*.rlib files in the sysroot.
            let _ = fs::remove_dir_all(&sysroot_lib_dir);
            let _ = fs::create_dir_all(&sysroot_lib_dir);

            let json_stdout = String::from_utf8_lossy(&build_output.stdout);
            for line in json_stdout.lines() {
                if line.contains("\"reason\":\"compiler-artifact\"") && line.contains(".rlib") {
                    let parts: Vec<&str> = line.split("\"filenames\":[").collect();
                    if parts.len() > 1 {
                        let file_part = parts[1].split(']').next().unwrap_or("");
                        for f in file_part.split(',') {
                            let cleaned = f.trim_matches('"');
                            if cleaned.ends_with(".rlib") || cleaned.ends_with(".rmeta") {
                                let src = std::path::Path::new(cleaned);
                                if let Some(fname) = src.file_name() {
                                    let dest = sysroot_lib_dir.join(fname);
                                    let _ = fs::copy(src, &dest);
                                }
                            }
                        }
                    }
                }
            }

            // Emit --sysroot flag pointing at the sysroot we just built.
            let abs_sysroot = match fs::canonicalize(&sysroot_dir) {
                Ok(p) => p.to_string_lossy().replace("\\\\?\\", "").replace('\\', "/"),
                Err(_) => sysroot_dir.to_string_lossy().replace('\\', "/"),
            };
            let target_rustflags = format!(
                "[target.{}]\nrustflags = [\n    \"--sysroot\", \"{}\",\n]\n\n",
                custom_name, abs_sysroot
            );
            config_toml.push_str(&target_rustflags);
        }
    }

    fs::write(spec_dir.join("config.toml"), config_toml).expect("wrote config.toml");
    println!("Done. Run `cargo build -p demo`.");
}
