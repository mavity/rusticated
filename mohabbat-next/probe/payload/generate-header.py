from pathlib import Path
import sys


def emit_array(name: str, data: bytes) -> str:
    values = ", ".join(f"0x{byte:02x}" for byte in data)
    return f"static const unsigned char {name}[] = {{{values}}};\n"


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: generate-header.py <payload-dir> <output-header>", file=sys.stderr)
        return 2

    payload_dir = Path(sys.argv[1])
    output = Path(sys.argv[2])
    x64 = (payload_dir / "probe-x86_64.bin").read_bytes()
    arm64 = (payload_dir / "probe-aarch64.bin").read_bytes()
    if not x64 or not arm64:
        print("payload slices must not be empty", file=sys.stderr)
        return 1

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(
        "#ifndef MOHABBAT_NEXT_PROBE_PAYLOADS_H\n"
        "#define MOHABBAT_NEXT_PROBE_PAYLOADS_H\n"
        + emit_array("probe_payload_x86_64", x64)
        + emit_array("probe_payload_aarch64", arm64)
        + "#endif\n",
        encoding="ascii",
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
