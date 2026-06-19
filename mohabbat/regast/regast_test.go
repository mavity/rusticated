package regast

import (
	"regexp"
	"strings"
	"testing"
)

func mustPreprocess(t *testing.T, src string) *Source {
	t.Helper()
	s, err := Preprocess("test.go", []byte(src))
	if err != nil {
		t.Fatalf("preprocess: %v", err)
	}
	return s
}

func mustCompile(t *testing.T, pat string) *Pattern {
	t.Helper()
	p, err := Compile(pat)
	if err != nil {
		t.Fatalf("compile %q: %v", pat, err)
	}
	return p
}

func matchStr(s *Source, cap []int) string {
	return string(s.Src[cap[0]:cap[1]])
}

// varExpr wraps an expression in a minimal compilable file.
func varExpr(expr string) string {
	return "package p\n\nvar _ = " + expr + "\n"
}

// TestPlainParity checks that patterns without node-groups behave like the
// standard library regexp over the same bytes.
func TestPlainParity(t *testing.T) {
	src := "package p\n\nvar x = foo(a, bc, def)\n"
	s := mustPreprocess(t, src)
	pats := []string{`foo`, `[a-z]+`, `\w+\(`, `a, \w+`, `def\)`, `package\s+\w+`, `z+`}
	for _, pat := range pats {
		want := regexp.MustCompile(pat).FindIndex([]byte(src))
		p := mustCompile(t, pat)
		cap, ok := p.Find(s)
		if want == nil {
			if ok {
				t.Errorf("%q: got match %v, want none", pat, cap[:2])
			}
			continue
		}
		if !ok {
			t.Errorf("%q: no match, want %v", pat, want)
			continue
		}
		if cap[0] != want[0] || cap[1] != want[1] {
			t.Errorf("%q: got [%d,%d], want %v", pat, cap[0], cap[1], want)
		}
	}
}

func TestPlainParityFindAll(t *testing.T) {
	src := "package p\n\nvar x = foo(a, bc, def)\n"
	s := mustPreprocess(t, src)
	pat := `[a-z]+`
	want := regexp.MustCompile(pat).FindAllIndex([]byte(src), -1)
	got := mustCompile(t, pat).FindAll(s)
	if len(got) != len(want) {
		t.Fatalf("count: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i][0] != want[i][0] || got[i][1] != want[i][1] {
			t.Errorf("match %d: got [%d,%d] want %v", i, got[i][0], got[i][1], want[i])
		}
	}
}

// TestGapWordRule exercises the whitespace rule, which only constrains
// junctions between two word (identifier) characters. Spacing around operators
// and punctuation is freely ignorable because it does not change the AST.
func TestGapWordRule(t *testing.T) {
	cases := []struct {
		name string
		src  string
		pat  string
		want bool
	}{
		// Around the '.' operator, spacing is irrelevant on both sides.
		{"dot-tight-pat-tight", "package p\n\nvar _ = a.b\n", `⦃a\.b⦄`, true},
		{"dot-tight-pat-spaced", "package p\n\nvar _ = a.b\n", `⦃a \. b⦄`, true},
		{"dot-spaced-pat-tight", "package p\n\nvar _ = a . b\n", `⦃a\.b⦄`, true},
		{"dot-spaced-pat-spaced", "package p\n\nvar _ = a . b\n", `⦃a \. b⦄`, true},
		{"dot-comment", "package p\n\nvar _ = a /*x*/ . b\n", `⦃a\.b⦄`, true},
		// Around the '+' operator, likewise.
		{"plus-tight-pat-spaced", "package p\n\nvar _ = a+b\n", `⦃a \+ b⦄`, true},
		{"plus-spaced-pat-tight", "package p\n\nvar _ = a + b\n", `⦃a\+b⦄`, true},
		// Between two word characters, the pattern's spacing must agree with
		// the source. 'x int' (two tokens) needs a space; 'xy' (one token)
		// must not be split.
		{"word-space-needs-space", "package p\n\nfunc f(x int) {}\n", `⦃x int⦄`, true},
		{"word-extra-space-ok", "package p\n\nfunc f(x  int) {}\n", `⦃x int⦄`, true},
		{"word-no-merge", "package p\n\nvar xy = 0\n", `⦃x y⦄`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := mustPreprocess(t, c.src)
			p := mustCompile(t, c.pat)
			_, ok := p.Find(s)
			if ok != c.want {
				t.Errorf("src=%q pat=%q: got %v want %v", c.src, c.pat, ok, c.want)
			}
		})
	}
}

// TestGolden is the primary source of evidence: each case is an input string, a
// regast pattern, a replacement template, and the exact expected output. The
// cases double as usage documentation.
func TestGolden(t *testing.T) {
	cases := []struct {
		name string
		in   string
		pat  string
		repl string
		want string
	}{
		// --- Plain regex (no node-groups): behaves like ordinary regexp. ---
		{
			name: "plain literal replace",
			in:   "package p\n\nvar x = 1\n",
			pat:  `x = 1`,
			repl: `y = 2`,
			want: "package p\n\nvar y = 2\n",
		},
		{
			name: "plain regex replaces every match",
			in:   "package p\n\nvar a = 12\nvar b = 345\n",
			pat:  `\d+`,
			repl: `N`,
			want: "package p\n\nvar a = N\nvar b = N\n",
		},
		{
			name: "plain regex group swap",
			in:   "package p\n\nvar _ = \"a-b\"\n",
			pat:  `(\w)-(\w)`,
			repl: `$2-$1`,
			want: "package p\n\nvar _ = \"b-a\"\n",
		},

		// --- Node-groups: match a whole AST node, spacing-insensitive. ---
		{
			name: "rename qualified identifier",
			in:   "package p\n\nvar _ = unix.Major\n",
			pat:  `⦃unix\.Major⦄`,
			repl: `syscall.Major`,
			want: "package p\n\nvar _ = syscall.Major\n",
		},
		{
			name: "rename ignores spacing around dot",
			in:   "package p\n\nvar _ = unix . Major\n",
			pat:  `⦃unix\.Major⦄`,
			repl: `syscall.Major`,
			want: "package p\n\nvar _ = syscall.Major\n",
		},
		{
			name: "rename ignores a comment in the selector",
			in:   "package p\n\nvar _ = unix /*c*/ . Major\n",
			pat:  `⦃unix\.Major⦄`,
			repl: `syscall.Major`,
			want: "package p\n\nvar _ = syscall.Major\n",
		},
		{
			name: "wrap a node, $1 is its raw text",
			in:   "package p\n\nvar _ = foo.bar\n",
			pat:  `⦃\w+\.\w+⦄`,
			repl: `wrap($1)`,
			want: "package p\n\nvar _ = wrap(foo.bar)\n",
		},
		{
			name: "node capture preserves original spacing",
			in:   "package p\n\nvar _ = foo . bar\n",
			pat:  `⦃\w+\.\w+⦄`,
			repl: `wrap($1)`,
			want: "package p\n\nvar _ = wrap(foo . bar)\n",
		},
		{
			name: "swap binary operands",
			in:   "package p\n\nvar _ = a + b\n",
			pat:  `⦃(\w+) \+ (\w+)⦄`,
			repl: `$3 + $2`,
			want: "package p\n\nvar _ = b + a\n",
		},
		{
			name: "swap operands matches tight source too",
			in:   "package p\n\nvar _ = a+b\n",
			pat:  `⦃(\w+) \+ (\w+)⦄`,
			repl: `$3 + $2`,
			want: "package p\n\nvar _ = b + a\n",
		},
		{
			name: "nested node-group capture",
			in:   "package p\n\nvar _ = foo.bar\n",
			pat:  `⦃⦃\w+⦄\.\w+⦄`,
			repl: `$2`,
			want: "package p\n\nvar _ = foo\n",
		},
		{
			name: "same-start picks the identifier, not the selector",
			in:   "package p\n\nvar _ = foo.bar\n",
			pat:  `⦃foo⦄`,
			repl: `X`,
			want: "package p\n\nvar _ = X.bar\n",
		},
		{
			name: "replace all selectors",
			in:   "package p\n\nvar _ = a.b\nvar _ = c.d\n",
			pat:  `⦃\w+\.\w+⦄`,
			repl: `X`,
			want: "package p\n\nvar _ = X\nvar _ = X\n",
		},
		{
			name: "rename a typed field, word spacing required",
			in:   "package p\n\nfunc f(x int) {}\n",
			pat:  `⦃x int⦄`,
			repl: `y int64`,
			want: "package p\n\nfunc f(y int64) {}\n",
		},

		// --- No match leaves the input untouched. ---
		{
			name: "no match is a no-op",
			in:   "package p\n\nvar _ = a.b\n",
			pat:  `⦃x\.y⦄`,
			repl: `Z`,
			want: "package p\n\nvar _ = a.b\n",
		},
		{
			name: "word pattern does not merge two identifiers",
			in:   "package p\n\nvar xy = 0\n",
			pat:  `⦃x y⦄`,
			repl: `Z`,
			want: "package p\n\nvar xy = 0\n",
		},
		{
			name: "word pattern does not split a single identifier",
			in:   "package p\n\nfunc f(a b) {}\n",
			pat:  `⦃ab⦄`,
			repl: `Z`,
			want: "package p\n\nfunc f(a b) {}\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := mustPreprocess(t, c.in)
			p := mustCompile(t, c.pat)
			out, err := p.Replace(s, c.repl)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != c.want {
				t.Errorf("in=%q pat=%q repl=%q\n got=%q\nwant=%q", c.in, c.pat, c.repl, string(out), c.want)
			}
		})
	}
}
func TestWholeNodeMatch(t *testing.T) {
	src := varExpr("foo.bar")
	s := mustPreprocess(t, src)
	p := mustCompile(t, `⦃\w+\.\w+⦄`)
	cap, ok := p.Find(s)
	if !ok {
		t.Fatal("no match")
	}
	if got := matchStr(s, cap); got != "foo.bar" {
		t.Errorf("match = %q, want foo.bar", got)
	}
}

// TestNoSpill verifies a greedy inner pattern cannot match across a larger node
// boundary: an identifier-only pattern matches the identifier, not the selector.
func TestNoSpill(t *testing.T) {
	src := varExpr("foo.bar")
	s := mustPreprocess(t, src)
	p := mustCompile(t, `⦃[a-z]+⦄`)
	var subs []string
	for _, cap := range p.FindAll(s) {
		subs = append(subs, matchStr(s, cap))
	}
	joined := strings.Join(subs, ",")
	if !strings.Contains(joined, "foo") || !strings.Contains(joined, "bar") {
		t.Errorf("expected foo and bar among matches, got %q", joined)
	}
	for _, sub := range subs {
		if strings.Contains(sub, ".") {
			t.Errorf("match %q spilled across a node boundary", sub)
		}
	}
}

// TestNestedNodeGroups checks a node-group nested inside another.
func TestNestedNodeGroups(t *testing.T) {
	src := varExpr("foo.bar")
	s := mustPreprocess(t, src)
	p := mustCompile(t, `⦃⦃\w+⦄\.\w+⦄`)
	cap, ok := p.Find(s)
	if !ok {
		t.Fatal("no match")
	}
	if got := matchStr(s, cap); got != "foo.bar" {
		t.Errorf("outer match = %q, want foo.bar", got)
	}
	// Group 1 is the outer node-group, group 2 the inner one (foo).
	if len(cap) < 6 || cap[4] < 0 {
		t.Fatalf("expected inner capture, cap=%v", cap)
	}
	if got := string(s.Src[cap[4]:cap[5]]); got != "foo" {
		t.Errorf("inner node = %q, want foo", got)
	}
}

// TestSameStartAmbiguity checks the smallest fitting node is chosen when the
// pattern only fits the identifier, even though a larger node shares the start.
func TestSameStartAmbiguity(t *testing.T) {
	src := varExpr("foo.bar")
	s := mustPreprocess(t, src)
	// Whole-node identifier match: must land on the Ident node, not the Selector.
	p := mustCompile(t, `⦃foo⦄`)
	cap, ok := p.Find(s)
	if !ok {
		t.Fatal("no match")
	}
	if got := matchStr(s, cap); got != "foo" {
		t.Errorf("match = %q, want foo", got)
	}
}

// TestReplaceNodeText checks that a node-group reference expands to raw text.
func TestReplaceNodeText(t *testing.T) {
	src := varExpr("foo.bar")
	s := mustPreprocess(t, src)
	p := mustCompile(t, `⦃\w+\.\w+⦄`)
	out, err := p.Replace(s, "X$1Y")
	if err != nil {
		t.Fatal(err)
	}
	want := varExpr("Xfoo.barY")
	if string(out) != want {
		t.Errorf("replace =\n%q\nwant\n%q", out, want)
	}
}

// TestReplacePreservesComments checks replacement output uses raw source text,
// including comments inside a matched node.
func TestReplacePreservesComments(t *testing.T) {
	src := varExpr("a /*keep*/ + b")
	s := mustPreprocess(t, src)
	p := mustCompile(t, `⦃a \+ b⦄`)
	out, err := p.Replace(s, "[$1]")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "[a /*keep*/ + b]") {
		t.Errorf("replace did not preserve raw node text: %q", out)
	}
}

// TestCRLF checks byte offsets are correct with Windows line endings.
func TestCRLF(t *testing.T) {
	src := "package p\r\n\r\nvar _ = a.b\r\n"
	s := mustPreprocess(t, src)
	p := mustCompile(t, `⦃a\.b⦄`)
	cap, ok := p.Find(s)
	if !ok {
		t.Fatal("no match")
	}
	if got := matchStr(s, cap); got != "a.b" {
		t.Errorf("match = %q, want a.b", got)
	}
}

// TestPartiallyParseable checks node-groups still work when the file has errors
// elsewhere but the relevant part parses.
func TestPartiallyParseable(t *testing.T) {
	src := "package p\n\nvar _ = a.b\n\nfunc broken( {\n"
	s, err := Preprocess("test.go", []byte(src))
	if err != nil {
		t.Skipf("parser produced no AST: %v", err)
	}
	p := mustCompile(t, `⦃a\.b⦄`)
	cap, ok := p.Find(s)
	if !ok {
		t.Fatal("no match in partially-parseable file")
	}
	if got := matchStr(s, cap); got != "a.b" {
		t.Errorf("match = %q, want a.b", got)
	}
}

// TestCompileErrors checks malformed node-group brackets are rejected.
func TestCompileErrors(t *testing.T) {
	bad := []string{`⦃ a`, `a ⦄`, `⦃ ⦃ x ⦄`}
	for _, pat := range bad {
		if _, err := Compile(pat); err == nil {
			t.Errorf("Compile(%q): expected error, got nil", pat)
		}
	}
}

// TestSignificant unit-tests the significance predicate and node lookup.
func TestSignificant(t *testing.T) {
	// Offsets: a=0 space=1 /*c*/=2..7 space=7 b=8 nl=9
	src := "a /*c*/ b\n"
	s := mustPreprocess(t, src)
	checks := []struct {
		i    int
		want bool
	}{
		{0, true},    // a
		{1, false},   // space
		{2, false},   // start of comment
		{6, false},   // inside comment
		{7, false},   // space
		{8, true},    // b
		{9, false},   // newline
		{100, false}, // out of range
	}
	for _, c := range checks {
		if got := s.significant(c.i); got != c.want {
			t.Errorf("significant(%d) = %v, want %v", c.i, got, c.want)
		}
	}
}

// TestEndsForStart checks node-start lookup returns sorted unique ends.
func TestEndsForStart(t *testing.T) {
	src := varExpr("foo.bar")
	s := mustPreprocess(t, src)
	start := strings.Index(src, "foo")
	ends := s.endsForStart(start)
	if len(ends) < 2 {
		t.Fatalf("expected at least Ident and Selector ends, got %v", ends)
	}
	for i := 1; i < len(ends); i++ {
		if ends[i] <= ends[i-1] {
			t.Errorf("ends not strictly ascending/unique: %v", ends)
		}
	}
	// The largest end should reach the end of "foo.bar".
	wantMax := start + len("foo.bar")
	if ends[len(ends)-1] != wantMax {
		t.Errorf("max end = %d, want %d", ends[len(ends)-1], wantMax)
	}
}
