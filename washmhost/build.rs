fn main() {
    // On Windows GNU (MinGW) targets, Rust's TLS support emits _tls_index which
    // conflicts with MinGW's tlssup.o definition. Allow the duplicate so linking succeeds.
    if std::env::var("CARGO_CFG_TARGET_ENV").as_deref() == Ok("gnu")
        && std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("windows")
    {
        println!("cargo:rustc-link-arg=-Wl,--allow-multiple-definition");
    }
}
