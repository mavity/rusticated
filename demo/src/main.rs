use chrono;
use fast_std::fs::File;
use fast_std::io::{AsyncRead, AsyncWrite};
use fast_std::rt::{PollStatus, poll_step, spawn};
use fast_std::tty::{stdin, stdout};
use fast_std::vec::Vec;

fn main() {
    spawn(async_main());

    loop {
        match poll_step().expect("fast-std runtime poll failed") {
            PollStatus::Done => break,
            PollStatus::Ready => continue,
            PollStatus::Idle { .. } => {
                std::thread::sleep(std::time::Duration::from_millis(10));
            }
        }
    }
}

async fn async_main() {
    let mut out = stdout();
    let mut input = stdin();

    write_all(&mut out, b"fast-std demo: type a line and press Enter\n").await;
    write_all(&mut out, b"> ").await;

    let line = read_line(&mut input).await;
    if let Ok(line) = line {
        write_all(&mut out, b"You typed: ").await;
        write_all(&mut out, &line).await;
        write_all(&mut out, b"\n").await;
    } else {
        write_all(&mut out, b"Failed to read line\n").await;
    }

    let path = "fast_std_demo.txt";
    let contents = b"fast-std demo file contents\n".to_vec();
    if let Err(err) = write_demo_file(path, contents).await {
        let msg = format!("Failed to write demo file: {}\n", err);
        write_all(&mut out, msg.as_bytes()).await;
        return;
    }

    write_all(&mut out, b"Created demo file `fast_std_demo.txt`.\n").await;
    if let Ok(last_byte) = read_last_byte(path).await {
        let msg = format!("Last byte in file: {:?}\n", last_byte as char);
        write_all(&mut out, msg.as_bytes()).await;
    } else {
        write_all(&mut out, b"Unable to read last byte from demo file\n").await;
    }

    match std::env::current_exe().and_then(|exe| std::fs::metadata(&exe).map(|m| (exe, m))) {
        Ok((exe, meta)) => match meta.modified() {
            Ok(modified) => {
                let dt = chrono::DateTime::<chrono::Local>::from(modified);
                let ago = format_age(modified);
                let msg = format!(
                    "Executable: {}\nLast modified: {} ({})\n",
                    exe.display(),
                    dt.format("%Y-%m-%d %H:%M:%S"),
                    ago,
                );
                write_all(&mut out, msg.as_bytes()).await;
            }
            Err(e) => {
                let msg = format!("Could not read modification time: {}\n", e);
                write_all(&mut out, msg.as_bytes()).await;
            }
        },
        Err(e) => {
            let msg = format!("Could not locate current executable: {}\n", e);
            write_all(&mut out, msg.as_bytes()).await;
        }
    }
}

fn format_age(t: std::time::SystemTime) -> String {
    match std::time::SystemTime::now().duration_since(t) {
        Ok(d) => {
            let secs = d.as_secs();
            if secs < 60 {
                "just now".to_string()
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
        Err(_) => "in the future".to_string(),
    }
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

async fn read_line(reader: &mut impl AsyncRead) -> fast_std::io::Result<Vec<u8>> {
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

async fn write_demo_file(path: &str, bytes: Vec<u8>) -> fast_std::io::Result<()> {
    let mut file = File::create(path).await?;
    let (result, _buf) = file.write(bytes).await;
    result.map(|_| ())
}

async fn read_last_byte(path: &str) -> fast_std::io::Result<u8> {
    let mut file = File::open(path).await?;
    let buf = Vec::with_capacity(1024);
    let (result, buf) = file.read(buf).await;
    let n = result?;
    if n == 0 {
        Err(fast_std::io::Error::new(
            fast_std::io::ErrorKind::UnexpectedEof,
            "empty file",
        ))
    } else {
        Ok(buf[n - 1])
    }
}
