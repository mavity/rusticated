use brotli_decompressor::{Allocator, SliceWrapper, SliceWrapperMut, BrotliDecompressStream, BrotliResult, HuffmanCode};

/// Box<[T]>-backed allocated memory region.
/// Uses heap allocation with proper initialization and alignment.
pub struct Rebox<T> {
    b: Box<[T]>,
}

impl<T> Default for Rebox<T> {
    fn default() -> Self {
        Rebox {
            b: Vec::new().into_boxed_slice(),
        }
    }
}

impl<T> SliceWrapper<T> for Rebox<T> {
    fn slice(&self) -> &[T] {
        &self.b
    }
}

impl<T> SliceWrapperMut<T> for Rebox<T> {
    fn slice_mut(&mut self) -> &mut [T] {
        &mut self.b
    }
}

#[derive(Default, Clone, Copy)]
pub struct HeapAllocator;

impl Allocator<u8> for HeapAllocator {
    type AllocatedMemory = Rebox<u8>;
    fn alloc_cell(&mut self, size: usize) -> Rebox<u8> {
        Rebox {
            b: vec![0u8; size].into_boxed_slice(),
        }
    }
    fn free_cell(&mut self, _data: Rebox<u8>) {}
}

impl Allocator<u32> for HeapAllocator {
    type AllocatedMemory = Rebox<u32>;
    fn alloc_cell(&mut self, size: usize) -> Rebox<u32> {
        Rebox {
            b: vec![0u32; size].into_boxed_slice(),
        }
    }
    fn free_cell(&mut self, _data: Rebox<u32>) {}
}

impl Allocator<HuffmanCode> for HeapAllocator {
    type AllocatedMemory = Rebox<HuffmanCode>;
    fn alloc_cell(&mut self, size: usize) -> Rebox<HuffmanCode> {
        Rebox {
            b: vec![HuffmanCode::default(); size].into_boxed_slice(),
        }
    }
    fn free_cell(&mut self, _data: Rebox<HuffmanCode>) {}
}

pub fn decompress_to_writer<W: FnMut(&[u8])>(input: &[u8], mut writer: W) -> Result<(), ()> {
    let mut state = Box::new(brotli_decompressor::BrotliState::new(
        HeapAllocator,
        HeapAllocator,
        HeapAllocator,
    ));

    let mut available_in = input.len();
    let mut input_offset = 0;

    let mut output_buf = vec![0u8; 65536]; // 64KB output buffer on heap

    loop {
        let mut available_out = output_buf.len();
        let mut output_offset = 0;

        let result = BrotliDecompressStream(
            &mut available_in,
            &mut input_offset,
            input,
            &mut available_out,
            &mut output_offset,
            &mut output_buf,
            &mut 0,
            &mut *state,
        );

        if output_offset > 0 {
            writer(&output_buf[..output_offset]);
        }

        match result {
            BrotliResult::ResultSuccess => return Ok(()),
            BrotliResult::ResultFailure => return Err(()),
            BrotliResult::NeedsMoreInput => {
                if available_in == 0 {
                    return Err(());
                }
            }
            BrotliResult::NeedsMoreOutput => {}
        }
    }
}
