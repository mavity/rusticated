//! Single-pass `#![no_std]` WASM-to-CLIF translator and PAL-backed JIT code arena emitter.

use crate::abi::{MEM_CODE_EXEC_RX, MEM_CODE_JIT_RW, MEM_READWRITE, abi, globals, pal_fn};
use alloc::vec;
use alloc::vec::Vec;
use core::ptr;
use cranelift_codegen::binemit::Reloc;
use cranelift_codegen::ir::condcodes::IntCC;
use cranelift_codegen::ir::types::{F32, F64, I32, I64};
use cranelift_codegen::ir::{
    self, AbiParam, ExternalName, Function, InstBuilder, MemFlagsData, Signature, TrapCode,
    UserExternalName, UserFuncName, Value,
};
use cranelift_codegen::isa::{self, CallConv, TargetFrontendConfig};
use cranelift_codegen::settings;
use cranelift_codegen::{Context, FinalizedMachReloc, FinalizedRelocTarget};
use cranelift_frontend::{FunctionBuilder, FunctionBuilderContext, Variable};
use target_lexicon::{
    Architecture, BinaryFormat, Environment, OperatingSystem, PointerWidth, Triple, Vendor,
};
use wasmparser::{BinaryReaderError, FuncType, Imports, Operator, Parser, Payload, ValType};

const WASM_PAGE_SIZE: usize = 65536;
const VENEER_SIZE: usize = 16;

#[derive(Clone)]
struct CompiledFuncBlob {
    code: Vec<u8>,
    relocs: Vec<ResolvedReloc>,
    veneer_count: usize,
}

#[derive(Clone)]
struct ResolvedReloc {
    offset: u32,
    kind: Reloc,
    target_func_idx: u32,
    addend: i64,
}

enum ControlFrame {
    Block {
        end_block: ir::Block,
        num_results: usize,
        stack_depth: usize,
        end_reached: bool,
    },
    Loop {
        header_block: ir::Block,
        end_block: ir::Block,
        num_params: usize,
        num_results: usize,
        stack_depth: usize,
        end_reached: bool,
    },
    If {
        else_block: ir::Block,
        end_block: ir::Block,
        num_results: usize,
        stack_depth: usize,
        has_else: bool,
        head_reachable: bool,
        then_ends_reachable: bool,
        end_reached: bool,
    },
}

fn valtype_to_ir(ty: ValType) -> ir::Type {
    match ty {
        ValType::I32 => I32,
        ValType::I64 => I64,
        ValType::F32 => F32,
        ValType::F64 => F64,
        _ => I64,
    }
}

fn functype_to_sig(ft: &FuncType, call_conv: CallConv) -> Signature {
    let mut sig = Signature::new(call_conv);
    for &p in ft.params() {
        sig.params.push(AbiParam::new(valtype_to_ir(p)));
    }
    for &r in ft.results() {
        sig.returns.push(AbiParam::new(valtype_to_ir(r)));
    }
    sig
}

#[cfg(target_arch = "x86_64")]
pub type ConcreteTargetIsa = cranelift_codegen::isa::x64::X64Backend;
#[cfg(target_arch = "aarch64")]
pub type ConcreteTargetIsa = cranelift_codegen::isa::aarch64::AArch64Backend;

pub fn create_target_isa() -> ConcreteTargetIsa {
    // Construct shared::Flags directly from its 12-byte representation (zero DESCRIPTORS / TEMPLATE tables!):
    // Byte 0: regalloc_algorithm = 1 (SinglePass)
    // Byte 1: opt_level = 0 (None)
    // Byte 4: probestack_size_log2 = 12
    let mut flag_bytes = [0u8; 12];
    flag_bytes[0] = 1;
    flag_bytes[4] = 12;
    let flags: settings::Flags = unsafe { core::mem::transmute(flag_bytes) };

    #[cfg(target_arch = "x86_64")]
    {
        let triple = Triple {
            architecture: Architecture::X86_64,
            vendor: Vendor::Unknown,
            operating_system: OperatingSystem::None_,
            environment: Environment::Unknown,
            binary_format: BinaryFormat::Unknown,
        };
        let x64_flags: cranelift_codegen::isa::x64::settings::Flags =
            unsafe { core::mem::zeroed() };
        cranelift_codegen::isa::x64::X64Backend::new_with_flags(triple, flags, x64_flags).unwrap()
    }

    #[cfg(target_arch = "aarch64")]
    {
        let triple = Triple {
            architecture: Architecture::Aarch64(target_lexicon::Aarch64Architecture::Aarch64),
            vendor: Vendor::Unknown,
            operating_system: OperatingSystem::None_,
            environment: Environment::Unknown,
            binary_format: BinaryFormat::Unknown,
        };
        let arm64_flags: cranelift_codegen::isa::aarch64::settings::Flags =
            unsafe { core::mem::zeroed() };
        cranelift_codegen::isa::aarch64::AArch64Backend::new_with_flags(triple, flags, arm64_flags)
    }
}

#[inline(always)]
pub fn default_frontend_config() -> TargetFrontendConfig {
    TargetFrontendConfig {
        default_call_conv: default_call_conv(),
        pointer_width: PointerWidth::U64,
        page_size_align_log2: 12,
    }
}

#[inline(always)]
pub fn default_call_conv() -> CallConv {
    #[cfg(target_arch = "x86_64")]
    {
        CallConv::WindowsFastcall
    }
    #[cfg(target_arch = "aarch64")]
    {
        CallConv::SystemV
    }
}

pub unsafe fn compile_and_initialize_wasm() {
    let host = abi();
    if host.wasm_bytes.is_null() || host.wasm_len == 0 {
        return;
    }
    (host.rusticated_debug)(1);
    let wasm = core::slice::from_raw_parts(host.wasm_bytes, host.wasm_len);
    let isa = create_target_isa();
    let call_conv = default_call_conv();

    let mut types: Vec<FuncType> = Vec::new();
    let mut func_type_indices: Vec<u32> = Vec::new();
    let mut num_imported_funcs: u32 = 0;
    let mut run_export_func_idx: Option<u32> = None;
    let mut initial_mem_pages: usize = 1;
    let mut max_mem_pages: usize = 256;
    let mut code_bodies: Vec<wasmparser::FunctionBody<'_>> = Vec::new();

    for payload in Parser::new(0).parse_all(wasm) {
        let Ok(payload) = payload else {
            (host.host_panic)(b"wasm parse error".as_ptr(), 16);
        };
        match payload {
            Payload::TypeSection(reader) => {
                for rec_group in reader.into_iter_err_on_gc_types() {
                    if let Ok(ft) = rec_group {
                        types.push(ft);
                    }
                }
            }
            Payload::ImportSection(reader) => {
                for imports in reader {
                    if let Ok(Imports::Single(_, imp)) = imports {
                        if let wasmparser::TypeRef::Func(type_idx) = imp.ty {
                            func_type_indices.push(type_idx);
                            num_imported_funcs += 1;
                        }
                    }
                }
            }
            Payload::FunctionSection(reader) => {
                for type_idx in reader {
                    if let Ok(idx) = type_idx {
                        func_type_indices.push(idx);
                    }
                }
            }
            Payload::MemorySection(reader) => {
                for mem in reader {
                    if let Ok(m) = mem {
                        initial_mem_pages = m.initial as usize;
                        if let Some(max) = m.maximum {
                            max_mem_pages = max as usize;
                        }
                    }
                }
            }
            Payload::ExportSection(reader) => {
                for exp in reader {
                    if let Ok(e) = exp {
                        if e.kind == wasmparser::ExternalKind::Func
                            && (e.name == "run" || e.name == "_initialize" || e.name == "_start")
                        {
                            run_export_func_idx = Some(e.index);
                        }
                    }
                }
            }
            Payload::CodeSectionEntry(body) => {
                code_bodies.push(body);
            }
            _ => {}
        }
    }
    (host.rusticated_debug)(2);

    // 1. Allocate guest linear memory via PAL
    let g = globals();
    let mem_len = initial_mem_pages.max(1) * WASM_PAGE_SIZE;
    let mem_cap = max_mem_pages.max(initial_mem_pages).max(1) * WASM_PAGE_SIZE;
    (host.rusticated_debug)(10);
    let mem_ptr = (host.alloc_mem)(mem_len, WASM_PAGE_SIZE, MEM_READWRITE);
    if mem_ptr.is_null() {
        (host.host_panic)(b"guest memory allocation failed".as_ptr(), 30);
    }
    (host.rusticated_debug)(11);
    g.guest_mem_base = mem_ptr;
    g.guest_mem_len = mem_len;
    g.guest_mem_cap = mem_cap;

    // 2. Compile each WASM function with Cranelift
    let mut ctx = Context::new();
    let mut fb_ctx = FunctionBuilderContext::new();
    let mut ctrl_plane = Default::default();
    let mut compiled_blobs: Vec<CompiledFuncBlob> = Vec::with_capacity(code_bodies.len());

    for (local_idx, body) in code_bodies.iter().enumerate() {
        let stage = 100 + (local_idx as i32) * 10;
        let func_idx = num_imported_funcs + (local_idx as u32);
        let sig_idx = func_type_indices[func_idx as usize] as usize;
        let sig = functype_to_sig(&types[sig_idx], call_conv);

        ctx.clear();
        ctx.func = Function::with_name_signature(UserFuncName::user(0, func_idx), sig);

        let g_mem_base = g.guest_mem_base;
        let g_mem_len = g.guest_mem_len;
        (host.rusticated_debug)(stage);
        if translate_wasm_body_with_mem(
            body,
            &mut ctx.func,
            &mut fb_ctx,
            default_frontend_config(),
            &types,
            &func_type_indices,
            call_conv,
            g_mem_base,
            g_mem_len,
        )
        .is_err()
        {
            (host.host_panic)(b"wasm translation failed".as_ptr(), 23);
        }
        (host.rusticated_debug)(stage + 1);
        (host.rusticated_debug)(stage + 2);
        if ctx
            .compile_with_hook(&isa, &mut ctrl_plane, |phase| {
                (host.rusticated_debug)(stage + 10 + phase as i32);
            })
            .is_err()
        {
            (host.host_panic)(b"cranelift compile failed".as_ptr(), 24);
        }
        (host.rusticated_debug)(stage + 3);
        let compiled = ctx.compiled_code().unwrap();

        let mut relocs = Vec::new();
        let mut veneer_count = 0;
        for &FinalizedMachReloc {
            offset,
            kind,
            ref target,
            addend,
        } in compiled.buffer.relocs()
        {
            if let FinalizedRelocTarget::ExternalName(ExternalName::User(nameref)) = target {
                let ext_name = &ctx.func.params.user_named_funcs()[*nameref];
                if matches!(
                    kind,
                    Reloc::Arm64Call | Reloc::X86CallPCRel4 | Reloc::X86CallPLTRel4
                ) {
                    veneer_count += 1;
                }
                relocs.push(ResolvedReloc {
                    offset,
                    kind,
                    target_func_idx: ext_name.index,
                    addend,
                });
            }
        }

        compiled_blobs.push(CompiledFuncBlob {
            code: compiled.code_buffer().to_vec(),
            relocs,
            veneer_count,
        });
    }
    (host.rusticated_debug)(2000);

    // 3. Pack compiled functions into a contiguous RW JIT arena & patch relocations
    let mut func_offsets: Vec<usize> = Vec::with_capacity(compiled_blobs.len());
    let mut total_code_size: usize = 0;
    for blob in &compiled_blobs {
        total_code_size = (total_code_size + 15) & !15;
        func_offsets.push(total_code_size);
        total_code_size += blob.code.len() + blob.veneer_count * VENEER_SIZE;
    }

    if total_code_size == 0 {
        return;
    }

    let arena_size = (total_code_size + 16383) & !16383;
    (host.rusticated_debug)(2001);
    let code_base = (host.alloc_mem)(arena_size, 16384, MEM_CODE_JIT_RW);
    if code_base.is_null() {
        (host.host_panic)(b"jit code allocation failed".as_ptr(), 26);
    }

    for (i, blob) in compiled_blobs.iter().enumerate() {
        let dst = code_base.add(func_offsets[i]);
        ptr::copy_nonoverlapping(blob.code.as_ptr(), dst, blob.code.len());
    }
    (host.rusticated_debug)(2002);

    // Patch intra-module calls
    for (i, blob) in compiled_blobs.iter().enumerate() {
        let func_ptr = code_base.add(func_offsets[i]);
        let mut next_veneer = 0usize;
        for reloc in &blob.relocs {
            if reloc.target_func_idx < num_imported_funcs {
                continue;
            }
            let target_local = (reloc.target_func_idx - num_imported_funcs) as usize;
            let target_addr = code_base.add(func_offsets[target_local]) as isize
                + (reloc.addend as isize);
            let at = func_ptr.add(reloc.offset as usize);

            match reloc.kind {
                Reloc::X86PCRel4 | Reloc::X86CallPCRel4 | Reloc::X86CallPLTRel4 => {
                    let delta = target_addr - (at as isize);
                    if let Ok( disp32 ) = i32::try_from(delta) {
                        ptr::write_unaligned(at.cast::<i32>(), disp32);
                    } else {
                        let veneer = func_ptr.add(blob.code.len() + next_veneer * VENEER_SIZE);
                        next_veneer += 1;
                        ptr::copy_nonoverlapping([0xffu8, 0x25, 0, 0, 0, 0].as_ptr(), veneer, 6);
                        ptr::write_unaligned(veneer.add(6).cast::<u64>(), (target_addr + 4) as u64);
                        let v_disp = i32::try_from((veneer as isize) - (at as isize) - 4).unwrap();
                        ptr::write_unaligned(at.cast::<i32>(), v_disp);
                    }
                }
                Reloc::Arm64Call => {
                    let diff = (target_addr - (at as isize)) >> 2;
                    let iptr = at.cast::<u32>();
                    if (diff >> 25 == -1) || (diff >> 25 == 0) {
                        let imm26 = (diff as u32) & 0x03ff_ffff;
                        let inst = ptr::read_unaligned(iptr);
                        ptr::write_unaligned(iptr, inst | imm26);
                    } else {
                        let veneer = func_ptr.add(blob.code.len() + next_veneer * VENEER_SIZE);
                        next_veneer += 1;
                        ptr::write_unaligned(veneer.cast::<u32>(), 0x5800_0050); // ldr x16, #8
                        ptr::write_unaligned(veneer.add(4).cast::<u32>(), 0xd61f_0200); // br x16
                        ptr::write_unaligned(veneer.add(8).cast::<u64>(), target_addr as u64);
                        let v_diff = ((veneer as isize) - (at as isize)) >> 2;
                        let imm26 = (v_diff as u32) & 0x03ff_ffff;
                        let inst = ptr::read_unaligned(iptr);
                        ptr::write_unaligned(iptr, inst | imm26);
                    }
                }
                _ => {}
            }
        }
    }
    (host.rusticated_debug)(2003);

    // 4. Finalize W^X permissions and instruction cache
    (host.protect_mem)(code_base, arena_size, MEM_CODE_EXEC_RX);
    (host.rusticated_debug)(2004);
    (host.flush_icache)(code_base, arena_size);
    (host.rusticated_debug)(2005);
    g.jit_code_base = code_base;
    g.jit_code_len = arena_size;

    if let Some(run_idx) = run_export_func_idx {
        if run_idx >= num_imported_funcs {
            let local_run = (run_idx - num_imported_funcs) as usize;
            let fn_ptr = code_base.add(func_offsets[local_run]);
            g.guest_run_fn = Some(core::mem::transmute::<*mut u8, pal_fn!(fn())>(fn_ptr));
        }
    }
}

pub fn translate_wasm_body_with_mem(
    body: &wasmparser::FunctionBody<'_>,
    func: &mut Function,
    fb_ctx: &mut FunctionBuilderContext,
    frontend_cfg: isa::TargetFrontendConfig,
    types: &[FuncType],
    func_type_indices: &[u32],
    call_conv: CallConv,
    guest_mem_base: *mut u8,
    guest_mem_len: usize,
) -> Result<(), BinaryReaderError> {
    let mut builder = FunctionBuilder::new(func, fb_ctx);
    let entry_block = builder.create_block();
    builder.append_block_params_for_function_params(entry_block);
    builder.switch_to_block(entry_block);
    builder.seal_block(entry_block);

    let num_params = builder.func.signature.params.len();
    for i in 0..num_params {
        let param_ty = builder.func.signature.params[i].value_type;
        let var = builder.declare_var(param_ty);
        let val = builder.block_params(entry_block)[i];
        builder.def_var(var, val);
    }

    let mut locals_reader = body.get_locals_reader()?;
    for _ in 0..locals_reader.get_count() {
        let (count, wasm_ty) = locals_reader.read()?;
        let ir_ty = valtype_to_ir(wasm_ty);
        let zero = match ir_ty {
            I32 => builder.ins().iconst(I32, 0),
            I64 => builder.ins().iconst(I64, 0),
            F32 => builder.ins().f32const(ir::immediates::Ieee32::with_bits(0)),
            _ => builder.ins().f64const(ir::immediates::Ieee64::with_bits(0)),
        };
        for _ in 0..count {
            let var = builder.declare_var(ir_ty);
            builder.def_var(var, zero);
        }
    }

    let exit_block = builder.create_block();
    let num_func_results = builder.func.signature.returns.len();
    for i in 0..num_func_results {
        let ret_ty = builder.func.signature.returns[i].value_type;
        builder.append_block_param(exit_block, ret_ty);
    }

    let mut val_stack: Vec<Value> = Vec::new();
    let mut ctrl_stack: Vec<ControlFrame> = vec![ControlFrame::Block {
        end_block: exit_block,
        num_results: num_func_results,
        stack_depth: 0,
        end_reached: false,
    }];
    let mut reachable = true;

    let mut ops_reader = body.get_operators_reader()?;
    while !ops_reader.eof() {
        let op = ops_reader.read()?;
        if !reachable {
            match op {
                Operator::Block { blockty } => {
                    let (_, num_results) = blocktype_arity(blockty, types);
                    let end_block = builder.create_block();
                    ctrl_stack.push(ControlFrame::Block {
                        end_block,
                        num_results,
                        stack_depth: val_stack.len(),
                        end_reached: false,
                    });
                }
                Operator::Loop { blockty } => {
                    let (num_params, num_results) = blocktype_arity(blockty, types);
                    let header_block = builder.create_block();
                    let end_block = builder.create_block();
                    ctrl_stack.push(ControlFrame::Loop {
                        header_block,
                        end_block,
                        num_params,
                        num_results,
                        stack_depth: val_stack.len(),
                        end_reached: false,
                    });
                }
                Operator::If { blockty } => {
                    let (_, num_results) = blocktype_arity(blockty, types);
                    let else_block = builder.create_block();
                    let end_block = builder.create_block();
                    ctrl_stack.push(ControlFrame::If {
                        else_block,
                        end_block,
                        num_results,
                        stack_depth: val_stack.len(),
                        has_else: false,
                        head_reachable: false,
                        then_ends_reachable: false,
                        end_reached: false,
                    });
                }
                Operator::Else => {
                    if let Some(ControlFrame::If {
                        else_block,
                        stack_depth,
                        has_else,
                        head_reachable,
                        ..
                    }) = ctrl_stack.last_mut()
                    {
                        *has_else = true;
                        val_stack.truncate(*stack_depth);
                        if *head_reachable {
                            builder.switch_to_block(*else_block);
                            builder.seal_block(*else_block);
                            reachable = true;
                        }
                    }
                }
                Operator::End => {
                    let Some(frame) = ctrl_stack.pop() else {
                        break;
                    };
                    match frame {
                        ControlFrame::Block {
                            end_block,
                            stack_depth,
                            end_reached,
                            ..
                        } => {
                            val_stack.truncate(stack_depth);
                            builder.switch_to_block(end_block);
                            builder.seal_block(end_block);
                            if end_reached {
                                val_stack.extend_from_slice(builder.block_params(end_block));
                                reachable = true;
                            }
                        }
                        ControlFrame::Loop {
                            header_block,
                            end_block,
                            stack_depth,
                            end_reached,
                            ..
                        } => {
                            builder.seal_block(header_block);
                            val_stack.truncate(stack_depth);
                            builder.switch_to_block(end_block);
                            builder.seal_block(end_block);
                            if end_reached {
                                val_stack.extend_from_slice(builder.block_params(end_block));
                                reachable = true;
                            }
                        }
                        ControlFrame::If {
                            else_block,
                            end_block,
                            num_results,
                            stack_depth,
                            has_else,
                            head_reachable,
                            then_ends_reachable,
                            end_reached,
                        } => {
                            if !has_else && head_reachable {
                                builder.switch_to_block(else_block);
                                builder.seal_block(else_block);
                                if num_results == 0 {
                                    builder.ins().jump(end_block, &[]);
                                }
                            }
                            val_stack.truncate(stack_depth);
                            builder.switch_to_block(end_block);
                            builder.seal_block(end_block);
                            if end_reached || then_ends_reachable || (!has_else && head_reachable) {
                                val_stack.extend_from_slice(builder.block_params(end_block));
                                reachable = true;
                            }
                        }
                    }
                }
                _ => {}
            }
            continue;
        }

        match op {
            Operator::Unreachable => {
                builder.ins().trap(TrapCode::user(1).unwrap());
                reachable = false;
            }
            Operator::Nop => {}
            Operator::Block { blockty } => {
                let (_, num_results) = blocktype_arity(blockty, types);
                let end_block = builder.create_block();
                append_blockty_results(&mut builder, end_block, blockty, types);
                ctrl_stack.push(ControlFrame::Block {
                    end_block,
                    num_results,
                    stack_depth: val_stack.len(),
                    end_reached: false,
                });
            }
            Operator::Loop { blockty } => {
                let (num_params, num_results) = blocktype_arity(blockty, types);
                let header_block = builder.create_block();
                let end_block = builder.create_block();
                append_blockty_results(&mut builder, end_block, blockty, types);
                builder.ins().jump(header_block, &[]);
                builder.switch_to_block(header_block);
                ctrl_stack.push(ControlFrame::Loop {
                    header_block,
                    end_block,
                    num_params,
                    num_results,
                    stack_depth: val_stack.len(),
                    end_reached: false,
                });
            }
            Operator::If { blockty } => {
                let cond = val_stack.pop().unwrap();
                let (_, num_results) = blocktype_arity(blockty, types);
                let then_block = builder.create_block();
                let else_block = builder.create_block();
                let end_block = builder.create_block();
                append_blockty_results(&mut builder, end_block, blockty, types);
                builder.ins().brif(cond, then_block, &[], else_block, &[]);
                builder.seal_block(then_block);
                builder.switch_to_block(then_block);
                ctrl_stack.push(ControlFrame::If {
                    else_block,
                    end_block,
                    num_results,
                    stack_depth: val_stack.len(),
                    has_else: false,
                    head_reachable: true,
                    then_ends_reachable: false,
                    end_reached: false,
                });
            }
            Operator::Else => {
                if let Some(ControlFrame::If {
                    else_block,
                    end_block,
                    num_results,
                    stack_depth,
                    has_else,
                    then_ends_reachable,
                    ..
                }) = ctrl_stack.last_mut()
                {
                    *has_else = true;
                    *then_ends_reachable = true;
                    let args_start = val_stack.len() - *num_results;
                    let args: smallvec::SmallVec<[ir::BlockArg; 4]> = val_stack[args_start..]
                        .iter()
                        .copied()
                        .map(ir::BlockArg::Value)
                        .collect();
                    builder.ins().jump(*end_block, &args);
                    val_stack.truncate(*stack_depth);
                    builder.switch_to_block(*else_block);
                    builder.seal_block(*else_block);
                }
            }
            Operator::End => {
                let Some(frame) = ctrl_stack.pop() else {
                    break;
                };
                match frame {
                    ControlFrame::Block {
                        end_block,
                        num_results,
                        stack_depth,
                        ..
                    } => {
                        let args_start = val_stack.len() - num_results;
                        let args: smallvec::SmallVec<[ir::BlockArg; 4]> = val_stack[args_start..]
                            .iter()
                            .copied()
                            .map(ir::BlockArg::Value)
                            .collect();
                        builder.ins().jump(end_block, &args);
                        val_stack.truncate(stack_depth);
                        builder.switch_to_block(end_block);
                        builder.seal_block(end_block);
                        val_stack.extend_from_slice(builder.block_params(end_block));
                    }
                    ControlFrame::Loop {
                        header_block,
                        end_block,
                        num_results,
                        stack_depth,
                        ..
                    } => {
                        builder.seal_block(header_block);
                        let args_start = val_stack.len() - num_results;
                        let args: smallvec::SmallVec<[ir::BlockArg; 4]> = val_stack[args_start..]
                            .iter()
                            .copied()
                            .map(ir::BlockArg::Value)
                            .collect();
                        builder.ins().jump(end_block, &args);
                        val_stack.truncate(stack_depth);
                        builder.switch_to_block(end_block);
                        builder.seal_block(end_block);
                        val_stack.extend_from_slice(builder.block_params(end_block));
                    }
                    ControlFrame::If {
                        else_block,
                        end_block,
                        num_results,
                        stack_depth,
                        has_else,
                        ..
                    } => {
                        let args_start = val_stack.len() - num_results;
                        let args: smallvec::SmallVec<[ir::BlockArg; 4]> = val_stack[args_start..]
                            .iter()
                            .copied()
                            .map(ir::BlockArg::Value)
                            .collect();
                        builder.ins().jump(end_block, &args);
                        if !has_else {
                            builder.switch_to_block(else_block);
                            builder.seal_block(else_block);
                            builder.ins().jump(end_block, &[]);
                        }
                        val_stack.truncate(stack_depth);
                        builder.switch_to_block(end_block);
                        builder.seal_block(end_block);
                        val_stack.extend_from_slice(builder.block_params(end_block));
                    }
                }
            }
            Operator::Br { relative_depth } => {
                let target_idx = ctrl_stack.len() - 1 - (relative_depth as usize);
                let (target_block, arity) = branch_target_and_arity(&mut ctrl_stack[target_idx]);
                let args_start = val_stack.len() - arity;
                let args: smallvec::SmallVec<[ir::BlockArg; 4]> = val_stack[args_start..]
                    .iter()
                    .copied()
                    .map(ir::BlockArg::Value)
                    .collect();
                builder.ins().jump(target_block, &args);
                reachable = false;
            }
            Operator::BrIf { relative_depth } => {
                let cond = val_stack.pop().unwrap();
                let target_idx = ctrl_stack.len() - 1 - (relative_depth as usize);
                let (target_block, arity) = branch_target_and_arity(&mut ctrl_stack[target_idx]);
                let args_start = val_stack.len() - arity;
                let args: smallvec::SmallVec<[ir::BlockArg; 4]> = val_stack[args_start..]
                    .iter()
                    .copied()
                    .map(ir::BlockArg::Value)
                    .collect();
                let fallthrough = builder.create_block();
                builder.ins().brif(cond, target_block, &args, fallthrough, &[]);
                builder.seal_block(fallthrough);
                builder.switch_to_block(fallthrough);
            }
            Operator::Return => {
                let arity = num_func_results;
                let args_start = val_stack.len() - arity;
                builder.ins().return_(&val_stack[args_start..]);
                reachable = false;
            }
            Operator::Call { function_index } => {
                let sig_idx = func_type_indices[function_index as usize] as usize;
                let sig = functype_to_sig(&types[sig_idx], call_conv);
                let num_args = sig.params.len();
                let sig_ref = builder.import_signature(sig);
                let user_name_ref = builder
                    .func
                    .declare_imported_user_function(UserExternalName::new(0, function_index));
                let ext_func = builder.import_function(ir::ExtFuncData {
                    name: ExternalName::User(user_name_ref),
                    signature: sig_ref,
                    colocated: false,
                    patchable: false,
                });
                let args_start = val_stack.len() - num_args;
                let call_inst = builder.ins().call(ext_func, &val_stack[args_start..]);
                val_stack.truncate(args_start);
                val_stack.extend_from_slice(builder.inst_results(call_inst));
            }
            Operator::Drop => {
                let _ = val_stack.pop();
            }
            Operator::Select => {
                let c = val_stack.pop().unwrap();
                let v2 = val_stack.pop().unwrap();
                let v1 = val_stack.pop().unwrap();
                val_stack.push(builder.ins().select(c, v1, v2));
            }
            Operator::LocalGet { local_index } => {
                let var = Variable::from_u32(local_index);
                val_stack.push(builder.use_var(var));
            }
            Operator::LocalSet { local_index } => {
                let val = val_stack.pop().unwrap();
                let var = Variable::from_u32(local_index);
                builder.def_var(var, val);
            }
            Operator::LocalTee { local_index } => {
                let val = *val_stack.last().unwrap();
                let var = Variable::from_u32(local_index);
                builder.def_var(var, val);
            }
            Operator::I32Const { value } => {
                val_stack.push(builder.ins().iconst(I32, value as u32 as i64));
            }
            Operator::I64Const { value } => {
                val_stack.push(builder.ins().iconst(I64, value));
            }
            Operator::F32Const { value } => {
                val_stack.push(
                    builder
                        .ins()
                        .f32const(ir::immediates::Ieee32::with_bits(value.bits())),
                );
            }
            Operator::F64Const { value } => {
                val_stack.push(
                    builder
                        .ins()
                        .f64const(ir::immediates::Ieee64::with_bits(value.bits())),
                );
            }
            Operator::I32Load { memarg } => {
                let addr = emit_checked_mem_addr(&mut builder, &mut val_stack, memarg.offset as u32, 4, guest_mem_base, guest_mem_len);
                let val = builder.ins().load(I32, MemFlagsData::new(), addr, 0);
                val_stack.push(val);
            }
            Operator::I64Load { memarg } => {
                let addr = emit_checked_mem_addr(&mut builder, &mut val_stack, memarg.offset as u32, 8, guest_mem_base, guest_mem_len);
                let val = builder.ins().load(I64, MemFlagsData::new(), addr, 0);
                val_stack.push(val);
            }
            Operator::I32Store { memarg } | Operator::I64Store { memarg } => {
                let val = val_stack.pop().unwrap();
                let size = if matches!(op, Operator::I32Store { .. }) { 4 } else { 8 };
                let addr = emit_checked_mem_addr(&mut builder, &mut val_stack, memarg.offset as u32, size, guest_mem_base, guest_mem_len);
                builder.ins().store(MemFlagsData::new(), val, addr, 0);
            }
            Operator::I32Eqz | Operator::I64Eqz => {
                let v = val_stack.pop().unwrap();
                let cmp = builder.ins().icmp_imm_s(IntCC::Equal, v, 0);
                val_stack.push(builder.ins().uextend(I32, cmp));
            }
            Operator::I32Eq | Operator::I64Eq => emit_icmp(&mut builder, &mut val_stack, IntCC::Equal),
            Operator::I32Ne | Operator::I64Ne => emit_icmp(&mut builder, &mut val_stack, IntCC::NotEqual),
            Operator::I32LtS | Operator::I64LtS => emit_icmp(&mut builder, &mut val_stack, IntCC::SignedLessThan),
            Operator::I32LtU | Operator::I64LtU => emit_icmp(&mut builder, &mut val_stack, IntCC::UnsignedLessThan),
            Operator::I32GtS | Operator::I64GtS => emit_icmp(&mut builder, &mut val_stack, IntCC::SignedGreaterThan),
            Operator::I32GtU | Operator::I64GtU => emit_icmp(&mut builder, &mut val_stack, IntCC::UnsignedGreaterThan),
            Operator::I32LeS | Operator::I64LeS => emit_icmp(&mut builder, &mut val_stack, IntCC::SignedLessThanOrEqual),
            Operator::I32LeU | Operator::I64LeU => emit_icmp(&mut builder, &mut val_stack, IntCC::UnsignedLessThanOrEqual),
            Operator::I32GeS | Operator::I64GeS => emit_icmp(&mut builder, &mut val_stack, IntCC::SignedGreaterThanOrEqual),
            Operator::I32GeU | Operator::I64GeU => emit_icmp(&mut builder, &mut val_stack, IntCC::UnsignedGreaterThanOrEqual),
            Operator::I32Add | Operator::I64Add => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().iadd(a, b));
            }
            Operator::I32Sub | Operator::I64Sub => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().isub(a, b));
            }
            Operator::I32Mul | Operator::I64Mul => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().imul(a, b));
            }
            Operator::I32And | Operator::I64And => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().band(a, b));
            }
            Operator::I32Or | Operator::I64Or => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().bor(a, b));
            }
            Operator::I32Xor | Operator::I64Xor => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().bxor(a, b));
            }
            Operator::I32Shl | Operator::I64Shl => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().ishl(a, b));
            }
            Operator::I32ShrS | Operator::I64ShrS => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().sshr(a, b));
            }
            Operator::I32ShrU | Operator::I64ShrU => {
                let b = val_stack.pop().unwrap();
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().ushr(a, b));
            }
            Operator::I64ExtendI32S => {
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().sextend(I64, a));
            }
            Operator::I64ExtendI32U => {
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().uextend(I64, a));
            }
            Operator::I32WrapI64 => {
                let a = val_stack.pop().unwrap();
                val_stack.push(builder.ins().ireduce(I32, a));
            }
            _ => {}
        }
    }

    if reachable {
        builder.ins().return_(&val_stack);
    }

    builder.finalize(frontend_cfg);
    Ok(())
}

fn blocktype_arity(blockty: wasmparser::BlockType, types: &[FuncType]) -> (usize, usize) {
    match blockty {
        wasmparser::BlockType::Empty => (0, 0),
        wasmparser::BlockType::Type(_) => (0, 1),
        wasmparser::BlockType::FuncType(idx) => {
            let ft = &types[idx as usize];
            (ft.params().len(), ft.results().len())
        }
    }
}

fn append_blockty_results(
    builder: &mut FunctionBuilder<'_>,
    block: ir::Block,
    blockty: wasmparser::BlockType,
    types: &[FuncType],
) {
    match blockty {
        wasmparser::BlockType::Empty => {}
        wasmparser::BlockType::Type(ty) => {
            builder.append_block_param(block, valtype_to_ir(ty));
        }
        wasmparser::BlockType::FuncType(idx) => {
            for &ty in types[idx as usize].results() {
                builder.append_block_param(block, valtype_to_ir(ty));
            }
        }
    }
}

fn branch_target_and_arity(frame: &mut ControlFrame) -> (ir::Block, usize) {
    match frame {
        ControlFrame::Block {
            end_block,
            num_results,
            end_reached,
            ..
        }
        | ControlFrame::If {
            end_block,
            num_results,
            end_reached,
            ..
        } => {
            *end_reached = true;
            (*end_block, *num_results)
        }
        ControlFrame::Loop {
            header_block,
            num_params,
            ..
        } => (*header_block, *num_params),
    }
}

fn emit_icmp(builder: &mut FunctionBuilder<'_>, val_stack: &mut Vec<Value>, cc: IntCC) {
    let b = val_stack.pop().unwrap();
    let a = val_stack.pop().unwrap();
    let cmp = builder.ins().icmp(cc, a, b);
    val_stack.push(builder.ins().uextend(I32, cmp));
}

fn emit_checked_mem_addr(
    builder: &mut FunctionBuilder<'_>,
    val_stack: &mut Vec<Value>,
    offset: u32,
    access_size: u8,
    guest_mem_base: *mut u8,
    guest_mem_len: usize,
) -> Value {
    let index32 = val_stack.pop().unwrap();
    let index64 = builder.ins().uextend(I64, index32);
    let end_offset = (offset as i64) + (access_size as i64);
    let end_addr = builder.ins().iadd_imm_s(index64, end_offset);

    let bound_val = builder.ins().iconst(I64, guest_mem_len as i64);
    let oob = builder
        .ins()
        .icmp(IntCC::UnsignedGreaterThan, end_addr, bound_val);
    builder.ins().trapnz(oob, TrapCode::HEAP_OUT_OF_BOUNDS);

    let base_val = builder.ins().iconst(I64, guest_mem_base as usize as i64);
    let native_base = builder.ins().iadd(base_val, index64);
    if offset == 0 {
        native_base
    } else {
        builder.ins().iadd_imm_s(native_base, offset as i64)
    }
}
