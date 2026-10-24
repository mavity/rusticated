package assets

import (
	"reflect"
	"testing"
)

func TestModelTiersHaveVerifiedArtifactsAndSizes(t *testing.T) {
	want := map[string]struct {
		native int64
		web    int64
	}{
		"gemma-4-e2b":     {2_588_147_712, 2_008_432_640},
		"gemma-4-e4b":     {3_659_530_240, 2_969_059_328},
		"gemma-4-12b":     {6_883_278_368, 5_986_074_624},
		"gemma-4-26b-a4b": {15_786_524_672, 15_786_524_672},
		"gemma-4-31b":     {19_217_989_632, 19_217_989_632},
	}
	tiers := ModelTiers()
	if len(tiers) != len(want) {
		t.Fatalf("got %d model tiers, want %d", len(tiers), len(want))
	}
	for _, tier := range tiers {
		sizes, ok := want[tier.ID]
		if !ok {
			t.Fatalf("unexpected model tier %q", tier.ID)
		}
		if tier.NativeSize != sizes.native || tier.WebSize != sizes.web {
			t.Errorf("%s sizes = (%d, %d), want (%d, %d)", tier.ID, tier.NativeSize, tier.WebSize, sizes.native, sizes.web)
		}
		if tier.NativeURL == "" || tier.WebURL == "" {
			t.Errorf("%s is missing a native or browser artifact URL", tier.ID)
		}
	}
	if _, ok := LookupModelTier("not-a-tier"); ok {
		t.Fatal("LookupModelTier accepted an unknown ID")
	}
	if _, _, err := ModelTierAsset("not-a-tier"); err == nil {
		t.Fatal("ModelTierAsset accepted an unknown ID")
	}
}

func TestModelTiersReturnsIndependentSlice(t *testing.T) {
	first := ModelTiers()
	first[0].ID = "changed"
	if got := ModelTiers()[0].ID; got != "gemma-4-e2b" {
		t.Fatalf("mutating returned tiers changed catalog ID to %q", got)
	}
}

func TestHardwareTierRecommendation(t *testing.T) {
	tests := []struct {
		name        string
		ram, accel  uint64
		want        string
		constrained bool
	}{
		{"4GB", 4 << 30, 0, "gemma-4-e2b", true},
		{"8GB", 8 << 30, 0, "gemma-4-e4b", true},
		{"16GB", 16 << 30, 0, "gemma-4-12b", false},
		{"24GB GPU", 24 << 30, 16 << 30, "gemma-4-26b-a4b", false},
		{"32GB GPU", 32 << 30, 24 << 30, "gemma-4-31b", false},
		{"26B without GPU", 32 << 30, 0, "gemma-4-12b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := profileFromSlots("linux", []MemorySlot{
				{Kind: MemKindSystemTotal, Bytes: tt.ram},
				{Kind: MemKindGPUDiscrete, Bytes: tt.accel},
			})
			if got := recommendTier(profile); got != tt.want {
				t.Errorf("recommended tier = %q, want %q", got, tt.want)
			}
			if profile.ConstrainedMemory != tt.constrained {
				t.Errorf("ConstrainedMemory = %t, want %t", profile.ConstrainedMemory, tt.constrained)
			}
		})
	}
}

func TestBrowserTierRecommendationRequiresWebGPUArtifactCapacity(t *testing.T) {
	profile := profileFromSlots("browser", []MemorySlot{{Kind: MemKindSystemTotal, Bytes: 16 << 30}})
	profile.HasWebGPU = true
	profile.WebGPUMaxBuffer = 6_000_000_000
	if got := recommendTier(profile); got != "gemma-4-12b" {
		t.Fatalf("recommended browser tier = %q, want gemma-4-12b", got)
	}
	profile.WebGPUMaxBuffer = 1_000_000_000
	if got := recommendTier(profile); got != "" {
		t.Fatalf("recommended tier without enough WebGPU buffer capacity = %q, want none", got)
	}
	profile.HasWebGPU = false
	profile.WebGPUMaxBuffer = 20_000_000_000
	if got := recommendTier(profile); got != "" {
		t.Fatalf("recommended tier without WebGPU = %q, want none", got)
	}
}

func TestValidateMemorySlots(t *testing.T) {
	if err := validateMemorySlots(make([]MemorySlot, MemorySlotCount)); err != nil {
		t.Fatalf("valid slot count rejected: %v", err)
	}
	if err := validateMemorySlots(make([]MemorySlot, MemorySlotCount+1)); err == nil {
		t.Fatal("too many slots accepted")
	}
	if err := validateMemorySlots([]MemorySlot{{Kind: 255}}); err == nil {
		t.Fatal("unknown memory kind accepted")
	}
	if got := ModelTiers()[0].MinRecommendedRAMBytes; !reflect.DeepEqual(got, uint64(4<<30)) {
		t.Fatalf("E2B recommended RAM = %d, want %d", got, uint64(4<<30))
	}
}
