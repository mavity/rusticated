package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCraneliftIsaPatches(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "src")
	abiDir := filepath.Join(contextDir, "machinst")
	x64Dir := filepath.Join(contextDir, "isa", "x64")
	irDir := filepath.Join(contextDir, "ir")
	verifierDir := filepath.Join(contextDir, "verifier")
	if err := os.MkdirAll(abiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(x64Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(irDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(verifierDir, 0o755); err != nil {
		t.Fatal(err)
	}

	contextSource := `impl Context {
    pub fn compile_stencil(
        &mut self,
        isa: &dyn TargetIsa,
        ctrl_plane: &mut ControlPlane,
    ) -> CodegenResult<CompiledCodeStencil> {}

    pub fn optimize(
        &mut self,
        isa: &dyn TargetIsa,
        ctrl_plane: &mut ControlPlane,
    ) -> CodegenResult<()> {}

    /// Compile the function,
    ///
    /// Run the function through all the passes necessary to generate code.
    pub fn compile(
        &mut self,
        isa: &dyn TargetIsa,
        ctrl_plane: &mut ControlPlane,
    ) -> CompileResult<'_, &CompiledCode> {}

    pub fn canonicalize_nans(&mut self, isa: &dyn TargetIsa) -> CodegenResult<()> {
        let has_vector_support = match isa.triple().architecture {
            _ => true,
        };
    }

    pub fn verify<'a, FOI: Into<FlagsOrIsa<'a>>>(&self, fisa: FOI) -> VerifierResult<()> {
        let _ = fisa;
        Ok(())
    }

    /// Run the verifier
    pub fn verify_if<'a, FOI: Into<FlagsOrIsa<'a>>>(&self, fisa: FOI) -> CodegenResult<()> {
        let _ = fisa;
        Ok(())
    }

    /// Perform constant-phi
    pub fn remove_constant_phis<'a, FOI: Into<FlagsOrIsa<'a>>>(&mut self, fisa: FOI) -> CodegenResult<()> {}

    #[deprecated]
    pub fn compile_and_emit(&mut self, isa: &dyn TargetIsa) {}
}`
	x64Source := `pub(crate) struct X64Backend {}
impl X64Backend {
    fn new_with_flags() {}
}`
	manifestSource := `[dependencies.hashbrown]
version = "0.17"
features = ["default-hasher"]
default-features = false

[dependencies.log]
version = "0.4.28"
default-features = false
`
	libSource := `#[cfg(not(feature = "std"))]
use hashbrown::{HashMap, HashSet, hash_map};
#[cfg(feature = "std")]
use std::collections::{HashMap, HashSet, hash_map};
pub type FxHashMap<K, V> = HashMap<K, V, rustc_hash::FxBuildHasher>;
pub type FxHashSet<V> = HashSet<V, rustc_hash::FxBuildHasher>;
`
	abiSource := `impl<M: ABIMachineSpec> Callee<M> {
    pub fn new(
        f: &ir::Function,
        isa: &dyn TargetIsa,
        isa_flags: &M::F,
        sigs: &SigSet,
    ) -> CodegenResult<Self> {
        let _ = CodegenError::Unsupported(format!("invalid dynamic vector type: {}", data.dyn_ty));
    }
}
fn dynamic_types(f: &Function) {
    let dynamic_type_sizes = HashMap::with_capacity(f.dfg.dynamic_types.len());
}`
	vcodeSource := `fn make_maps() {
    let _: HashMap<u32, u32> = HashMap::new();
    let pool_uses = HashMap::with_capacity(expected_num_constants);
    let well_known_uses = HashMap::new();
    let u64s = HashMap::new();
    if want_disasm && !inst.is_args() {
        let mut s = state.clone();
        writeln!(disasm, "  {}", inst.pretty_print_inst(&mut s)).unwrap();
    }
    if want_disasm {
        writeln!(&mut disasm, "block{}:", block.index()).unwrap();
    }
    let user_stack_map_disasm = if want_disasm {
        user_stack_map.as_ref().map(|m| format!("  ; {m:?}"))
    } else {
        None
    };
    (user_stack_map, user_stack_map_disasm)
    let mut debug_tag_disasm = None;
    if want_disasm {
        debug_tag_disasm =
            Some(format!("  ; ^-- debug @ {pos:?}: {tags:?}"));
    }
}`
	memflagsSource := `fn make_maps() {
    let a: HashMap<u32, u32> = HashMap::new();
    let b: HashMap<u32, u32> = HashMap::new();
}
pub fn insert(&mut self, data: MemFlagsData) -> Result<MemFlags, MemFlagsSetOverflow> {
    self.dedupe_map.insert(data, key);
    Ok(key)
}`
	verifierSource := `fn make_map() {
    let _: HashMap<u32, u32> = crate::HashMap::new();
}`
	settingsSource := `impl<'a> From<&'a dyn TargetIsa> for FlagsOrIsa<'a> {
    fn from(isa: &'a dyn TargetIsa) -> FlagsOrIsa<'a> {
        FlagsOrIsa {
            flags: isa.flags(),
            isa: Some(isa),
        }
    }
}`
	timingSource := `#[doc=$desc]
#[must_use]
pub fn $pass() -> Box<dyn Any> {
    start_pass(Pass::$pass)
}`
	lowerSource := "CodegenError::Unsupported(format!(\n    \"should be implemented in ISLE: inst = `{}`" + `, type = ` + "`{:?}`\",\n    self.f.dfg.display_inst(inst),\n    ty,\n))`"
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(manifestSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "lib.rs"), []byte(libSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "context.rs"), []byte(contextSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "timing.rs"), []byte(timingSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abiDir, "lower.rs"), []byte(lowerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abiDir, "abi.rs"), []byte(abiSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abiDir, "vcode.rs"), []byte(vcodeSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(irDir, "memflags.rs"), []byte(memflagsSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verifierDir, "mod.rs"), []byte(verifierSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "settings.rs"), []byte(settingsSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(x64Dir, "mod.rs"), []byte(x64Source), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := applyCraneliftIsaPatches(dir); err != nil {
		t.Fatalf("apply Cranelift ISA patches: %v", err)
	}

	patchedContext, err := os.ReadFile(filepath.Join(contextDir, "context.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(patchedContext), "&impl TargetIsa") != len(contextIsaPatches)+1 {
		t.Fatalf("expected %d generic Context ISA parameters including the diagnostic wrapper, got source:\n%s", len(contextIsaPatches)+1, patchedContext)
	}
	for _, expected := range []string{
		"pub fn compile_and_emit(&mut self, isa: &impl TargetIsa)",
		"pub fn compile_with_hook(",
		"on_phase(1);",
	} {
		if !strings.Contains(string(patchedContext), expected) {
			t.Fatalf("generic compilation API is missing %q:\n%s", expected, patchedContext)
		}
	}

	patchedABI, err := os.ReadFile(filepath.Join(abiDir, "abi.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patchedABI), "isa: &(impl TargetIsa + ?Sized)") {
		if !strings.Contains(string(patchedABI), "isa: &impl TargetIsa") {
			t.Fatalf("Callee::new ISA parameter was not genericized:\n%s", patchedABI)
		}
	}

	patchedSettings, err := os.ReadFile(filepath.Join(contextDir, "settings.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patchedSettings), "impl<'a, T: TargetIsa> From<&'a T> for FlagsOrIsa<'a>") ||
		!strings.Contains(string(patchedSettings), "impl<'a> From<&'a dyn TargetIsa> for FlagsOrIsa<'a>") {
		t.Fatalf("FlagsOrIsa did not retain both concrete and trait-object conversions:\n%s", patchedSettings)
	}

	patchedX64, err := os.ReadFile(filepath.Join(x64Dir, "mod.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patchedX64), "pub struct X64Backend") ||
		!strings.Contains(string(patchedX64), "pub fn new_with_flags(") {
		t.Fatalf("X64 concrete backend API was not exposed:\n%s", patchedX64)
	}

	patchedManifest, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(patchedManifest), `features = ["default-hasher"]`) {
		t.Fatalf("freestanding Cranelift still enables hashbrown's default hasher:\n%s", patchedManifest)
	}
	patchedLib, err := os.ReadFile(filepath.Join(contextDir, "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patchedLib), "type HashMap<K, V, S = rustc_hash::FxBuildHasher>") ||
		!strings.Contains(string(patchedLib), "type HashSet<V, S = rustc_hash::FxBuildHasher>") {
		t.Fatalf("freestanding maps do not default to Fx hashing:\n%s", patchedLib)
	}
}

func TestReplaceRegexExactlyOnceRejectsMissingAndDuplicateAnchors(t *testing.T) {
	patch := contextIsaPatches[0]
	if _, err := replaceRegexExactlyOnce("context.rs", "no matching method", patch); err == nil {
		t.Fatal("expected a missing patch anchor to fail")
	}

	duplicate := `pub fn compile_stencil(&mut self, isa: &dyn TargetIsa);
pub fn compile_stencil(&mut self, isa: &dyn TargetIsa);`
	if _, err := replaceRegexExactlyOnce("context.rs", duplicate, patch); err == nil {
		t.Fatal("expected duplicate patch anchors to fail")
	}
}

func TestLockedCraneliftVersion(t *testing.T) {
	lock := `version = 4

[[package]]
name = "cranelift-entity"
version = "0.136.5"

[[package]]
name = "cranelift-codegen"
version = "0.136.5"
`
	version, err := lockedCraneliftVersion(lock)
	if err != nil {
		t.Fatalf("read locked Cranelift version: %v", err)
	}
	if version != "0.136.5" {
		t.Fatalf("expected locked version 0.136.5, got %q", version)
	}
	if _, err := lockedCraneliftVersion(lock + `
[[package]]
name = "cranelift-codegen"
version = "0.136.6"
`); err == nil {
		t.Fatal("expected duplicate Cranelift lock entries to fail")
	}
}

func TestPrepareCraneliftRuntimeWorkspace(t *testing.T) {
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	rustBuildDir := filepath.Join(root, ".build", "rust")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `[package]
name = "runtime"
version = "0.1.0"
edition = "2024"

[workspace]

[lib]
crate-type = ["staticlib"]
path = "src/lib.rs"
doctest = false
`
	lock := `version = 4
`
	if err := os.WriteFile(filepath.Join(runtimeDir, "Cargo.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "Cargo.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}

	overlayDir := filepath.Join(rustBuildDir, craneliftOverlayName)
	bumpaloDir := filepath.Join(rustBuildDir, bumpaloOverlayName)
	hashbrownDir := filepath.Join(rustBuildDir, hashbrownOverlayName)
	indexmapDir := filepath.Join(rustBuildDir, indexmapOverlayName)
	wasmparserDir := filepath.Join(rustBuildDir, wasmparserOverlayName)
	regalloc2Dir := filepath.Join(rustBuildDir, regalloc2OverlayName)
	workspaceDir, err := prepareCraneliftRuntimeWorkspace(runtimeDir, rustBuildDir, overlayDir, bumpaloDir, hashbrownDir, indexmapDir, wasmparserDir, regalloc2Dir)
	if err != nil {
		t.Fatalf("prepare generated runtime workspace: %v", err)
	}
	generatedManifest, err := os.ReadFile(filepath.Join(workspaceDir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	generated := string(generatedManifest)
	if !strings.Contains(generated, `path = "`+filepath.ToSlash(filepath.Join(runtimeDir, "src", "lib.rs"))+`"`) {
		t.Fatalf("generated manifest does not point at tracked runtime source:\n%s", generated)
	}
	if !strings.Contains(generated, `[patch.crates-io]
cranelift-codegen = { path = "`+filepath.ToSlash(overlayDir)+`" }
bumpalo = { path = "`+filepath.ToSlash(bumpaloDir)+`" }
hashbrown = { path = "`+filepath.ToSlash(hashbrownDir)+`" }
indexmap = { path = "`+filepath.ToSlash(indexmapDir)+`" }
wasmparser = { path = "`+filepath.ToSlash(wasmparserDir)+`" }
regalloc2 = { path = "`+filepath.ToSlash(regalloc2Dir)+`" }`) {
		t.Fatalf("generated manifest does not patch to the Cranelift overlay:\n%s", generated)
	}

	originalManifest, err := os.ReadFile(filepath.Join(runtimeDir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(originalManifest) != manifest {
		t.Fatal("runtime manifest was modified while generating the overlay workspace")
	}
	generatedLock, err := os.ReadFile(filepath.Join(workspaceDir, "Cargo.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if string(generatedLock) != lock {
		t.Fatal("generated workspace lockfile differs from the tracked lockfile")
	}
}

func TestBumpaloOverlayPatches(t *testing.T) {
	snippet := `#[repr(transparent)]
struct EmptyChunkFooter(ChunkFooter);

unsafe impl Sync for EmptyChunkFooter {}

static EMPTY_CHUNK: EmptyChunkFooter = EmptyChunkFooter(ChunkFooter {
    data: NonNull::dangling(),
    layout: Layout::new::<ChunkFooter>(),
    prev: Cell::new(NonNull::dangling()),
    ptr: Cell::new(NonNull::dangling()),
    allocated_bytes: 0,
});

impl EmptyChunkFooter {
    fn get(&'static self) -> NonNull<ChunkFooter> {
        NonNull::from(&self.0)
    }
}`
	s1, err := replaceRegexExactlyOnce("lib.rs", snippet, bumpaloEmptyChunkPatch)
	if err != nil {
		t.Fatalf("apply bumpaloEmptyChunkPatch: %v", err)
	}
	s2, err := replaceRegexExactlyOnce("lib.rs", s1, bumpaloEmptyChunkGetPatch)
	if err != nil {
		t.Fatalf("apply bumpaloEmptyChunkGetPatch: %v", err)
	}
	if !strings.Contains(s2, "get_bump_empty_chunk()") {
		t.Fatalf("expected get_bump_empty_chunk call in patched bumpalo:\n%s", s2)
	}
}

func TestHashbrownOverlayPatches(t *testing.T) {
	snippet := `pub(crate) const fn static_empty() -> &'static [Tag; Group::WIDTH] {
    const ALIGNED_TAGS: AlignedTags = AlignedTags {
        _align: [],
        tags: [Tag::EMPTY; Group::WIDTH],
    };
    &ALIGNED_TAGS.tags
}`
	res, err := replaceRegexExactlyOnce("neon.rs", snippet, hashbrownStaticEmptyPatch)
	if err != nil {
		t.Fatalf("apply hashbrownStaticEmptyPatch: %v", err)
	}
	if !strings.Contains(res, "get_hashbrown_empty_tags()") {
		t.Fatalf("expected get_hashbrown_empty_tags call in patched hashbrown:\n%s", res)
	}
}

func TestWasmparserFormatErrPatch(t *testing.T) {
	snippet := `macro_rules! format_err {
    ($offset:expr, $($arg:tt)*) => {
        crate::Error::fmt(format_args!($($arg)*), $offset)
    }
}`
	res, err := replaceRegexExactlyOnce("lib.rs", snippet, wasmparserFormatErrPatch)
	if err != nil {
		t.Fatalf("apply wasmparserFormatErrPatch: %v", err)
	}
	if !strings.Contains(res, "crate::Error { offset: $offset }") {
		t.Fatalf("expected format_err! to be stubbed:\n%s", res)
	}
}

func TestRcForTypePatches(t *testing.T) {
	snippetAArch64 := `fn rc_for_type(ty: &Type) -> CodegenResult<(&[RegClass], &[Type])> {
    match *ty {
        I8 | I16 | I32 | I64 => Ok((&[RegClass::Int], slice::from_ref(ty))),
        _ => Err(CodegenError::Unsupported("Unexpected SSA-value type".into())),
    }
}
    fn canonical_type_for_rc`

	res, err := replaceRegexExactlyOnce("mod.rs", snippetAArch64, rcForTypeAArch64Patch)
	if err != nil {
		t.Fatalf("apply rcForTypeAArch64Patch: %v", err)
	}
	if !strings.Contains(res, "get_heap_rc_int()") {
		t.Fatalf("expected get_heap_rc_int call in patched aarch64 rc_for_type:\n%s", res)
	}
}
