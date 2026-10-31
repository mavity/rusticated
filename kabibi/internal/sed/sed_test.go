package sed

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/u-root/u-root/pkg/core"
)

func runSedTest(t *testing.T, input string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), core.IO{
		Stdin:  bytes.NewBufferString(input),
		Stdout: &stdout,
		Stderr: &stderr,
		Dir:    t.TempDir(),
	}, args...)
	return stdout.String(), stderr.String(), code
}

func TestSubstituteBREAndExtended(t *testing.T) {
	got, stderr, code := runSedTest(t, "foo foo\n", `s/\(foo\)/[\1]/g`)
	if code != 0 || stderr != "" || got != "[foo] [foo]\n" {
		t.Fatalf("BRE substitution = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "a\nb\n", "-E", `s/(a|b)/<&>/`)
	if code != 0 || stderr != "" || got != "<a>\n<b>\n" {
		t.Fatalf("ERE substitution = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "aaaa\n", `s/a/X/2g`)
	if code != 0 || stderr != "" || got != "aXXX\n" {
		t.Fatalf("global substitution from occurrence = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "aaaa\n", `s/a/X/3`)
	if code != 0 || stderr != "" || got != "aaXa\n" {
		t.Fatalf("numbered substitution = (%q, %q, %d)", got, stderr, code)
	}
}

func TestAddressesHoldSpaceAndBranches(t *testing.T) {
	got, stderr, code := runSedTest(t, "a\nb\nc\nd\n", "-n", `2,3p`)
	if code != 0 || stderr != "" || got != "b\nc\n" {
		t.Fatalf("range print = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "a\nb\nc\n", `1h;2g`)
	if code != 0 || stderr != "" || got != "a\na\nc\n" {
		t.Fatalf("hold-space script = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "a\n", "-n", `s/a/A/;t changed; :changed;p`)
	if code != 0 || stderr != "" || got != "A\n" {
		t.Fatalf("branch script = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "a\n", `b; s/a/A/`)
	if code != 0 || stderr != "" || got != "a\n" {
		t.Fatalf("unlabeled branch = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "a\n", `s/a/A/;t; s/A/X/`)
	if code != 0 || stderr != "" || got != "A\n" {
		t.Fatalf("unlabeled test branch = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "a\nb\n", `s/a/A/;N;t matched;s/A/X/;:matched`)
	if code != 0 || stderr != "" || got != "X\nb\n" {
		t.Fatalf("N resets substitution test = (%q, %q, %d)", got, stderr, code)
	}
}

func TestGNUAddressFormsAndExitStatus(t *testing.T) {
	got, stderr, code := runSedTest(t, "1\n2\n3\n4\n5\n6\n", "-n", `2,+2p`)
	if code != 0 || stderr != "" || got != "2\n3\n4\n" {
		t.Fatalf("relative range = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "1\n2\n3\n4\n5\n6\n", "-n", `2,~3p`)
	if code != 0 || stderr != "" || got != "2\n3\n" {
		t.Fatalf("tilde range = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "match\nlater\n", "-n", `0,/match/p`)
	if code != 0 || stderr != "" || got != "match\n" {
		t.Fatalf("zero-address range = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "first\nsecond\n", `q 7`)
	if code != 7 || stderr != "" || got != "first\n" {
		t.Fatalf("q exit status = (%q, %q, %d)", got, stderr, code)
	}
}

func TestGNUOptionsAndAppendQueueBeforeNext(t *testing.T) {
	got, stderr, code := runSedTest(t, "abcd\n", "-nE", `s/(a)(b)/\1-\2/;p`)
	if code != 0 || stderr != "" || got != "a-bcd\n" {
		t.Fatalf("clustered options = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "one\ntwo\n", "a\\\nqueued\nn")
	if code != 0 || stderr != "" || got != "one\nqueued\ntwo\n" {
		t.Fatalf("append queue before n = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "one\ntwo\n", "a\\\nqueued\nq")
	if code != 0 || stderr != "" || got != "one\nqueued\n" {
		t.Fatalf("append queue before q = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "abcd\n", "-n", "-l", "3", "l")
	if code != 0 || stderr != "" || got != "abc\\\nd$\n" {
		t.Fatalf("line wrapping = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "one\ntwo\n", "#n\np")
	if code != 0 || stderr != "" || got != "one\ntwo\n" {
		t.Fatalf("#n script directive = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "aa\n", `s/a/A/;s//B/`)
	if code != 0 || stderr != "" || got != "AB\n" {
		t.Fatalf("empty regex reuse = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "a\n", "--posix", `s/a/A/I`)
	if code == 0 || got != "" || stderr == "" {
		t.Fatalf("GNU substitution flag under --posix = (%q, %q, %d)", got, stderr, code)
	}
}

func TestMultilineAndByteModes(t *testing.T) {
	got, stderr, code := runSedTest(t, "a\nb\nc\nd\n", "-n", `N;P;D`)
	if code != 0 || stderr != "" || got != "a\nb\nc\n" {
		t.Fatalf("N/P/D script = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "abc\n", `y/abc/ABC/`)
	if code != 0 || stderr != "" || got != "ABC\n" {
		t.Fatalf("transliteration = (%q, %q, %d)", got, stderr, code)
	}
	got, stderr, code = runSedTest(t, "a\n", `y/\x61/Z/`)
	if code != 0 || stderr != "" || got != "Z\n" {
		t.Fatalf("escaped transliteration = (%q, %q, %d)", got, stderr, code)
	}
	var stdout, stderrBuf bytes.Buffer
	code = Main(context.Background(), core.IO{
		Stdin:  bytes.NewReader([]byte{'a', 0, 'b', 0}),
		Stdout: &stdout,
		Stderr: &stderrBuf,
		Dir:    t.TempDir(),
	}, "-z", `s/b/B/`)
	if code != 0 || stderrBuf.Len() != 0 || !bytes.Equal(stdout.Bytes(), []byte{'a', 0, 'B', 0}) {
		t.Fatalf("NUL-data substitution = (%v, %q, %d)", stdout.Bytes(), stderrBuf.String(), code)
	}
}

func TestChangeCommandWithNegatedAddress(t *testing.T) {
	got, stderr, code := runSedTest(t, "a\nb\nc\n", `2!c\X`)
	if code != 0 || stderr != "" || got != "X\nb\nX\n" {
		t.Fatalf("negated change command = (%q, %q, %d)", got, stderr, code)
	}
}

func TestInPlaceBackupAndScriptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "script.sed")
	if err := os.WriteFile(script, []byte(`s/o/O/g`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), core.IO{Stdout: &stdout, Stderr: &stderr, Dir: dir}, "-i.bak", "-f", "script.sed", "input")
	if code != 0 || stderr.Len() != 0 || stdout.Len() != 0 {
		t.Fatalf("in-place invocation = (%q, %q, %d)", stdout.String(), stderr.String(), code)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "One\ntwO\n" {
		t.Fatalf("edited file = %q", got)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "one\ntwo\n" {
		t.Fatalf("backup file = %q", backup)
	}
}

func TestInPlaceQuitStopsRemainingFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), core.IO{Stdout: &stdout, Stderr: &stderr, Dir: dir}, "-i", `s/a/A/;q`, "first", "second")
	if code != 0 || stderr.Len() != 0 || stdout.Len() != 0 {
		t.Fatalf("in-place quit = (%q, %q, %d)", stdout.String(), stderr.String(), code)
	}
	first, err := os.ReadFile(filepath.Join(dir, "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "second"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "A\n" || string(second) != "a\n" {
		t.Fatalf("in-place files after q = (%q, %q)", first, second)
	}
}

func TestInputFilesUseInvocationDirectoryAndPreserveNames(t *testing.T) {
	dir := t.TempDir()
	for name, contents := range map[string]string{"input": "line\n", "first": "hold\n", "second": "b\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), core.IO{Stdout: &stdout, Stderr: &stderr, Dir: dir}, "-n", "F", "input")
	if code != 0 || stderr.Len() != 0 || stdout.String() != "input\n" {
		t.Fatalf("file command = (%q, %q, %d)", stdout.String(), stderr.String(), code)
	}
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), core.IO{Stdout: &stdout, Stderr: &stderr, Dir: dir}, "-s", "/hold/h; /b/g", "first", "second")
	if code != 0 || stderr.Len() != 0 || stdout.String() != "hold\nhold\n" {
		t.Fatalf("separate-file hold space = (%q, %q, %d)", stdout.String(), stderr.String(), code)
	}
}

func TestReadFileCommandsAndPatternExecution(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "extra"), []byte("x\ny\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), core.IO{
		Stdin:  bytes.NewBufferString("a\nb\nc\n"),
		Stdout: &stdout,
		Stderr: &stderr,
		Dir:    dir,
	}, `R extra`)
	if code != 0 || stderr.Len() != 0 || stdout.String() != "a\nx\nb\ny\nc\n" {
		t.Fatalf("R command = (%q, %q, %d)", stdout.String(), stderr.String(), code)
	}
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), core.IO{
		Stdin:  bytes.NewBufferString("exit 0\n"),
		Stdout: &stdout,
		Stderr: &stderr,
		Dir:    dir,
	}, "e")
	if code != 0 || stderr.Len() != 0 || stdout.String() != "\n" {
		t.Fatalf("e command = (%q, %q, %d)", stdout.String(), stderr.String(), code)
	}
}

func TestWriteFilesAreTruncatedBeforeReadingInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	if err := os.WriteFile(path, []byte("old contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), core.IO{
		Stdin: bytes.NewReader(nil), Stdout: &stdout, Stderr: &stderr, Dir: dir,
	}, "-n", `w output`)
	if code != 0 || stderr.Len() != 0 || stdout.Len() != 0 {
		t.Fatalf("w with empty input = (%q, %q, %d)", stdout.String(), stderr.String(), code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("w output was not truncated: %q", data)
	}
}

func TestInvalidScriptReturnsErrorStatus(t *testing.T) {
	got, stderr, code := runSedTest(t, "a\n", `s/[//`)
	if code == 0 || got != "" || stderr == "" {
		t.Fatalf("invalid script result = (%q, %q, %d)", got, stderr, code)
	}
}
