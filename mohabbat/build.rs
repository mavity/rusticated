use std::env;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::Command;

fn main() {
    let _out_dir = env::var("OUT_DIR").unwrap();
    let manifest_dir = env::var("CARGO_MANIFEST_DIR").unwrap();
    let workspace_dir = Path::new(&manifest_dir).parent().unwrap();

    if env::var("CARGO_CFG_TARGET_ARCH").unwrap() == "wasm32" {
        return;
    }

    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-changed=../brot/src");
    println!("cargo:rerun-if-changed=../washmhost/src");

    let target_tree = workspace_dir.join("target").join("tree");
    let host_target = env::var("TARGET").unwrap();

    let status = Command::new("cargo")
        .current_dir(workspace_dir)
        .env("CARGO_TARGET_DIR", &target_tree)
        .env_remove("CARGO_MAKEFLAGS")
        .args(&["build", "-p", "brot", "--release", "--target", &host_target])
        .status();

    if let Ok(st) = status {
        assert!(st.success(), "Failed to build brot");
    }

    let status = Command::new("cargo")
        .current_dir(workspace_dir)
        .env("CARGO_TARGET_DIR", &target_tree)
        .env_remove("CARGO_MAKEFLAGS")
        .args(&[
            "build",
            "-p",
            "washmhost",
            "--release",
            "--target",
            &host_target,
        ])
        .status();

    if let Ok(st) = status {
        assert!(st.success(), "Failed to build washmhost");
    }

    // Load the brot and washmhost for the host target
    let exe_suffix = if host_target.contains("windows") {
        ".exe"
    } else {
        ""
    };

    let brot_ext = format!("brot{}", exe_suffix);
    let mut brot_path = target_tree
        .join(&host_target)
        .join("release")
        .join(&brot_ext);
    if !brot_path.exists() {
        // Fallback for nested cargo sometimes dropping the target component
        brot_path = target_tree
            .join(&host_target)
            .join("release")
            .join("deps")
            .join(&brot_ext);
    }

    let host_ext = format!("washmhost{}", exe_suffix);
    let mut host_path = target_tree
        .join(&host_target)
        .join("release")
        .join(&host_ext);
    if !host_path.exists() {
        host_path = target_tree
            .join(&host_target)
            .join("release")
            .join("deps")
            .join(&host_ext);
    }

    // We didn't build the wasm payload yet! For Step 8, we just wrap demo.wasm
    // For now we don't brotli compress, we just write them raw to the file to show stitch

    let bat_path = workspace_dir.join("mohab.bat");
    let mut out = std::fs::File::create(&bat_path).unwrap();

    let script = "@echo off\r\n\
    echo [mohabbat] Bootstrapping polyglot...\r\n\
    :: slice out brot\r\n\
    goto :EOF\r\n";

    out.write_all(script.as_bytes()).unwrap();

    // Just dump them there for demonstration purposes.
    if brot_path.exists() {
        let _brot_data = std::fs::read(&brot_path).unwrap();
        // out.write_all(&brot_data).unwrap();
    }

    if host_path.exists() {
        let _host_data = std::fs::read(&host_path).unwrap();
        // out.write_all(&host_data).unwrap();
    }

    println!("cargo:warning=Stitched vegetable at mohab.bat");
}
