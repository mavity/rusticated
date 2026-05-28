use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

fn main() {
    let output = Command::new("rustc").arg("-vV").output().expect("Failed to run rustc");
    let rustc_out = String::from_utf8_lossy(&output.stdout);
    let host = rustc_out
        .lines()
        .find(|L| L.starts_with("host: "))
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
        let status = Command::new("cargo")
            .arg("build")
            // .arg("--manifest-path").arg("Cargo.toml") // implicitly in root
            .arg("-p").arg("rusticated")
            .arg("-Z").arg("build-std=core,alloc,compiler_builtins")
            .arg("--config").arg("unstable.json-target-spec=true")
            .arg("--target").arg(json_path.to_string_lossy().to_string())
            .status().expect("cargo build failed");
        
        if status.success() {
            // find the output rlib
            // target/<custom_name>/debug/deps/librusticated-*.rlib
            let deps_dir = target_dir.join(custom_name).join("debug").join("deps");
            if let Ok(entries) = fs::read_dir(&deps_dir) {
                let mut rustflags = format!("[target.{}]\nrustflags = [\n", custom_name);

                let mut find_rlib = |prefix: &str, crate_name: &str| {
                    if let Some(rlib) = fs::read_dir(&deps_dir).unwrap().filter_map(|e| e.ok())
                        .filter(|e| {
                            let name = e.file_name().to_string_lossy().to_string();
                            name.starts_with(prefix) && name.ends_with(".rlib")
                        })
                        .max_by_key(|e| e.metadata().and_then(|m| m.modified()).ok()) 
                    {
                        // Use absolute paths so rustflags work from crate subdirectories (e.g. brot)
                        if let Ok(abs_path) = std::fs::canonicalize(rlib.path()) {
                            // On Windows, canonicalize prepends \\?\. Strip it so rustc doesn't complain.
                            let rl_path = abs_path.to_string_lossy().replace("\\\\?\\", "").replace('\\', "/");
                            rustflags.push_str(&format!("    \"--extern\", \"{}={}\",\n", crate_name, rl_path));
                        }
                    }
                };

                find_rlib("libstd-", "std");
                find_rlib("libcore-", "core");
                find_rlib("liballoc-", "alloc");
                find_rlib("libcompiler_builtins-", "compiler_builtins");

                rustflags.push_str("]\n\n");
                config_toml.push_str(&rustflags);
            }
        }
    }

    fs::write(spec_dir.join("config.toml"), config_toml).expect("wrote config.toml");
    println!("Done. Run `cargo build -p demo`.");
}
