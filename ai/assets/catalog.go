package assets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const modelRepositoryBase = "https://huggingface.co/litert-community"

// ModelTier describes one supported Gemma 4 LiteRT-LM artifact.
type ModelTier struct {
	ID                     string
	Name                   string
	NativeURL              string
	NativeSize             int64
	WebURL                 string
	WebSize                int64
	MinRecommendedRAMBytes uint64
	MinAcceleratorBytes    uint64
	PracticalContextTokens int
	WebGPUSupported        bool
	NativeRequiresGPU      bool
}

var gemma4Tiers = [...]ModelTier{
	{
		ID:                     "gemma-4-e2b",
		Name:                   "Gemma 4 E2B",
		NativeURL:              modelRepositoryBase + "/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it.litertlm",
		NativeSize:             2_588_147_712,
		WebURL:                 modelRepositoryBase + "/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it-web.litertlm",
		WebSize:                2_008_432_640,
		MinRecommendedRAMBytes: 4 << 30,
		MinAcceleratorBytes:    2 << 30,
		PracticalContextTokens: 8192,
		WebGPUSupported:        true,
	},
	{
		ID:                     "gemma-4-e4b",
		Name:                   "Gemma 4 E4B",
		NativeURL:              modelRepositoryBase + "/gemma-4-E4B-it-litert-lm/resolve/main/gemma-4-E4B-it.litertlm",
		NativeSize:             3_659_530_240,
		WebURL:                 modelRepositoryBase + "/gemma-4-E4B-it-litert-lm/resolve/main/gemma-4-E4B-it-web.litertlm",
		WebSize:                2_969_059_328,
		MinRecommendedRAMBytes: 8 << 30,
		MinAcceleratorBytes:    4 << 30,
		PracticalContextTokens: 16384,
		WebGPUSupported:        true,
	},
	{
		ID:                     "gemma-4-12b",
		Name:                   "Gemma 4 12B",
		NativeURL:              modelRepositoryBase + "/gemma-4-12B-it-litert-lm/resolve/main/gemma-4-12B-it.litertlm",
		NativeSize:             6_883_278_368,
		WebURL:                 modelRepositoryBase + "/gemma-4-12B-it-litert-lm/resolve/main/gemma-4-12B-it-web.litertlm",
		WebSize:                5_986_074_624,
		MinRecommendedRAMBytes: 16 << 30,
		MinAcceleratorBytes:    8 << 30,
		PracticalContextTokens: 24576,
		WebGPUSupported:        true,
	},
	{
		ID:                     "gemma-4-26b-a4b",
		Name:                   "Gemma 4 26B MoE",
		NativeURL:              modelRepositoryBase + "/gemma-4-26B-A4B-it-litert-lm/resolve/main/gemma-4-26B-A4B-it-gpu.litertlm",
		NativeSize:             15_786_524_672,
		WebURL:                 modelRepositoryBase + "/gemma-4-26B-A4B-it-litert-lm/resolve/main/gemma-4-26B-A4B-it-web.litertlm",
		WebSize:                15_786_524_672,
		MinRecommendedRAMBytes: 24 << 30,
		MinAcceleratorBytes:    16 << 30,
		PracticalContextTokens: 32768,
		NativeRequiresGPU:      true,
	},
	{
		ID:                     "gemma-4-31b",
		Name:                   "Gemma 4 31B",
		NativeURL:              modelRepositoryBase + "/gemma-4-31B-it-litert-lm/resolve/main/gemma-4-31B-it-gpu.litertlm",
		NativeSize:             19_217_989_632,
		WebURL:                 modelRepositoryBase + "/gemma-4-31B-it-litert-lm/resolve/main/gemma-4-31B-it-web.litertlm",
		WebSize:                19_217_989_632,
		MinRecommendedRAMBytes: 32 << 30,
		MinAcceleratorBytes:    24 << 30,
		PracticalContextTokens: 32768,
		NativeRequiresGPU:      true,
	},
}

// ModelTiers returns the supported tiers in increasing model-size order.
func ModelTiers() []ModelTier {
	return append([]ModelTier(nil), gemma4Tiers[:]...)
}

// LookupModelTier returns a tier by its stable ID.
func LookupModelTier(id string) (ModelTier, bool) {
	for _, tier := range gemma4Tiers {
		if tier.ID == id {
			return tier, true
		}
	}
	return ModelTier{}, false
}

// ModelTierAsset returns the host-specific URL and exact size for a tier.
func ModelTierAsset(id string) (url string, size int64, err error) {
	tier, ok := LookupModelTier(id)
	if !ok {
		return "", 0, fmt.Errorf("unknown Gemma 4 tier %q", id)
	}
	if HostOS() == "browser" {
		if !tier.WebGPUSupported {
			return "", 0, fmt.Errorf("%s is not supported by the browser WebGPU backend", tier.Name)
		}
		return tier.WebURL, tier.WebSize, nil
	}
	return tier.NativeURL, tier.NativeSize, nil
}

// EnsureModelTier caches the selected host-specific model artifact.
func EnsureModelTier(ctx context.Context, id string, opts FetchOptions) (string, error) {
	url, size, err := ModelTierAsset(id)
	if err != nil {
		return "", err
	}
	return EnsureModel(ctx, url, size, opts)
}

func ModelTierDownloadSize(id, cacheDir string) (int64, error) {
	url, size, err := ModelTierAsset(id)
	if err != nil {
		return 0, err
	}
	if HostOS() == "browser" {
		if browserModelSize(url) > 0 {
			return 0, nil
		}
		return size, nil
	}
	if info, err := os.Stat(filepath.Join(cacheDir, filepath.Base(url))); err == nil && info.Size() >= size {
		return 0, nil
	} else if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("inspect cached model %s: %w", filepath.Base(url), err)
	}
	return size, nil
}

func EmbeddingModelDownloadSize(cacheDir string) (int64, error) {
	if HostOS() == "browser" {
		return 0, nil
	}
	path := filepath.Join(cacheDir, filepath.Base(DefaultEmbeddingModelURL))
	if info, err := os.Stat(path); err == nil && info.Size() >= DefaultEmbeddingModelSize {
		return 0, nil
	} else if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("inspect cached embedding model: %w", err)
	}
	return DefaultEmbeddingModelSize, nil
}
