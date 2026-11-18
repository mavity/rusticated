use ruzstd::frame_decoder::{BlockDecodingStrategy, FrameDecoder};
use ruzstd::io::Read;

pub fn decompress(mut input: &[u8], output: &mut [u8]) -> Result<usize, ()> {
    let mut decoder = FrameDecoder::new();
    decoder.init(&mut input).map_err(|_| ())?;

    let mut output_offset = 0;
    while !input.is_empty() {
        decoder
            .decode_blocks(&mut input, BlockDecodingStrategy::UptoBlocks(1))
            .map_err(|_| ())?;

        while decoder.can_collect() > 0 {
            if output_offset >= output.len() {
                return Err(());
            }
            let bytes_written = decoder.read(&mut output[output_offset..]).map_err(|_| ())?;
            if bytes_written == 0 {
                break;
            }
            output_offset += bytes_written;
        }

        if decoder.is_finished() {
            break;
        }
    }

    Ok(output_offset)
}

pub fn decompress_to_vec(mut input: &[u8]) -> Result<alloc::vec::Vec<u8>, ()> {
    let mut decoder = FrameDecoder::new();
    decoder.init(&mut input).map_err(|_| ())?;

    let mut out = alloc::vec::Vec::new();
    let mut chunk = [0u8; 16384];
    loop {
        if !input.is_empty() {
            decoder
                .decode_blocks(&mut input, BlockDecodingStrategy::UptoBlocks(1))
                .map_err(|_| ())?;
        }
        while decoder.can_collect() > 0 {
            let n = decoder.read(&mut chunk).map_err(|_| ())?;
            if n == 0 {
                break;
            }
            out.extend_from_slice(&chunk[..n]);
        }
        if decoder.is_finished() && decoder.can_collect() == 0 {
            break;
        }
        if input.is_empty() && !decoder.is_finished() {
            decoder
                .decode_blocks(&mut input, BlockDecodingStrategy::All)
                .map_err(|_| ())?;
            while decoder.can_collect() > 0 {
                let n = decoder.read(&mut chunk).map_err(|_| ())?;
                if n == 0 {
                    break;
                }
                out.extend_from_slice(&chunk[..n]);
            }
            break;
        }
    }
    Ok(out)
}
