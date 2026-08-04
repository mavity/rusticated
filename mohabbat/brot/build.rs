fn main() {
    // Read washmhost size from environment (set by build system)
    // If not set, default to 0 (will be zero-initialized in binary)
    let washmhost_len = std::env::var("MOHABBAT_WASHMHOST_LEN")
        .unwrap_or_else(|_| "0".to_string());
    
    // Emit as a compile-time env var so it can be accessed via env!()
    println!("cargo:rustc-env=MOHABBAT_WASHMHOST_LEN={}", washmhost_len);
}
