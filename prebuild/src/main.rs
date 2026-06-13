use std::collections::HashMap;
use std::fs;
use std::path::PathBuf;
use std::process::Command;

fn extend_pre_link_args(spec: &mut serde_json::Value, flavor: &str, args: &[&str]) {
    let pre_link_args = spec
        .as_object_mut()
        .unwrap()
        .entry("pre-link-args")
        .or_insert_with(|| serde_json::json!({}));
    let args_obj = pre_link_args.as_object_mut().unwrap();
    let entry = args_obj
        .entry(flavor)
        .or_insert_with(|| serde_json::json!([]));
    let arr = entry.as_array_mut().unwrap();
    for arg in args {
        if !arr.iter().any(|v| v == arg) {
            arr.push(serde_json::json!(arg));
        }
    }
}

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
        (
            "x86_64-pc-windows-gnullvm",
            "x86_64-rusticated-windows-gnullvm",
        ),
        // ("x86_64-pc-windows-gnu", "x86_64-rusticated-windows-gnu"),
        // ("x86_64-pc-windows-msvc", "x86_64-rusticated-windows-msvc"),
        ("x86_64-unknown-linux-gnu", "x86_64-rusticated-linux"),
        (
            "aarch64-pc-windows-gnullvm",
            "aarch64-rusticated-windows-gnullvm",
        ),
        // ("aarch64-pc-windows-gnu", "aarch64-rusticated-windows-gnu"),
        // ("aarch64-pc-windows-msvc", "aarch64-rusticated-windows-msvc"),
        ("aarch64-unknown-linux-gnu", "aarch64-rusticated-linux"),
        (
            "wasm32-unknown-unknown",
            "wasm32-rusticated-unknown-unknown",
        ),
    ];

    let target_dir = PathBuf::from("target");
    let spec_dir = target_dir.join("rusticated-spec");
    fs::create_dir_all(&spec_dir).expect("Failed to create spec dir");

    // Important: Clear out the config in case it taints the build-std below
    // Write an empty string so Cargo doesn't fail on a missing include file.
    let _ = fs::write(spec_dir.join("config.toml"), "");

    let rust_target_path = fs::canonicalize(&spec_dir)
        .unwrap_or_else(|_| spec_dir.clone())
        .to_string_lossy()
        .replace("\\\\?\\", "")
        .replace('\\', "/");

    let mut config_toml = String::new();
    let mut built_targets: Vec<String> = Vec::new();

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

        let is_windows_msvc = base_target.contains("-windows-msvc");
        let is_windows_gnu =
            base_target.contains("-windows-gnu") || base_target.contains("-windows-gnullvm");

        spec.as_object_mut()
            .unwrap()
            .insert("panic-strategy".to_string(), serde_json::json!("abort"));

        if base_target.contains("-linux-gnu") {
            // Keep the real Linux OS target while still enforcing no-default libraries
            // for the rusticated sysroot. This preserves unix cfg branches and avoids
            // fake no-OS target semantics.
            spec.as_object_mut()
                .unwrap()
                .insert("os".to_string(), serde_json::json!("linux"));

            // Disable PIE for custom linux target to avoid relocation issues without a CRT.
            spec.as_object_mut().unwrap().insert(
                "position-independent-executables".to_string(),
                serde_json::json!(false),
            );
            spec.as_object_mut()
                .unwrap()
                .insert("relocation-model".to_string(), serde_json::json!("static"));
        }

        // Set target-family correctly based on base_target.
        let families = if base_target.contains("-linux-") {
            vec!["unix", "rusticated"]
        } else if base_target.contains("-darwin") || base_target.contains("-freebsd") {
            vec!["unix", "rusticated"]
        } else if base_target.contains("-windows-") {
            vec!["windows", "rusticated"]
        } else if base_target.contains("wasm32-") {
            vec!["wasm", "rusticated"]
        } else {
            vec!["rusticated"]
        };

        spec.as_object_mut()
            .unwrap()
            .insert("target-family".to_string(), serde_json::json!(families));

        if is_windows_msvc {
            extend_pre_link_args(
                &mut spec,
                "msvc",
                &[
                    "/NOLOGO",
                    "/NXCOMPAT",
                    "/DYNAMICBASE",
                    "/ENTRY:mainCRTStartup",
                    "/SUBSYSTEM:CONSOLE",
                    "/FORCE:MULTIPLE",
                    "/NODEFAULTLIB",
                ],
            );
            extend_pre_link_args(
                &mut spec,
                "lld-link",
                &[
                    "/NOLOGO",
                    "/NXCOMPAT",
                    "/DYNAMICBASE",
                    "/ENTRY:mainCRTStartup",
                    "/SUBSYSTEM:CONSOLE",
                    "/FORCE:MULTIPLE",
                    "/NODEFAULTLIB",
                ],
            );
        }
        if is_windows_gnu {
            // Remove default MinGW import libraries for custom no-std gnullvm/gnu targets.
            spec.as_object_mut()
                .unwrap()
                .insert("late-link-args".to_string(), serde_json::json!({}));

            let arch_arg = if base_target.starts_with("x86_64") {
                "i386pep"
            } else {
                "arm64pe"
            };

            extend_pre_link_args(
                &mut spec,
                "gnu",
                &[
                    "-m",
                    arch_arg,
                    "--entry=mainCRTStartup",
                    "--subsystem=console",
                ],
            );
            extend_pre_link_args(
                &mut spec,
                "gnu-cc",
                &[
                    "-nolibc",
                    "--unwindlib=none",
                    "-m",
                    arch_arg,
                    "-Wl,--entry=mainCRTStartup",
                    "-Wl,--subsystem=console",
                ],
            );
            extend_pre_link_args(
                &mut spec,
                "gnu-lld",
                &[
                    "-m",
                    arch_arg,
                    "--entry=mainCRTStartup",
                    "--subsystem=console",
                ],
            );
            extend_pre_link_args(
                &mut spec,
                "gnu-lld-cc",
                &[
                    "-nolibc",
                    "--unwindlib=none",
                    "-m",
                    arch_arg,
                    "-Wl,--entry=mainCRTStartup",
                    "-Wl,--subsystem=console",
                ],
            );
        }

        if base_target.contains("-linux-gnu") {
            // Stop rustc from trying to link default libraries like libc and libgcc_s
            spec.as_object_mut().unwrap().insert(
                "late-link-args".to_string(),
                serde_json::json!({
                    "gnu": ["-nostdlib"],
                    "gcc": ["-nostdlib"],
                    "gnu-cc": ["-nostdlib"],
                    "gnu-lld": ["-nostdlib"],
                    "gnu-lld-cc": ["-nostdlib"]
                }),
            );

            // Force no-default-libraries to true
            spec.as_object_mut()
                .unwrap()
                .insert("no-default-libraries".to_string(), serde_json::json!(true));

            // Force lld to not look for default libraries
            extend_pre_link_args(
                &mut spec,
                "gnu-lld",
                &["-nostdlib", "--no-dynamic-linker", "--build-id=none"],
            );
            extend_pre_link_args(
                &mut spec,
                "gnu-lld-cc",
                &[
                    "-nostdlib",
                    "-nodefaultlibs",
                    "-nostartfiles",
                    "-Wl,--build-id=none",
                ],
            );
            extend_pre_link_args(&mut spec, "gnu", &["-nostdlib"]);
            extend_pre_link_args(
                &mut spec,
                "gnu-cc",
                &["-nostdlib", "-nodefaultlibs", "-nostartfiles"],
            );
            extend_pre_link_args(
                &mut spec,
                "gcc",
                &["-nostdlib", "-nodefaultlibs", "-nostartfiles"],
            );

            // Change linker flavor to gnu-lld to avoid the wrapper's default libs
            spec.as_object_mut()
                .unwrap()
                .insert("linker-flavor".to_string(), serde_json::json!("gnu-lld"));
        }

        let obj = spec.as_object_mut().unwrap();
        obj.insert("crt-static-respected".to_string(), serde_json::json!(true));
        obj.insert("no-default-libraries".to_string(), serde_json::json!(true));
        if is_windows_msvc || is_windows_gnu {
            obj.insert("crt-static-default".to_string(), serde_json::json!(true));
        }
        if base_target.contains("-windows-gnullvm") {
            obj.insert("linker".to_string(), serde_json::json!("rust-lld"));
            obj.insert("linker-flavor".to_string(), serde_json::json!("gnu-lld"));
        }
        if base_target.contains("-linux-gnu") {
            obj.insert("linker".to_string(), serde_json::json!("rust-lld"));
            obj.insert("linker-flavor".to_string(), serde_json::json!("gnu-lld"));
            // Use an empty environment so this rusticated Linux target does not
            // automatically pull in GNU CRT libraries.
            obj.insert("env".to_string(), serde_json::json!(""));
        }
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

        // Build rusticated in release mode for this target; consumer crates may
        // still be built in debug using the generated config.
        let existing_rustflags = std::env::var("RUSTFLAGS").unwrap_or_default();
        let mut rustflags = if existing_rustflags.is_empty() {
            "-Zunstable-options --cfg backtrace_in_libstd".to_string()
        } else {
            format!(
                "{} -Zunstable-options --cfg backtrace_in_libstd",
                existing_rustflags
            )
        };

        if base_target.contains("-linux-gnu") {
            rustflags.push_str(" -A explicit-builtin-cfgs-in-flags --cfg rusticated_linux");
        }

        if base_target.contains("-linux-") {}

        let mut build_cmd = Command::new("cargo");
        build_cmd.env("RUSTFLAGS", &rustflags);
        build_cmd.env("RUST_TARGET_PATH", &rust_target_path);
        let target_arg = if custom_name.contains("wasm32") {
            custom_name.to_string()
        } else {
            fs::canonicalize(&json_path)
                .unwrap_or_else(|_| json_path.clone())
                .to_string_lossy()
                .replace("\\\\?\\", "")
                .replace('\\', "/")
        };

        build_cmd
            .arg("build")
            .arg("-p")
            .arg("rusticated")
            .arg("--release")
            .arg("-Z")
            .arg("build-std=core,alloc,compiler_builtins")
            .arg("-Z")
            .arg("build-std-features=compiler-builtins-mem")
            .arg("--config")
            .arg("unstable.json-target-spec=true")
            .arg("--target")
            .arg(&target_arg)
            .arg("--message-format=json");

        let cmd_line = std::iter::once(build_cmd.get_program().to_string_lossy().into_owned())
            .chain(
                build_cmd
                    .get_args()
                    .map(|a| a.to_string_lossy().into_owned()),
            )
            .collect::<Vec<_>>()
            .join(" ");
        println!("     > {}", cmd_line);

        let build_output = build_cmd.output().expect("cargo build failed");

        if !build_output.status.success() {
            let stderr = String::from_utf8_lossy(&build_output.stderr);
            eprintln!("     < failed {}:\n{}", custom_name, stderr);
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
            panic!("rusticated build failed for target {}", custom_name);
        }

        println!("     < succeeded {}", custom_name);

        let deps_dir = target_dir.join(custom_name).join("release").join("deps");
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
                            Err(_) => path
                                .to_string_lossy()
                                .replace("\\\\?\\", "")
                                .replace('\\', "/"),
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

        built_targets.push(custom_name.to_string());

        let mut target_rustflags = format!("[target.{}]\nrustflags = [\n", custom_name);
        target_rustflags.push_str("    \"-Zunstable-options\",\n");
        target_rustflags.push_str("    \"--cfg\", \"backtrace_in_libstd\",\n");
        for (crate_name, abs_path) in paths.iter() {
            if matches!(
                crate_name.as_str(),
                "std" | "core" | "alloc" | "compiler_builtins"
            ) {
                target_rustflags.push_str(&format!(
                    "    \"--extern\", \"{}={}\",\n",
                    crate_name, abs_path
                ));
            }
        }
        let abs_deps_dir = match fs::canonicalize(&deps_dir) {
            Ok(p) => p
                .to_string_lossy()
                .replace("\\\\?\\", "")
                .replace('\\', "/"),
            Err(_) => deps_dir
                .to_string_lossy()
                .replace("\\\\?\\", "")
                .replace('\\', "/"),
        };
        target_rustflags.push_str(&format!("    \"-L\", \"dependency={}\",\n", abs_deps_dir));
        if custom_name.contains("-linux") {
            target_rustflags.push_str("    \"--cfg\", \"rusticated_linux\",\n");
        }
        target_rustflags.push_str("]\n\n");
        config_toml.push_str(&target_rustflags);
    }

    if built_targets.is_empty() {
        panic!("No rusticated targets were successfully built")
    }

    let host_rusticated_target = if host.ends_with("-windows-msvc") {
        let arch = host.split('-').next().unwrap_or("x86_64");
        let candidates = [
            format!("{}-rusticated-windows-gnullvm", arch),
            format!("{}-rusticated-windows-gnu", arch),
            // format!("{}-rusticated-windows-msvc", arch),
        ];
        candidates
            .iter()
            .find(|t| built_targets.contains(&t.to_string()))
            .cloned()
            .unwrap_or_else(|| built_targets[0].clone())
    } else if host.contains("windows-gnullvm") {
        let target = host.replace("-pc-", "-rusticated-");
        if built_targets.contains(&target) {
            target
        } else {
            built_targets[0].clone()
        }
    } else if host.contains("windows-gnu") {
        let target = host.replace("-pc-", "-rusticated-");
        if built_targets.contains(&target) {
            target
        } else {
            built_targets[0].clone()
        }
    } else if host.contains("-linux-gnu") {
        let arch = host.split('-').next().unwrap_or("x86_64");
        let target = format!("{}-rusticated-linux", arch);
        if built_targets.contains(&target) {
            target
        } else {
            built_targets[0].clone()
        }
    } else if host.contains("-unknown-unknown") {
        let arch = host.split('-').next().unwrap_or("wasm32");
        let target = format!("{}-rusticated-unknown-unknown", arch);
        if built_targets.contains(&target) {
            target
        } else {
            built_targets[0].clone()
        }
    } else {
        let target = host.replace("-pc-", "-rusticated-");
        if built_targets.contains(&target) {
            target
        } else {
            built_targets[0].clone()
        }
    };

    let abs_spec_dir = fs::canonicalize(&spec_dir).unwrap_or_else(|_| {
        std::env::current_dir()
            .expect("Failed to get current directory")
            .join(&spec_dir)
    });
    let abs_json = abs_spec_dir
        .join(format!("{}.json", host_rusticated_target))
        .to_string_lossy()
        .replace("\\\\?\\", "")
        .replace('\\', "/");

    let mut final_config = String::new();
    final_config.push_str(&format!(
        "[env]\nRUST_TARGET_PATH = \"{}\"\n\n",
        rust_target_path
    ));
    final_config.push_str(&format!("[build]\ntarget = \"{}\"\n\n", abs_json));
    final_config.push_str("[unstable]\njson-target-spec = true\n\n");
    final_config.push_str(&config_toml);

    fs::write(spec_dir.join("config.toml"), final_config).expect("wrote config.toml");

    // Resolve GOROOT dynamically, preferring the version specified in mohabbat-go/go.mod
    let go_mod_path = PathBuf::from("mohabbat-go/go.mod");
    let mut goroot = None;

    if let Ok(content) = fs::read_to_string(&go_mod_path) {
        if let Some(ver) = content
            .lines()
            .find(|l| l.trim().starts_with("go "))
            .map(|l| l.trim().trim_start_matches("go ").trim())
        {
            println!("     > attempting to resolve GOROOT for go{}", ver);

            // First priority: check %HOME%/sdk/go{ver}
            let home = std::env::var("USERPROFILE")
                .or_else(|_| std::env::var("HOME"))
                .unwrap_or_default();
            if !home.is_empty() {
                let sdk_path = PathBuf::from(&home).join("sdk").join(format!("go{}", ver));
                if sdk_path.exists() {
                    println!("     > found SDK in %HOME%/sdk: {}", sdk_path.display());
                    goroot = Some(sdk_path);
                }
            }

            if goroot.is_none() {
                let go_cmd = format!("go{}", ver);
                println!("     > attempting to resolve GOROOT for {}", go_cmd);

                // Try running the specific go version command (e.g. go1.26.4)
                if let Ok(out) = Command::new(&go_cmd).args(["env", "GOROOT"]).output() {
                    if out.status.success() {
                        let path =
                            PathBuf::from(String::from_utf8_lossy(&out.stdout).trim().to_string());
                        if path.exists() {
                            println!("     > using GOROOT from {}: {}", go_cmd, path.display());
                            goroot = Some(path);
                        }
                    }
                }
            }

            // If that didn't work, try running 'go env GOROOT' FROM INSIDE mohabbat-go/
            // Go 1.21+ toolchain management might pick up the version from go.mod correctly.
            if goroot.is_none() {
                println!(
                    "     > attempting to resolve GOROOT by running 'go env GOROOT' inside mohabbat-go/"
                );
                if let Ok(out) = Command::new("go")
                    .args(["env", "GOROOT"])
                    .current_dir("mohabbat-go")
                    .output()
                {
                    if out.status.success() {
                        let path =
                            PathBuf::from(String::from_utf8_lossy(&out.stdout).trim().to_string());
                        if path.exists() {
                            println!(
                                "     > using GOROOT from 'go' inside mohabbat-go/: {}",
                                path.display()
                            );
                            goroot = Some(path);
                        }
                    }
                }
            }
        }
    }

    let goroot = match goroot {
        Some(p) => p,
        None => {
            println!("     > falling back to default go for GOROOT");
            let out = Command::new("go").args(["env", "GOROOT"]).output();
            match out {
                Ok(o) if o.status.success() => {
                    PathBuf::from(String::from_utf8_lossy(&o.stdout).trim().to_string())
                }
                _ => {
                    panic!("failed to find GOROOT");
                }
            }
        }
    };

    // Generate the Go build overlay that maps WASI source files to our
    // rusticated replacements, enabling `go build -overlay target/overlay.json`.
    generate_go_overlay(&goroot, &target_dir).expect("Failed to generate Go overlay");

    println!("Done. Run `cargo build -p demo`.");
}

fn generate_go_overlay(goroot: &PathBuf, target_dir: &PathBuf) -> std::io::Result<()> {
    // Resolve repo root as the current working directory when prebuild runs.
    let repo_root = std::env::current_dir()?;
    let overlay_dir = repo_root.join("overlay-go");

    // Helper to canonicalize a path, stripping Windows \\?\ prefix.
    let canon = |p: PathBuf| -> String {
        let s = fs::canonicalize(&p)
            .unwrap_or(p)
            .to_string_lossy()
            .replace("\\\\?\\", "")
            .replace('\\', "/");
        s
    };

    let replacements: Vec<(&str, String)> = vec![
        // Runtime
        (
            "src/runtime/lock_wasip1.go",
            canon(overlay_dir.join("runtime/lock_rusticated.go")),
        ),
        (
            "src/runtime/os_wasip1.go",
            canon(overlay_dir.join("runtime/os_rusticated.go")),
        ),
        (
            "src/runtime/netpoll_wasip1.go",
            canon(overlay_dir.join("runtime/netpoll_rusticated.go")),
        ),
        (
            "src/runtime/stubs_wasm.go",
            canon(overlay_dir.join("runtime/stubs_rusticated.go")),
        ),
        // Syscall
        (
            "src/syscall/fs_wasip1.go",
            canon(overlay_dir.join("syscall/fs_rusticated.go")),
        ),
        (
            "src/syscall/syscall_wasip1.go",
            canon(overlay_dir.join("syscall/syscall_rusticated.go")),
        ),
        (
            "src/syscall/net_wasip1.go",
            canon(overlay_dir.join("syscall/net_rusticated.go")),
        ),
        (
            "src/syscall/os_wasip1.go",
            canon(overlay_dir.join("syscall/os_rusticated.go")),
        ),
        // Internal
        (
            "src/internal/syscall/unix/at_wasip1.go",
            canon(overlay_dir.join("internal/syscall/unix/at_rusticated.go")),
        ),
        (
            "src/internal/syscall/unix/utimes_wasip1.go",
            canon(overlay_dir.join("internal/syscall/unix/utimes_rusticated.go")),
        ),
        (
            "src/internal/syscall/unix/nonblocking_wasip1.go",
            canon(overlay_dir.join("internal/syscall/unix/nonblocking_rusticated.go")),
        ),
        (
            "src/internal/syscall/unix/fcntl_wasip1.go",
            canon(overlay_dir.join("internal/syscall/unix/fcntl_rusticated.go")),
        ),
        (
            "src/internal/syscall/unix/net_wasip1.go",
            canon(overlay_dir.join("internal/syscall/unix/net_rusticated.go")),
        ),
    ];

    let mut entries = String::new();
    let mut first = true;
    for (goroot_rel, dst_str) in replacements {
        let src = goroot.join(goroot_rel);
        if !src.exists() {
            eprintln!("warning: overlay source not found: {}", src.display());
            continue;
        }
        let src_str = canon(src);
        if !first {
            entries.push_str(",\n");
        }
        first = false;
        entries.push_str(&format!("    \"{}\": \"{}\"", src_str, dst_str));
    }

    let json = format!("{{\n  \"Replace\": {{\n{entries}\n  }}\n}}\n");
    let overlay_path = target_dir.join("overlay.json");
    fs::write(&overlay_path, &json)?;
    println!("wrote {}", overlay_path.display());
    Ok(())
}
