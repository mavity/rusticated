#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
probe_dir="$(cd -- "$script_dir/../.." && pwd)"
build_dir="$script_dir/.build"
output_dir="${MOHABBAT_NEXT_UNIX_OUT:-$script_dir/out}"
rust_target_dir="$build_dir/rust"
flat_target_dir="$build_dir/flat"
flat_output_dir="$build_dir/components"
cargo_home="$build_dir/cargo-home"

mkdir -p "$output_dir"
output_dir="$(cd -- "$output_dir" && pwd)"
cd "$probe_dir"

for tool in cargo cmp ld.lld llvm-objcopy llvm-readobj llvm-nm llvm-objdump; do
    command -v "$tool" >/dev/null 2>&1 || {
        printf 'required build tool not found: %s\n' "$tool" >&2
        exit 1
    }
done

host_os="$(uname -s)"
host_arch="$(uname -m)"
case "$host_arch" in
    arm64) host_arch=aarch64 ;;
esac

case "$host_os" in
    Darwin)
        flavor=darwin
        runner_target="${host_arch}-apple-darwin"
        ;;
    Linux)
        flavor=linux
        runner_target="${host_arch}-unknown-linux-gnu"
        if [ -n "${TERMUX_VERSION:-}" ] || [ "${PREFIX:-}" = /data/data/com.termux/files/usr ]; then
            [ "$host_arch" = aarch64 ] || {
                echo "Termux support requires AArch64." >&2
                exit 1
            }
            runner_target=aarch64-linux-android
            export CARGO_TARGET_AARCH64_LINUX_ANDROID_LINKER="${CARGO_TARGET_AARCH64_LINUX_ANDROID_LINKER:-${CC:-clang}}"
        fi
        ;;
    *)
        printf 'unsupported Unix host: %s\n' "$host_os" >&2
        exit 1
        ;;
esac

case "$host_arch" in
    x86_64) flat_target=x86_64-unknown-none ;;
    aarch64) flat_target=aarch64-unknown-none ;;
    *)
        printf 'unsupported CPU architecture: %s\n' "$host_arch" >&2
        exit 1
        ;;
esac

mkdir -p "$rust_target_dir" "$flat_target_dir" "$flat_output_dir" "$cargo_home"
export CARGO_HOME="$cargo_home"

find_archive() {
    local directory="$1"
    local pattern="$2"
    local matches=("$directory"/$pattern)
    if [ ! -f "${matches[0]}" ] || [ -f "${matches[1]:-}" ]; then
        printf 'expected one archive matching %s in %s\n' "$pattern" "$directory" >&2
        return 1
    fi
    printf '%s\n' "${matches[0]}"
}

publish_asset() {
    local source="$1"
    local destination="$2"
    if [ -f "$destination" ]; then
        if cmp -s "$source" "$destination"; then
            return
        fi
        printf 'conflicting asset already exists: %s\n' "$destination" >&2
        return 1
    fi
    cp "$source" "$destination"
}

build_flat() {
    local package="$1"
    local feature="$2"
    local archive_pattern="$3"
    local entry="$4"
    local linker_script="$5"
    local output_name="$6"
    local target_dir="$flat_target_dir/$package"
    local dependencies="$target_dir/$flat_target/release/deps"
    local elf="$flat_output_dir/$output_name.elf"
    local binary="$flat_output_dir/$output_name.bin"
    local feature_args=()
    if [ -n "$feature" ]; then
        feature_args=(--features "$feature")
    fi

    cargo +nightly clean --manifest-path "$probe_dir/Cargo.toml" \
        --target "$flat_target" --target-dir "$target_dir"
    cargo +nightly -Z build-std=core build \
        --manifest-path "$probe_dir/Cargo.toml" \
        --package "$package" --release --target "$flat_target" \
        --target-dir "$target_dir" "${feature_args[@]}"

    local package_archive core_archive builtins_archive
    package_archive="$(find_archive "$dependencies" "$archive_pattern")"
    core_archive="$(find_archive "$dependencies" 'libcore-*.rlib')"
    builtins_archive="$(find_archive "$dependencies" 'libcompiler_builtins-*.rlib')"
    ld.lld -T "$linker_script" --build-id=none --gc-sections \
        --no-undefined --start-group "$package_archive" "$core_archive" \
        "$builtins_archive" --end-group -o "$elf"

    if llvm-readobj --relocations "$elf" |
        grep -Eq '^[[:space:]]*0x[0-9A-Fa-f]+'; then
        printf 'relocations found in %s\n' "$output_name" >&2
        return 1
    fi
    if [ -n "$(llvm-nm --undefined-only "$elf")" ]; then
        printf 'undefined symbols found in %s\n' "$output_name" >&2
        return 1
    fi
    if llvm-readobj --sections "$elf" | grep -Eq '\.(tdata|tbss)'; then
        printf 'TLS found in %s\n' "$output_name" >&2
        return 1
    fi
    if llvm-objdump --disassemble-symbols="$entry" "$elf" |
        grep -Eiq '(^|[[:space:]])(syscall|sysenter|svc)([[:space:]]|$)|(^|[[:space:]])int[[:space:]]+0x80'; then
        printf 'direct system call found in OS-blind component %s\n' "$output_name" >&2
        return 1
    fi

    llvm-objcopy -O binary --only-section=.payload "$elf" "$binary"
    local binary_size
    binary_size="$(wc -c < "$binary")"
    if [ "$binary_size" -le 0 ] || [ "$binary_size" -gt 4096 ]; then
        printf '%s must fit within one 4 KiB page (got %s bytes)\n' \
            "$output_name" "$binary_size" >&2
        return 1
    fi
}

build_flat mohabbat-next-payload "" 'libmohabbat_next_payload-*.rlib' \
    probe_code_start "$probe_dir/payload/payload.ld" "probe-$host_arch"
build_flat mohabbat-next-unix-platform "$flavor" \
    'libmohabbat_next_unix_platform-*.rlib' unix_platform_entry \
    "$probe_dir/platform/unix/platform.ld" "platform-$flavor-$host_arch"

publish_asset "$flat_output_dir/probe-$host_arch.bin" \
    "$output_dir/probe-$host_arch.bin"
publish_asset "$flat_output_dir/platform-$flavor-$host_arch.bin" \
    "$output_dir/platform-$flavor-$host_arch.bin"

cargo +nightly build --manifest-path "$script_dir/Cargo.toml" \
    --release --target "$runner_target" --target-dir "$rust_target_dir"
runner="$rust_target_dir/$runner_target/release/mohabbat-next-brot"
if [ ! -f "$runner" ]; then
    printf 'Brot runner was not produced: %s\n' "$runner" >&2
    exit 1
fi
publish_asset "$runner" "$output_dir/brot-$runner_target"
chmod 700 "$output_dir/brot-$runner_target"
printf 'Built and published %s platform, %s payload, and Brot runner for %s.\n' \
    "$flavor" "$host_arch" "$runner_target"
