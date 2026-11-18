pub fn find_entry<'a>(pool: &'a [u8], name: &[u8]) -> Option<&'a [u8]> {
    if pool.len() < 16 || &pool[0..8] != b"MHBTPOOL" {
        return None;
    }
    let version = u32::from_le_bytes([pool[8], pool[9], pool[10], pool[11]]);
    let count = u32::from_le_bytes([pool[12], pool[13], pool[14], pool[15]]) as usize;
    if version != 1 || 16usize.checked_add(count.checked_mul(40)?)? > pool.len() {
        return None;
    }
    let mut i = 0;
    while i < count {
        let toc_off = 16 + i * 40;
        let name_bytes = &pool[toc_off..toc_off + 32];
        let mut nlen = 0;
        while nlen < 32 && name_bytes[nlen] != 0 {
            nlen += 1;
        }
        if &name_bytes[..nlen] == name {
            let off = u32::from_le_bytes([
                pool[toc_off + 32],
                pool[toc_off + 33],
                pool[toc_off + 34],
                pool[toc_off + 35],
            ]) as usize;
            let len = u32::from_le_bytes([
                pool[toc_off + 36],
                pool[toc_off + 37],
                pool[toc_off + 38],
                pool[toc_off + 39],
            ]) as usize;
            if len == 0 || off.checked_add(len)? > pool.len() {
                return None;
            }
            return Some(&pool[off..off + len]);
        }
        i += 1;
    }
    None
}
