#![no_std]
#![no_main]

extern crate alloc;

use alloc::format;
use alloc::string::String;
use std::fs::File;
use std::io::{AsyncRead, AsyncWrite};
use std::tty::{stdin, stdout};
use alloc::vec::Vec;

std::spawn!(async_main());

async fn async_main() {
    let mut out = stdout();
    let mut input = stdin();

    write_all(&mut out, b"rusticated demo: type a line and press Enter (5s timeout)\n").await;
    write_all(&mut out, b"> ").await;

    let timeout_fut = std::time::sleep(core::time::Duration::from_secs(5));
    let read_fut = read_line(&mut input);

    match std::rt::select(timeout_fut, read_fut).await {
        std::rt::Either::Left(_) => {
            write_all(&mut out, b"\nTimed out waiting for input!\n").await;
        }
        std::rt::Either::Right(res) => {
            if let Ok(line) = res {
                write_all(&mut out, b"You typed: ").await;
                write_all(&mut out, &line).await;
                write_all(&mut out, b"\n").await;
            } else {
                write_all(&mut out, b"Failed to read line\n").await;
            }
        }
    }

    let path = "rusticated_demo.txt";
    let contents = b"rusticated demo file contents\n".to_vec();
    if let Err(err) = write_demo_file(path, contents).await {
        let msg = format!("Failed to write demo file: {}\n", err);
        write_all(&mut out, msg.as_bytes()).await;
        return;
    }

    write_all(&mut out, b"Created demo file `rusticated_demo.txt`.\n").await;
    if let Ok(last_byte) = read_last_byte(path).await {
        let msg = format!("Last byte in file: {:?}\n", last_byte as char);
        write_all(&mut out, msg.as_bytes()).await;
    } else {
        write_all(&mut out, b"Unable to read last byte from demo file\n").await;
    }

    let args = std::env::get_args();
    let exe = args.first().map(|s| s.as_str()).unwrap_or("<unknown>");
    match std::fs::metadata(exe).await {
        Ok(meta) => {
            let mtime_ns = meta.modified_ns();
            let now_ns = std::time::now_ns();
            let msg = format!(
                "Executable: {}\nLast modified: {} UTC ({})\n",
                exe,
                format_datetime_ns(mtime_ns),
                format_age_ns(mtime_ns, now_ns),
            );
            write_all(&mut out, msg.as_bytes()).await;
        }
        Err(e) => {
            let msg = format!("Could not stat executable: {}\n", e);
            write_all(&mut out, msg.as_bytes()).await;
        }
    }
}

fn format_age_ns(mtime_ns: u64, now_ns: u64) -> String {
    if now_ns <= mtime_ns {
        return String::from("in the future");
    }
    let secs = (now_ns - mtime_ns) / 1_000_000_000;
    if secs < 60 {
        String::from("just now")
    } else if secs < 3600 {
        let m = secs / 60;
        format!("{} minute{} ago", m, if m == 1 { "" } else { "s" })
    } else if secs < 86400 {
        let h = secs / 3600;
        format!("{} hour{} ago", h, if h == 1 { "" } else { "s" })
    } else {
        let days = secs / 86400;
        format!("{} day{} ago", days, if days == 1 { "" } else { "s" })
    }
}

/// Convert nanoseconds since UNIX epoch to `YYYY-MM-DD HH:MM:SS` (UTC).
fn format_datetime_ns(ns: u64) -> String {
    let secs = ns / 1_000_000_000;
    // Days since 1970-01-01 (civil calendar algorithm by Howard Hinnant)
    let z = (secs / 86400) as i64 + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = (z - era * 146_097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146_096) / 365;
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    let rem = secs % 86400;
    let h = rem / 3600;
    let min = (rem % 3600) / 60;
    let s = rem % 60;
    format!("{:04}-{:02}-{:02} {:02}:{:02}:{:02}", y, m, d, h, min, s)
}

async fn write_all(writer: &mut impl AsyncWrite, bytes: &[u8]) {
    let mut buf = bytes.to_vec();
    while !buf.is_empty() {
        let (result, mut returned) = writer.write(buf).await;
        match result {
            Ok(written) => {
                if written >= returned.len() {
                    break;
                }
                buf = returned.split_off(written);
            }
            Err(_) => break,
        }
    }
}

async fn read_line(reader: &mut impl AsyncRead) -> std::io::Result<Vec<u8>> {
    let mut result = Vec::new();
    loop {
        let buf = Vec::with_capacity(128);
        let (res, mut buf) = reader.read(buf).await;
        let n = res?;
        if n == 0 {
            break;
        }
        result.append(&mut buf);
        if result.contains(&b'\n') {
            break;
        }
    }
    Ok(result)
}

async fn write_demo_file(path: &str, bytes: Vec<u8>) -> std::io::Result<()> {
    let mut file = File::create(path).await?;
    let (result, _buf) = file.write(bytes).await;
    result.map(|_| ())
}

async fn read_last_byte(path: &str) -> std::io::Result<u8> {
    let mut file = File::open(path).await?;
    let buf = Vec::with_capacity(1024);
    let (result, buf) = file.read(buf).await;
    let n = result?;
    if n == 0 {
        Err(std::io::Error::new(
            std::io::ErrorKind::UnexpectedEof,
            "empty file",
        ))
    } else {
        Ok(buf[n - 1])
    }
}
