use std::collections::HashMap;
use std::fs;
use std::path::PathBuf;
use std::process::Command;

fn main() {
    let output = Command::new("rustc")
        .arg("-vV")
        .output()
        .expect("Failed to run rustc");
    let rustc_out = String::from_utf8_lossy(&output.stdout);
    let host = rustc_out
        .lines()
        .find(|l| l.starts_with("host: "))
        .expect("No host line in rustc -vV")
        .trim_start_matches("host: ")
        .trim();

    let base_targets = [
        ("x86_64-pc-windows-msvc", "x86_64-rusticated-windows-msvc"),
        ("x86_64-unknown-linux-gnu", "x86_64-rusticated-linux-gnu"),
        ("aarch64-pc-windows-msvc", "aarch64-rusticated-windows-msvc"),
        ("aarch64-unknown-linux-gnu", "aarch64-rusticated-linux-gnu"),
        (
            "wasm32-unknown-unknown",
            "wasm32-rusticated-unknown-unknown",
        ),
    ];

    let host_rusticated_target = base_targets
        .iter()
        .find(|(t, _)| *t == host)
        .map(|(_, r)| *r)
        .unwrap_or("x86_64-rusticated-windows-msvc"); // fallback

    let target_dir = PathBuf::from("target");
    let spec_dir = target_dir.join("rusticated-spec");
    fs::create_dir_all(&spec_dir).expect("Failed to create spec dir");

    // Important: Clear out the config in case it taints the build-std below
    // Write an empty string so Cargo doesn't fail on a missing include file.
    let _ = fs::write(spec_dir.join("config.toml"), "");

    let mut config_toml = String::new();

    // Set RUST_TARGET_PATH so all workspace crates can find target specs by name without .json
    let rust_target_path = spec_dir.display().to_string().replace('\\', "/");
    config_toml.push_str(&format!(
        "[env]\nRUST_TARGET_PATH = \"{}\"\n\n",
        rust_target_path
    ));

    let abs_json = spec_dir
        .join(format!("{}.json", host_rusticated_target))
        .canonicalize()
        .unwrap_or_else(|_| spec_dir.join(format!("{}.json", host_rusticated_target)))
        .to_string_lossy()
        .replace("\\\\?\\", "")
        .replace('\\', "/");
    config_toml.push_str(&format!("[build]\ntarget = \"{}\"\n\n", abs_json));

    config_toml.push_str("[unstable]\njson-target-spec = true\n\n");

    for (base_target, custom_name) in base_targets {
        let output = Command::new("rustc")
            .arg("-Z")
            .arg("unstable-options")
            .arg("--print")
            .arg("target-spec-json")
            .arg("--target")
            .arg(base_target)
            .output()
            .expect("Failed to invoke rustc");

        if !output.status.success() {
            println!(
                "Skipping {} (rustc error or missing component)",
                base_target
            );
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

        // Now build rusticated for this target in debug mode so the target and
        // consumer builds both use the same artifact flavor.
        let existing_rustflags = std::env::var("RUSTFLAGS").unwrap_or_default();
        let rustflags = if existing_rustflags.is_empty() {
            "--cfg backtrace_in_libstd".to_string()
        } else {
            format!("{} --cfg backtrace_in_libstd", existing_rustflags)
        };

        let build_output = Command::new("cargo")
            .env("RUSTFLAGS", &rustflags)
            .arg("build")
            .arg("-p")
            .arg("rusticated")
            .arg("-Z")
            .arg("build-std=core,alloc,compiler_builtins")
            .arg("-Z")
            .arg("build-std-features=compiler-builtins-mem")
            .arg("--config")
            .arg("unstable.json-target-spec=true")
            .arg("--target")
            .arg(json_path.to_string_lossy().to_string())
            .arg("--message-format=json")
            .output()
            .expect("cargo build failed");

        if !build_output.status.success() {
            let stderr = String::from_utf8_lossy(&build_output.stderr);
            eprintln!("cargo build failed for target {}:\n{}", custom_name, stderr);
            let json_stdout = String::from_utf8_lossy(&build_output.stdout);
            for line in json_stdout.lines() {
                if let Ok(value) = serde_json::from_str::<serde_json::Value>(line) {
                    if value["reason"] == "compiler-message" {
                        if let Some(msg) = value["message"]["rendered"].as_str() {
                            eprintln!("{}", msg);
                        }
                    }
                }
            }
            std::process::exit(build_output.status.code().unwrap_or(1));
        }

        let deps_dir = target_dir.join(custom_name).join("debug").join("deps");
        let mut paths: HashMap<String, String> = HashMap::new();

        let json_stdout = String::from_utf8_lossy(&build_output.stdout);
        for line in json_stdout.lines() {
            if let Ok(value) = serde_json::from_str::<serde_json::Value>(line) {
                if value["reason"] != "compiler-artifact" {
                    continue;
                }
                if let Some(files) = value["filenames"].as_array() {
                    for filename in files.iter().filter_map(|f| f.as_str()) {
                        if !filename.ends_with(".rlib") {
                            continue;
                        }
                        let path = std::path::Path::new(filename);
                        let filename = path.file_name().and_then(|n| n.to_str()).unwrap_or("");
                        let abs_path = match fs::canonicalize(path) {
                            Ok(p) => p
                                .to_string_lossy()
                                .replace("\\\\?\\", "")
                                .replace('\\', "/"),
                            Err(_) => path.to_string_lossy().replace('\\', "/"),
                        };

                        let crate_name = if filename == "libstd.rlib" {
                            "std".to_string()
                        } else if let Some(stripped) = filename.strip_prefix("lib") {
                            if let Some(idx) = stripped.rfind('-') {
                                stripped[..idx].to_string()
                            } else {
                                stripped.trim_end_matches(".rlib").to_string()
                            }
                        } else {
                            continue;
                        };
                        paths.insert(crate_name, abs_path);
                    }
                }
            }
        }

        if !paths.contains_key("std") {
            panic!("Missing built artifact for std when generating sysroot config");
        }

        let mut target_rustflags = format!("[target.{}]\nrustflags = [\n", custom_name);
        target_rustflags.push_str("    \"--cfg\", \"backtrace_in_libstd\",\n");
        for (crate_name, abs_path) in paths.iter() {
            target_rustflags.push_str(&format!(
                "    \"--extern\", \"{}={}\",\n",
                crate_name, abs_path
            ));
        }
        let abs_deps_dir = match fs::canonicalize(&deps_dir) {
            Ok(p) => p
                .to_string_lossy()
                .replace("\\\\?\\", "")
                .replace('\\', "/"),
            Err(_) => deps_dir.to_string_lossy().replace('\\', "/"),
        };
        target_rustflags.push_str(&format!("    \"-L\", \"dependency={}\",\n", abs_deps_dir));
        target_rustflags.push_str("]\n\n");
        config_toml.push_str(&target_rustflags);
    }

    fs::write(spec_dir.join("config.toml"), config_toml).expect("wrote config.toml");
    println!("Done. Run `cargo build -p demo`.");
}
