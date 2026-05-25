//! Build script for rusticated.
//! Generates target specifications and configuration for the crate.

use ::std::env;
use ::std::fs;
use ::std::path::PathBuf;

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

    // Invoke rustc to get the default target spec for the compilation target.
    let base_target = if target.ends_with("-rusticated") {
        target.split("-rusticated").next().unwrap().to_string() + "-unknown-linux-gnu"
    } else { target.clone() };
    let output = std::process::Command::new(&rustc)
        .arg("-Z").arg("unstable-options").arg("--print").arg("target-spec-json").arg("--target").arg(&base_target)
        .output().expect("Failed to invoke rustc to get target spec json");

    if !output.status.success() {
        panic!(
            "rustc target-spec-json failed: {}",
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
    if target.contains("-windows") {
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
    if target.contains("-windows-msvc") || target.contains("-windows-gnu") {
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

    let arch = target.split('-').next().unwrap_or(&target);
    let custom_target_name = format!("{}-rusticated", arch);
    let json_file_name = format!("{}.json", custom_target_name);
    let json_path = spec_dir.join(&json_file_name);

    fs::write(&json_path, spec_json).expect("Failed to write target json");

    // Generate a config.toml that points to this target json
    let config_toml = format!(
        r#"[build]
target = "{}"

[unstable]
build-std = ["core", "alloc", "compiler_builtins"]
build-std-features = ["compiler-builtins-mem"]
json-target-spec = true

[dependencies]
std = {{ path = "../../", package = "rusticated" }}
"#,
        json_path.display()
    )
    .replace('\\', "/");

    fs::write(spec_dir.join("config.toml"), config_toml).expect("Failed to write config.toml");

    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-env-changed=TARGET");
    println!("cargo:rerun-if-env-changed=HOST");
    println!("cargo:rerun-if-env-changed=RUSTC");
    println!("cargo:rerun-if-env-changed=CARGO_TARGET_DIR");
}
