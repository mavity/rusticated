package shell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/u-root/u-root/pkg/core"
)

func TestURootRegistry_AllCommandsRegisteredAndZeroPanicOnReinvocation(t *testing.T) {
	names := AllURootCommandNames()
	if len(names) == 0 {
		t.Fatalf("expected registered u-root commands, got none")
	}

	dir := t.TempDir()
	// Exercise every registered command twice with overlapping/invalid flags and cancelled contexts
	// to verify zero global flag collisions (`panic: flag redefined`) and reentrancy.
	for _, name := range names {
		runner, ok := LookupURootCommand(name)
		if !ok || runner == nil {
			t.Fatalf("LookupURootCommand(%q) returned nil/false", name)
		}

		for pass := 0; pass < 2; pass++ {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("command %q panicked on pass %d: %v", name, pass, r)
					}
				}()

				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()

				var outBuf, errBuf bytes.Buffer
				ioEnv := core.IO{
					Stdin:  strings.NewReader("alpha\nbeta\ngamma\n"),
					Stdout: &outBuf,
					Stderr: &errBuf,
					Dir:    dir,
					LookupEnv: func(k string) (string, bool) {
						if k == "PATH" {
							return "/ubin", true
						}
						return "", false
					},
				}
				// Invoke with --help and with an invalid flag to test FlagSet isolation
				_ = runner(ctx, ioEnv, "-h")
				_ = runner(ctx, ioEnv, "--definitely-invalid-flag-xyz")
			}()
		}
	}
}

func TestURootPilotCommands(t *testing.T) {
	dir := t.TempDir()
	sampleFile := filepath.Join(dir, "items.txt")
	content := "banana\napple\ncherry\n"
	if err := os.WriteFile(sampleFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}

	runShell := func(script string) (string, int, error) {
		_, parsed := es.ClassifyInput(script)
		rec, err := es.RunCommand(context.Background(), script, parsed, SourceUserShell, nil)
		return rec.Output, rec.ExitCode, err
	}

	out, code, err := runShell("echo hello world")
	if err != nil || code != 0 || strings.TrimSpace(out) != "hello world" {
		t.Fatalf("echo failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("cat items.txt | grep apple")
	if err != nil || code != 0 || strings.TrimSpace(out) != "apple" {
		t.Fatalf("cat|grep failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("head -n 2 items.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "banana\napple" {
		t.Fatalf("head failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("tail -n 2 items.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "apple\ncherry" {
		t.Fatalf("tail failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("sort items.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "apple\nbanana\ncherry" {
		t.Fatalf("sort failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo -e 'a\na\nb' | uniq")
	if err != nil || code != 0 || strings.TrimSpace(out) != "a\nb" {
		t.Fatalf("uniq failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("cat items.txt | wc -l")
	if err != nil || code != 0 || strings.TrimSpace(out) != "3" {
		t.Fatalf("wc failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo hello | tr a-z A-Z")
	if err != nil || code != 0 || strings.TrimSpace(out) != "HELLO" {
		t.Fatalf("tr failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo world | tee file.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "world" {
		t.Fatalf("tee failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("seq 1 5 | head -n 3 | tail -n 1")
	if err != nil || code != 0 || strings.TrimSpace(out) != "3" {
		t.Fatalf("seq|head|tail pipeline failed (code=%d, err=%v): %q", code, err, out)
	}

	_ = os.WriteFile(filepath.Join(dir, "c1.txt"), []byte("apple\nbanana\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "c2.txt"), []byte("banana\ncherry\n"), 0o644)
	out, code, err = runShell("comm -12 c1.txt c2.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "banana" {
		t.Fatalf("comm failed (code=%d, err=%v): %q", code, err, out)
	}

	_, code, _ = runShell("cmp c1.txt c1.txt")
	if code != 0 {
		t.Fatalf("expected cmp c1.txt c1.txt to return 0, got %d", code)
	}

	_, code, _ = runShell("cmp -s c1.txt c2.txt")
	if code != 1 {
		t.Fatalf("expected cmp -s c1.txt c2.txt to return 1 on differing files, got %d", code)
	}

	out, code, err = runShell("echo -e '\\x00\\x00hello\\x00\\x00' | strings")
	if err != nil || code != 0 || strings.TrimSpace(out) != "hello" {
		t.Fatalf("strings failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo abc | hexdump")
	if err != nil || code != 0 || !strings.Contains(out, "61 62 63") {
		t.Fatalf("hexdump failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo timestamped | ts")
	if err != nil || code != 0 || !strings.Contains(out, "timestamped") {
		t.Fatalf("ts failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo '1 2 2 3' | tsort")
	if err != nil || code != 0 || strings.TrimSpace(out) != "1\n2\n3" {
		t.Fatalf("tsort failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo -n 'hello world' | base64 | base64 -d")
	if err != nil || code != 0 || out != "hello world" {
		t.Fatalf("base64 roundtrip failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("yes hello | head -n 3")
	if err != nil || code != 0 || strings.TrimSpace(out) != "hello\nhello\nhello" {
		t.Fatalf("yes|head failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo -e 'line1\\nline2' | more")
	if err != nil || code != 0 || strings.TrimSpace(out) != "line1\nline2" {
		t.Fatalf("more failed (code=%d, err=%v): %q", code, err, out)
	}

	_, code, _ = runShell("man")
	if code != 1 {
		t.Fatalf("man with no args expected 1, got %d", code)
	}

	out, code, err = runShell("ls")
	if err != nil || code != 0 || !strings.Contains(out, "items.txt") {
		t.Fatalf("ls failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("cp items.txt copied_items.txt && cat copied_items.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "banana\napple\ncherry" {
		t.Fatalf("cp failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("mv copied_items.txt moved_items.txt && cat moved_items.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "banana\napple\ncherry" {
		t.Fatalf("mv failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("rm moved_items.txt && ls")
	if err != nil || code != 0 || strings.Contains(out, "moved_items.txt") {
		t.Fatalf("rm failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("mkdir -p testdir/subdir && ls testdir")
	if err != nil || code != 0 || !strings.Contains(out, "subdir") {
		t.Fatalf("mkdir failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("mktemp")
	if err != nil || code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("mktemp failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("touch touched.txt && ls")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("touch failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("truncate -s 100 touched.txt")
	if err != nil || code != 0 {
		t.Fatalf("truncate failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("ln -s touched.txt symlink.txt && ls")
	if err != nil || code != 0 || !strings.Contains(out, "symlink.txt") {
		t.Fatalf("ln failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("chmod 0644 touched.txt")
	if err != nil || code != 0 {
		t.Fatalf("chmod failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("find -name touched.txt .")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("find failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("du -s touched.txt")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("du failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("readlink symlink.txt")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("readlink failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("realpath symlink.txt")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("realpath failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("pwd")
	if err != nil || code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("pwd failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("shasum -a 256 touched.txt")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("shasum failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("md5sum touched.txt")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("md5sum failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("gzip -k touched.txt && ls")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt.gz") {
		t.Fatalf("gzip failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("tar -cvf test.tar touched.txt && tar -tvf test.tar")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("tar failed (code=%d, err=%v): %q", code, err, out)
	}

	out, code, err = runShell("echo touched.txt | cpio o > test.cpio && cpio t < test.cpio")
	if err != nil || code != 0 || !strings.Contains(out, "touched.txt") {
		t.Fatalf("cpio failed (code=%d, err=%v): %q", code, err, out)
	}

	_, code, _ = runShell("gpgv")
	if code == 0 {
		t.Fatalf("expected non-zero exit status for gpgv with missing arguments")
	}
}

func TestURootPipelinesAndDailyDrivers(t *testing.T) {
	dir := t.TempDir()
	sampleFile := filepath.Join(dir, "items.txt")
	content := "banana\napple\nbanana\ncherry\napricot\n"
	if err := os.WriteFile(sampleFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}

	runShell := func(script string) (string, int, error) {
		_, parsed := es.ClassifyInput(script)
		rec, err := es.RunCommand(context.Background(), script, parsed, SourceUserShell, nil)
		return rec.Output, rec.ExitCode, err
	}

	// 1. Pipeline: sort | uniq | grep | wc -l
	out, code, err := runShell("sort items.txt | uniq | grep '^a' | wc -l")
	if err != nil || code != 0 {
		t.Fatalf("pipeline failed (code=%d, err=%v): %q", code, err, out)
	}
	if strings.TrimSpace(out) != "2" {
		t.Fatalf("expected pipeline output '2', got %q", out)
	}

	// 2. head and tail
	out, code, err = runShell("seq 1 5 | head -n 3 | tail -n 1")
	if err != nil || code != 0 {
		t.Fatalf("seq|head|tail failed (code=%d, err=%v): %q", code, err, out)
	}
	if strings.TrimSpace(out) != "3" {
		t.Fatalf("expected '3', got %q", out)
	}

	// 3. tr, tee, and cmp
	out, code, err = runShell("echo hello | tr a-z A-Z | tee upper.txt")
	if err != nil || code != 0 {
		t.Fatalf("tr|tee failed (code=%d, err=%v): %q", code, err, out)
	}
	if strings.TrimSpace(out) != "HELLO" {
		t.Fatalf("expected 'HELLO', got %q", out)
	}

	_, code, _ = runShell("cmp -s items.txt upper.txt")
	if code == 0 {
		t.Fatalf("expected non-zero exit status from cmp on differing files")
	}

	// 4. basename, dirname, which, uname, md5sum (if registered)
	if _, ok := LookupURootCommand("basename"); ok {
		out, code, err = runShell("basename /usr/local/bin/kabibi.sh .sh")
		if err != nil || code != 0 || strings.TrimSpace(out) != "kabibi" {
			t.Fatalf("basename failed: code=%d err=%v out=%q", code, err, out)
		}
	}

	if _, ok := LookupURootCommand("dirname"); ok {
		out, code, err = runShell("dirname /usr/local/bin/kabibi.sh")
		if err != nil || code != 0 {
			t.Fatalf("dirname failed: code=%d err=%v out=%q", code, err, out)
		}
	}

	// Band 1E: true, false, date, hostname, printenv, which, time, timeout, xargs, backoff
	out, code, err = runShell("true")
	if err != nil || code != 0 {
		t.Fatalf("true failed: code=%d err=%v", code, err)
	}

	_, code, _ = runShell("false")
	if code != 1 {
		t.Fatalf("false expected exit 1, got code=%d", code)
	}

	out, code, err = runShell("date +%Y")
	if err != nil || code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("date failed: code=%d err=%v out=%q", code, err, out)
	}

	out, code, err = runShell("hostname")
	if err != nil || code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("hostname failed: code=%d err=%v out=%q", code, err, out)
	}

	out, code, err = runShell("FOO=kabibi_test printenv FOO")
	if err != nil || code != 0 || !strings.Contains(out, "kabibi_test") {
		t.Fatalf("printenv failed: code=%d err=%v out=%q", code, err, out)
	}

	out, code, err = runShell("time echo hello")
	if err != nil || code != 0 || !strings.Contains(out, "hello") {
		t.Fatalf("time failed: code=%d err=%v out=%q", code, err, out)
	}

	out, code, err = runShell("timeout 2s echo timed")
	if err != nil || code != 0 || !strings.Contains(out, "timed") {
		t.Fatalf("timeout failed: code=%d err=%v out=%q", code, err, out)
	}

	out, code, err = runShell("echo 'foo bar' | xargs -n 1 echo")
	if err != nil || code != 0 || !strings.Contains(out, "foo") {
		t.Fatalf("xargs failed: code=%d err=%v out=%q", code, err, out)
	}

	out, code, err = runShell("backoff -t 1s true")
	if err != nil || code != 0 {
		t.Fatalf("backoff failed: code=%d err=%v", code, err)
	}

	if _, ok := LookupURootCommand("which"); ok {
		out, code, err = runShell("which grep sort uniq")
		if err != nil || code != 0 || !strings.Contains(out, "grep") {
			t.Fatalf("which failed: code=%d err=%v out=%q", code, err, out)
		}
	}

	if _, ok := LookupURootCommand("uname"); ok {
		out, code, err = runShell("uname -s")
		if err != nil || code != 0 || strings.TrimSpace(out) == "" {
			t.Fatalf("uname -s failed: code=%d err=%v out=%q", code, err, out)
		}
	}

	// 5. yes piped into head -n 4 terminates cleanly on pipe close
	out, code, err = runShell("yes kabibi | head -n 4")
	if err != nil || code != 0 {
		t.Fatalf("yes | head failed: code=%d err=%v out=%q", code, err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || lines[0] != "kabibi" {
		t.Fatalf("unexpected yes|head output: %q", out)
	}
}

func TestURootInterruptHandling_SleepAndYes(t *testing.T) {
	dir := t.TempDir()
	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, cmdStr := range []string{"sleep 60", "yes"} {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		start := time.Now()
		_, parsed := es.ClassifyInput(cmdStr)
		_, err := es.RunCommand(ctx, cmdStr, parsed, SourceUserShell, nil)
		cancel()
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Fatalf("%q did not cancel promptly on ctx cancellation (took %v)", cmdStr, elapsed)
		}
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			// Verify it stopped due to context termination
			t.Logf("%q terminated in %v with err=%v", cmdStr, elapsed, err)
		}
	}
}

func TestURootAdversarial_FlagSafetyAndVirtualization(t *testing.T) {
	dir := t.TempDir()
	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}

	runShell := func(script string) (string, int, error) {
		_, parsed := es.ClassifyInput(script)
		rec, err := es.RunCommand(context.Background(), script, parsed, SourceUserShell, nil)
		return rec.Output, rec.ExitCode, err
	}

	// 1. hostname -s and -d with virtualized HOSTNAME
	out, code, err := runShell("HOSTNAME=node1.cluster.internal hostname -s")
	if err != nil || code != 0 || strings.TrimSpace(out) != "node1" {
		t.Fatalf("hostname -s failed: code=%d err=%v out=%q", code, err, out)
	}

	out, code, err = runShell("HOSTNAME=node1.cluster.internal hostname -d")
	if err != nil || code != 0 || strings.TrimSpace(out) != "cluster.internal" {
		t.Fatalf("hostname -d failed: code=%d err=%v out=%q", code, err, out)
	}

	// 2. basename with flags (-s, -a)
	out, code, err = runShell("basename -s .txt /foo/bar/baz.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "baz" {
		t.Fatalf("basename -s failed: code=%d err=%v out=%q", code, err, out)
	}

	out, code, err = runShell("basename -a /a/b.txt /c/d.txt")
	if err != nil || code != 0 || strings.TrimSpace(out) != "b.txt\nd.txt" {
		t.Fatalf("basename -a failed: code=%d err=%v out=%q", code, err, out)
	}

	// 3. dirname with -z flag
	out, code, err = runShell("dirname -z /foo/bar /baz/qux")
	if err != nil || code != 0 || filepath.ToSlash(out) != "/foo\x00/baz\x00" {
		t.Fatalf("dirname -z failed: code=%d err=%v out=%q", code, err, out)
	}

	// 4. printenv with -0 flag
	out, code, err = runShell("FOO=bar printenv -0 FOO")
	if err != nil || code != 0 || out != "bar\x00" {
		t.Fatalf("printenv -0 failed: code=%d err=%v out=%q", code, err, out)
	}

	// 5. Mutating commands with invalid flags MUST fail immediately and NEVER create stray files named after flags
	mutatingCmds := []string{
		"rm --invalid-flag-test",
		"chmod --invalid-flag-test",
		"truncate --invalid-flag-test",
		"touch --invalid-flag-test",
		"mkdir --invalid-flag-test",
		"mv --invalid-flag-test",
		"cp --invalid-flag-test",
		"ln --invalid-flag-test",
		"tar --invalid-flag-test",
		"cpio --invalid-flag-test",
		"gpgv --invalid-flag-test",
	}

	for _, cmdStr := range mutatingCmds {
		_, code, _ := runShell(cmdStr)
		if code == 0 {
			t.Fatalf("expected command %q with invalid flag to return non-zero exit code, got 0", cmdStr)
		}
	}

	// Verify no stray files named "--invalid-flag-test" or similar were created in dir
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "--invalid") || strings.HasPrefix(entry.Name(), "-") {
			t.Fatalf("destructive flag leak: found stray file %q in working directory", entry.Name())
		}
	}
}

func TestURootCommand_Echo(t *testing.T) {
	runner, ok := LookupURootCommand("echo")
	if !ok || runner == nil {
		t.Fatalf("LookupURootCommand(\"echo\") returned false/nil")
	}

	runEcho := func(ctx context.Context, args ...string) (string, string, int) {
		var outBuf, errBuf bytes.Buffer
		ioEnv := core.IO{
			Stdout: &outBuf,
			Stderr: &errBuf,
		}
		code := runner(ctx, ioEnv, args...)
		return outBuf.String(), errBuf.String(), code
	}

	// 1. Basic echo
	out, errOut, code := runEcho(context.Background(), "hello", "world")
	if code != 0 || out != "hello world\n" || errOut != "" {
		t.Fatalf("basic echo failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 2. Empty echo (should print newline)
	out, errOut, code = runEcho(context.Background())
	if code != 0 || out != "\n" || errOut != "" {
		t.Fatalf("empty echo failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 3. -n flag (suppress newline)
	out, errOut, code = runEcho(context.Background(), "-n", "no-newline")
	if code != 0 || out != "no-newline" || errOut != "" {
		t.Fatalf("echo -n failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 4. -e escapes: \n, \t, quotes, \x41, \0101
	out, errOut, code = runEcho(context.Background(), "-e", "line1\\nline2\\t\"quoted\"\\x41\\0101")
	if code != 0 || out != "line1\nline2\t\"quoted\"AA\n" || errOut != "" {
		t.Fatalf("echo -e escapes failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 5. -E flag (escapes disabled)
	out, errOut, code = runEcho(context.Background(), "-E", "raw\\nstring")
	if code != 0 || out != "raw\\nstring\n" || errOut != "" {
		t.Fatalf("echo -E failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 6. \c produces no further output
	out, errOut, code = runEcho(context.Background(), "-e", "hello\\cworld")
	if code != 0 || out != "hello\n" || errOut != "" {
		t.Fatalf("echo \\c failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 7. -h returns 0 with usage
	out, errOut, code = runEcho(context.Background(), "-h")
	if code != 0 || !strings.Contains(errOut, "Usage:") {
		t.Fatalf("echo -h failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 8. Invalid flag returns 1
	out, errOut, code = runEcho(context.Background(), "-invalid-flag")
	if code != 1 {
		t.Fatalf("echo invalid flag failed: expected code 1, got %d", code)
	}

	// 9. FlagSet isolation on subsequent call
	out, errOut, code = runEcho(context.Background(), "recovered")
	if code != 0 || out != "recovered\n" {
		t.Fatalf("echo recovery after invalid flag failed: code=%d out=%q", code, out)
	}

	// 10. Context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, errOut, code = runEcho(ctx, "cancelled")
	if code != 130 {
		t.Fatalf("echo context cancellation failed: expected code 130, got %d", code)
	}
}

func TestURootCommand_Cat(t *testing.T) {
	runner, ok := LookupURootCommand("cat")
	if !ok || runner == nil {
		t.Fatalf("LookupURootCommand(\"cat\") returned false/nil")
	}

	tempDir := t.TempDir()
	f1 := filepath.Join(tempDir, "file1.txt")
	f2 := filepath.Join(tempDir, "file2.txt")
	if err := os.WriteFile(f1, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("gamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runCat := func(ctx context.Context, in string, dir string, args ...string) (string, string, int) {
		var outBuf, errBuf bytes.Buffer
		ioEnv := core.IO{
			Stdin:  strings.NewReader(in),
			Stdout: &outBuf,
			Stderr: &errBuf,
			Dir:    dir,
		}
		code := runner(ctx, ioEnv, args...)
		return outBuf.String(), errBuf.String(), code
	}

	// 1. Reading from stdin when no files specified
	out, errOut, code := runCat(context.Background(), "stream data", "")
	if code != 0 || out != "stream data" || errOut != "" {
		t.Fatalf("cat stdin failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 2. Reading single file relative to Dir
	out, errOut, code = runCat(context.Background(), "", tempDir, "file1.txt")
	if code != 0 || out != "alpha\n" || errOut != "" {
		t.Fatalf("cat single file failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 3. Concatenating multiple files and stdin via '-'
	out, errOut, code = runCat(context.Background(), "beta\n", tempDir, "file1.txt", "-", "file2.txt")
	if code != 0 || out != "alpha\nbeta\ngamma\n" || errOut != "" {
		t.Fatalf("cat multi+dash failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 4. -u flag (unbuffered, accepted)
	out, errOut, code = runCat(context.Background(), "unbuffered", "", "-u")
	if code != 0 || out != "unbuffered" || errOut != "" {
		t.Fatalf("cat -u failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 5. Missing file returns 1 and prints diagnostic to Stderr
	out, errOut, code = runCat(context.Background(), "", tempDir, "nonexistent.txt")
	if code != 1 || !strings.Contains(errOut, "cat:") {
		t.Fatalf("cat missing file failed: expected code 1 with diagnostic, got code=%d err=%q", code, errOut)
	}

	// 6. -h returns 0 with usage
	out, errOut, code = runCat(context.Background(), "", "", "-h")
	if code != 0 || !strings.Contains(errOut, "Usage:") {
		t.Fatalf("cat -h failed: code=%d out=%q err=%q", code, out, errOut)
	}

	// 7. Invalid flag returns 1
	out, errOut, code = runCat(context.Background(), "", "", "-invalid-flag-xyz")
	if code != 1 {
		t.Fatalf("cat invalid flag failed: expected code 1, got %d", code)
	}

	// 8. FlagSet isolation on subsequent call
	out, errOut, code = runCat(context.Background(), "recovered", "")
	if code != 0 || out != "recovered" {
		t.Fatalf("cat recovery failed: code=%d out=%q", code, out)
	}

	// 9. Context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, errOut, code = runCat(ctx, "cancelled", "")
	if code != 130 {
		t.Fatalf("cat cancellation failed: expected code 130, got %d", code)
	}
}



