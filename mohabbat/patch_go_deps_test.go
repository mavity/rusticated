package mohabbat

import (
	"sort"
	"testing"
)

func TestParseJitTargets(t *testing.T) {
	gomod := `module foo

go 1.20

require (
	github.com/u-root/u-root v0.12.0
	golang.org/x/sys v0.26.0
	other.com/foo v1.0.0
)

replace github.com/u-root/u-root => ./mohabbat/rusticated-jit/github.com/u-root/u-root
replace golang.org/x/sys => ../../mohabbat/rusticated-jit/golang.org/x/sys
replace other.com/foo => ../local/other
`
	jitBase := "/abs/workspace/mohabbat/rusticated-jit"

	targets := parseJitTargets(gomod, jitBase)

	// Sort for deterministic checks
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].module < targets[j].module
	})

	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d: %v", len(targets), targets)
	}

	if targets[0].module != "github.com/u-root/u-root" {
		t.Errorf("expected module github.com/u-root/u-root, got %s", targets[0].module)
	}

	if targets[1].module != "golang.org/x/sys" {
		t.Errorf("expected module golang.org/x/sys, got %s", targets[1].module)
	}
}

func TestModVersionFromGomod(t *testing.T) {
	gomod := `module foo // hello

go 1.20

require (
	github.com/u-root/u-root v0.12.0 // indirect
	golang.org/x/sys v0.26.0-dev.1
)`

	v1, ok1 := modVersionFromGomod(gomod, "github.com/u-root/u-root")
	if !ok1 || v1 != "v0.12.0" {
		t.Errorf("expected v0.12.0, got %s (ok=%v)", v1, ok1)
	}

	v2, ok2 := modVersionFromGomod(gomod, "golang.org/x/sys")
	if !ok2 || v2 != "v0.26.0-dev.1" {
		t.Errorf("expected v0.26.0-dev.1, got %s (ok=%v)", v2, ok2)
	}

	_, ok3 := modVersionFromGomod(gomod, "not/found")
	if ok3 {
		t.Errorf("expected not found to be false")
	}
}

func TestEncodeModPath(t *testing.T) {
	cases := []struct {
		in  string
		out string
	}{
		{"github.com/u-root/u-root", "github.com/u-root/u-root"},
		{"golang.org/x/sys", "golang.org/x/sys"},
		{"github.com/SomeOne/Repo", "github.com/!some!one/!repo"},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := encodeModPath(c.in)
			if got != c.out {
				t.Errorf("expected %s, got %s", c.out, got)
			}
		})
	}
}
