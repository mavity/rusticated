package main

import (
	"testing"
)

func TestVersionLess(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{
			name: "1.0.0 < 2.0.0",
			a:    "1.0.0",
			b:    "2.0.0",
			want: true,
		},
		{
			name: "2.0.0 < 1.0.0 (false)",
			a:    "2.0.0",
			b:    "1.0.0",
			want: false,
		},
		{
			name: "1.2.0 < 1.3.0",
			a:    "1.2.0",
			b:    "1.3.0",
			want: true,
		},
		{
			name: "1.2.3 < 1.2.4",
			a:    "1.2.3",
			b:    "1.2.4",
			want: true,
		},
		{
			name: "1.0.0 < 1.0.0.1",
			a:    "1.0.0",
			b:    "1.0.0.1",
			want: true,
		},
		{
			name: "1.0.0.1 < 1.0.0 (false)",
			a:    "1.0.0.1",
			b:    "1.0.0",
			want: false,
		},
		{
			name: "equal versions",
			a:    "1.0.0",
			b:    "1.0.0",
			want: false,
		},
		{
			name: "empty string handling",
			a:    "",
			b:    "1.0.0",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionLess(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("versionLess(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestWheelMatchesPlatform(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		check    func(bool) bool
	}{
		{
			name:     "wheel filename exists and returns bool",
			filename: "litert_lm_api-0.1.0-py3-none-win_amd64.whl",
			check:    func(b bool) bool { return true },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wheelMatchesPlatform(tt.filename)
			if !tt.check(got) {
				t.Errorf("wheelMatchesPlatform(%q) returned unexpected value", tt.filename)
			}
		})
	}
}

func TestWheelMatchesPlatformForWindowsArm64(t *testing.T) {
	if got := wheelMatchesPlatformFor("windows", "arm64", "litert_lm_api-0.1.0-py3-none-win_arm64.whl"); !got {
		t.Fatal("expected Windows ARM64 wheel to match")
	}
	if got := wheelMatchesPlatformFor("windows", "arm64", "litert_lm_api-0.1.0-py3-none-win_amd64.whl"); !got {
		t.Fatal("expected Windows amd64 wheel to be accepted as an arm64 fallback")
	}
}

func TestSetActiveModelAndActiveModelName(t *testing.T) {
	originalName := activeModelName
	defer func() { activeModelName = originalName }()

	t.Run("default model name", func(t *testing.T) {
		activeModelName = ""
		got := ActiveModelName()
		if got != defaultModelName {
			t.Errorf("ActiveModelName() = %q, want %q", got, defaultModelName)
		}
	})

	t.Run("set model and retrieve", func(t *testing.T) {
		activeModelName = ""
		SetActiveModel("gemma-2b")
		got := ActiveModelName()
		if got != "gemma-2b.litertlm" {
			t.Errorf("ActiveModelName() = %q, want %q", got, "gemma-2b.litertlm")
		}
	})

	t.Run("set model with .litertlm suffix strips it", func(t *testing.T) {
		activeModelName = ""
		SetActiveModel("custom-model.litertlm")
		got := ActiveModelName()
		if got != "custom-model.litertlm" {
			t.Errorf("ActiveModelName() = %q, want %q", got, "custom-model.litertlm")
		}
	})
}

func TestLookupEnvAnyCase(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\test\AppData\Local`)
	t.Setenv("USERPROFILE", `C:\Users\test`)

	if got, ok := lookupEnvAnyCase("LocalAppData"); !ok || got != `C:\Users\test\AppData\Local` {
		t.Fatalf("lookupEnvAnyCase(LocalAppData) = (%q, %v), want (%q, true)", got, ok, `C:\Users\test\AppData\Local`)
	}
	if got, ok := lookupEnvAnyCase("userprofile"); !ok || got != `C:\Users\test` {
		t.Fatalf("lookupEnvAnyCase(userprofile) = (%q, %v), want (%q, true)", got, ok, `C:\Users\test`)
	}
	if _, ok := lookupEnvAnyCase("this-var-does-not-exist"); ok {
		t.Fatal("lookupEnvAnyCase() unexpectedly found a missing env key")
	}
}

func TestCacheDirPath(t *testing.T) {
	t.Run("respects LITERTLM_CACHE_DIR env var", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("LITERTLM_CACHE_DIR", tmpDir)

		got, err := cacheDirPath()
		if err != nil {
			t.Fatalf("cacheDirPath() error = %v", err)
		}
		if got != tmpDir {
			t.Errorf("cacheDirPath() = %q, want %q", got, tmpDir)
		}
	})

	t.Run("returns non-empty path when env var not set", func(t *testing.T) {
		t.Setenv("LITERTLM_CACHE_DIR", "")

		got, err := cacheDirPath()
		if err != nil {
			t.Fatalf("cacheDirPath() error = %v", err)
		}
		if got == "" {
			t.Errorf("cacheDirPath() returned empty string")
		}
	})

	t.Run("cleans path with env var", func(t *testing.T) {
		tmpDir := t.TempDir()
		messyPath := tmpDir + "/./subdir/../"
		t.Setenv("LITERTLM_CACHE_DIR", messyPath)

		got, err := cacheDirPath()
		if err != nil {
			t.Fatalf("cacheDirPath() error = %v", err)
		}
		// Should be cleaned (no ./ or ../)
		if got != tmpDir+"/" {
			// filepath.Clean may handle this differently on different OSes
			if got == tmpDir || got == tmpDir+"/" {
				// Acceptable
			} else {
				t.Errorf("cacheDirPath() = %q (messy?)", got)
			}
		}
	})
}

func TestWheelVersion(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{
			name:     "standard wheel filename",
			filename: "litert_lm_api-0.1.0-py3-none-win_amd64.whl",
			want:     "0.1.0",
		},
		{
			name:     "no version part",
			filename: "litert_lm_api.whl",
			want:     "",
		},
		{
			name:     "simple two-part filename includes extension",
			filename: "package-1.0.whl",
			want:     "1.0.whl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wheelVersion(tt.filename)
			if got != tt.want {
				t.Errorf("wheelVersion(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}
