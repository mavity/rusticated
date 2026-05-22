use alloc_no_stdlib::{Allocator, SliceWrapper, SliceWrapperMut};
use brotli_decompressor::{BrotliDecompressStream, BrotliResult, HuffmanCode};

extern crate alloc;

pub struct AllocatedMemory<T: 'static>(&'static mut [T]);

impl<T> Default for AllocatedMemory<T> {
    fn default() -> Self {
        AllocatedMemory(&mut [])
    }
}

impl<T> SliceWrapper<T> for AllocatedMemory<T> {
    fn slice(&self) -> &[T] {
        self.0
    }
}

impl<T> SliceWrapperMut<T> for AllocatedMemory<T> {
    fn slice_mut(&mut self) -> &mut [T] {
        self.0
    }
}

#[derive(Default, Clone, Copy)]
pub struct HeapAllocator;

impl Allocator<u8> for HeapAllocator {
    type AllocatedMemory = AllocatedMemory<u8>;
    fn alloc_cell(&mut self, size: usize) -> Self::AllocatedMemory {
        unsafe {
            let layout = core::alloc::Layout::from_size_align_unchecked(size, core::mem::align_of::<u8>());
            let ptr = alloc::alloc::alloc(layout);
            if ptr.is_null() {
                AllocatedMemory(&mut [])
            } else {
                AllocatedMemory(core::slice::from_raw_parts_mut(ptr as *mut u8, size))
            }
        }
    }

    fn free_cell(&mut self, ptr: Self::AllocatedMemory) {
        unsafe {
            let size = core::mem::size_of_val(ptr.0);
            let layout = core::alloc::Layout::from_size_align_unchecked(size, core::mem::align_of::<u8>());
            alloc::alloc::dealloc(ptr.0.as_mut_ptr(), layout);
        }
    }
}

impl Allocator<u32> for HeapAllocator {
    type AllocatedMemory = AllocatedMemory<u32>;
    fn alloc_cell(&mut self, size: usize) -> Self::AllocatedMemory {
        unsafe {
            let layout = core::alloc::Layout::from_size_align_unchecked(size * 4, core::mem::align_of::<u32>());
            let ptr = alloc::alloc::alloc(layout);
            if ptr.is_null() {
                AllocatedMemory(&mut [])
            } else {
                AllocatedMemory(core::slice::from_raw_parts_mut(ptr as *mut u32, size))
            }
        }
    }

    fn free_cell(&mut self, ptr: Self::AllocatedMemory) {
        unsafe {
            let size = core::mem::size_of_val(ptr.0);
            let layout = core::alloc::Layout::from_size_align_unchecked(size, core::mem::align_of::<u32>());
            alloc::alloc::dealloc(ptr.0.as_mut_ptr() as *mut u8, layout);
        }
    }
}

impl Allocator<HuffmanCode> for HeapAllocator {
    type AllocatedMemory = AllocatedMemory<HuffmanCode>;
    fn alloc_cell(&mut self, size: usize) -> Self::AllocatedMemory {
        unsafe {
            let layout = core::alloc::Layout::from_size_align_unchecked(size * core::mem::size_of::<HuffmanCode>(), core::mem::align_of::<HuffmanCode>());
            let ptr = alloc::alloc::alloc(layout);
            if ptr.is_null() {
                AllocatedMemory(&mut [])
            } else {
                AllocatedMemory(core::slice::from_raw_parts_mut(ptr as *mut HuffmanCode, size))
            }
        }
    }

    fn free_cell(&mut self, ptr: Self::AllocatedMemory) {
        unsafe {
            let size = core::mem::size_of_val(ptr.0);
            let layout = core::alloc::Layout::from_size_align_unchecked(size, core::mem::align_of::<HuffmanCode>());
            alloc::alloc::dealloc(ptr.0.as_mut_ptr() as *mut u8, layout);
        }
    }
}

pub fn decompress_to_writer<W: FnMut(&[u8])>(
    input: &[u8],
    mut writer: W,
) -> Result<(), ()> {
    let mut state = brotli_decompressor::BrotliState::new(HeapAllocator, HeapAllocator, HeapAllocator);
    
    let mut available_in = input.len();
    let mut input_offset = 0;
    
    let mut output_buf = [0u8; 4096];
    
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
            &mut 0, // total_out
            &mut state,
        );
        
        if output_offset > 0 {
            writer(&output_buf[..output_offset]);
        }
        
        match result {
            BrotliResult::ResultSuccess => return Ok(()),
            BrotliResult::ResultFailure => return Err(()),
            BrotliResult::NeedsMoreInput => {
                if available_in == 0 {
                    return Err(()); // Unexpected EOF
                }
            }
            BrotliResult::NeedsMoreOutput => {}
        }
    }
}


