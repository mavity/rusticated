//! Build script for rusticated.
//! Generates target specifications and configuration for the crate.

use ::std::env;
use ::std::fs;
use ::std::path::{Path, PathBuf};

fn main() {
    let out_dir = env::var_os("OUT_DIR")
        .map(PathBuf::from)
        .expect("OUT_DIR not set");
    let target = env::var("TARGET").unwrap_or_else(|_| env::var("HOST").expect("HOST not set"));
    let rustc = env::var("RUSTC").unwrap_or_else(|_| "rustc".into());

    let target_dir = if let Some(dir) = env::var_os("CARGO_TARGET_DIR") {
        PathBuf::from(dir)
    } else {
        let mut dir = out_dir.clone();
        while dir.file_name().map(|n| n != "target").unwrap_or(true) {
            if !dir.pop() {
                break;
            }
        }
        if dir.file_name().map(|n| n != "target").unwrap_or(true) {
            panic!("Could not locate Cargo target directory from OUT_DIR");
        }
        dir
    };

    let spec_dir = target_dir.join("rusticated-spec");
    fs::create_dir_all(&spec_dir).expect("Failed to create spec dir");
    let current_json_path = spec_dir.join(format!("{}-rusticated.json", target.split('-').next().unwrap_or(&target)));

    let base_targets = [
        "x86_64-unknown-linux-gnu",
        "aarch64-unknown-linux-gnu",
        "x86_64-pc-windows-msvc",
        "aarch64-pc-windows-msvc",
    ];

    for base_target in base_targets {
        let output = std::process::Command::new(&rustc)
            .arg("-Z")
            .arg("unstable-options")
            .arg("--print")
            .arg("target-spec-json")
            .arg("--target")
            .arg(base_target)
            .output()
            .expect("Failed to invoke rustc to get target spec json");

        if !output.status.success() {
            panic!(
                "rustc target-spec-json failed for {}: {}",
                base_target,
                String::from_utf8_lossy(&output.stderr)
            );
        }

        let mut spec: serde_json::Value =
            serde_json::from_slice(&output.stdout).expect("Failed to parse rustc target-spec-json");

        let obj = spec
            .as_object_mut()
            .expect("Expected target spec to be a JSON object");

        // Enforce our basic sysroot properties
        obj.insert("panic-strategy".to_string(), serde_json::json!("abort"));
        if base_target.contains("-windows") {
            obj.insert("crt-static-default".to_string(), serde_json::json!(true));
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

        // For Windows, ensure entry point and console subsystem
        if base_target.contains("-windows-msvc") || base_target.contains("-windows-gnu") {
            let pre_link_args = serde_json::json!({
                "msvc": [
                    "/NOLOGO",
                    "/NXCOMPAT",
                    "/DYNAMICBASE",
                    "/ENTRY:mainCRTStartup",
                    "/SUBSYSTEM:CONSOLE",
                    "/FORCE:MULTIPLE"
                ],
                "lld-link": [
                    "/NOLOGO",
                    "/NXCOMPAT",
                    "/DYNAMICBASE",
                    "/ENTRY:mainCRTStartup",
                    "/SUBSYSTEM:CONSOLE",
                    "/FORCE:MULTIPLE"
                ] // Also catch msvc-lld which rustc sometimes outputs
            });
            obj.insert("pre-link-args".to_string(), pre_link_args);
        }

        let spec_json =
            serde_json::to_string_pretty(&spec).expect("Failed to serialize modified target spec");

        let arch = base_target.split('-').next().unwrap_or(base_target);
        let custom_target_name = format!("{}-rusticated.json", arch);
        let json_path = spec_dir.join(&custom_target_name);

        write_if_changed(&json_path, spec_json.as_bytes());
    }

    // Discover the compiled sysroot rlib so we can pass it as --extern std=...
    // sysroot is a [build-dependencies] of this package so it is compiled
    // before this build script runs.
    let profile = env::var("PROFILE").unwrap_or_else(|_| "debug".into());
    let deps_dir = target_dir.join(&profile).join("deps");
    let sysroot_rlib = fs::read_dir(&deps_dir)
        .expect("Failed to read deps dir")
        .filter_map(|e| e.ok())
        .filter(|e| {
            let name = e.file_name();
            let name = name.to_string_lossy();
            name.starts_with("libsysroot-") && name.ends_with(".rlib")
        })
        .max_by_key(|e| e.metadata().and_then(|m| m.modified()).ok())
        .map(|e| e.path())
        .expect("Could not find libsysroot-*.rlib in deps dir; ensure sysroot is listed under [build-dependencies] in Cargo.toml");
    let sysroot_rlib_str = sysroot_rlib.to_string_lossy().replace('\\', "/");
    println!("cargo:rerun-if-changed={}", sysroot_rlib.display());

    // Generate a config.toml that points to this target json
    let json_path_str = current_json_path.display().to_string();
    let config_toml = format!(
        r#"[build]
target = "{json_path_str}"
rustflags = [
    # 1. Maps any ambient 'extern crate std;' lookups directly to the sysroot implementation
    "--extern", "std={sysroot_rlib_str}",
    # 2. Instructs the compiler to enforce standard prelude lookup rules
    "-Z", "prelude"
]

[unstable]
build-std = ["core", "alloc", "compiler_builtins"]
build-std-features = ["compiler-builtins-mem"]
json-target-spec = true

[dependencies]
std = {{ path = "../../../", package = "rusticated" }}
core = {{ path = "../../../" }}
alloc = {{ path = "../../../" }}
compiler_builtins = {{ version = "0.1.106", features = ["mem"] }}


"#
    )
    .replace('\\', "/");

    fs::write(spec_dir.join("config.toml"), config_toml).expect("Failed to write config.toml");

    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-env-changed=TARGET");
    println!("cargo:rerun-if-env-changed=HOST");
    println!("cargo:rerun-if-env-changed=RUSTC");
    println!("cargo:rerun-if-env-changed=CARGO_TARGET_DIR");
}

fn write_if_changed(path: &Path, contents: &[u8]) {
    if let Ok(existing) = fs::read(path) {
        if existing == contents {
            return;
        }
    }
    fs::write(path, contents).expect("Failed to write target spec");
}
