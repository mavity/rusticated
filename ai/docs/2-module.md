# AI/LitertLM consolidation

**WARNING!** Do not use terminal commands, scripts for file editing. Use file editing tools ONLY.

### 0. Prerequisites: Environment Fixes
Before creating the new `ai` module, the `washmhost` environment must be updated so that the WASM guest correctly inherits the user's terminal Working Directory (CWD). This is a hard requirement for resolving relative file paths (e.g., `--image ai/docs/image.png`).
- **`mohabbat/run.go`:** Update the host runner to capture the actual terminal working directory via `os.Getwd()` and pass it as a `--cwd <path>` CLI argument to the `washmhost` process.
- **`mohabbat/washmhost/main.go`:** Update the host program to parse this `--cwd` flag, call `os.Chdir()` to sync its execution context back to the user's terminal, and then strip the flag before passing the remaining arguments to the WASM module.

### 1. The `ai` Module Architecture

**WARNING!** Built-in file editing tools are exclusive for editing files. You must not resort to terminal commands or scripts (such as Python, Bash or Go or any other programming language) for file editing.

The new module will be created at `/target/rusticated/ai`. It will have two packages:
- `ai` (root): The main API for model interaction, covering both LLMs (Engine/Conversation) and generic vision models (TFLite Interpreter).
- `ai/assets`: A unified downloader/extractor for the runtime wheel, single models, and zipped model archives.

#### 1.1. Root Package API (`ai`)
We want to expose a clean, idiomatic Go API that completely hides `purego`, C-pointers, WASI pointer math, and the LiteRT JSON schema.

**Files:**
- `go.mod`: `module ai`
- `ai.go`: Defines the public types (`Engine`, `Conversation`, `PromptPart`, `Role`, `Interpreter`) and high-level methods.
- `engine_native.go` & `engine_wasip1.go`: Implementations for the LLM Engine (ports of `lm_native.go` and `lm_wasip1.go`), containing the C-trampolines and internal JSON encoding/decoding.
- `tflite_native.go` & `tflite_wasip1.go`: Implementations for raw TFLite tensors (ports of `litert_native.go` and `litert_wasip1.go`).
- `dl_unix.go` & `dl_windows.go`: Brought over to support `purego` dynamic loading for both the LLM and TFLite bindings.

**Key Types & Methods (Conceptual):**
- *LLM Engine:*
  - `type Engine struct {...}`
  - `func NewEngine(libPath, modelPath, backend string) (*Engine, error)`
  - `func (e *Engine) NewConversation() (*Conversation, error)`
  - `func (c *Conversation) Send(ctx context.Context, parts []PromptPart, onToken func(string)) error`
  - `func (c *Conversation) SendText(ctx context.Context, text string, onToken func(string)) error`
- *TFLite Vision/Generic Inference:*
  - `type Interpreter struct {...}`
  - `func NewInterpreter(libPath, modelPath string) (*Interpreter, error)`
  - `func (i *Interpreter) SetInput(index int, data []float32) error`
  - `func (i *Interpreter) GetOutput(index int) ([]float32, []int32, error)`
  - `func (i *Interpreter) Invoke() error`

#### 1.2. Assets Subpackage (`ai/assets`)
This package manages all networking, file system caching, PyPI discovery, and extraction. It is completely UI-agnostic, using a simple closure for progress updates.

**Files:**
- `assets.go`: The main entry points (`EnsureRuntime`, `EnsureModel`, `EnsureZipModels`).
- `fetch.go`: The internal downloading logic (HTTP requests, validation, extraction).
- `platform_native.go`: Robust OS/Arch detector for native builds (using `runtime.GOOS`/`runtime.GOARCH`).
- `platform_wasip1.go`: Robust OS/Arch detector for WASM builds. **ABI Constraint:** This file MUST use `syscall.GetPlatformInfo()` (specifically fields like `OSName` and `BuildPlatform`) to correctly identify the underlying host OS and architecture (e.g., Darwin/ARM64), overriding the default `wasip1/wasm`.

**Key Types & Methods (Conceptual):**
- `type FetchOptions struct { CacheDir string; OnProgress func(stage, details string, percent int) }`
- `func EnsureRuntime(ctx context.Context, opts FetchOptions) (string, error)`: **Offline-First Constraint:** Must use the robust platform detection to search the local cache via `findCachedWheel` *before* attempting any network request to PyPI. Downloads the PyPI wheel (if necessary), extracts it, and returns the path to the native library used by both `Engine` and `Interpreter`.
- `func EnsureModel(ctx context.Context, url string, expectedSize int64, opts FetchOptions) (string, error)`: Handles single-file model downloads (like Gemma).
- `func EnsureZipModels(ctx context.Context, url string, requiredFiles []string, opts FetchOptions) (map[string]string, error)`: Downloads a zip, extracts the required models (e.g., EasyOCR), and returns a map of their absolute paths.

### 2. Integration into `ocr4`

**WARNING!** Use only file editing tools for modifying files. Avoid terminal commands or indeed scripts for file editing.

Once the `ai` module is complete, `ocr4` is refactored to be **100% pure Go business logic** with zero C-FFI or raw downloading logic.

**Files to modify:**
- `go.mod`: Add `replace ai => ../ai`.
- `refine.go`: Replace manual JSON marshaling and `extractTokenText` with `conv.Send()` using `ai.PromptPart`.
- `ocr.go` (and similar): Switch from using the internal TFLite wrapper to `ai.Interpreter` for the DBNet and recognizer models.
- `assets.go`:
  - Delete all downloading, PyPI JSON parsing, wheel extraction, and zip extraction logic.
  - In `EnsureAssets`, simply call `ai/assets.EnsureRuntime`, `ai/assets.EnsureModel`, and `ai/assets.EnsureZipModels`, mapping the returned strings to the config object.
- `main.go`: Update engine and interpreter initializations to use the `ai` package.

**Files to delete entirely from `ocr4`:**
- `lm_native.go`
- `lm_wasip1.go`
- `litert_native.go`
- `litert_wasip1.go`
- `dl_unix.go`
- `dl_windows.go`

### 3. Summary of Execution Steps
1. **Step 0: Implement CWD Fix:** Implement the `--cwd` CLI argument passing in `mohabbat/run.go` and directory sync via `os.Chdir()` in `mohabbat/washmhost/main.go` to guarantee relative paths resolve correctly.
2. Create `/ai` dir and initialize module.
3. Implement `ai/assets`, explicitly including the robust OS/Arch detectors (`platform_native.go` / `platform_wasip1.go`) to enable offline-first cache resolution.
4. Implement `ai` root API by lifting both LLM and TFLite `purego` bindings from `ocr4`, fully encapsulating the JSON/tensor logic.
5. Clean up `ocr4`: delete all C-FFI files (`*_native.go`, `*_wasip1.go`, `dl_*.go`), replace `assets.go` logic with the new unified API calls, and update inference code to use the `ai` module.
6. Verify by running `ocr4` with a relative path (e.g., `--image ai/docs/image.png`).

### 4. Safety Constraints
- **Do NOT delete or modify the user's `~/.cache` directories.** The offline-first design must rely on and respect existing cached assets.
- **Do NOT modify any other files in the `mohabbat` module** beyond the targeted `--cwd` argument fix specified in Step 0.

**WARNING!** Use file editing tools for modifying files. Refrain from terminal commands, scripts for file editing.