package hashline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeLineHash_WhitespaceTolerantAndBlankEF(t *testing.T) {
	// Blank or whitespace-only lines must hash to "ef" (xxHash64("") & 0xFF == 0xef)
	if got := ComputeLineHash(""); got != "ef" {
		t.Fatalf("ComputeLineHash(\"\") = %q, want \"ef\"", got)
	}
	if got := ComputeLineHash("   \t  \r"); got != "ef" {
		t.Fatalf("ComputeLineHash(spaces) = %q, want \"ef\"", got)
	}

	// Indentation differences must produce the exact same line hash
	h1 := ComputeLineHash("func foo() {")
	h2 := ComputeLineHash("    func foo() {  \r")
	h3 := ComputeLineHash("\t\tfunc   foo()   {")
	if h1 != h2 || h1 != h3 {
		t.Fatalf("expected identical hashes across whitespace variations: %q vs %q vs %q", h1, h2, h3)
	}
}

func TestApplyHashlineEdits_BottomUpAndRelocation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.go")
	initial := strings.Join([]string{
		"package main",
		"",
		"func alpha() int {",
		"\treturn 1",
		"}",
		"",
		"func beta() int {",
		"\treturn 2",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	hAlphaRet := ComputeLineHash("\treturn 1")
	hBetaRet := ComputeLineHash("\treturn 2")

	// Suppose 3 lines were inserted at the top of the file since the LLM read it,
	// shifting alpha's return from line 4 to line 7 and beta's return from line 8 to line 11.
	shifted := "// header 1\n// header 2\n// header 3\n" + initial
	if err := os.WriteFile(p, []byte(shifted), 0o644); err != nil {
		t.Fatal(err)
	}

	// Apply two edits using the OLD line numbers (4 and 8), plus an accidental "4:xx|" prefix in content.
	res, err := ApplyHashlineEdits(p, "sample.go", []EditOp{
		{
			Op:          "replace",
			StartAnchor: "4:" + hAlphaRet,
			Content:     "4:" + hAlphaRet + "|\treturn 10",
		},
		{
			Op:          "replace",
			StartAnchor: "8:" + hBetaRet,
			Content:     "\treturn 20",
		},
	})
	if err != nil {
		t.Fatalf("ApplyHashlineEdits failed: %v", err)
	}
	if res.AppliedHunk != 2 || res.Relocated != 2 {
		t.Fatalf("expected 2 applied and 2 relocated hunks, got %+v", res)
	}

	updatedBytes, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(updatedBytes)
	if !strings.Contains(updated, "\treturn 10\n") || !strings.Contains(updated, "\treturn 20\n") {
		t.Fatalf("unexpected file content after edit:\n%s", updated)
	}
	if strings.Contains(updated, hAlphaRet+"|") {
		t.Fatalf("expected accidental LINE:HASH| prefix to be stripped, got:\n%s", updated)
	}
}

func TestApplyHashlineEdits_MismatchReturnsRecoverySnippet(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mismatch.txt")
	if err := os.WriteFile(p, []byte("line one\nline two\nline three\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyHashlineEdits(p, "mismatch.txt", []EditOp{
		{
			Op:          "replace",
			StartAnchor: "2:00",
			Content:     "line 2 replaced",
		},
	})
	if err == nil {
		t.Fatal("expected error for mismatched hash")
	}
	if !strings.Contains(err.Error(), "Current lines around line 2:") || !strings.Contains(err.Error(), "2:"+ComputeLineHash("line two")+"|line two") {
		t.Fatalf("expected recovery snippet with current hashline in error, got:\n%v", err)
	}
}
