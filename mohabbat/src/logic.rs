use std::fs::{File, OpenOptions};
use std::io::{Error, ErrorKind, Read, Seek, SeekFrom, Write};

#[repr(C, packed)]
pub struct MohabbatMeta {
    pub magic: [u8; 8],
    pub pool_len: u64,
    pub washmhost_offset: u64,
    pub washmhost_len: u64,
    pub payload_offset: u64,
    pub payload_len: u64,
    pub reserved: u64,
}

pub fn patch_meta_buf(buf: &mut [u8], meta: &MohabbatMeta) -> Result<(), Error> {
    let magic = b"MOHABBAT";
    let mut matches = 0;
    let mut pos = 0;

    for (i, window) in buf.windows(magic.len()).enumerate() {
        if window == magic {
            matches += 1;
            pos = i;
        }
    }

    if matches == 1 {
        let p = pos + magic.len();
        buf[p..p + 8].copy_from_slice(&meta.pool_len.to_le_bytes());
        buf[p + 8..p + 16].copy_from_slice(&meta.washmhost_offset.to_le_bytes());
        buf[p + 16..p + 24].copy_from_slice(&meta.washmhost_len.to_le_bytes());
        buf[p + 24..p + 32].copy_from_slice(&meta.payload_offset.to_le_bytes());
        buf[p + 32..p + 40].copy_from_slice(&meta.payload_len.to_le_bytes());
        buf[p + 40..p + 48].copy_from_slice(&meta.reserved.to_le_bytes());
        Ok(())
    } else if matches == 0 {
        Err(Error::new(ErrorKind::NotFound, "Magic not found"))
    } else {
        Err(Error::new(
            ErrorKind::InvalidData,
            "Magic found multiple times",
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_patch_meta_buf() {
        let mut buf = vec![0u8; 100];
        buf[10..18].copy_from_slice(b"MOHABBAT");

        let meta = MohabbatMeta {
            magic: *b"MOHABBAT",
            pool_len: 100,
            washmhost_offset: 200,
            washmhost_len: 300,
            payload_offset: 400,
            payload_len: 500,
            reserved: 0,
        };

        patch_meta_buf(&mut buf, &meta).unwrap();

        assert_eq!(&buf[10..18], b"MOHABBAT");
        assert_eq!(u64::from_le_bytes(buf[18..26].try_into().unwrap()), 100);
        assert_eq!(u64::from_le_bytes(buf[26..34].try_into().unwrap()), 200);
        assert_eq!(u64::from_le_bytes(buf[34..42].try_into().unwrap()), 300);
        assert_eq!(u64::from_le_bytes(buf[42..50].try_into().unwrap()), 400);
        assert_eq!(u64::from_le_bytes(buf[50..58].try_into().unwrap()), 500);
        assert_eq!(u64::from_le_bytes(buf[58..66].try_into().unwrap()), 0);
    }
}
