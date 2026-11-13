import os
import re
import select
import subprocess
import sys


PATTERN = (
    rb"^Mohabbat-next ready \(compiled [^\)]+\)\. Enter an empty line to exit\.\r?\n"
    rb"> echo: alpha\r?\n> echo: beta\r?\n> $"
)


def run_with_pipes(command: list[str]) -> tuple[int, bytes]:
    process = subprocess.Popen(
        command,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    output, _ = process.communicate(b"alpha\r\nbeta\n\n", timeout=10)
    return process.returncode, output


def run_with_pty(command: list[str]) -> tuple[int, bytes]:
    import pty
    import termios
    import tty

    master, slave = pty.openpty()
    tty.setraw(slave)
    attributes = termios.tcgetattr(slave)
    attributes[3] &= ~termios.ECHO
    termios.tcsetattr(slave, termios.TCSANOW, attributes)
    process = subprocess.Popen(
        command,
        stdin=slave,
        stdout=slave,
        stderr=slave,
        close_fds=True,
    )
    os.close(slave)
    os.write(master, b"alpha\r\nbeta\n\n")

    output = bytearray()
    while True:
        ready, _, _ = select.select([master], [], [], 10)
        if not ready:
            process.kill()
            raise TimeoutError("payload did not finish its interactive exchange")
        try:
            chunk = os.read(master, 4096)
        except OSError:
            break
        if not chunk:
            break
        output.extend(chunk)
    return process.wait(timeout=10), bytes(output)


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: test_bundle.py <bundle-command> [arguments...]", file=sys.stderr)
        return 2

    command = sys.argv[1:]
    if os.name == "nt":
        status, output = run_with_pipes(command)
    else:
        status, output = run_with_pty(command)

    if status != 0:
        print(f"bundle exited with status {status}", file=sys.stderr)
        print(output.decode(errors="replace"), file=sys.stderr)
        return 1
    if not re.match(PATTERN, output):
        print("bundle output did not match the acceptance transcript", file=sys.stderr)
        print(f"pattern: {PATTERN!r}", file=sys.stderr)
        print(f"actual:   {output!r}", file=sys.stderr)
        return 1
    print("interactive bundle acceptance passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
