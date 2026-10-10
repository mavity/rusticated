use ruzstd::frame_decoder::{BlockDecodingStrategy, FrameDecoder};
use ruzstd::io::Read;
use alloc::vec;

pub fn decompress(mut input: &[u8], output: &mut [u8]) -> Result<(), ()> {
    let mut decoder = FrameDecoder::new();
    decoder.init(&mut input).map_err(|_| ())?;
    
    let mut output_offset = 0;
    while !input.is_empty() {
        decoder.decode_blocks(&mut input, BlockDecodingStrategy::UptoBlocks(1)).map_err(|_| ())?;
        
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
    
    if output_offset != output.len() {
        return Err(());
    }
    Ok(())
}
