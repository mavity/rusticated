//! Build script for rusticated.
//! Generates target specifications and configuration for the crate.

use ::std::env;
use ::std::fs;
use ::std::path::PathBuf;

fn main() {
    let out_dir = env::var_os("OUT_DIR").map(PathBuf::from).expect("OUT_DIR not set");
    let host = env::var("HOST").expect("HOST not set");
    let rustc = env::var("RUSTC").unwrap_or_else(|_| "rustc".into());
    
    // We want to find the workspace target directory.
    let mut target_dir = out_dir.clone();
    while target_dir.file_name().map(|n| n != "target").unwrap_or(true) {
        if !target_dir.pop() { break; }
    }
    
    if target_dir.file_name().map(|n| n != "target").unwrap_or(true) {
        target_dir = PathBuf::from(env::var_os("CARGO_MANIFEST_DIR").expect("MANIFEST_DIR not set"))
            .join("target");
    }

    let spec_dir = target_dir.join("rusticated-spec");
    fs::create_dir_all(&spec_dir).expect("Failed to create spec dir");

    // Invoke rustc to get the default target spec for the host
    let output = std::process::Command::new(&rustc)
        .arg("-Z")
        .arg("unstable-options")
        .arg("--print")
        .arg("target-spec-json")
        .arg("--target")
        .arg(&host)
        .output()
        .expect("Failed to invoke rustc to get target spec json");

    if !output.status.success() {
        panic!("rustc target-spec-json failed: {}", String::from_utf8_lossy(&output.stderr));
    }

    let mut spec: serde_json::Value = serde_json::from_slice(&output.stdout)
        .expect("Failed to parse rustc target-spec-json");

    let obj = spec.as_object_mut().expect("Expected target spec to be a JSON object");

    // Enforce our basic sysroot properties
    obj.insert("panic-strategy".to_string(), serde_json::json!("abort"));
    if host.contains("-windows") {
        obj.insert("crt-static-default".to_string(), serde_json::json!(true));
    }
    obj.insert("crt-static-respected".to_string(), serde_json::json!(true));
    obj.insert("no-default-libraries".to_string(), serde_json::json!(true));

    // For Windows, ensure entry point and console subsystem
    if host.contains("-windows-msvc") || host.contains("-windows-gnu") {
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

    let spec_json = serde_json::to_string_pretty(&spec).expect("Failed to serialize modified target spec");

    let arch = host.split('-').next().unwrap_or(&host);
    let custom_target_name = format!("{}-rusticated", arch);
    let json_file_name = format!("{}.json", custom_target_name);
    let json_path = spec_dir.join(&json_file_name);

    fs::write(&json_path, spec_json).expect("Failed to write target json");

    // Generate a config.toml that points to this target json
    let config_toml = format!(r#"[build]
target = "{}"

[unstable]
build-std = ["core", "alloc", "compiler_builtins"]
build-std-features = ["compiler-builtins-mem"]
json-target-spec = true
"#, json_path.display()).replace('\\', "/");

    fs::write(spec_dir.join("config.toml"), config_toml).expect("Failed to write config.toml");

    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-env-changed=HOST");
    println!("cargo:rerun-if-env-changed=RUSTC");
}
