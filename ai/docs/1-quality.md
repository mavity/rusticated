# OCR Quality Analysis: ocr4 — EasyOCR (CRAFT) Model

## How to Run During Development

```
go run . ocr4 -r --image c:\Users\mihai\rustica\ai\docs\image.png --save
```

**Rules that must never be broken:**
- Always use `go run . ocr4 -r` from `C:\Users\mihai\rustica` (Mohabbat rebuilds and runs the WASM).
- **Never pipe or redirect its output** — washmhost I/O does not propagate through PowerShell pipes. Use `--save` for file output.
- The `-r` flag rebuilds ocr4 before running; omitting it runs the stale binary.

---

## Root Cause of the Original Fragmentation Problem

The initial implementation applied **DB (Differentiable Binarization) post-processing** to the detector's probability map. This was wrong because the Qualcomm EasyOCR detector is **CRAFT (Character Region Awareness for Text Detection)**, not DB.

**Evidence:**

Qualcomm AI Hub Models source, `model.py` (`v0.63.0`):
```python
# https://github.com/qualcomm/ai-hub-models/blob/v0.63.0/src/qai_hub_models/models/easyocr/model.py
from easyocr.craft import CRAFT as CRAFTDetector
...
if isinstance(self.model, CRAFTDetector):
    image = normalize_image_torchvision(image)
    return self.model(image)[0]
```

The `[0]` indexing gives the first element of the tuple `(y, feature)` returned by CRAFT, where `y` has shape `[batch, H/2, W/2, 2]` — **two channels**, not one.

---

## CRAFT Output Format and Correct Post-Processing

### Two-Channel Output

From the EasyOCR detection pipeline, `detection.py`:
```python
# https://github.com/JaidedAI/EasyOCR/blob/master/easyocr/detection.py
y, feature = net(x)
for out in y:
    score_text = out[:, :, 0]   # channel 0 — character region heat map
    score_link = out[:, :, 1]   # channel 1 — affinity / link map between adjacent chars
    boxes, polys, mapper = getDetBoxes(
        score_text, score_link, text_threshold, link_threshold, low_text, ...)
```

**Channel 1 (score_link) is the key to line-level detection.** It assigns high probability to the inter-character and inter-word gaps *within the same text line*, bridging isolated character blobs into coherent word and line regions. Without it, connected-component analysis on channel 0 alone produces character-level fragments (we observed 151 fragments for ~25 text lines).

### Canonical Thresholds

From `easyocr.py`, `readtext()` defaults:
```python
# https://github.com/JaidedAI/EasyOCR/blob/master/easyocr/easyocr.py
text_threshold = 0.7    # peak score_text inside component must exceed this
low_text       = 0.4    # binary threshold on score_text channel
link_threshold = 0.4    # binary threshold on score_link channel
```

### `getDetBoxes_core` Algorithm (`craft_utils.py`)

```python
# https://github.com/JaidedAI/EasyOCR/blob/master/easyocr/craft_utils.py
ret, text_score = cv2.threshold(textmap, low_text, 1, 0)
ret, link_score = cv2.threshold(linkmap, link_threshold, 1, 0)
text_score_comb = np.clip(text_score + link_score, 0, 1)   # logical OR of both maps
nLabels, labels, stats, _ = cv2.connectedComponentsWithStats(
    text_score_comb.astype(np.uint8), connectivity=4)

for k in range(1, nLabels):
    if stats[k, cv2.CC_STAT_AREA] < 10: continue
    if np.max(textmap[labels == k]) < text_threshold: continue   # peak filter
    # adaptive dilation proportional to component size
    niter = int(math.sqrt(size * min(w, h) / (w * h)) * 2)
    # min-area rotated rect on dilated segmentation mask
    rectangle = cv2.minAreaRect(np_contours)
```

Key points compared to what we originally had:
1. **Combined binary map**: `text_score_comb = text_score | link_score`. We were using only channel 0.
2. **Thresholds 0.4/0.4/0.7**, not 0.20. Our `DBThreshold = 0.20` was wrong for this model.
3. **Box filter is MAX** of score_text inside component ≥ 0.7 — not mean. We had mean-based DB filter.
4. **Adaptive per-component dilation** (not a fixed 2×2 kernel). Each component is dilated by a radius proportional to `sqrt(area)`.

---

## Changes Made to ocr4

| Constant / function | Before (wrong — DB) | After (correct — CRAFT) |
|---|---|---|
| `DBThreshold = 0.20` | single-channel pixel threshold | replaced by `CRAFTLowText = 0.4` |
| `DBBoxThreshold = 0.25` | mean probability filter | replaced by `CRAFTTextThreshold = 0.7` (peak) |
| — | ignored | `CRAFTLinkThreshold = 0.4` added |
| Binary map construction | `probMap[i*mapC] > 0.20` | `score_text > 0.4 || score_link > 0.4` |
| Component box filter | `mean(score_text) > 0.25` | `max(score_text) > 0.7` |

The `extractQuads` pipeline (connected components → convex hull → min-area rect → Minkowski unclip → PerspectiveCrop) correctly implements the CRAFT post-processing path once the binary map is constructed from both channels.

---

## What Still Applies from the Original Plan

The [1-quality.md improvement proposal](#improvement-proposal-ocr4-engine-refactoring) was written comparing to PaddleOCR's DB pipeline. The geometry and recognition improvements remain valid regardless of which detector model is used:

- **Rotated min-area rects** — implemented via `minAreaRectFromHull` ✓
- **Minkowski unclip** — implemented via `unclipRect` (ratio 1.5) ✓
- **PerspectiveCrop for dewarping** — implemented in `preprocess.go` ✓
- **2×2 dilation** — still present; in CRAFT context it fills gaps before combined-map analysis ✓
- **Arithmetic mean CTC confidence** — implemented ✓
- **Zero padding for recognizer** — implemented ✓
- **GPU-first backend** — `--backend` now defaults to `"gpu"` ✓

---

## Improvement Proposal: `ocr4` Engine Refactoring

*(Original proposal preserved below; references to DB-specific thresholds should be read in light of the CRAFT correction above.)*

## Executive Summary

This proposal establishes correct parity between `ocr4` and the EasyOCR/CRAFT pipeline. It targets the elimination of heuristic post-processing, incorporates the CRAFT dual-channel binary map construction, and updates the detection and recognition pipelines to the standards established by the EasyOCR source.

---

## Key Refactoring Goals

1. **Correct binary map construction:** Use `score_text | score_link` with thresholds `low_text=0.4` and `link_threshold=0.4` from the CRAFT/EasyOCR source.

2. **Correct box filter:** Peak `score_text` in component ≥ `text_threshold=0.7` (not mean).

3. **Geometry & Dewarping:** Min-area rotated rectangles + Minkowski unclip + PerspectiveCrop.

4. **Recognition Confidence:** Arithmetic mean CTC confidence.

---

## 1. Detection Engine (CRAFT-correct)

### 1.1 Dual-Channel Binary Map

- **Source:** [`detection.py`](https://github.com/JaidedAI/EasyOCR/blob/master/easyocr/detection.py), [`craft_utils.py`](https://github.com/JaidedAI/EasyOCR/blob/master/easyocr/craft_utils.py)
- **Thresholds:** `low_text=0.4`, `link_threshold=0.4` (from [`easyocr.py`](https://github.com/JaidedAI/EasyOCR/blob/master/easyocr/easyocr.py) `readtext` defaults)
- **Combined map:** `binary[i] = score_text[i] > 0.4 || score_link[i] > 0.4`

### 1.2 Box Filter

- **Source:** `getDetBoxes_core` in [`craft_utils.py`](https://github.com/JaidedAI/EasyOCR/blob/master/easyocr/craft_utils.py)
- `if np.max(textmap[labels==k]) < text_threshold: continue` where `text_threshold=0.7`

### 1.3 Contour → Min-Area Rect → Minkowski Unclip → PerspectiveCrop

Per-component: convex hull → rotating calipers → `unclipRect(ratio=1.5)` → `PerspectiveCrop`.

### 1.4 Dilation

The original CRAFT code uses adaptive per-component dilation (`niter = sqrt(area * min(w,h)/(w*h)) * 2`). The current implementation uses a fixed 2×2 kernel, which is a simplification. Adaptive dilation from `craft_utils.py` could be added as a future improvement.

---

## 2. Recognition Pipeline

### 2.1 PerspectiveCrop

Each quad (rotated min-area rect, Minkowski-expanded) is dewarped via a full 8-DOF perspective transform (bilinear, backward-warp) in `preprocess.go`.

**Note on recognizer input shape:**
- Model expects `[1, 64, 800, 1]` NHWC, grayscale, raw `[0,1]` (model applies `(x-0.5)/0.5` internally)
- Source: [`model.py`](https://github.com/qualcomm/ai-hub-models/blob/v0.63.0/src/qai_hub_models/models/easyocr/model.py)

### 2.2 Arithmetic Mean CTC Confidence

```
C_line = (1/N) Σ P_i
```

This matches `np.mean(conf_list)` from PaddleOCR's `rec_postprocess.py` and is less punishing than geometric mean for short/ambiguous crops.

---

## 3. Remaining Work

- **Adaptive component dilation** — port `niter = int(sqrt(size * min(w,h) / (w*h)) * 2)` from `craft_utils.py`
- **Calibrate `CRAFTTextThreshold`** — 0.7 is the default but can be tuned per deployment
- **Platt scaling** — `PlattA, PlattB` remain at identity defaults; held-out data needed


## Phase 1 gap analysis: ocr4 vs. PaddleOCR DB post-processing

### Detection — what ocr4 does

ocr4's `extractBBoxes` (`ocr.go:145`):
- Fixed 608×800 letterbox with **black border padding**
- Applies a single pixel threshold (`DBThreshold = 0.20`)
- **BFS flood-fill** per connected component, takes axis-aligned min/max extents (no contour fitting)
- Converts map→image coords, adds a fixed ±4/5 px pad
- Filters h > 2w as vertical noise
- Heuristic `groupByTextLine` (fixed 6 px Y-gap) and `splitTallBoxes` (1.4× median height)

### Detection — what PaddleOCR does that ocr4 doesn't

1. **Adaptive resize, multiples of 32.**  
   `DetResizeForTest` (`operators.py:207`) scales the image so both sides are multiples of 32 (`round(dim/32)*32`), respecting either a max-side or min-side constraint. PP-OCRv5 uses `limit_type='min', limit_side_len=64`, meaning the image can grow arbitrarily large in one axis. ocr4's black letterbox forces all images into the same 608×800 canvas, which over-downscales wide or high-resolution images and wastes capacity on padding.

2. **Two-stage box filtering with `box_thresh`.**  
   After the pixel threshold (`thresh = 0.3`), PaddleOCR computes the **mean probability inside each contour** and discards boxes below `box_thresh = 0.6` (`db_postprocess.py:133`). This kills fragmented-noise clusters that happen to pass the pixel threshold. ocr4 only checks `area >= DBMinArea`.

3. **Contour → `cv2.minAreaRect` → rotated quad.**  
   `cv2.findContours` + `get_mini_boxes` gives the **minimum-area rotated rectangle** rather than the axis-aligned bounding box. For text tilted even 5–10°, an AABB is noticeably taller and wastes width budget in the recogniser.

4. **Unclip expansion (the canonical DB step).**  
   The DB paper's key post-processing step uses a **Minkowski expansion** proportional to `area/perimeter × unclip_ratio` (default 1.5–2.0) via `pyclipper` (`db_postprocess.py:145`). This makes sure the crop covers the full character cells, not just the stroke centres. ocr4 replaces this with a flat ±4/5 px pad — for small font sizes that pad is proportionally too large; for large fonts it's too small.

5. **Optional dilation on the binary map.**  
   A 2×2 kernel can bridge near-adjacent text pixels before contour extraction, reducing fragmentation of spaced-out text. ocr4 has no equivalent.

6. **Perspective warp on crop extraction.**  
   `get_rotate_crop_image` (`utility.py:868`) uses `cv2.getPerspectiveTransform` + `cv2.warpPerspective` with `BORDER_REPLICATE`. This dewarps rotated text into a tight horizontal strip. ocr4's `CropRect` just clips an AABB, leaving slant artefacts in the crop.

---

### Recognition — what PaddleOCR does that ocr4 doesn't

1. **Angle classifier (180° flip).**  
   `use_angle_cls` runs a tiny binary classifier (0°/180°) on each crop before recognition. Upside-down text is silently mis-decoded in ocr4. The classifier is a cheap guard; its PP-OCR models (`cls_image_shape = "3, 48, 192"`) are very small.

2. **Color (3-channel) input for PP-OCR models.**  
   The standard PP-OCRv4/v5 recogniser expects `[3, 48, 320]` BGR input normalised as `(x/255 - 0.5)/0.5`. ocr4 uses grayscale input because the EasyOCR CRNN model is grayscale-only. If ever switching to a PP-OCR rec model, this matters.

3. **Aspect-ratio sorted batching.**  
   PaddleOCR sorts crops by `w/h`, groups into batches of 6, and zero-pads within the batch to a common width. This allows real batched inference. ocr4 invokes the recogniser once per crop.

4. **Background padding value.**  
   `ResizeHeightAndWidth` (`preprocess.go:74`) pads remaining width with `GrayscaleAt(strip.At(0, 0))` — the first pixel. PaddleOCR pads with zero (which after `(x-0.5)/0.5` normalization maps to `-1.0`, the canonical "empty" for centre-normalised models). A random first-pixel value can create a spurious brightness gradient in the padded region.

5. **Confidence: arithmetic mean vs. geometric mean.**  
   PaddleOCR uses `np.mean(conf_list)`. ocr4 uses a geometric mean (`exp(mean(ln p))`), which is far harsher — a single very-low-probability character drives the whole line confidence to near zero even when most characters are certain. For short crops with one ambiguous character, geometric mean will over-trigger `NeedsLLM`.

---

### Summary of highest-impact changes to consider

| Priority | Area | Problem in ocr4 | PaddleOCR approach |
|---|---|---|---|
| High | Detection | Fixed letterbox, no adaptive resize | Adaptive resize, multiple-of-32 both sides |
| High | Detection | No `unclip` expansion | `pyclipper` Minkowski expand with `unclip_ratio` |
| High | Detection | AABB only | Rotated min-area rect + perspective warp on crop |
| High | Detection | No per-box confidence filter | `box_thresh` on mean prob inside contour |
| Medium | Recognition | No 180° flip guard | Lightweight angle classifier |
| Medium | Recognition | Bad background pad value | Always pad with normalised zero |
| Medium | Recognition | Geometric mean confidence | Arithmetic mean (less punishing) |
| Low | Recognition | One-at-a-time inference | Aspect-ratio sorted batches |

# Improvement Proposal: `ocr4` Engine Refactoring

## Executive Summary

This proposal establishes complete parity between `ocr4` and PaddleOCR's Differentiable Binarization (DB) pipeline. It targets the elimination of heuristic post-processing ("naive fight" algorithms), incorporates missing morphological steps, and updates the detection and recognition pipelines to adhere strictly to mathematical standards.

---

## Key Refactoring Goals & Standard Parity

1. **Remove Ad-Hoc Post-Processing:** Deprecate and remove heuristic functions (`groupByTextLine` and `splitTallBoxes`).


2. **Standardize DB Post-Processing:** Add binary probability map dilation, contour tracing, two-stage `box_thresh` scoring, and Vatti/Minkowski `unclip` polygon expansion.


3. **Geometry & Dewarping:** Compute rotated minimum bounding rectangles (`cv2.minAreaRect` equivalent) and apply $2\times3$ perspective transformations to extract flat, upright line crops.


4. **Recognition Confidence Calibration:** Switch CTC decoding confidence from geometric mean to arithmetic mean to prevent over-triggering Phase 2 refinement.



---

## 1. Phase 1 Detection Engine Upgrades

### 1.1 Adaptive Scaling (`limit_side_len` & Multiples of 32)

* **Problem:** `ocr4` forces all input images into a static $608 \times 800$ letterbox with black padding (`ocr.go:94`).


* **PaddleOCR Standard:** `DetResizeForTest` scales image dimensions preserving aspect ratio such that both width and height are rounded to the nearest multiple of 32. Side lengths are constrained using `limit_type` (`'min'` or `'max'`) and `limit_side_len` (e.g., 640 or 960).


* **Implementation Plan:**
```go
// Resize keeping aspect ratio, respecting min/max constraints and rounding both axes to multiples of 32.
func ResizeAdaptive32(img image.Image, limitSideLen int, limitType string) *image.NRGBA

```



### 1.2 Binary Probability Map Morphological Dilation

* **Problem:** Sparse character strokes or disconnected dot components (e.g., "i", "j", punctuation) can shatter into multiple isolated candidate boxes.


* **PaddleOCR Standard:** Optional binary dilation step (`use_dilation=True`) using a $2 \times 2$ structuring element on the thresholded probability map prior to contour detection.
* **Implementation Plan:**
* Implement 2D binary morphological dilation on `[]bool` probability map before contour extraction.



### 1.3 Contour Tracing, Rotated Rectangles & Minkowski Unclipping

* **Problem:** Bounding boxes in `ocr4` are axis-aligned AABBs (`ocr.go:145`) padded with a static $\pm 4/\pm 5\text{ px}$ margin.


* **PaddleOCR Standard:**
1. Extract connected component contours from the binary map.


2. Evaluate average predicted probability inside the contour against `box_thresh = 0.6`.


3. Calculate minimum-area rotated rectangle (quadrilateral) for passing contours.


4. Perform Vatti polygon clipping / Minkowski expansion (`unclip_ratio = 1.5 - 2.0`):



$$\text{Distance} = \frac{\text{Area} \times \text{unclip\_ratio}}{\text{Perimeter}}$$





### 1.4 Deprecation & Direct Removal of Heuristics

* **Explicit Action:** Once polygon unclipping and rotated quad extraction are in place:
* **Delete `groupByTextLine` (`ocr.go:215`):** Unclip polygons from the DB model natively group complete text lines. Custom Y-gap merging distorts closely-spaced multi-line layouts.


* **Delete `splitTallBoxes` (`ocr.go:245`):** Unclipped DB boundaries eliminate tall combined-box artifacts.





---

## 2. Crop Extraction & Recognition Pipeline

### 2.1 Perspective Transformation Dewarping

* **Problem:** Straight rectangular clipping (`CropRect`) leaves rotated or slanted text tilted within the crop.


* **PaddleOCR Standard:** `get_rotate_crop_image` computes $2\times3$ affine or perspective transformation matrices for each 4-point polygon to output a horizontal, dewarped rectangular tensor.



### 2.2 Arithmetic Mean CTC Confidence & Canonical Normalization

* **Problem:** Geometric mean scoring (`ocr.go:343`) heavily penalizes individual low-confidence characters, driving whole-line scores to near zero and triggering unnecessary LLM refinement (`NeedsLLM = true`).


* **PaddleOCR Standard:**
1. Arithmetic mean probability calculation:



$$C_{\text{line}} = \frac{1}{N} \sum_{i=1}^{N} P_i$$


2. Set recognizer padding pixels to normalized zero (which maps to $-1.0$ in $(x-0.5)/0.5$ space) rather than copying the edge pixel (`strip.At(0, 0)`).





---

## 3. Architecture Flow Diagram

```
                       PADDLEOCR PARITY PIPELINE
                       
 +------------------+      +---------------------------+      +-----------------------+
 |   Input Image    | ---> | Adaptive Resize (Mult 32) | ---> | DB Detector Inference |
 +------------------+      +---------------------------+      +-----------------------+
                                                                          |
 +------------------+      +---------------------------+                  v
 | Perspective Crop | <--- | Minkowski Unclip Polygon  | <--- | 2x2 Binary Dilation   |
 +------------------+      +---------------------------+      +-----------------------+
          |                             ^                                 |
          |                      (Replaces AABB &                         v
          |                     flat pixel pad)         +-----------------------+
          |                                             | Contours + box_thresh |
          v                                             +-----------------------+
 +------------------+      +---------------------------+
 | Angle Cls Check  | ---> |   CRNN/SVTR Recognizer    |
 +------------------+      +---------------------------+
                                         |
                                         v
                           +---------------------------+
                           |  Arithmetic Mean Conf     |
                           +---------------------------+
                                         |
                                         v
                             NeedsLLM Gate (τ < 0.80)

```

---

## 4. Implementation Roadmap

### Phase 1: Decoder Corrections & Code Cleanup

* Update CTC decoding to arithmetic mean.


* Update recognizer canvas padding to normalized $-1.0$.



### Phase 2: Post-Processing Parity

* Add 2D binary morphological dilation ($2\times2$ kernel).
* Integrate `clipper` / `pyclipper` library bindings in Go for Minkowski unclipping.


* Implement two-stage `box_thresh` candidate filtering.


* **Remove `groupByTextLine` and `splitTallBoxes**`.



### Phase 3: Geometry & Crop Dewarping

* Implement minimum area rotated bounding rects (`cv2.minAreaRect` equivalent).


* Implement bilinear perspective transform crop extractor (`preprocess.go`).



### Phase 4: Full Validation

* Re-calibrate Platt scaling parameters against benchmark document datasets.

* **RERUN** the ful end-to-end validation on the image.png and save to the file.

* **REVIEW** the resulting file and assess if it correctly matches the image.