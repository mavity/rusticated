# Architectural Design for Edge-Native Optical Character Recognition and LLM Post-Processing in Go via LiteRT-LM

## System Architecture and Two-Phase Execution Pipeline

Deploying high-precision Optical Character Recognition (OCR) systems directly on edge infrastructure requires balancing execution throughput, visual fidelity, and semantic comprehension. Traditional lightweight vision pipelines—such as Differentiable Binarization (DB) detectors paired with Connectionist Temporal Classification (CTC) sequence recognizers—deliver fast line-level text localization and character decoding. However, these traditional vision models operate without broad linguistic context, leaving them vulnerable to misinterpreting degraded, low-contrast, or handwritten characters. Conversely, large multimodal language models possess strong contextual reasoning capabilities but incur significant computational overhead and risk hallucinations or layout drift when forced to re-transcribe full-page document images without structural boundaries.

To address these constraints, this architecture implements a two-phase document processing pipeline within a zero-CGO Command Line Interface (CLI) application built in Go. The system uses Google’s LiteRT (formerly TensorFlow Lite) and LiteRT-LM runtimes through pure Go Foreign Function Interface (FFI) bindings via the `purego` library. By avoiding CGO, the application maintains cross-compilation across target operating systems while retaining near-native execution performance on CPU, GPU, and NPU hardware backends managed natively by LiteRT.

### Pipeline Overview

* **Phase 1 (Deterministic Extraction)**: Detection and recognition models extract candidate text lines and per-frame confidence metrics.

* **Line Calibration Filter**: Evaluation engine compares line scores ($C_{\text{line}}$) against an operational threshold ($\tau = 0.80$).

* **Phase 2 (Gemma 4 Line Refinement)**:

* **Step A (Document Context Prefill)**: Prefilling 1–5 pages of surrounding document text into Gemma 4's Key-Value (KV) cache.

* **Step B (Targeted Line Pass)**: Passing a line crop image alongside target line prompts into Gemma 4 to correct non-confident lines.

| Pipeline Stage              | Primary Function                                                   | Underlying Neural Models         | Data Inputs | Output Artifacts | LiteRT Hardware Backend |
| --------------------------- | ------------------------------------------------------------------ | -------------------------------- | ----------- | ---------------- | ----------------------- |
| **Phase 1: Line Detection** | Spatial line-level text localization & bounding polygon generation | DB (Differentiable Binarization) |             |                  |                         |

\| Full-resolution image tensor ($1 \times 3 \times H \times W$) | Line polygon coordinates & text probability mask | CPU (XNNPACK) / GPU (WebGPU/Vulkan)

|
| **Phase 1: Line Recognition** | Line-level sequence decoding & probability extraction | SVTR / CRNN (CNN + RNN + CTC)

\| Normalized line image strips ($1 \times 3 \times 32 \times W$) | Candidate text lines & frame log-probabilities

\| CPU (XNNPACK) / GPU (WebGPU)

|
| **Line Calibration Filter** | Uncertainty localization & score calibration | Geometric mean reduction & Platt scaling

\| Frame-level probability vectors

\| Line confidence scores ($C_{\text{line}}$) & low-confidence flags

\| Go Native Runtime (CPU) |
| **Phase 2: Context Prefill** | Macro-textual domain & language context ingestion | Gemma 4 (E2B / E4B native text prefill)

\| 1–5 pages of extracted Phase 1 text | Primed KV cache session state in GPU/NPU memory

\| LiteRT CPU / GPU / NPU

|
| **Phase 2: Targeted Line Pass** | Micro-visual patch re-inspection & line text correction | Gemma 4 (native ViT encoder + text decoder)

\| Line image crop + target line prompt + retained KV cache

\| Corrected text string for target line only

\| LiteRT GPU (WebGPU/Direct3D 12) / NPU

|

***

## Phase 1 Engine: Line Extraction and Calibration

### Neural Network Mechanics and Line CTC Decoding

The Phase 1 text recognition network processes normalized horizontal image strips of height $H = 32$ pixels and dynamic width $W$. The visual feature extractor projects the line pixel grid into a sequential feature representation across $T$ horizontal time steps. A fully connected classification layer converts these feature vectors into a sequence of logit vectors $z_t \in \mathbb{R}^K$, where $K$ corresponds to the size of the character vocabulary $V$ plus the Connectionist Temporal Classification (CTC) blank token $\epsilon$.

The raw probability $P(y_t = k \mid x)$ of observing vocabulary token $k$ at frame index $t$ given input image features $x$ is computed using the softmax function:

$P(y_t = k \mid x) = \frac{\exp(z_{t,k})}{\sum_{j=1}^{K} \exp(z_{t,j})}$

During standard CTC greedy decoding, the alignment sequence $\pi = (\pi_1, \pi_2, \dots, \pi_T)$ is formed by selecting the argmax token at each frame $t$: $\pi_t = \arg\max_k P(y_t = k \mid x)$. The collapse operator $\mathcal{B}$ removes adjacent duplicate tokens and deletes all blank tokens $\epsilon$ to produce the finalized text transcript $S = \mathcal{B}(\pi) = (c_1, c_2, \dots, c_N)$. The pure Go execution layer intercepts these logit vectors prior to CTC collapse, retaining the underlying probability distributions assigned to each non-blank character alignment.

### Line Confidence Metrics ($C_{\text{line}}$)

Rather than relying on global document average confidence metrics, the Phase 1 engine evaluates character probabilities at the line level:

The Character Confidence Score $S_{\text{char}}(c_i)$ represents the softmax probability assigned to the chosen character $c_i$ at its corresponding non-blank frame alignment $t_i$:

$S_{\text{char}}(c_i) = P(y_{t_i} = c_i \mid x)$

The Line Confidence Score $C_{\text{line}}$ is calculated as the geometric mean of the constituent character probabilities across a decoded text line containing $N$ valid characters:

$C_{\text{line}} = \exp \left( \frac{1}{N} \sum_{i=1}^{N} \ln S_{\text{char}}(c_i) \right) = \left( \prod_{i=1}^{N} S_{\text{char}}(c_i) \right)^{\frac{1}{N}}$

The geometric mean is mathematically preferable to the arithmetic mean because it penalizes isolated low-probability character predictions more severely. A single degraded or smudged character scoring $S_{\text{char}} = 0.10$ within a thirty-character line will lower $C_{\text{line}}$ significantly, reliably flagging the entire line for Phase 2 inspection even if the remaining twenty-nine characters score above $0.98$.

### Calibration Metrics and Expected Calibration Error (ECE)

Neural network outputs frequently exhibit miscalibration, outputting overconfident probability distributions even when making incorrect predictions. This calibration discrepancy is measured using the Expected Calibration Error (ECE). The dataset of $N_{\text{total}}$ line predictions is grouped into $M$ equally spaced confidence intervals $B_m \subset (0, 1]$. The ECE is defined as the weighted absolute difference between empirical accuracy $\text{acc}(B_m)$ and average predicted confidence $\text{conf}(B_m)$ across all bins:

$\text{ECE} = \sum_{m=1}^{M} \frac{\vert{}B_m\vert{}}{N_{\text{total}}} \left\vert{} \text{acc}(B_m) - \text{conf}(B_m) \right\vert{}$

Empirical evaluations across open-source and commercial OCR engines demonstrate substantial variations in error rates and calibration accuracy. Open-source systems like PaddleOCR exhibit higher ECE values ($11.5\% - 22.6\%$) compared to managed cloud services ($1.1\% - 3.6\%$).

\| OCR Engine | Character Error Rate (CER %)

\| Bounding-Box Error Rate (BER %)

\| Expected Calibration Error (ECE %)

\| Operational Score Reliability Level

|
\| --- | --- | --- | --- | --- |
| **AWS Textract** | 1.3 - 3.1 | 3.9 - 6.3 | 1.1 - 3.0 | High Calibration Accuracy (>= 95%)

|
| **Azure AI Vision** | 0.8 - 2.3 | 1.5 - 5.9 | 1.4 - 3.0 | High Calibration Accuracy (>= 95%)

|
| **Google Cloud Vision** | 0.6 - 5.3 | 1.2 - 9.9 | 2.3 - 3.6 | Moderate Calibration Accuracy (80% - 94%)

|
| **DocTR (Open Source)** | 3.2 - 7.7 | 9.4 - 19.7 | 2.5 - 5.4 | Moderate Calibration Accuracy (80% - 94%)

|
| **EasyOCR (Open Source)** | 12.0 - 28.9 | 44.2 - 75.6 | 10.3 - 17.4 | Low Calibration Accuracy (60% - 79%)

|
| **PaddleOCR / PP-OCRv4** | 4.3 - 8.9 | 19.7 - 36.5 | 11.5 - 22.6 | Poor Calibration Accuracy (< 60%)

|

Because PP-OCR models exhibit a high ECE, raw uncalibrated probabilities cannot directly serve as exact decision boundaries. The Phase 1 engine applies a parametric Platt scaling transformation to raw confidence scores, mapping them to calibrated probabilities before evaluating against the line threshold $\tau = 0.80$.

***

## Phase 2 Engine: Line-by-Line Refinement with Gemma 4

### Model Selection and On-Device Constraints

Phase 2 uses a single Gemma 4 deployment packaged in the `.litertlm` container format and executed via LiteRT-LM. Gemma 4 features an integrated Vision Transformer (ViT) encoder (\~150M parameters), enabling a single model binary to process both pure text prompts and multimodal image-text prompts.

| Model Identifier | Parameter Scale | Quantization Strategy | File Size (.litertlm) | Native Visual Encoder | Supported Hardware Accelerators |
| ---------------- | --------------- | --------------------- | --------------------- | --------------------- | ------------------------------- |
| **Gemma 4 E2B**  | 2.0 Billion     |                       |                       |                       |                                 |

\| int8 weights / fp16 act | \~2.58 GB

\| Integrated ViT (\~150M) | CPU (XNNPACK) / GPU (WebGPU)

|
| **Gemma 4 E4B** | 4.0 Billion

\| int4 / int8 mixed | \~2.80 GB | Integrated ViT (\~150M) | CPU / GPU / NPU

|

### Macro-Context KV Cache Caching Optimization

Submitting 1–5 pages of surrounding document text (~1,500–5,000 tokens) provides deep linguistic context, helping resolve visual character ambiguities (e.g., `rn` vs. `m`, `0` vs. `O`, or `l` vs. `1`). However, re-evaluating thousands of prompt tokens for every non-confident line on edge hardware would introduce noticeable prefill latency.

To eliminate this overhead, LiteRT-LM (`litert-go` `lm` package) supports multi-turn session state preservation and KV-cache serialization. The CLI executes a single initial prefill turn containing the multi-page text context:

$\text{KV}_{\text{doc}} = \text{Prefill}(\text{Text}_{1..5\text{ pages}})$

Once $\text{KV}_{\text{doc}}$ is allocated in device memory, subsequent queries for low-confidence lines append their specific line prompt and image crop directly onto the existing KV cache state. Per-line execution costs are thereby reduced from evaluating full multi-page prefill matrices to merely processing the line image patch and generating 10–30 decoding tokens.

### Micro-Visual Line Patch Ingestion and Prompt Structure

For each line flagging $C_{\text{line}} < \tau$, the CLI extracts the bounding line crop from Phase 1. Resampling a single-line image crop matches the model's visual attention directly to the target text area, preventing multi-column confusion or line misalignments.

To enforce precise output generation from Gemma 4, the prompt leverages the retained document context and XML structural tags:

```
System: You are an expert post-OCR text correction engine. The surrounding document context has been provided in our conversation history.
Task: Inspect the attached image crop for the target line, reference the surrounding document context, and re-read the target line accurately.

Line Location: Page 2, Line 14
Phase 1 Candidate Output: "The vendor confirms that inv0ice total is $1,450.0O payable by EOM"

Instruction: Output ONLY the corrected text string for this specific line. Do not output explanations, quotes, or markdown tags.

```

Gemma 4 combines visual features from the line crop with language context from the KV cache to resolve line-level errors—such as correcting `inv0ice` to `invoice` and `$1,450.0O` to `$1,450.00`—while leaving the rest of the page untouched.

***

## Technical Hurdles in Pure Go Integration (`purego` + LiteRT / LiteRT-LM)

### Zero-CGO Architecture and FFI Dynamic Linking

Using CGO to interface Go applications with C/C++ shared libraries introduces operational challenges, including slow compilation times, complex cross-compiler toolchain dependencies, and glibc platform lock-in. To maintain a clean zero-CGO build pipeline, this application uses `purego`.

`purego` loads the native LiteRT dynamic shared library (`libLiteRt.so`, `libLiteRt.dylib`, or `LiteRt.dll`) at runtime using host system calls (`dlopen`/`dlsym` on POSIX systems; `LoadLibrary`/`GetProcAddress` on Windows). Function signatures are bound directly to Go function variables at application startup. Go 1.26 optimized this dynamic FFI execution path by reducing internal system stack transition overhead (`cgocall`) by approximately 30%, enabling efficient foreign function invocation.

```go
package main

import (
	"fmt"
	"github.com/ebitengine/purego"
)

type LiteRtCompiledModelCreateFunc func(
	env uintptr,
	modelPath *byte,
	accelerator uint32,
	outModel *uintptr,
) int32

var liteRtCompiledModelCreate LiteRtCompiledModelCreateFunc

func InitLiteRtSymbols(libPath string) error {
	handle, err := purego.Dlopen(libPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fmt.Errorf("unable to load LiteRT library: %w", err)
	}
	purego.RegisterLibFunc(&liteRtCompiledModelCreate, handle, "LiteRtCompiledModelCreate")
	return nil
}

```

### Goroutine Stack Movement and Memory Pinning (`runtime.Pinner`)

A major technical challenge when executing dynamic C calls from Go without CGO involves memory safety during stack management. Go uses growable goroutine stacks. During runtime execution, the Go scheduler may relocate a goroutine's stack to a new memory segment, updating internal stack pointers automatically.

However, when a memory pointer is passed across the FFI boundary to a C library via `unsafe.Pointer`, the Go stack mover remains unaware of references held by C code. If a stack shift occurs mid-call, the C function will write output data to an invalidated memory location, corrupting host process memory.

To resolve this, every Go-allocated memory buffer whose address reaches the LiteRT C API must be explicitly pinned using `runtime.Pinner`:

```go
func ExecuteInference(modelHandle uintptr, inputTensor []float32, outputTensor []float32) error {
	var pinner runtime.Pinner

	// Pin memory locations to prevent GC stack relocation during execution
	pinner.Pin(&inputTensor[0])
	pinner.Pin(&outputTensor[0])
	defer pinner.Unpin()

	status := liteRtRunAsync(
		modelHandle,
		uintptr(unsafe.Pointer(&inputTensor[0])),
		uintptr(unsafe.Pointer(&outputTensor[0])),
	)
	if status != 0 {
		return fmt.Errorf("inference execution failed with error code: %d", status)
	}
	return nil
}

```

For performance-critical code paths, such as Phase 1 batch line processing, pinning long-lived input and output buffers during engine initialization avoids the per-invocation overhead of ephemeral `runtime.Pinner` instances.

### Cross-ABI Struct Layout and Padding Alignment

When declaring Go struct representations of C types for FFI passing, field alignments and byte padding must match host compiler ABI rules exactly. `purego` does not automatically calculate or insert structural padding bytes.

For example, on MSVC (Windows), `LiteRtRankedTensorType` contains bitfield flags (`rank` and `has_strides`) that are not packed, placing the dimension array pointer at offset 12. On GCC/Clang (Linux/macOS), these bitfields are packed, placing the pointer at offset 8. The Go bindings account for these platform-specific layout differences using conditional target build tags:

```go
// Windows MSVC Target Struct Alignment Rules
//go:build windows

type LiteRtRankedTensorType struct {
	ElementType uint32
	Rank        uint32
	HasStrides  uint8
	_           [3]byte // Explicit padding bytes aligning Dims pointer to offset 12
	Dims        *int32
}

```

### Static Execution Graphs, KV-Cache Bounds, and GPU Caching

LiteRT-LM models ship as static computation graphs. Context lengths are bounded by the static KV-cache size compiled into the `.litertlm` container (e.g., 8192 or 32768 tokens). To handle variable multi-page document lengths efficiently, the engine uses prefill bucketing, padding document inputs to discrete bucket shapes.

When targeting GPU backends via WebGPU or Vulkan, LiteRT compiles compute shaders during the first model invocation. To prevent cold-start delays on CLI runs, the application configures disk shader caching via `WithGPUCacheDir`. Warm starts read precompiled GPU shaders directly from disk, reducing startup latency from several seconds to milliseconds.

***

## Programmatic Execution Workflow

The complete processing workflow within the zero-CGO Go CLI application operates through the following unified execution path:

```go
package main

import (
	"fmt"
	"image"
	"github.com/vladimirvivien/litert-go/litert"
	"github.com/vladimirvivien/litert-go/lm"
)

type CLIConfig struct {
	Threshold       float64
	GPUCacheDir     string
	Gemma4ModelPath string
}

type TextLine struct {
	ID          int
	BoundingBox image.Rectangle
	Text        string
	Confidence  float64
}

func ProcessDocumentPipeline(imgPath string, documentPagesText string, cfg CLIConfig) error {
	// Step 1: Execute Phase 1 Deterministic Line Extraction
	ocrEngine, err := InitPhase1OCREngine()
	if err != nil {
		return fmt.Errorf("Phase 1 initialization failed: %w", err)
	}
	defer ocrEngine.Close()

	lines, err := ocrEngine.ExtractLinesAndMetrics(imgPath)
	if err != nil {
		return fmt.Errorf("Phase 1 text extraction failed: %w", err)
	}

	// Step 2: Evaluate Line Confidence Scores against Threshold τ
	var lowConfidenceLines []TextLine
	for _, line := range lines {
		if line.Confidence < cfg.Threshold { // τ = 0.80
			lowConfidenceLines = append(lowConfidenceLines, line)
		}
	}

	// Fast Path: If all lines meet the confidence threshold, emit Phase 1 results directly
	if len(lowConfidenceLines) == 0 {
		fmt.Println("Document successfully processed via Phase 1 fast path.")
		EmitResults(lines)
		return nil
	}

	// Step 3: Initialize Gemma 4 Engine via LiteRT-LM
	gemmaEngine, err := lm.Open(
		cfg.Gemma4ModelPath,
		litert.WithAccelerator(litert.AccelGPU),
		litert.WithGPUCacheDir(cfg.GPUCacheDir),
	)
	if err != nil {
		return fmt.Errorf("Phase 2 Gemma 4 initialization failed: %w", err)
	}
	defer gemmaEngine.Close()

	// Step 4: Establish Multi-Turn Conversation Session & Prefill Document Context
	session := gemmaEngine.NewConversation()
	
	prefillPrompt := fmt.Sprintf(
		"System: You are an OCR post-processing engine. Here is the 1-5 page document context:\n<doc_context>\n%s\n</doc_context>",
		documentPagesText,
	)
	_, err = session.Send(prefillPrompt)
	if err != nil {
		return fmt.Errorf("KV Cache prefill failed: %w", err)
	}

	// Step 5: Process Each Non-Confident Line with Target Line Prompt + Line Crop Image
	for idx, line := range lowConfidenceLines {
		lineCrop := CropImageRegion(imgPath, line.BoundingBox)
		
		linePrompt := fmt.Sprintf(
			"Line ID %d candidate: '%s'. Read the attached line image crop, reference the document context, and output ONLY the corrected line text.",
			line.ID, line.Text,
		)

		// Send line crop image alongside prompt; session retains KV cache of document context
		correctedText, err := session.SendFromImage(lineCrop, linePrompt)
		if err == nil && correctedText != "" {
			lowConfidenceLines[idx].Text = correctedText
		}
	}

	EmitResults(lines)
	return nil
}

```

The CLI execution flow proceeds as follows:

The application parses command-line flags, configures dynamic library locations, and loads the native LiteRT runtime using `purego`.

Phase 1 executes text detection and recognition on the input document image. The engine computes per-frame CTC log-probabilities and evaluates line confidence scores ($C_{\text{line}}$) using geometric mean reduction.

The calibration filter compares each line's confidence score against the operational threshold ($\tau = 0.80$). If all lines exceed $\tau$, the CLI outputs the Phase 1 transcript directly, bypassing Phase 2 entirely.

If low-confidence lines are identified, the application initializes Gemma 4 via LiteRT-LM (`lm` package) and starts a conversation session. It sends an initial prefill prompt containing 1–5 pages of extracted document text, populating the KV cache.

For each non-confident line, the CLI crops the target line image rectangle and sends a query containing the line crop image alongside the target line prompt. Gemma 4 utilizes its native visual encoder and the retained document context in its KV cache to re-generate text strictly for that line.

Finally, corrected line entries replace their Phase 1 counterparts in the document layout, the output is formatted as JSON or plain text, and all pinned memory handles are released.

***

## Actionable Architecture Directives

1. **Deploy Zero-CGO Bindings for Cross-Platform CLI Tools**: Implement dynamic FFI bindings via `purego` to eliminate C compiler dependencies during builds. This simplifies deployment pipelines while leveraging Go 1.26's optimized FFI stack transition performance.

2. **Apply Line-Level Calibration Scaling**: Calculate line confidence ($C_{\text{line}}$) via geometric mean reduction on character probabilities, applying Platt scaling before evaluating against threshold $\tau = 0.80$ to account for Expected Calibration Error (ECE).

3. **Prefill Document Context into KV Cache**: Perform a single initial prefill pass of 1–5 pages of surrounding document text into Gemma 4's KV cache session state, avoiding repeated prefill latency across multiple line queries.

4. **Isolate Line Crops for Target Pass**: Pass tightly cropped line-level image rectangles to Gemma 4's native visual encoder during post-processing to align visual attention strictly to the target text region.

5. **Restrict Decoding Token Bounds**: Restrict Gemma 4's output prompt to re-generate text strictly for the target line, minimizing decoding latency ($10\text{--}30$ tokens per line) and preventing layout drift.

