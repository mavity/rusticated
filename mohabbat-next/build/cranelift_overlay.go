package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	craneliftOverlayName   = "cranelift-codegen-overlay"
	bumpaloOverlayName     = "bumpalo-overlay"
	hashbrownOverlayName   = "hashbrown-overlay"
	indexmapOverlayName    = "indexmap-overlay"
	wasmparserOverlayName  = "wasmparser-overlay"
	regalloc2OverlayName   = "regalloc2-overlay"
	craneliftWorkspaceName = "runtime-cranelift-overlay"
)

type craneliftSourcePatch struct {
	name        string
	pattern     string
	replacement string
}

var contextIsaPatches = []craneliftSourcePatch{
	{
		name:        "Context::compile_and_emit ISA argument",
		pattern:     `(?s)(pub fn compile_and_emit\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
	{
		name:        "Context::compile_stencil ISA argument",
		pattern:     `(?s)(pub fn compile_stencil\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
	{
		name:        "Context::optimize ISA argument",
		pattern:     `(?s)(pub fn optimize\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
	{
		name:        "Context::compile ISA argument",
		pattern:     `(?s)(pub fn compile\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
	{
		name:        "Context::canonicalize_nans ISA argument",
		pattern:     `(?s)(pub fn canonicalize_nans\s*\(\s*&mut self,\s*isa:\s*)&dyn TargetIsa`,
		replacement: `$1&impl TargetIsa`,
	},
}

var calleeIsaPatch = craneliftSourcePatch{
	name:        "Callee::new ISA argument",
	pattern:     `(?s)(pub fn new\s*\(\s*f:\s*&ir::Function,\s*isa:\s*)&dyn TargetIsa`,
	replacement: `$1&impl TargetIsa`,
}

var flagsOrIsaPatch = craneliftSourcePatch{
	name:    "FlagsOrIsa generic concrete-ISA conversion",
	pattern: `impl<'a> From<&'a dyn TargetIsa> for FlagsOrIsa<'a> \{`,
	replacement: `impl<'a, T: TargetIsa> From<&'a T> for FlagsOrIsa<'a> {
    fn from(isa: &'a T) -> FlagsOrIsa<'a> {
        FlagsOrIsa {
            flags: isa.flags(),
            isa: Some(isa),
        }
    }
}

impl<'a> From<&'a dyn TargetIsa> for FlagsOrIsa<'a> {`,
}

var compileHookPatch = craneliftSourcePatch{
	name:    "Context::compile_with_hook compatibility wrapper",
	pattern: `(?s)(    /// Compile the function,\r?\n    ///\r?\n    /// Run the function through all the passes necessary to generate)`,
	replacement: `    /// Compile the function and report completion to a diagnostic hook.
    pub fn compile_with_hook(
        &mut self,
        isa: &impl TargetIsa,
        ctrl_plane: &mut ControlPlane,
        mut on_phase: impl FnMut(u8),
    ) -> CompileResult<'_, &CompiledCode> {
        let result = self.compile(isa, ctrl_plane);
        if result.is_ok() {
            on_phase(1);
        }
        result
    }

$1`,
}

var x64BackendVisibilityPatch = craneliftSourcePatch{
	name:        "X64Backend visibility for concrete runtime construction",
	pattern:     `pub\(crate\) struct X64Backend`,
	replacement: `pub struct X64Backend`,
}

var x64ConstructorVisibilityPatch = craneliftSourcePatch{
	name:        "X64Backend::new_with_flags visibility",
	pattern:     `(?m)^    fn new_with_flags\(`,
	replacement: `    pub fn new_with_flags(`,
}

var hashbrownManifestPatch = craneliftSourcePatch{
	name:        "disable hashbrown default hasher in freestanding Cranelift",
	pattern:     `(?s)(\[dependencies\.hashbrown\]\r?\nversion = "0\.17"\r?\n)features = \["default-hasher"\]\r?\n`,
	replacement: `$1`,
}

var noStdHashAliasesPatch = craneliftSourcePatch{
	name:    "use Fx hashing for freestanding Cranelift maps",
	pattern: `(?s)#\[cfg\(not\(feature = "std"\)\)\]\r?\nuse hashbrown::\{HashMap, HashSet, hash_map\};`,
	replacement: `#[cfg(not(feature = "std"))]
use hashbrown::{HashMap as HashBrownMap, HashSet as HashBrownSet, hash_map};
#[cfg(not(feature = "std"))]
type HashMap<K, V, S = rustc_hash::FxBuildHasher> = HashBrownMap<K, V, S>;
#[cfg(not(feature = "std"))]
type HashSet<V, S = rustc_hash::FxBuildHasher> = HashBrownSet<V, S>;`,
}

var hashMapNewPatch = craneliftSourcePatch{
	name:        "construct freestanding hash map with Fx hasher",
	pattern:     `HashMap::new\(\)`,
	replacement: `HashMap::with_hasher(rustc_hash::FxBuildHasher::default())`,
}

var hashMapCapacityPatch = craneliftSourcePatch{
	name:        "construct freestanding hash map with capacity and Fx hasher",
	pattern:     `HashMap::with_capacity\(expected_num_constants\)`,
	replacement: `HashMap::with_capacity_and_hasher(expected_num_constants, rustc_hash::FxBuildHasher::default())`,
}

var dynamicTypeHashMapCapacityPatch = craneliftSourcePatch{
	name:        "construct dynamic type map with capacity and Fx hasher",
	pattern:     `HashMap::with_capacity\(f\.dfg\.dynamic_types\.len\(\)\)`,
	replacement: `HashMap::with_capacity_and_hasher(f.dfg.dynamic_types.len(), rustc_hash::FxBuildHasher::default())`,
}

var canonicalizeNansVectorPatch = craneliftSourcePatch{
	name:        "avoid isa_flags in canonicalize_nans",
	pattern:     `(?s)let has_vector_support = match isa\.triple\(\)\.architecture \{.*?_ => true,\r?\n\s*\};`,
	replacement: `let has_vector_support = true;`,
}

var contextVerifyStubPatch = craneliftSourcePatch{
	name:        "stub Context::verify",
	pattern:     `(?s)(pub fn verify<'a, FOI: Into<FlagsOrIsa<'a>>>\(&self, )fisa: FOI(\) -> VerifierResult<\(\)> \{).*?(\r?\n    \}\r?\n\r?\n    /// Run the verifier)`,
	replacement: "${1}_fisa: FOI${2}\n        Ok(())\n    }\n\n    /// Run the verifier",
}

var contextVerifyIfStubPatch = craneliftSourcePatch{
	name:        "stub Context::verify_if",
	pattern:     `(?s)(pub fn verify_if<'a, FOI: Into<FlagsOrIsa<'a>>>\(&self, )fisa: FOI(\) -> CodegenResult<\(\)> \{).*?(\r?\n    \}\r?\n\r?\n    /// Perform constant-phi)`,
	replacement: "${1}_fisa: FOI${2}\n        Ok(())\n    }\n\n    /// Perform constant-phi",
}

var vcodeDisasmPatch = craneliftSourcePatch{
	name:        "elide vcode disassembly formatting",
	pattern:     `(?s)if want_disasm && !inst\.is_args\(\) \{\r?\n\s*let mut s = state\.clone\(\);\r?\n\s*writeln!\(disasm, "  \{\}", inst\.pretty_print_inst\(&mut s\)\)\.unwrap\(\);\r?\n\s*\}`,
	replacement: `let _ = (want_disasm, disasm);`,
}

var vcodeBlockDisasmPatch = craneliftSourcePatch{
	name: "elide vcode block disasm",
	pattern: `(?s)if want_disasm \{\r?\n\s*writeln!\(&mut disasm, "block\{\}:", block\.index\(\)\)\.unwrap\(\);\r?\n\s*\}`,
	replacement: `/* block disasm elided */`,
}

var vcodeStackMapDisasmPatch = craneliftSourcePatch{
	name: "elide vcode stack map disasm",
	pattern: `(?s)let user_stack_map_disasm = if want_disasm \{.*?\};\r?\n\s*\(user_stack_map, user_stack_map_disasm\)`,
	replacement: `let user_stack_map_disasm: Option<String> = None;
                                (user_stack_map, user_stack_map_disasm)`,
}

var vcodeDebugTagDisasmPatch = craneliftSourcePatch{
	name: "elide vcode debug tag disasm",
	pattern: `(?s)let mut debug_tag_disasm = None;(.*?)if want_disasm \{\r?\n\s*debug_tag_disasm =\r?\n\s*Some\(format!\("  ; \^-- debug @ \{pos:\?\}: \{tags:\?\}"\)\);\r?\n\s*\}`,
	replacement: `let mut debug_tag_disasm: Option<String> = None;$1/* debug tag disasm elided */`,
}

var unsupportedErrorFormatPatch = craneliftSourcePatch{
	name:        "elide format! in unsupported dynamic vector error",
	pattern:     `CodegenError::Unsupported\(format!\("invalid dynamic vector type: \{\}", data\.dyn_ty\)\)`,
	replacement: `CodegenError::Unsupported("invalid dynamic vector type".into())`,
}

var logDependencyPatch = craneliftSourcePatch{
	name:        "disable log output in cranelift-codegen",
	pattern:     `(?s)(\[dependencies\.log\]\r?\nversion = "0\.4\.28"\r?\ndefault-features = false\r?\n)`,
	replacement: `${1}features = ["release_max_level_off"]` + "\n",
}

var timingDisabledPatch = craneliftSourcePatch{
	name: "elide timing passes and Box<dyn Any>",
	pattern: `(?s)#\[doc=\$desc\]\r?\n\s*#\[must_use\]\r?\n\s*pub fn \$pass\(\) -> Box<dyn Any> \{\r?\n\s*start_pass\(Pass::\$pass\)\r?\n\s*\}`,
	replacement: `#[doc=$$desc]
            #[must_use]
            pub fn $$pass() -> () {}`,
}

var lowerIsleErrorPatch = craneliftSourcePatch{
	name: "elide display_inst in lower ISLE error",
	pattern: `(?s)CodegenError::Unsupported\(format!\(\s*"should be implemented in ISLE:.*?\)\s*\)`,
	replacement: `CodegenError::Unsupported("unimplemented in ISLE".into())`,
}

var aarch64PrintWithStateStubPatch = craneliftSourcePatch{
	name:        "stub aarch64 print_with_state",
	pattern:     `(?s)pub fn print_with_state\(&self, state: &mut EmitState\) -> String \{.*?\r?\n//={20,}`,
	replacement: "pub fn print_with_state(&self, _state: &mut EmitState) -> String { String::new() }\n}\n\n//=============================================================================",
}

var x64PrettyPrintStubPatch = craneliftSourcePatch{
	name:        "stub x64 PrettyPrint for Inst",
	pattern:     `(?s)impl PrettyPrint for Inst \{.*?impl fmt::Debug for Inst \{`,
	replacement: "impl PrettyPrint for Inst {\n    fn pretty_print(&self, _: u8) -> String { String::new() }\n}\n\nimpl fmt::Debug for Inst {",
}

var x64DebugStubPatch = craneliftSourcePatch{
	name:        "stub x64 Debug for Inst",
	pattern:     `(?s)impl fmt::Debug for Inst \{.*?\r?\n\}`,
	replacement: "impl fmt::Debug for Inst {\n    fn fmt(&self, _fmt: &mut fmt::Formatter) -> fmt::Result { Ok(()) }\n}",
}

var stringifyCodeBytesStubPatch = craneliftSourcePatch{
	name:        "stub stringify_code_bytes",
	pattern:     `(?s)pub fn stringify_code_bytes\(&self\) -> String \{.*?\r?\n    \}`,
	replacement: "pub fn stringify_code_bytes(&self) -> String { String::new() }",
}

var bumpaloEmptyChunkPatch = craneliftSourcePatch{
	name: "eject bumpalo EMPTY_CHUNK to heap redirection",
	pattern: `(?s)#\[repr\(transparent\)\]\r?\nstruct EmptyChunkFooter\(ChunkFooter\);\r?\n\r?\nunsafe impl Sync for EmptyChunkFooter \{\}\r?\n\r?\nstatic EMPTY_CHUNK: EmptyChunkFooter = EmptyChunkFooter\(ChunkFooter \{.*?allocated_bytes: 0,\r?\n\}\);`,
	replacement: `#[allow(improper_ctypes)]
unsafe extern "C" {
    fn get_bump_empty_chunk() -> *mut ChunkFooter;
}

#[repr(transparent)]
struct EmptyChunkFooter(());

unsafe impl Sync for EmptyChunkFooter {}

static EMPTY_CHUNK: EmptyChunkFooter = EmptyChunkFooter(());`,
}

var bumpaloEmptyChunkGetPatch = craneliftSourcePatch{
	name: "bumpalo EmptyChunkFooter::get redirection",
	pattern: `(?s)impl EmptyChunkFooter \{\r?\n\s*fn get\(&'static self\) -> NonNull<ChunkFooter> \{\r?\n\s*NonNull::from\(&self\.0\)\r?\n\s*\}\r?\n\}`,
	replacement: `impl EmptyChunkFooter {
    #[inline(always)]
    fn get(&self) -> NonNull<ChunkFooter> {
        unsafe { NonNull::new_unchecked(get_bump_empty_chunk()) }
    }
}`,
}

var hashbrownDefaultHasherPatch = craneliftSourcePatch{
	name:        "disable foldhash default hasher in hashbrown",
	pattern:     `(?s)(\[features\]\r?\ndefault = \[\r?\n)\s*"default-hasher",\r?\n`,
	replacement: `$1`,
}

var hashbrownStaticEmptyPatch = craneliftSourcePatch{
	name: "eject hashbrown static_empty to heap redirection",
	pattern: `(?s)pub\(crate\) const fn static_empty\(\) -> &'static \[Tag; Group::WIDTH\] \{.*?&ALIGNED_TAGS\.tags\r?\n\s*\}`,
	replacement: `pub(crate) fn static_empty() -> &'static [Tag; Group::WIDTH] {
        #[allow(improper_ctypes)]
        unsafe extern "C" { fn get_hashbrown_empty_tags() -> *const Tag; }
        unsafe { &*(get_hashbrown_empty_tags() as *const [Tag; Group::WIDTH]) }
    }`,
}

var unexpectedSSAValuePatch = craneliftSourcePatch{
	name:        "elide format! in unexpected SSA-value type",
	pattern:     `CodegenError::Unsupported\(format!\(\s*"Unexpected SSA-value type: \{ty\}"\s*\)\)`,
	replacement: `CodegenError::Unsupported("Unexpected SSA-value type".into())`,
}

var memFlagsLinearScanPatch = craneliftSourcePatch{
	name: "linear scan in MemFlagsSet::insert",
	pattern: `(?s)pub fn insert\(&mut self, data: MemFlagsData\) -> Result<MemFlags, MemFlagsSetOverflow> \{.*?self\.dedupe_map\.insert\(data, key\);\r?\n\s*Ok\(key\)\r?\n\s*\}`,
	replacement: `pub fn insert(&mut self, data: MemFlagsData) -> Result<MemFlags, MemFlagsSetOverflow> {
        for (key, &val) in self.mem_flags.iter() {
            if val == data {
                return Ok(key);
            }
        }
        let key = self.mem_flags.push(data);
        Ok(key)
    }`,
}

var wasmparserFormatErrPatch = craneliftSourcePatch{
	name: "elide format_args! in wasmparser format_err!",
	pattern: `(?s)macro_rules! format_err \{.*?crate::Error::fmt\(format_args!\(\$\(\$arg\)\*\), \$offset\)\r?\n\s*\}\r?\n\}`,
	replacement: `macro_rules! format_err {
    ($$offset:expr, $$($$arg:tt)*) => {
        crate::Error { offset: $$offset }
    }
}`,
}

var rcForTypeAArch64Patch = craneliftSourcePatch{
	name: "eject AArch64 rc_for_type to heap slices",
	pattern: `(?s)fn rc_for_type\(ty: &Type\) -> CodegenResult<\(&\[RegClass\], &\[Type\]\)> \{.*?(\r?\n    fn canonical_type_for_rc)`,
	replacement: `fn rc_for_type(ty: &Type) -> CodegenResult<(&'static [RegClass], &'static [Type])> {
        #[allow(improper_ctypes)]
        unsafe extern "C" {
            fn get_heap_rc_int() -> *const RegClass;
            fn get_heap_rc_float() -> *const RegClass;
            fn get_heap_rc_int_int() -> *const RegClass;
            fn get_heap_ty_i64_i64() -> *const Type;
            fn get_heap_ty_i8x16() -> *const Type;
        }
        unsafe {
            let rc_int = core::slice::from_raw_parts(get_heap_rc_int(), 1);
            let rc_float = core::slice::from_raw_parts(get_heap_rc_float(), 1);
            let rc_int_int = core::slice::from_raw_parts(get_heap_rc_int_int(), 2);
            let ty_i64_i64 = core::slice::from_raw_parts(get_heap_ty_i64_i64(), 2);
            let ty_i8x16 = core::slice::from_raw_parts(get_heap_ty_i8x16(), 1);
            match *ty {
                I8 | I16 | I32 | I64 => Ok((rc_int, alloc::boxed::Box::leak(alloc::boxed::Box::new([*ty])))),
                F16 | F32 | F64 | F128 => Ok((rc_float, alloc::boxed::Box::leak(alloc::boxed::Box::new([*ty])))),
                I128 => Ok((rc_int_int, ty_i64_i64)),
                _ if ty.is_dynamic_vector() => Ok((rc_float, ty_i8x16)),
                _ => Err(CodegenError::Unsupported("Unexpected SSA-value type".into())),
            }
        }
    }$1`,
}

var rcForTypeX64Patch = craneliftSourcePatch{
	name: "eject x64 rc_for_type to heap slices",
	pattern: `(?s)fn rc_for_type\(ty: &Type\) -> CodegenResult<\(&\[RegClass\], &\[Type\]\)> \{.*?(\r?\n    fn canonical_type_for_rc)`,
	replacement: `fn rc_for_type(ty: &Type) -> CodegenResult<(&'static [RegClass], &'static [Type])> {
        #[allow(improper_ctypes)]
        unsafe extern "C" {
            fn get_heap_rc_int() -> *const RegClass;
            fn get_heap_rc_float() -> *const RegClass;
            fn get_heap_rc_int_int() -> *const RegClass;
            fn get_heap_ty_i64_i64() -> *const Type;
            fn get_heap_ty_i8x16() -> *const Type;
        }
        unsafe {
            let rc_int = core::slice::from_raw_parts(get_heap_rc_int(), 1);
            let rc_float = core::slice::from_raw_parts(get_heap_rc_float(), 1);
            let rc_int_int = core::slice::from_raw_parts(get_heap_rc_int_int(), 2);
            let ty_i64_i64 = core::slice::from_raw_parts(get_heap_ty_i64_i64(), 2);
            let ty_i8x16 = core::slice::from_raw_parts(get_heap_ty_i8x16(), 1);
            match *ty {
                types::I8 | types::I16 | types::I32 | types::I64 => Ok((rc_int, alloc::boxed::Box::leak(alloc::boxed::Box::new([*ty])))),
                types::F16 | types::F32 | types::F64 | types::F128 => Ok((rc_float, alloc::boxed::Box::leak(alloc::boxed::Box::new([*ty])))),
                types::I128 => Ok((rc_int_int, ty_i64_i64)),
                _ if ty.is_dynamic_vector() => Ok((rc_float, ty_i8x16)),
                _ => Err(CodegenError::Unsupported("Unexpected SSA-value type".into())),
            }
        }
    }$1`,
}

const selfContainedHasherSource = `use core::hash::{BuildHasher, Hasher};

#[allow(missing_docs)]
#[derive(Clone, Debug, Default)]
pub struct DefaultHashBuilder;

impl BuildHasher for DefaultHashBuilder {
    type Hasher = DefaultHasher;

    #[inline(always)]
    fn build_hasher(&self) -> DefaultHasher {
        DefaultHasher(0x517cc1b727220a95)
    }
}

#[allow(missing_docs)]
#[derive(Clone, Debug, Default)]
pub struct DefaultHasher(u64);

impl Hasher for DefaultHasher {
    #[inline(always)]
    fn finish(&self) -> u64 {
        self.0
    }

    #[inline(always)]
    fn write(&mut self, bytes: &[u8]) {
        for &byte in bytes {
            self.0 = self.0.rotate_left(5) ^ (byte as u64);
            self.0 = self.0.wrapping_mul(0x517cc1b727220a95);
        }
    }

    #[inline(always)]
    fn write_u8(&mut self, i: u8) {
        self.0 = self.0.rotate_left(5) ^ (i as u64);
        self.0 = self.0.wrapping_mul(0x517cc1b727220a95);
    }

    #[inline(always)]
    fn write_u16(&mut self, i: u16) {
        self.0 = self.0.rotate_left(5) ^ (i as u64);
        self.0 = self.0.wrapping_mul(0x517cc1b727220a95);
    }

    #[inline(always)]
    fn write_u32(&mut self, i: u32) {
        self.0 = self.0.rotate_left(5) ^ (i as u64);
        self.0 = self.0.wrapping_mul(0x517cc1b727220a95);
    }

    #[inline(always)]
    fn write_u64(&mut self, i: u64) {
        self.0 = self.0.rotate_left(5) ^ i;
        self.0 = self.0.wrapping_mul(0x517cc1b727220a95);
    }

    #[inline(always)]
    fn write_usize(&mut self, i: usize) {
        self.write_u64(i as u64);
    }

    #[inline(always)]
    fn write_i32(&mut self, i: i32) {
        self.write_u32(i as u32);
    }

    #[inline(always)]
    fn write_i64(&mut self, i: i64) {
        self.write_u64(i as u64);
    }

    #[inline(always)]
    fn write_isize(&mut self, i: isize) {
        self.write_u64(i as u64);
    }
}
`

const selfContainedWasmparserErrorSource = `//! Wasmparser error handling stub
#![allow(missing_docs, dead_code)]
use core::fmt;
use crate::WasmFeatures;
use crate::prelude::*;

#[derive(Debug, Clone)]
pub struct Error {
    pub(crate) offset: u64,
}

#[derive(Debug, Clone, Copy)]
pub(crate) enum ErrorKind {
    Uncategorized,
    InvalidHeapType,
    WasmFeature(WasmFeatures),
}

pub type Result<T, E = Error> = core::result::Result<T, E>;

impl core::error::Error for Error {}

impl fmt::Display for Error {
    fn fmt(&self, _f: &mut fmt::Formatter) -> fmt::Result {
        Ok(())
    }
}

impl Error {
    #[cold]
    pub(crate) fn _new(_kind: ErrorKind, _message: String, offset: u64) -> Self {
        Error { offset }
    }

    #[cold]
    pub(crate) fn new(_message: impl Into<String>, offset: u64) -> Self {
        Error { offset }
    }

    #[cold]
    pub(crate) fn invalid_heap_type(_msg: &'static str, offset: u64) -> Self {
        Error { offset }
    }

    #[cold]
    pub(crate) fn wasm_feature(
        _feature: crate::WasmFeatures,
        _msg: impl fmt::Display,
        offset: u64,
    ) -> Self {
        Error { offset }
    }

    #[cold]
    pub(crate) fn fmt(_args: fmt::Arguments<'_>, offset: u64) -> Self {
        Error { offset }
    }

    #[cold]
    pub(crate) fn eof(offset: u64, _needed_hint: usize) -> Self {
        Error { offset }
    }

    pub(crate) fn kind(&self) -> ErrorKind {
        ErrorKind::Uncategorized
    }

    pub fn message(&self) -> &str {
        "wasm parse error"
    }

    pub fn offset(&self) -> u64 {
        self.offset
    }

    pub(crate) fn set_message(&mut self, _message: impl Into<String>) {}

    pub fn missing_wasm_feature(&self) -> Option<WasmFeatures> {
        None
    }

    pub(crate) fn needed_hint(&self) -> Option<usize> {
        None
    }

    pub(crate) fn without_needed_hint(self) -> Self {
        self
    }
}
`

var cargoPackageVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)

func prepareCraneliftOverlay(runtimeDir, rustBuildDir string, verbose bool) (workspaceDir string, retErr error) {
	lockPath := filepath.Join(runtimeDir, "Cargo.lock")
	lockData, err := os.ReadFile(lockPath)
	if err != nil {
		return "", fmt.Errorf("read Cranelift lockfile %s: %w", lockPath, err)
	}

	cargoHome := filepath.Join(rustBuildDir, "cargo-home")
	if err := os.MkdirAll(cargoHome, 0o755); err != nil {
		return "", fmt.Errorf("create Cargo home %s: %w", cargoHome, err)
	}

	if err := fetchAllCrates(runtimeDir, cargoHome, verbose); err != nil {
		return "", err
	}

	craneliftOverlayDir, err := prepareCraneliftCodegen(runtimeDir, string(lockData), rustBuildDir, cargoHome, verbose)
	if err != nil {
		return "", err
	}

	bumpaloOverlayDir, err := prepareBumpaloOverlay(string(lockData), rustBuildDir, cargoHome, verbose)
	if err != nil {
		return "", err
	}

	hashbrownOverlayDir, err := prepareHashbrownOverlay(string(lockData), rustBuildDir, cargoHome, verbose)
	if err != nil {
		return "", err
	}

	indexmapOverlayDir, err := prepareIndexmapOverlay(string(lockData), rustBuildDir, cargoHome, verbose)
	if err != nil {
		return "", err
	}

	wasmparserOverlayDir, err := prepareWasmparserOverlay(string(lockData), rustBuildDir, cargoHome, verbose)
	if err != nil {
		return "", err
	}

	regalloc2OverlayDir, err := prepareRegalloc2Overlay(string(lockData), rustBuildDir, cargoHome, verbose)
	if err != nil {
		return "", err
	}

	workspaceDir, err = prepareCraneliftRuntimeWorkspace(runtimeDir, rustBuildDir, craneliftOverlayDir, bumpaloOverlayDir, hashbrownOverlayDir, indexmapOverlayDir, wasmparserOverlayDir, regalloc2OverlayDir)
	if err != nil {
		return "", err
	}
	return workspaceDir, nil
}

func fetchAllCrates(runtimeDir, cargoHome string, verbose bool) error {
	manifestPath := filepath.Join(runtimeDir, "Cargo.toml")
	cmd := exec.Command("cargo", "+nightly", "fetch", "--locked", "--manifest-path", manifestPath)
	cmd.Dir = runtimeDir
	cmd.Env = append(os.Environ(), "CARGO_HOME="+cargoHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cargo fetch crates: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	if verbose && len(out) > 0 {
		fmt.Print(string(out))
	}
	return nil
}

func prepareCraneliftCodegen(runtimeDir, lock, rustBuildDir, cargoHome string, verbose bool) (string, error) {
	version, err := lockedPackageVersion(lock, "cranelift-codegen")
	if err != nil {
		return "", fmt.Errorf("resolve locked Cranelift version: %w", err)
	}
	sourceDir, err := fetchCrateSource(cargoHome, "cranelift-codegen", version)
	if err != nil {
		return "", err
	}

	overlayDir := filepath.Join(rustBuildDir, craneliftOverlayName)
	stagingDir := overlayDir + ".staging"
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("create Cranelift staging directory %s: %w", stagingDir, err)
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	if err := copyCraneliftSource(sourceDir, stagingDir); err != nil {
		return "", fmt.Errorf("copy Cranelift source into overlay: %w", err)
	}
	if err := applyCraneliftIsaPatches(stagingDir); err != nil {
		return "", fmt.Errorf("patch Cranelift source overlay: %w", err)
	}
	_ = os.RemoveAll(overlayDir)
	if err := os.Rename(stagingDir, overlayDir); err != nil {
		return "", fmt.Errorf("publish Cranelift overlay %s: %w", overlayDir, err)
	}
	if verbose {
		fmt.Printf("🍆  Prepared Cranelift %s source overlay at %s\n", version, overlayDir)
	}
	return overlayDir, nil
}

func prepareBumpaloOverlay(lock, rustBuildDir, cargoHome string, verbose bool) (string, error) {
	version, err := lockedPackageVersion(lock, "bumpalo")
	if err != nil {
		return "", fmt.Errorf("resolve locked bumpalo version: %w", err)
	}
	sourceDir, err := fetchCrateSource(cargoHome, "bumpalo", version)
	if err != nil {
		return "", err
	}

	overlayDir := filepath.Join(rustBuildDir, bumpaloOverlayName)
	stagingDir := overlayDir + ".staging"
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("create bumpalo staging directory %s: %w", stagingDir, err)
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	if err := copyCraneliftSource(sourceDir, stagingDir); err != nil {
		return "", fmt.Errorf("copy bumpalo source into overlay: %w", err)
	}

	libPath := filepath.Join(stagingDir, "src", "lib.rs")
	libData, err := os.ReadFile(libPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", libPath, err)
	}
	libSource, err := replaceRegexExactlyOnce(libPath, string(libData), bumpaloEmptyChunkPatch)
	if err != nil {
		return "", err
	}
	libSource, err = replaceRegexExactlyOnce(libPath, libSource, bumpaloEmptyChunkGetPatch)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(libPath, []byte(libSource), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", libPath, err)
	}

	_ = os.RemoveAll(overlayDir)
	if err := os.Rename(stagingDir, overlayDir); err != nil {
		return "", fmt.Errorf("publish bumpalo overlay %s: %w", overlayDir, err)
	}
	if verbose {
		fmt.Printf("🍆  Prepared bumpalo %s source overlay at %s\n", version, overlayDir)
	}
	return overlayDir, nil
}

func prepareHashbrownOverlay(lock, rustBuildDir, cargoHome string, verbose bool) (string, error) {
	version, err := lockedPackageVersion(lock, "hashbrown")
	if err != nil {
		return "", fmt.Errorf("resolve locked hashbrown version: %w", err)
	}
	sourceDir, err := fetchCrateSource(cargoHome, "hashbrown", version)
	if err != nil {
		return "", err
	}

	overlayDir := filepath.Join(rustBuildDir, hashbrownOverlayName)
	stagingDir := overlayDir + ".staging"
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("create hashbrown staging directory %s: %w", stagingDir, err)
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	if err := copyCraneliftSource(sourceDir, stagingDir); err != nil {
		return "", fmt.Errorf("copy hashbrown source into overlay: %w", err)
	}

	manifestPath := filepath.Join(stagingDir, "Cargo.toml")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", manifestPath, err)
	}
	manifestSource, err := replaceRegexExactlyOnce(manifestPath, string(manifestData), hashbrownDefaultHasherPatch)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(manifestPath, []byte(manifestSource), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", manifestPath, err)
	}

	hasherPath := filepath.Join(stagingDir, "src", "hasher.rs")
	if err := os.WriteFile(hasherPath, []byte(selfContainedHasherSource), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", hasherPath, err)
	}

	for _, subpath := range []string{
		filepath.Join("src", "control", "group", "neon.rs"),
		filepath.Join("src", "control", "group", "sse2.rs"),
		filepath.Join("src", "control", "group", "generic.rs"),
	} {
		p := filepath.Join(stagingDir, subpath)
		if data, err := os.ReadFile(p); err == nil {
			if patched, err := replaceRegexExactlyOnce(p, string(data), hashbrownStaticEmptyPatch); err == nil {
				_ = os.WriteFile(p, []byte(patched), 0o644)
			}
		}
	}

	rawPath := filepath.Join(stagingDir, "src", "raw.rs")
	if rawData, err := os.ReadFile(rawPath); err == nil {
		rawSource := string(rawData)
		rawSource = strings.ReplaceAll(rawSource, "const NEW: Self = RawTableInner::new();", "")
		rawSource = strings.ReplaceAll(rawSource, "RawTableInner::NEW", "RawTableInner::new()")
		rawSource = strings.ReplaceAll(rawSource, "Self::NEW", "Self::new()")
		rawSource = strings.ReplaceAll(rawSource, "const fn new()", "fn new()")
		rawSource = strings.ReplaceAll(rawSource, "const fn new_in(", "fn new_in(")
		_ = os.WriteFile(rawPath, []byte(rawSource), 0o644)
	}

	tablePath := filepath.Join(stagingDir, "src", "table.rs")
	if tableData, err := os.ReadFile(tablePath); err == nil {
		tableSource := string(tableData)
		tableSource = strings.ReplaceAll(tableSource, "pub const fn new()", "pub fn new()")
		tableSource = strings.ReplaceAll(tableSource, "pub const fn new_in(", "pub fn new_in(")
		_ = os.WriteFile(tablePath, []byte(tableSource), 0o644)
	}

	mapPath := filepath.Join(stagingDir, "src", "map.rs")
	if mapData, err := os.ReadFile(mapPath); err == nil {
		mapSource := string(mapData)
		mapSource = strings.ReplaceAll(mapSource, "pub const fn with_hasher(", "pub fn with_hasher(")
		mapSource = strings.ReplaceAll(mapSource, "pub const fn with_hasher_in(", "pub fn with_hasher_in(")
		_ = os.WriteFile(mapPath, []byte(mapSource), 0o644)
	}

	setPath := filepath.Join(stagingDir, "src", "set.rs")
	if setData, err := os.ReadFile(setPath); err == nil {
		setSource := string(setData)
		setSource = strings.ReplaceAll(setSource, "pub const fn with_hasher(", "pub fn with_hasher(")
		setSource = strings.ReplaceAll(setSource, "pub const fn with_hasher_in(", "pub fn with_hasher_in(")
		_ = os.WriteFile(setPath, []byte(setSource), 0o644)
	}

	_ = os.RemoveAll(overlayDir)
	if err := os.Rename(stagingDir, overlayDir); err != nil {
		return "", fmt.Errorf("publish hashbrown overlay %s: %w", overlayDir, err)
	}
	if verbose {
		fmt.Printf("🍆  Prepared hashbrown %s source overlay at %s\n", version, overlayDir)
	}
	return overlayDir, nil
}

func prepareIndexmapOverlay(lock, rustBuildDir, cargoHome string, verbose bool) (string, error) {
	version, err := lockedPackageVersion(lock, "indexmap")
	if err != nil {
		return "", fmt.Errorf("resolve locked indexmap version: %w", err)
	}
	sourceDir, err := fetchCrateSource(cargoHome, "indexmap", version)
	if err != nil {
		return "", err
	}

	overlayDir := filepath.Join(rustBuildDir, indexmapOverlayName)
	stagingDir := overlayDir + ".staging"
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("create indexmap staging directory %s: %w", stagingDir, err)
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	if err := copyCraneliftSource(sourceDir, stagingDir); err != nil {
		return "", fmt.Errorf("copy indexmap source into overlay: %w", err)
	}

	for _, subpath := range []string{
		filepath.Join("src", "inner.rs"),
		filepath.Join("src", "map.rs"),
		filepath.Join("src", "set.rs"),
	} {
		p := filepath.Join(stagingDir, subpath)
		if data, err := os.ReadFile(p); err == nil {
			src := string(data)
			src = strings.ReplaceAll(src, "pub(crate) const fn new()", "pub(crate) fn new()")
			src = strings.ReplaceAll(src, "pub const fn new()", "pub fn new()")
			src = strings.ReplaceAll(src, "pub const fn with_hasher(", "pub fn with_hasher(")
			_ = os.WriteFile(p, []byte(src), 0o644)
		}
	}

	_ = os.RemoveAll(overlayDir)
	if err := os.Rename(stagingDir, overlayDir); err != nil {
		return "", fmt.Errorf("publish indexmap overlay %s: %w", overlayDir, err)
	}
	if verbose {
		fmt.Printf("🍆  Prepared indexmap %s source overlay at %s\n", version, overlayDir)
	}
	return overlayDir, nil
}

func prepareWasmparserOverlay(lock, rustBuildDir, cargoHome string, verbose bool) (string, error) {
	version, err := lockedPackageVersion(lock, "wasmparser")
	if err != nil {
		return "", fmt.Errorf("resolve locked wasmparser version: %w", err)
	}
	sourceDir, err := fetchCrateSource(cargoHome, "wasmparser", version)
	if err != nil {
		return "", err
	}

	overlayDir := filepath.Join(rustBuildDir, wasmparserOverlayName)
	stagingDir := overlayDir + ".staging"
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("create wasmparser staging directory %s: %w", stagingDir, err)
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	if err := copyCraneliftSource(sourceDir, stagingDir); err != nil {
		return "", fmt.Errorf("copy wasmparser source into overlay: %w", err)
	}

	errorPath := filepath.Join(stagingDir, "src", "error.rs")
	if err := os.WriteFile(errorPath, []byte(selfContainedWasmparserErrorSource), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", errorPath, err)
	}

	libPath := filepath.Join(stagingDir, "src", "lib.rs")
	if libData, err := os.ReadFile(libPath); err == nil {
		if libSource, err := replaceRegexExactlyOnce(libPath, string(libData), wasmparserFormatErrPatch); err == nil {
			_ = os.WriteFile(libPath, []byte(libSource), 0o644)
		}
	}

	parserPath := filepath.Join(stagingDir, "src", "parser.rs")
	if parserData, err := os.ReadFile(parserPath); err == nil {
		src := string(parserData)
		src = strings.ReplaceAll(src, `&format!("section too large, {len} goes past 0x{max_offset:x}")`, `"section too large"`)
		_ = os.WriteFile(parserPath, []byte(src), 0o644)
	}

	readerPath := filepath.Join(stagingDir, "src", "binary_reader.rs")
	if readerData, err := os.ReadFile(readerPath); err == nil {
		src := string(readerData)
		re := regexp.MustCompile(`(?s)format!\(\s*"magic header not detected:.*?"\s*\)`)
		src = re.ReplaceAllString(src, `"magic header not detected"`)
		re2 := regexp.MustCompile(`format!\("invalid atomic consistency ordering \{x\}"\)`)
		src = re2.ReplaceAllString(src, `"invalid atomic consistency ordering"`)
		_ = os.WriteFile(readerPath, []byte(src), 0o644)
	}

	_ = os.RemoveAll(overlayDir)
	if err := os.Rename(stagingDir, overlayDir); err != nil {
		return "", fmt.Errorf("publish wasmparser overlay %s: %w", overlayDir, err)
	}
	if verbose {
		fmt.Printf("🍆  Prepared wasmparser %s source overlay at %s\n", version, overlayDir)
	}
	return overlayDir, nil
}

func prepareRegalloc2Overlay(lock, rustBuildDir, cargoHome string, verbose bool) (string, error) {
	version, err := lockedPackageVersion(lock, "regalloc2")
	if err != nil {
		return "", fmt.Errorf("resolve locked regalloc2 version: %w", err)
	}
	sourceDir, err := fetchCrateSource(cargoHome, "regalloc2", version)
	if err != nil {
		return "", err
	}

	overlayDir := filepath.Join(rustBuildDir, regalloc2OverlayName)
	stagingDir := overlayDir + ".staging"
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("create regalloc2 staging directory %s: %w", stagingDir, err)
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	if err := copyCraneliftSource(sourceDir, stagingDir); err != nil {
		return "", fmt.Errorf("copy regalloc2 source into overlay: %w", err)
	}

	manifestPath := filepath.Join(stagingDir, "Cargo.toml")
	if manifestData, err := os.ReadFile(manifestPath); err == nil {
		src := string(manifestData)
		re := regexp.MustCompile(`(?s)(\[dependencies\.log\]\r?\nversion = "0\.4\.8"\r?\ndefault-features = false\r?\n)`)
		src = re.ReplaceAllString(src, `${1}features = ["release_max_level_off"]`+"\n")
		_ = os.WriteFile(manifestPath, []byte(src), 0o644)
	}

	fastallocPath := filepath.Join(stagingDir, "src", "fastalloc", "mod.rs")
	if fastallocData, err := os.ReadFile(fastallocPath); err == nil {
		src := string(fastallocData)
		re1 := regexp.MustCompile(`(?s)fn log_post_reload_at_begin_state\(&self, block: Block\) \{.*?\r?\n    \}`)
		src = re1.ReplaceAllString(src, `fn log_post_reload_at_begin_state(&self, _block: Block) {}`)
		re2 := regexp.MustCompile(`(?s)fn log_post_inst_processing_state\(&self, inst: Inst\) \{.*?\r?\n    \}`)
		src = re2.ReplaceAllString(src, `fn log_post_inst_processing_state(&self, _inst: Inst) {}`)
		re3 := regexp.MustCompile(`(?s)fn log_output<'a, F: Function>\(env: &Env<'a, F>\) \{.*?\r?\n\}`)
		src = re3.ReplaceAllString(src, `fn log_output<'a, F: Function>(_env: &Env<'a, F>) {}`)
		_ = os.WriteFile(fastallocPath, []byte(src), 0o644)
	}

	movesPath := filepath.Join(stagingDir, "src", "ion", "moves.rs")
	if movesData, err := os.ReadFile(movesPath); err == nil {
		src := string(movesData)
		re := regexp.MustCompile(`(?s)self\.annotate\(.*?\);`)
		src = re.ReplaceAllString(src, `/* annotate elided */`)
		_ = os.WriteFile(movesPath, []byte(src), 0o644)
	}

	mergePath := filepath.Join(stagingDir, "src", "ion", "merge.rs")
	if mergeData, err := os.ReadFile(mergePath); err == nil {
		src := string(mergeData)
		re := regexp.MustCompile(`(?s)self\.annotate\(.*?\);`)
		src = re.ReplaceAllString(src, `/* annotate elided */`)
		_ = os.WriteFile(mergePath, []byte(src), 0o644)
	}

	lruPath := filepath.Join(stagingDir, "src", "fastalloc", "lru.rs")
	if lruData, err := os.ReadFile(lruPath); err == nil {
		src := string(lruData)
		re := regexp.MustCompile(`(?s)impl fmt::Debug for Lru \{.*?\r?\n\}`)
		src = re.ReplaceAllString(src, "impl fmt::Debug for Lru {\n    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result { f.debug_struct(\"Lru\").finish() }\n}")
		_ = os.WriteFile(lruPath, []byte(src), 0o644)
	}

	dumpPath := filepath.Join(stagingDir, "src", "ion", "dump.rs")
	dumpSource := `//! Debugging output stubs.
use alloc::string::String;
use super::Env;
use crate::{Function, ProgPoint};

impl<'a, F: Function> Env<'a, F> {
    pub fn dump_state(&self) {}
    pub fn annotate(&mut self, _progpoint: ProgPoint, _s: String) {}
    pub fn dump_results(&self) {}
}
`
	_ = os.WriteFile(dumpPath, []byte(dumpSource), 0o644)

	_ = os.RemoveAll(overlayDir)
	if err := os.Rename(stagingDir, overlayDir); err != nil {
		return "", fmt.Errorf("publish regalloc2 overlay %s: %w", overlayDir, err)
	}
	if verbose {
		fmt.Printf("🍆  Prepared regalloc2 %s source overlay at %s\n", version, overlayDir)
	}
	return overlayDir, nil
}

func fetchCrateSource(cargoHome, pkgName, version string) (string, error) {
	pattern := filepath.Join(cargoHome, "registry", "src", "index.crates.io-*", pkgName+"-"+version)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("find fetched %s source using %s: %w", pkgName, pattern, err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one fetched %s source for version %s, found %d under %s", pkgName, version, len(matches), pattern)
	}
	return matches[0], nil
}

func fetchCraneliftSource(runtimeDir, version, cargoHome string, verbose bool) (string, error) {
	if err := fetchAllCrates(runtimeDir, cargoHome, verbose); err != nil {
		return "", err
	}
	return fetchCrateSource(cargoHome, "cranelift-codegen", version)
}

func prepareCraneliftRuntimeWorkspace(runtimeDir, rustBuildDir, craneliftDir, bumpaloDir, hashbrownDir, indexmapDir, wasmparserDir, regalloc2Dir string) (string, error) {
	manifestPath := filepath.Join(runtimeDir, "Cargo.toml")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read runtime manifest %s: %w", manifestPath, err)
	}
	manifest := string(manifestData)
	sourcePath := filepath.ToSlash(filepath.Join(runtimeDir, "src", "lib.rs"))
	manifest, err = replaceLiteralExactlyOnce(
		manifestPath,
		manifest,
		`path = "src/lib.rs"`,
		fmt.Sprintf("path = %q", sourcePath),
	)
	if err != nil {
		return "", fmt.Errorf("prepare runtime manifest overlay: %w", err)
	}
	if strings.Contains(manifest, "[patch.crates-io]") {
		return "", fmt.Errorf("runtime manifest still contains a tracked [patch.crates-io] override")
	}
	manifest += fmt.Sprintf(
		"\n[patch.crates-io]\ncranelift-codegen = { path = %q }\nbumpalo = { path = %q }\nhashbrown = { path = %q }\nindexmap = { path = %q }\nwasmparser = { path = %q }\nregalloc2 = { path = %q }\n",
		filepath.ToSlash(craneliftDir),
		filepath.ToSlash(bumpaloDir),
		filepath.ToSlash(hashbrownDir),
		filepath.ToSlash(indexmapDir),
		filepath.ToSlash(wasmparserDir),
		filepath.ToSlash(regalloc2Dir),
	)

	workspaceDir := filepath.Join(rustBuildDir, craneliftWorkspaceName)
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return "", fmt.Errorf("create generated runtime workspace %s: %w", workspaceDir, err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "Cargo.toml"), []byte(manifest), 0o644); err != nil {
		return "", fmt.Errorf("write generated runtime manifest: %w", err)
	}
	lockData, err := os.ReadFile(filepath.Join(runtimeDir, "Cargo.lock"))
	if err != nil {
		return "", fmt.Errorf("read runtime lockfile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "Cargo.lock"), lockData, 0o644); err != nil {
		return "", fmt.Errorf("write generated runtime lockfile: %w", err)
	}
	return workspaceDir, nil
}

func lockedPackageVersion(lock, pkgName string) (string, error) {
	var version string
	for _, block := range strings.Split(lock, "[[package]]") {
		name := ""
		blockVersion := ""
		for _, line := range strings.Split(block, "\n") {
			fields := strings.SplitN(strings.TrimSpace(line), "=", 2)
			if len(fields) != 2 {
				continue
			}
			value := strings.Trim(strings.TrimSpace(fields[1]), `"`)
			switch strings.TrimSpace(fields[0]) {
			case "name":
				name = value
			case "version":
				blockVersion = value
			}
		}
		if name == pkgName {
			if pkgName == "hashbrown" && !strings.HasPrefix(blockVersion, "0.17") {
				continue
			}
			if version != "" && pkgName != "hashbrown" {
				return "", fmt.Errorf("Cargo.lock contains multiple %s packages", pkgName)
			}
			version = blockVersion
		}
	}
	if version == "" {
		return "", fmt.Errorf("Cargo.lock has no %s package", pkgName)
	}
	if !cargoPackageVersionPattern.MatchString(version) {
		return "", fmt.Errorf("Cargo.lock has invalid %s version %q", pkgName, version)
	}
	return version, nil
}

func lockedCraneliftVersion(lock string) (string, error) {
	return lockedPackageVersion(lock, "cranelift-codegen")
}

func cargoPackageVersion(manifest string) (string, error) {
	inPackage := false
	for _, line := range strings.Split(manifest, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inPackage = trimmed == "[package]"
			continue
		}
		if !inPackage {
			continue
		}
		fields := strings.SplitN(trimmed, "=", 2)
		if len(fields) != 2 || strings.TrimSpace(fields[0]) != "version" {
			continue
		}
		version := strings.Trim(strings.TrimSpace(fields[1]), `"`)
		if !cargoPackageVersionPattern.MatchString(version) {
			return "", fmt.Errorf("invalid package version %q", version)
		}
		return version, nil
	}
	return "", fmt.Errorf("manifest has no package version")
}

func copyCraneliftSource(sourceDir, destinationDir string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("resolve source path %s: %w", path, err)
		}
		destination := filepath.Join(destinationDir, relative)
		if entry.IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", destination, err)
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in fetched Cranelift source: %s", path)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular Cranelift source entry: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		mode := info.Mode().Perm()
		if mode&0o200 == 0 {
			mode |= 0o200
		}
		if err := os.WriteFile(destination, data, mode); err != nil {
			return fmt.Errorf("write %s: %w", destination, err)
		}
		return nil
	})
}

func applyCraneliftIsaPatches(crateDir string) error {
	contextPath := filepath.Join(crateDir, "src", "context.rs")
	contextData, err := os.ReadFile(contextPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", contextPath, err)
	}
	contextSource := string(contextData)
	for _, patch := range contextIsaPatches {
		contextSource, err = replaceRegexExactlyOnce(contextPath, contextSource, patch)
		if err != nil {
			return err
		}
	}

	abiPath := filepath.Join(crateDir, "src", "machinst", "abi.rs")
	abiData, err := os.ReadFile(abiPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", abiPath, err)
	}
	abiSource, err := replaceRegexExactlyOnce(abiPath, string(abiData), calleeIsaPatch)
	if err != nil {
		return err
	}

	settingsPath := filepath.Join(crateDir, "src", "settings.rs")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}
	settingsSource, err := replaceRegexExactlyOnce(settingsPath, string(settingsData), flagsOrIsaPatch)
	if err != nil {
		return err
	}
	contextSource, err = replaceRegexExactlyOnce(contextPath, contextSource, compileHookPatch)
	if err != nil {
		return err
	}
	contextSource, err = replaceRegexExactlyOnce(contextPath, contextSource, canonicalizeNansVectorPatch)
	if err != nil {
		return err
	}
	contextSource, err = replaceRegexExactlyOnce(contextPath, contextSource, contextVerifyStubPatch)
	if err != nil {
		return err
	}
	contextSource, err = replaceRegexExactlyOnce(contextPath, contextSource, contextVerifyIfStubPatch)
	if err != nil {
		return err
	}

	x64Path := filepath.Join(crateDir, "src", "isa", "x64", "mod.rs")
	x64Data, err := os.ReadFile(x64Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", x64Path, err)
	}
	x64Source, err := replaceRegexExactlyOnce(x64Path, string(x64Data), x64BackendVisibilityPatch)
	if err != nil {
		return err
	}
	x64Source, err = replaceRegexExactlyOnce(x64Path, x64Source, x64ConstructorVisibilityPatch)
	if err != nil {
		return err
	}

	abiSource, err = replaceRegexExactlyOnce(abiPath, abiSource, unsupportedErrorFormatPatch)
	if err != nil {
		return err
	}

	x64InstPath := filepath.Join(crateDir, "src", "isa", "x64", "inst", "mod.rs")
	if x64InstData, err := os.ReadFile(x64InstPath); err == nil {
		src := string(x64InstData)
		patched, err := replaceRegexExactlyOnce(x64InstPath, src, x64PrettyPrintStubPatch)
		if err != nil {
			return err
		}
		src = patched
		patched, err = replaceRegexExactlyOnce(x64InstPath, src, x64DebugStubPatch)
		if err != nil {
			return err
		}
		src = patched
		patched, err = replaceRegexExactlyOnce(x64InstPath, src, rcForTypeX64Patch)
		if err != nil {
			return err
		}
		src = patched
		if err := os.WriteFile(x64InstPath, []byte(src), 0o644); err != nil {
			return err
		}
	}

	aarch64InstPath := filepath.Join(crateDir, "src", "isa", "aarch64", "inst", "mod.rs")
	if aarch64InstData, err := os.ReadFile(aarch64InstPath); err == nil {
		src := string(aarch64InstData)
		patched, err := replaceRegexExactlyOnce(aarch64InstPath, src, aarch64PrintWithStateStubPatch)
		if err != nil {
			return err
		}
		src = patched
		patched, err = replaceRegexExactlyOnce(aarch64InstPath, src, rcForTypeAArch64Patch)
		if err != nil {
			return err
		}
		src = patched
		if err := os.WriteFile(aarch64InstPath, []byte(src), 0o644); err != nil {
			return err
		}
	}

	bufferPath := filepath.Join(crateDir, "src", "machinst", "buffer.rs")
	if bufferData, err := os.ReadFile(bufferPath); err == nil {
		patched, err := replaceRegexExactlyOnce(bufferPath, string(bufferData), stringifyCodeBytesStubPatch)
		if err != nil {
			return err
		}
		if err := os.WriteFile(bufferPath, []byte(patched), 0o644); err != nil {
			return err
		}
	}

	manifestPath := filepath.Join(crateDir, "Cargo.toml")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", manifestPath, err)
	}
	manifestSource, err := replaceRegexExactlyOnce(manifestPath, string(manifestData), hashbrownManifestPatch)
	if err != nil {
		return err
	}
	manifestSource, err = replaceRegexExactlyOnce(manifestPath, manifestSource, logDependencyPatch)
	if err != nil {
		return err
	}

	vcodePath := filepath.Join(crateDir, "src", "machinst", "vcode.rs")
	vcodeData, err := os.ReadFile(vcodePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", vcodePath, err)
	}
	vcodeSource, err := replaceRegexExactlyOnce(vcodePath, string(vcodeData), vcodeDisasmPatch)
	if err != nil {
		return err
	}
	vcodeSource, err = replaceRegexExactlyOnce(vcodePath, vcodeSource, vcodeBlockDisasmPatch)
	if err != nil {
		return err
	}
	vcodeSource, err = replaceRegexExactlyOnce(vcodePath, vcodeSource, vcodeStackMapDisasmPatch)
	if err != nil {
		return err
	}
	vcodeSource, err = replaceRegexExactlyOnce(vcodePath, vcodeSource, vcodeDebugTagDisasmPatch)
	if err != nil {
		return err
	}
	if err := os.WriteFile(vcodePath, []byte(vcodeSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", vcodePath, err)
	}

	timingPath := filepath.Join(crateDir, "src", "timing.rs")
	timingData, err := os.ReadFile(timingPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", timingPath, err)
	}
	timingSource, err := replaceRegexExactlyOnce(timingPath, string(timingData), timingDisabledPatch)
	if err != nil {
		return err
	}
	if err := os.WriteFile(timingPath, []byte(timingSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", timingPath, err)
	}

	lowerPath := filepath.Join(crateDir, "src", "machinst", "lower.rs")
	lowerData, err := os.ReadFile(lowerPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", lowerPath, err)
	}
	lowerSource, err := replaceRegexExactlyOnce(lowerPath, string(lowerData), lowerIsleErrorPatch)
	if err != nil {
		return err
	}
	if err := os.WriteFile(lowerPath, []byte(lowerSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", lowerPath, err)
	}

	libPath := filepath.Join(crateDir, "src", "lib.rs")
	libData, err := os.ReadFile(libPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", libPath, err)
	}
	libSource, err := replaceRegexExactlyOnce(libPath, string(libData), noStdHashAliasesPatch)
	if err != nil {
		return err
	}

	memflagsPath := filepath.Join(crateDir, "src", "ir", "memflags.rs")
	memflagsData, err := os.ReadFile(memflagsPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", memflagsPath, err)
	}
	memflagsSource, err := replaceRegexExactlyOnce(memflagsPath, string(memflagsData), memFlagsLinearScanPatch)
	if err != nil {
		return err
	}
	if err := os.WriteFile(memflagsPath, []byte(memflagsSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", memflagsPath, err)
	}

	if err := os.WriteFile(contextPath, []byte(contextSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", contextPath, err)
	}
	if err := os.WriteFile(abiPath, []byte(abiSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", abiPath, err)
	}
	if err := os.WriteFile(settingsPath, []byte(settingsSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", settingsPath, err)
	}
	if err := os.WriteFile(x64Path, []byte(x64Source), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", x64Path, err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifestSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", manifestPath, err)
	}
	if err := os.WriteFile(libPath, []byte(libSource), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", libPath, err)
	}

	hashMapPatches := []struct {
		relativePath string
		patch        craneliftSourcePatch
		count        int
	}{
		{filepath.Join("src", "machinst", "vcode.rs"), hashMapNewPatch, 3},
		{filepath.Join("src", "machinst", "vcode.rs"), hashMapCapacityPatch, 1},
		{filepath.Join("src", "machinst", "abi.rs"), dynamicTypeHashMapCapacityPatch, 1},
		{filepath.Join("src", "ir", "memflags.rs"), hashMapNewPatch, 2},
		{filepath.Join("src", "verifier", "mod.rs"), hashMapNewPatch, 1},
	}
	for _, filePatch := range hashMapPatches {
		path := filepath.Join(crateDir, filePatch.relativePath)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		patched, err := replaceRegexExpected(path, string(data), filePatch.patch, filePatch.count)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}

	return nil
}

func replaceRegexExactlyOnce(file, source string, patch craneliftSourcePatch) (string, error) {
	return replaceRegexExpected(file, source, patch, 1)
}

func replaceRegexExpected(file, source string, patch craneliftSourcePatch, expected int) (string, error) {
	re, err := regexp.Compile(patch.pattern)
	if err != nil {
		return "", fmt.Errorf("compile patch pattern %q: %w", patch.name, err)
	}
	matches := re.FindAllStringIndex(source, expected+1)
	if len(matches) != expected {
		return "", fmt.Errorf("%s: expected %d matches in %s, found %d", patch.name, expected, file, len(matches))
	}
	return re.ReplaceAllString(source, patch.replacement), nil
}

func replaceLiteralExactlyOnce(file, source, old, replacement string) (string, error) {
	count := strings.Count(source, old)
	if count != 1 {
		return "", fmt.Errorf("%s: expected exactly one occurrence in %s, found %d", old, file, count)
	}
	return strings.Replace(source, old, replacement, 1), nil
}
