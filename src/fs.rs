//! File system utilities
#[cfg(not(target_family = "wasm"))]
pub use compio::fs::{File, OpenOptions};

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedFuture;

#[cfg(target_family = "wasm")]
/// WASM File
pub struct File {
    handle: u64,
}

#[cfg(target_family = "wasm")]
impl File {
    /// Open a file
    pub async fn open<P: AsRef<std::path::Path>>(path: P) -> std::io::Result<File> {
        OpenOptions::new().read(true).open(path).await
    }

    /// Create a file
    pub async fn create<P: AsRef<std::path::Path>>(path: P) -> std::io::Result<File> {
        OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(path)
            .await
    }
}

#[cfg(target_family = "wasm")]
impl crate::io::AsyncRead for File {
    async fn read<B: compio_buf::IoBufMut>(
        &mut self,
        mut buf: B,
    ) -> compio_buf::BufResult<usize, B> {
        let ptr = buf.buf_mut_ptr() as *mut u8;
        let len = buf.buf_capacity() as u32;
        let handle = self.handle;

        let (err, bytes_read, _) = OverlappedFuture::new(move |ov| {
            unsafe { imports::read(ov, handle, ptr, len) };
        })
        .await;

        if err != 0 {
            return compio_buf::BufResult(Err(std::io::Error::from_raw_os_error(err as i32)), buf);
        }

        unsafe { buf.set_len(bytes_read as usize) };
        compio_buf::BufResult(Ok(bytes_read as usize), buf)
    }
}

#[cfg(target_family = "wasm")]
impl crate::io::AsyncWrite for File {
    async fn write<B: compio_buf::IoBuf>(&mut self, buf: B) -> compio_buf::BufResult<usize, B> {
        let ptr = buf.buf_ptr();
        let len = buf.buf_len() as u32;
        let handle = self.handle;

        let (err, bytes_written, _) = OverlappedFuture::new(move |ov| {
            unsafe { imports::write(ov, handle, ptr, len) };
        })
        .await;

        if err != 0 {
            return compio_buf::BufResult(Err(std::io::Error::from_raw_os_error(err as i32)), buf);
        }

        compio_buf::BufResult(Ok(bytes_written as usize), buf)
    }

    async fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }

    async fn shutdown(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[cfg(target_family = "wasm")]
/// WASM OpenOptions
pub struct OpenOptions {
    read: bool,
    write: bool,
    append: bool,
    truncate: bool,
    create: bool,
    create_new: bool,
}

#[cfg(target_family = "wasm")]
impl OpenOptions {
    /// Create new OpenOptions
    pub fn new() -> Self {
        Self {
            read: false,
            write: false,
            append: false,
            truncate: false,
            create: false,
            create_new: false,
        }
    }

    /// Read flag
    pub fn read(&mut self, read: bool) -> &mut Self {
        self.read = read;
        self
    }

    /// Write flag
    pub fn write(&mut self, write: bool) -> &mut Self {
        self.write = write;
        self
    }

    /// Append flag
    pub fn append(&mut self, append: bool) -> &mut Self {
        self.append = append;
        self
    }

    /// Truncate flag
    pub fn truncate(&mut self, truncate: bool) -> &mut Self {
        self.truncate = truncate;
        self
    }

    /// Create flag
    pub fn create(&mut self, create: bool) -> &mut Self {
        self.create = create;
        self
    }

    /// Create new flag
    pub fn create_new(&mut self, create_new: bool) -> &mut Self {
        self.create_new = create_new;
        self
    }

    /// Open the file
    pub async fn open<P: AsRef<std::path::Path>>(&self, path: P) -> std::io::Result<File> {
        let path_str = path
            .as_ref()
            .to_str()
            .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::InvalidInput, "invalid path"))?;
        let path_ptr = path_str.as_ptr();
        let path_len = path_str.len() as u32;

        // Flags mapping (simplified)
        let mut flags = 0u32;
        if self.read {
            flags |= 1;
        }
        if self.write {
            flags |= 2;
        }
        if self.create {
            flags |= 4;
        }
        if self.truncate {
            flags |= 8;
        }
        if self.append {
            flags |= 16;
        }
        if self.create_new {
            flags |= 32;
        }

        let (err, handle, _) = OverlappedFuture::new(move |ov| {
            unsafe { imports::path_open(ov, path_ptr, path_len, flags) };
        })
        .await;

        if err != 0 {
            return Err(std::io::Error::from_raw_os_error(err as i32));
        }

        Ok(File { handle })
    }
}

#[cfg(target_family = "wasm")]
/// WASM DirReader
pub struct DirReader {
    handle: u64,
    continued: u64,
}

#[cfg(target_family = "wasm")]
impl DirReader {
    /// Read next batch of entries
    pub async fn read_entries(&mut self) -> std::io::Result<Option<Vec<String>>> {
        let mut buf = vec![0u8; 4096];
        let ptr = buf.as_mut_ptr();
        let len = buf.len() as u32;

        let (err, bytes_read, next_continued) = OverlappedFuture::new({
            let handle = self.handle;
            let continued = self.continued;
            move |ov| unsafe {
                (*ov).continued = continued;
                imports::dir_read(ov, handle, ptr, len);
            }
        })
        .await;

        if err != 0 {
            return Err(std::io::Error::from_raw_os_error(err as i32));
        }

        self.continued = next_continued;

        let entries = buf[..bytes_read as usize]
            .split(|&b| b == 0)
            .filter(|s| !s.is_empty())
            .map(|s| String::from_utf8_lossy(s).to_string())
            .collect();

        Ok(Some(entries))
    }
}

#[cfg(target_family = "wasm")]
/// Metadata for WASM
pub struct Metadata {
    #[allow(dead_code)]
    pub(crate) handle: u64,
}

#[cfg(target_family = "wasm")]
impl Metadata {
    /// Returns true if is file
    pub fn is_file(&self) -> bool {
        true
    } // FIXME
    /// Returns true if is dir
    pub fn is_dir(&self) -> bool {
        false
    } // FIXME
    /// Returns length
    pub fn len(&self) -> u64 {
        0
    } // FIXME
}

#[cfg(target_family = "wasm")]
/// Query metadata for a path
pub async fn metadata<P: AsRef<std::path::Path>>(path: P) -> std::io::Result<Metadata> {
    let path_str = path
        .as_ref()
        .to_str()
        .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::InvalidInput, "invalid path"))?;
    let path_ptr = path_str.as_ptr();
    let path_len = path_str.len() as u32;

    let (err, handle, _) = OverlappedFuture::new(move |ov| {
        unsafe { imports::path_stat(ov, path_ptr, path_len) };
    })
    .await;

    if err != 0 {
        return Err(std::io::Error::from_raw_os_error(err as i32));
    }

    Ok(Metadata { handle })
}
