package shell

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hugelgupf/p9/p9"
	"github.com/u-root/u-root/pkg/core"
	"golang.org/x/crypto/ssh"
)

func setupTestShell(t *testing.T) (string, func(string) (string, int, error)) {
	t.Helper()
	dir := t.TempDir()
	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatalf("NewExecutionSession failed: %v", err)
	}

	runShell := func(script string) (string, int, error) {
		_, parsed := es.ClassifyInput(script)
		rec, err := es.RunCommand(context.Background(), script, parsed, SourceUserShell, nil)
		return rec.Output, rec.ExitCode, err
	}

	return dir, runShell
}

// ---------------------------------------------------------------------------
// Band 1A: Pilot Utilities (echo, cat, grep)
// ---------------------------------------------------------------------------
func TestURoot_Band1A_Pilot(t *testing.T) {
	dir, run := setupTestShell(t)

	t.Run("echo", func(t *testing.T) {
		out, code, _ := run("echo hello world")
		if code != 0 || strings.TrimSpace(out) != "hello world" {
			t.Errorf("echo standard failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -n hello")
		if code != 0 || out != "hello" {
			t.Errorf("echo -n failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo 'a\\nb'")
		if code != 0 || out != "a\\nb\n" {
			t.Errorf("echo default escape handling failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'a\\nb'")
		if code != 0 || strings.TrimSpace(out) != "a\nb" {
			t.Errorf("echo -e newline failed: code=%d out=%q", code, out)
		}
	})

	t.Run("cat", func(t *testing.T) {
		f1 := filepath.Join(dir, "cat1.txt")
		f2 := filepath.Join(dir, "cat2.txt")
		_ = os.WriteFile(f1, []byte("part1\n"), 0o644)
		_ = os.WriteFile(f2, []byte("part2\n"), 0o644)

		out, code, _ := run("cat cat1.txt cat2.txt")
		if code != 0 || strings.TrimSpace(out) != "part1\npart2" {
			t.Errorf("cat multiple failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo piped | cat -")
		if code != 0 || strings.TrimSpace(out) != "piped" {
			t.Errorf("cat stdin failed: code=%d out=%q", code, out)
		}

		_, code, _ = run("cat definitely_nonexistent_file_xyz.txt")
		if code == 0 {
			t.Errorf("cat nonexistent expected non-zero, got %d", code)
		}
	})

	t.Run("grep", func(t *testing.T) {
		sample := filepath.Join(dir, "grep_sample.txt")
		content := "Apple\nbanana\ncherry\nPineapple\n"
		_ = os.WriteFile(sample, []byte(content), 0o644)

		out, code, _ := run("grep banana grep_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "banana" {
			t.Errorf("grep basic failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("grep -i apple grep_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "Apple\nPineapple" {
			t.Errorf("grep -i failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("grep -v apple grep_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "Apple\nbanana\ncherry" {
			t.Errorf("grep -v failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("grep -c Apple grep_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "1" {
			t.Errorf("grep -c failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("grep -n cherry grep_sample.txt")
		if code != 0 || !strings.Contains(out, "3:") {
			t.Errorf("grep -n failed: code=%d out=%q", code, out)
		}

		_, code, _ = run("grep -q banana grep_sample.txt")
		if code != 0 {
			t.Errorf("grep -q found expected 0, got %d", code)
		}

		_, code, _ = run("grep -q strawberry grep_sample.txt")
		if code != 1 {
			t.Errorf("grep -q not found expected 1, got %d", code)
		}

		out, code, _ = run("echo 'hello.world' | grep -F 'hello.world'")
		if code != 0 || strings.TrimSpace(out) != "hello.world" {
			t.Errorf("grep -F failed: code=%d out=%q", code, out)
		}
	})
}

// ---------------------------------------------------------------------------
// Band 1B: Text Processing & Filters (18 Commands)
// ---------------------------------------------------------------------------
func TestURoot_Band1B_TextProcessing(t *testing.T) {
	dir, run := setupTestShell(t)

	linesFile := filepath.Join(dir, "lines.txt")
	_ = os.WriteFile(linesFile, []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\n"), 0o644)

	t.Run("sed_execute_command", func(t *testing.T) {
		out, code, err := run(`printf 'needle\n' | sed -n "s/needle/printf computed/e;p"`)
		if err != nil || code != 0 || strings.TrimSpace(out) != "computed" {
			t.Errorf("sed execute substitution failed: code=%d out=%q err=%v", code, out, err)
		}
	})

	t.Run("head", func(t *testing.T) {
		out, code, _ := run("head -n 3 lines.txt")
		if code != 0 || strings.TrimSpace(out) != "one\ntwo\nthree" {
			t.Errorf("head -n failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("head -c 5 lines.txt")
		if code != 0 || out != "one\nt" {
			t.Errorf("head -c failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'a\\nb\\nc' | head -n 1")
		if code != 0 || strings.TrimSpace(out) != "a" {
			t.Errorf("head stdin failed: code=%d out=%q", code, out)
		}
	})

	t.Run("tail", func(t *testing.T) {
		out, code, _ := run("tail -n 2 lines.txt")
		if code != 0 || strings.TrimSpace(out) != "ten\neleven" {
			t.Errorf("tail -n failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'x\\ny\\nz' | tail -n 1")
		if code != 0 || strings.TrimSpace(out) != "z" {
			t.Errorf("tail stdin failed: code=%d out=%q", code, out)
		}
	})

	t.Run("sort", func(t *testing.T) {
		unsorted := filepath.Join(dir, "unsorted.txt")
		_ = os.WriteFile(unsorted, []byte("banana\napple\ncherry\napple\n"), 0o644)

		out, code, _ := run("sort unsorted.txt")
		if code != 0 || strings.TrimSpace(out) != "apple\napple\nbanana\ncherry" {
			t.Errorf("sort default failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("sort -r unsorted.txt")
		if code != 0 || strings.TrimSpace(out) != "cherry\nbanana\napple\napple" {
			t.Errorf("sort -r failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("sort -u unsorted.txt")
		if code != 0 || strings.TrimSpace(out) != "apple\nbanana\ncherry" {
			t.Errorf("sort -u failed: code=%d out=%q", code, out)
		}

		numFile := filepath.Join(dir, "nums.txt")
		_ = os.WriteFile(numFile, []byte("10\n2\n1\n20\n"), 0o644)
		out, code, _ = run("sort -n nums.txt")
		if code != 0 || strings.TrimSpace(out) != "1\n2\n10\n20" {
			t.Errorf("sort -n failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("sort -o sorted_out.txt unsorted.txt && cat sorted_out.txt")
		if code != 0 || !strings.Contains(out, "banana") {
			t.Errorf("sort -o failed: code=%d out=%q", code, out)
		}
	})

	t.Run("uniq", func(t *testing.T) {
		out, code, _ := run("echo -e 'a\\na\\nb\\nb\\nb\\nc' | uniq")
		if code != 0 || strings.TrimSpace(out) != "a\nb\nc" {
			t.Errorf("uniq default failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'a\\na\\nb' | uniq -c")
		if code != 0 || !strings.Contains(out, "2") || !strings.Contains(out, "a") {
			t.Errorf("uniq -c failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'a\\na\\nb\\nc\\nc' | uniq -d")
		if code != 0 || strings.TrimSpace(out) != "a\nc" {
			t.Errorf("uniq -d failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'one same\\n two same\\nthree other' | uniq -f 1")
		if code != 0 || strings.TrimSpace(out) != "one same\nthree other" {
			t.Errorf("uniq -f failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'xapple\\nyapple\\nzberry' | uniq -s 1")
		if code != 0 || strings.TrimSpace(out) != "xapple\nzberry" {
			t.Errorf("uniq -s failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'a\\na\\nb\\nc\\nc' | uniq -u")
		if code != 0 || strings.TrimSpace(out) != "b" {
			t.Errorf("uniq -u failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'a\\nA\\nb' | uniq -i")
		if code != 0 || strings.TrimSpace(out) != "a\nb" {
			t.Errorf("uniq -i failed: code=%d out=%q", code, out)
		}
	})

	t.Run("wc", func(t *testing.T) {
		f := filepath.Join(dir, "wc_sample.txt")
		_ = os.WriteFile(f, []byte("hello world\nsecond line\n"), 0o644)

		out, code, _ := run("wc -l wc_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "2 wc_sample.txt" {
			t.Errorf("wc -l failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("wc -w wc_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "4 wc_sample.txt" {
			t.Errorf("wc -w failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("wc -c wc_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "24 wc_sample.txt" {
			t.Errorf("wc -c failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("wc -m wc_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "24 wc_sample.txt" {
			t.Errorf("wc -m failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("wc -L wc_sample.txt")
		if code != 0 || strings.TrimSpace(out) != "11 wc_sample.txt" {
			t.Errorf("wc -L failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("printf '界\\n' | wc -L")
		if code != 0 || strings.TrimSpace(out) != "2" {
			t.Errorf("wc -L wide character failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("printf 'a\\tb\\n' | wc -L")
		if code != 0 || strings.TrimSpace(out) != "9" {
			t.Errorf("wc -L tab stop failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("printf '👨‍👩‍👧‍👦\\n' | wc -L")
		if code != 0 || strings.TrimSpace(out) != "2" {
			t.Errorf("wc -L grapheme failed: code=%d out=%q", code, out)
		}
	})

	t.Run("tr", func(t *testing.T) {
		out, code, _ := run("echo hello | tr a-z A-Z")
		if code != 0 || strings.TrimSpace(out) != "HELLO" {
			t.Errorf("tr replace failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo 'h1e2l3l4o' | tr -d '0-9'")
		if code != 0 || strings.TrimSpace(out) != "hello" {
			t.Errorf("tr -d failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo 'goooood' | tr -s 'o'")
		if code != 0 || strings.TrimSpace(out) != "god" {
			t.Errorf("tr -s failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo 'banana' | tr -cd 'a'")
		if code != 0 || strings.TrimSpace(out) != "aaa" {
			t.Errorf("tr -c -d failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo 'aaaabbbb' | tr -s a X")
		if code != 0 || strings.TrimSpace(out) != "Xbbbb" {
			t.Errorf("tr -s SET2 failed: code=%d out=%q", code, out)
		}
	})

	t.Run("tee", func(t *testing.T) {
		t1 := filepath.Join(dir, "t1.txt")
		t2 := filepath.Join(dir, "t2.txt")
		out, code, _ := run("echo stream | tee t1.txt t2.txt")
		if code != 0 || strings.TrimSpace(out) != "stream" {
			t.Errorf("tee stdout failed: code=%d out=%q", code, out)
		}
		c1, _ := os.ReadFile(t1)
		c2, _ := os.ReadFile(t2)
		if strings.TrimSpace(string(c1)) != "stream" || strings.TrimSpace(string(c2)) != "stream" {
			t.Errorf("tee files failed: c1=%q c2=%q", c1, c2)
		}

		_, code, _ = run("echo again | tee -a t1.txt")
		c1, _ = os.ReadFile(t1)
		if code != 0 || string(c1) != "stream\nagain\n" {
			t.Errorf("tee -a failed: code=%d file=%q", code, c1)
		}

		out, code, _ = run("echo more | tee -a t1.txt")
		c1, _ = os.ReadFile(t1)
		if code != 0 || strings.TrimSpace(string(c1)) != "stream\nagain\nmore" {
			t.Errorf("tee -a failed: %q", string(c1))
		}
	})

	t.Run("seq", func(t *testing.T) {
		out, code, _ := run("seq 3")
		if code != 0 || strings.TrimSpace(out) != "1\n2\n3" {
			t.Errorf("seq 1-arg failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("seq 2 4")
		if code != 0 || strings.TrimSpace(out) != "2\n3\n4" {
			t.Errorf("seq 2-args failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("seq -s , 1 3")
		if code != 0 || strings.TrimSpace(out) != "1,2,3" {
			t.Errorf("seq -s failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("seq -f %02g 1 3")
		if code != 0 || strings.TrimSpace(out) != "01\n02\n03" {
			t.Errorf("seq -f failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("seq 1 2 5")
		if code != 0 || strings.TrimSpace(out) != "1\n3\n5" {
			t.Errorf("seq 3-args failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("seq -s ',' 3")
		if code != 0 || strings.TrimSpace(out) != "1,2,3" {
			t.Errorf("seq -s failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("seq 3 -1 -1")
		if code != 0 || strings.TrimSpace(out) != "3\n2\n1\n0\n-1" {
			t.Errorf("seq negative increment after positive start failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("seq 3 -1 1")
		if code != 0 || strings.TrimSpace(out) != "3\n2\n1" {
			t.Errorf("seq negative increment to positive end failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("seq 1 -1 -3")
		if code != 0 || strings.TrimSpace(out) != "1\n0\n-1\n-2\n-3" {
			t.Errorf("seq negative increment to negative end failed: code=%d out=%q", code, out)
		}
	})

	t.Run("comm", func(t *testing.T) {
		c1 := filepath.Join(dir, "comm1.txt")
		c2 := filepath.Join(dir, "comm2.txt")
		_ = os.WriteFile(c1, []byte("a\nb\nc\n"), 0o644)
		_ = os.WriteFile(c2, []byte("b\nc\nd\n"), 0o644)

		out, code, _ := run("comm -12 comm1.txt comm2.txt")
		if code != 0 || !strings.Contains(out, "b") || !strings.Contains(out, "c") || strings.Contains(out, "a") || strings.Contains(out, "d") {
			t.Errorf("comm -12 failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("comm -23 comm1.txt comm2.txt")
		if code != 0 || strings.TrimSpace(out) != "a" {
			t.Errorf("comm -23 failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -e 'a\\nb' | comm - comm2.txt")
		if code != 0 || !strings.Contains(out, "a") || !strings.Contains(out, "b") {
			t.Errorf("comm stdin failed: code=%d out=%q", code, out)
		}
	})

	t.Run("cmp", func(t *testing.T) {
		f1 := filepath.Join(dir, "cmp1.txt")
		f2 := filepath.Join(dir, "cmp2.txt")
		_ = os.WriteFile(f1, []byte("hello"), 0o644)
		_ = os.WriteFile(f2, []byte("helzo"), 0o644)

		_, code, _ := run("cmp cmp1.txt cmp1.txt")
		if code != 0 {
			t.Errorf("cmp identical expected 0, got %d", code)
		}

		out, code, _ := run("cmp cmp1.txt cmp2.txt")
		if code != 1 || !strings.Contains(out, "char 4") {
			t.Errorf("cmp differ failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("cmp -s cmp1.txt cmp2.txt")
		if code != 1 || out != "" {
			t.Errorf("cmp -s expected silent code 1, got code=%d out=%q", code, out)
		}

		_, code, _ = run("cmp -n 3 cmp1.txt cmp2.txt")
		if code != 0 {
			t.Errorf("cmp -n equal prefix expected 0, got %d", code)
		}

		_, code, _ = run("cmp -n 4 cmp1.txt cmp2.txt")
		if code != 1 {
			t.Errorf("cmp -n differing prefix expected 1, got %d", code)
		}
	})

	t.Run("strings", func(t *testing.T) {
		binFile := filepath.Join(dir, "data.bin")
		_ = os.WriteFile(binFile, []byte{0, 0, 'h', 'e', 'l', 'l', 'o', 0, 'b', 'y', 'e', 0, 'w', 'o', 'r', 'l', 'd', '!', 0}, 0o644)

		out, code, _ := run("strings data.bin")
		if code != 0 || strings.TrimSpace(out) != "hello\nworld!" {
			t.Errorf("strings default failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("strings -n 6 data.bin")
		if code != 0 || strings.TrimSpace(out) != "world!" {
			t.Errorf("strings -n 6 failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("strings -t d data.bin")
		if code != 0 || strings.TrimSpace(out) != "2 hello\n12 world!" {
			t.Errorf("strings -t d failed: code=%d out=%q", code, out)
		}
	})

	t.Run("hexdump", func(t *testing.T) {
		out, code, _ := run("echo -n 'AA' | hexdump")
		if code != 0 || !strings.Contains(out, "4141") || strings.Contains(out, "|AA|") {
			t.Errorf("hexdump default failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -n 'AA' | hexdump -C")
		if code != 0 || !strings.Contains(out, "|AA|") || !strings.Contains(out, "41 41") {
			t.Errorf("hexdump -C failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -n 'ABCDEF' | hexdump -C -s 1 -n 2 -v")
		if code != 0 || !strings.Contains(out, "00000001") || !strings.Contains(out, "42 43") || strings.Contains(out, "44") {
			t.Errorf("hexdump -s -n -v failed: code=%d out=%q", code, out)
		}
	})

	t.Run("ts", func(t *testing.T) {
		out, code, _ := run("echo logged | ts")
		if code != 0 || !strings.Contains(out, "logged") {
			t.Errorf("ts failed: code=%d out=%q", code, out)
		}
	})

	t.Run("tsort", func(t *testing.T) {
		out, code, _ := run("echo 'a b b c c d' | tsort")
		if code != 0 || strings.TrimSpace(out) != "a\nb\nc\nd" {
			t.Errorf("tsort DAG failed: code=%d out=%q", code, out)
		}

		_, code, _ = run("echo 'a b b a' | tsort")
		if code != 1 {
			t.Errorf("tsort cycle expected code 1, got %d", code)
		}
	})

	t.Run("base64", func(t *testing.T) {
		out, code, _ := run("echo -n 'testing 123' | base64")
		if code != 0 || strings.TrimSpace(out) != "dGVzdGluZyAxMjM=" {
			t.Errorf("base64 encode failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -n 'dGVzdGluZyAxMjM=' | base64 -d")
		if code != 0 || out != "testing 123" {
			t.Errorf("base64 decode failed: code=%d out=%q", code, out)
		}
	})

	t.Run("yes", func(t *testing.T) {
		out, code, _ := run("yes foo | head -n 3")
		if code != 0 || strings.TrimSpace(out) != "foo\nfoo\nfoo" {
			t.Errorf("yes failed: code=%d out=%q", code, out)
		}
	})

	t.Run("more", func(t *testing.T) {
		out, code, _ := run("echo -e 'row1\\nrow2\\nrow3' | more")
		if code != 0 || strings.TrimSpace(out) != "row1\nrow2\nrow3" {
			t.Errorf("more failed: code=%d out=%q", code, out)
		}
	})

}

// ---------------------------------------------------------------------------
// Band 1C: File & Directory Operations (17 Commands)
// ---------------------------------------------------------------------------
func TestURoot_Band1C_FileAndDirOps(t *testing.T) {
	dir, run := setupTestShell(t)

	t.Run("ls", func(t *testing.T) {
		_ = os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("vis"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, ".hidden.txt"), []byte("hid"), 0o644)

		out, code, _ := run("ls")
		if code != 0 || !strings.Contains(out, "visible.txt") || strings.Contains(out, ".hidden.txt") {
			t.Errorf("ls default failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("ls -a")
		if code != 0 || !strings.Contains(out, ".hidden.txt") {
			t.Errorf("ls -a failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("mkdir -p testdir && ls -d testdir")
		if code != 0 || !strings.Contains(out, "testdir") {
			t.Errorf("ls -d failed: code=%d out=%q", code, out)
		}
	})

	t.Run("cp_mv_rm", func(t *testing.T) {
		src := filepath.Join(dir, "original.txt")
		_ = os.WriteFile(src, []byte("content"), 0o644)

		out, code, _ := run("cp original.txt copied.txt && cat copied.txt")
		if code != 0 || strings.TrimSpace(out) != "content" {
			t.Errorf("cp failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("mv copied.txt renamed.txt && cat renamed.txt")
		if code != 0 || strings.TrimSpace(out) != "content" {
			t.Errorf("mv failed: code=%d out=%q", code, out)
		}

		_ = os.WriteFile(filepath.Join(dir, "force_src.txt"), []byte("forced"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "force_dst.txt"), []byte("old"), 0o644)
		out, code, _ = run("mv -f force_src.txt force_dst.txt && cat force_dst.txt")
		if code != 0 || strings.TrimSpace(out) != "forced" {
			t.Errorf("mv -f failed: code=%d out=%q", code, out)
		}

		_ = os.WriteFile(filepath.Join(dir, "interactive_src.txt"), []byte("new"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "interactive_dst.txt"), []byte("old"), 0o644)
		out, code, _ = run("printf 'n\\n' | mv -i interactive_src.txt interactive_dst.txt; cat interactive_dst.txt")
		if code != 0 || !strings.Contains(out, "old") {
			t.Errorf("mv -i declined overwrite incorrectly: code=%d out=%q", code, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "interactive_src.txt")); err != nil {
			t.Errorf("mv -i decline removed source: %v", err)
		}
		out, code, _ = run("printf 'y\\n' | mv -i interactive_src.txt interactive_dst.txt; cat interactive_dst.txt")
		if code != 0 || !strings.Contains(out, "new") {
			t.Errorf("mv -i confirmation failed: code=%d out=%q", code, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "interactive_src.txt")); !os.IsNotExist(err) {
			t.Errorf("mv -i confirmation left source: err=%v", err)
		}

		out, code, _ = run("rm renamed.txt && ls")
		if code != 0 || strings.Contains(out, "renamed.txt") {
			t.Errorf("rm failed: code=%d out=%q", code, out)
		}

		emptyDir := filepath.Join(dir, "rm-empty")
		if err := os.Mkdir(emptyDir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, code, _ = run("rm -d rm-empty")
		if code != 0 {
			t.Errorf("rm -d empty directory failed: code=%d", code)
		}
		if _, err := os.Stat(emptyDir); !os.IsNotExist(err) {
			t.Errorf("rm -d left empty directory: err=%v", err)
		}

		nonEmptyDir := filepath.Join(dir, "rm-nonempty")
		if err := os.Mkdir(nonEmptyDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nonEmptyDir, "child"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, code, _ = run("rm -d rm-nonempty")
		if code == 0 {
			t.Error("rm -d unexpectedly removed a non-empty directory")
		}
		if _, err := os.Stat(filepath.Join(nonEmptyDir, "child")); err != nil {
			t.Errorf("rm -d removed non-empty directory contents: %v", err)
		}
		if err := os.RemoveAll(nonEmptyDir); err != nil {
			t.Fatal(err)
		}

		_, code, _ = run("rm -f non_existent_file_xyz.txt")
		if code != 0 {
			t.Errorf("rm -f expected code 0, got %d", code)
		}
	})

	t.Run("mkdir_mktemp", func(t *testing.T) {
		out, code, _ := run("mkdir new_folder && ls")
		if code != 0 || !strings.Contains(out, "new_folder") {
			t.Errorf("mkdir single failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("mkdir -p a/b/c && ls a/b")
		if code != 0 || !strings.Contains(out, "c") {
			t.Errorf("mkdir -p failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("mktemp")
		if code != 0 || strings.TrimSpace(out) == "" {
			t.Errorf("mktemp failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("mktemp -d")
		if code != 0 || strings.TrimSpace(out) == "" {
			t.Errorf("mktemp -d failed: code=%d out=%q", code, out)
		}
	})

	t.Run("touch_truncate", func(t *testing.T) {
		target := filepath.Join(dir, "touch_test.txt")
		_, code, _ := run("touch touch_test.txt")
		if code != 0 {
			t.Errorf("touch create failed: %d", code)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("touch did not create file: %v", err)
		}

		_, code, _ = run("truncate -s 128 touch_test.txt")
		if code != 0 {
			t.Errorf("truncate extend failed: %d", code)
		}
		fi, _ := os.Stat(target)
		if fi.Size() != 128 {
			t.Errorf("truncate size expected 128, got %d", fi.Size())
		}

		_, code, _ = run("truncate -s 16 touch_test.txt")
		if code != 0 {
			t.Errorf("truncate shrink failed: %d", code)
		}
		fi, _ = os.Stat(target)
		if fi.Size() != 16 {
			t.Errorf("truncate size expected 16, got %d", fi.Size())
		}
	})

	t.Run("ln_readlink_realpath", func(t *testing.T) {
		src := filepath.Join(dir, "target.txt")
		_ = os.WriteFile(src, []byte("target_content"), 0o644)

		_, code, _ := run("ln -s target.txt sym_link.txt")
		if code != 0 {
			t.Errorf("ln -s failed: %d", code)
		}

		out, code, _ := run("readlink sym_link.txt")
		if code != 0 || !strings.Contains(out, "target.txt") {
			t.Errorf("readlink failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("realpath sym_link.txt")
		if code != 0 || !strings.Contains(out, "target.txt") {
			t.Errorf("realpath failed: code=%d out=%q", code, out)
		}
	})

	t.Run("chmod_find_du_pwd", func(t *testing.T) {
		f := filepath.Join(dir, "perm.txt")
		_ = os.WriteFile(f, []byte("perm"), 0o644)

		_, code, _ := run("chmod 0755 perm.txt")
		if code != 0 {
			t.Errorf("chmod failed: %d", code)
		}

		out, code, _ := run("find -name perm.txt .")
		if code != 0 || !strings.Contains(out, "perm.txt") {
			t.Errorf("find -name failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("du -s perm.txt")
		if code != 0 || !strings.Contains(out, "perm.txt") {
			t.Errorf("du -s failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("pwd")
		if code != 0 || strings.TrimSpace(out) == "" {
			t.Errorf("pwd failed: code=%d out=%q", code, out)
		}
	})

	t.Run("basename_dirname", func(t *testing.T) {
		out, code, _ := run("basename /path/to/my_file.tar.gz .tar.gz")
		if code != 0 || strings.TrimSpace(out) != "my_file" {
			t.Errorf("basename with suffix failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("basename -a /a/b.txt /c/d.txt")
		if code != 0 || strings.TrimSpace(out) != "b.txt\nd.txt" {
			t.Errorf("basename -a failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("dirname /var/log/syslog")
		if code != 0 || filepath.ToSlash(strings.TrimSpace(out)) != "/var/log" {
			t.Errorf("dirname failed: code=%d out=%q", code, out)
		}
	})
}

// ---------------------------------------------------------------------------
// Band 1D: Checksums, Archival & Compression (6 Commands, 7 Entrypoints)
// ---------------------------------------------------------------------------
func TestURoot_Band1D_ArchivalAndChecksums(t *testing.T) {
	dir, run := setupTestShell(t)

	sample := filepath.Join(dir, "hash_me.txt")
	sampleContent := "checksum data string\n"
	_ = os.WriteFile(sample, []byte(sampleContent), 0o644)

	t.Run("shasum", func(t *testing.T) {
		out, code, _ := run("shasum -a 256 hash_me.txt")
		if code != 0 || !strings.Contains(out, "hash_me.txt") {
			t.Errorf("shasum -a 256 failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("echo -n 'abc' | shasum -a 1")
		// sha1 of 'abc' is ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad ... wait sha1 of abc is a9993e364706816aba3e25717850c26c9cd0d89d
		if code != 0 || !strings.Contains(out, "a9993e364706816aba3e25717850c26c9cd0d89d") {
			t.Errorf("shasum -a 1 failed: code=%d out=%q", code, out)
		}
	})

	t.Run("md5sum", func(t *testing.T) {
		h := md5.Sum([]byte(sampleContent))
		expectedMD5 := fmt.Sprintf("%x", h)

		out, code, _ := run("md5sum hash_me.txt")
		if code != 0 || !strings.Contains(out, expectedMD5) {
			t.Errorf("md5sum failed: code=%d out=%q", code, out)
		}

		checkFile := filepath.Join(dir, "sums.md5")
		_ = os.WriteFile(checkFile, []byte(fmt.Sprintf("%s hash_me.txt\n", expectedMD5)), 0o644)

		out, code, _ = run("md5sum -c sums.md5")
		if code != 0 || !strings.Contains(out, "OK") {
			t.Errorf("md5sum -c failed: code=%d out=%q", code, out)
		}
	})

	t.Run("gzip_and_gzcat", func(t *testing.T) {
		raw := filepath.Join(dir, "compress.txt")
		_ = os.WriteFile(raw, []byte("compress me please\n"), 0o644)

		out, code, _ := run("gzip -k compress.txt && ls")
		if code != 0 || !strings.Contains(out, "compress.txt.gz") {
			t.Errorf("gzip -k failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("gzcat compress.txt.gz")
		if code != 0 || strings.TrimSpace(out) != "compress me please" {
			t.Errorf("gzcat failed: code=%d out=%q", code, out)
		}

		_ = os.Remove(raw)
		out, code, _ = run("gzip -d compress.txt.gz && cat compress.txt")
		if code != 0 || strings.TrimSpace(out) != "compress me please" {
			t.Errorf("gzip -d failed: code=%d out=%q", code, out)
		}
	})

	t.Run("tar", func(t *testing.T) {
		f1 := filepath.Join(dir, "t1.txt")
		_ = os.WriteFile(f1, []byte("tar content 1"), 0o644)

		out, code, _ := run("tar -cvf test_archive.tar t1.txt && tar -tvf test_archive.tar")
		if code != 0 || !strings.Contains(out, "t1.txt") {
			t.Errorf("tar create and list failed: code=%d out=%q", code, out)
		}

		outDir := filepath.Join(dir, "extracted_tar")
		_ = os.Mkdir(outDir, 0o755)
		out, code, _ = run("tar -xvf test_archive.tar extracted_tar && ls extracted_tar")
		if code != 0 || !strings.Contains(out, "t1.txt") {
			t.Errorf("tar extract failed: code=%d out=%q", code, out)
		}
	})

	t.Run("cpio", func(t *testing.T) {
		f := filepath.Join(dir, "cpio_file.txt")
		_ = os.WriteFile(f, []byte("cpio data"), 0o644)

		out, code, _ := run("echo cpio_file.txt | cpio o > archive.cpio && cpio t < archive.cpio")
		if code != 0 || !strings.Contains(out, "cpio_file.txt") {
			t.Errorf("cpio o/t failed: code=%d out=%q", code, out)
		}
	})

	t.Run("gpgv", func(t *testing.T) {
		_, code, _ := run("gpgv")
		if code == 0 {
			t.Errorf("gpgv missing args expected non-zero, got %d", code)
		}
	})
}

// ---------------------------------------------------------------------------
// Band 1E: Shell Control & Builtin Utilities (11 Commands)
// ---------------------------------------------------------------------------
func TestURoot_Band1E_ShellControlAndBuiltins(t *testing.T) {
	_, run := setupTestShell(t)

	t.Run("true_and_false", func(t *testing.T) {
		_, code, _ := run("true")
		if code != 0 {
			t.Errorf("true expected 0, got %d", code)
		}

		_, code, _ = run("false")
		if code != 1 {
			t.Errorf("false expected 1, got %d", code)
		}
	})

	t.Run("sleep", func(t *testing.T) {
		start := time.Now()
		_, code, _ := run("sleep 20ms")
		elapsed := time.Since(start)
		if code != 0 || elapsed < 15*time.Millisecond {
			t.Errorf("sleep 20ms failed: code=%d elapsed=%v", code, elapsed)
		}
	})

	t.Run("which", func(t *testing.T) {
		out, code, _ := run("which grep sort uniq")
		if code != 0 || !strings.Contains(out, "grep") || !strings.Contains(out, "sort") {
			t.Errorf("which builtins failed: code=%d out=%q", code, out)
		}

		_, code, _ = run("which definitely_nonexistent_tool_12345")
		if code == 0 {
			t.Errorf("which nonexistent expected non-zero, got %d", code)
		}
	})

	t.Run("printenv", func(t *testing.T) {
		out, code, _ := run("MY_TEST_VAR=kabibi_rocks printenv MY_TEST_VAR")
		if code != 0 || strings.TrimSpace(out) != "kabibi_rocks" {
			t.Errorf("printenv single var failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("printenv")
		if code != 0 || len(strings.Split(out, "\n")) < 2 {
			t.Errorf("printenv dump failed: code=%d out=%q", code, out)
		}
	})

	t.Run("date", func(t *testing.T) {
		out, code, _ := run("date +%Y")
		if code != 0 || len(strings.TrimSpace(out)) != 4 {
			t.Errorf("date +%%Y failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("date -u")
		if code != 0 || strings.TrimSpace(out) == "" {
			t.Errorf("date -u failed: code=%d out=%q", code, out)
		}
	})

	t.Run("hostname", func(t *testing.T) {
		out, code, _ := run("hostname")
		if code != 0 || strings.TrimSpace(out) == "" {
			t.Errorf("hostname default failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("HOSTNAME=host.example.com hostname -s")
		if code != 0 || strings.TrimSpace(out) != "host" {
			t.Errorf("hostname -s failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("HOSTNAME=host.example.com hostname -d")
		if code != 0 || strings.TrimSpace(out) != "example.com" {
			t.Errorf("hostname -d failed: code=%d out=%q", code, out)
		}
	})

	t.Run("xargs", func(t *testing.T) {
		out, code, _ := run("echo 'one two three' | xargs -n 1 echo")
		if code != 0 || strings.TrimSpace(out) != "one\ntwo\nthree" {
			t.Errorf("xargs -n 1 failed: code=%d out=%q", code, out)
		}

		for _, command := range []string{
			"printf '%s\\0' 'a\\nb' | xargs -0",
			"printf '%s\\0' 'a\\nb' | xargs -0 echo",
		} {
			out, code, _ = run(command)
			if code != 0 || out != "a\\nb\n" {
				t.Errorf("%s failed: code=%d out=%q", command, code, out)
			}
		}
	})

	t.Run("time", func(t *testing.T) {
		out, code, _ := run("time echo shell_timing_test")
		if code != 0 || !strings.Contains(out, "shell_timing_test") {
			t.Errorf("shell time syntax failed: code=%d out=%q", code, out)
		}

		out, code, _ = run("command time echo uroot_timing_test")
		if code != 0 || !strings.Contains(out, "uroot_timing_test") ||
			!strings.Contains(out, "real ") || !strings.Contains(out, "user ") || !strings.Contains(out, "sys ") {
			t.Errorf("u-root time command failed: code=%d out=%q", code, out)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		out, code, _ := run("timeout 2s echo finished_fast")
		if code != 0 || !strings.Contains(out, "finished_fast") {
			t.Errorf("timeout fast failed: code=%d out=%q", code, out)
		}

		_, code, _ = run("timeout 30ms sleep 5")
		if code != 124 {
			t.Errorf("timeout slow expected exit 124, got %d", code)
		}
	})

	t.Run("backoff", func(t *testing.T) {
		_, code, _ := run("backoff -t 500ms true")
		if code != 0 {
			t.Errorf("backoff true failed: %d", code)
		}
	})
}

// ---------------------------------------------------------------------------
// Band 2: Data Transfer, Subshells & Network Transport
// ---------------------------------------------------------------------------
func TestURoot_Band2_DataTransferAndNetwork(t *testing.T) {
	dir, run := setupTestShell(t)

	t.Run("dd", func(t *testing.T) {
		in := filepath.Join(dir, "dd_in.txt")
		out := filepath.Join(dir, "dd_out.txt")
		_ = os.WriteFile(in, []byte("abcdefghijklmnop"), 0o644)

		// 1. Basic copy with bs and count
		_, code, _ := run("dd if=dd_in.txt of=dd_out.txt bs=4 count=2")
		if code != 0 {
			t.Fatalf("dd basic failed: code=%d", code)
		}
		data, err := os.ReadFile(out)
		if err != nil || string(data) != "abcdefgh" {
			t.Errorf("dd basic output mismatch: got %q, err=%v", string(data), err)
		}

		// 2. Skip and seek
		out2 := filepath.Join(dir, "dd_out2.txt")
		_, code, _ = run("dd if=dd_in.txt of=dd_out2.txt bs=2 skip=2 count=3")
		if code != 0 {
			t.Fatalf("dd skip/count failed: code=%d", code)
		}
		data, err = os.ReadFile(out2)
		if err != nil || string(data) != "efghij" {
			t.Errorf("dd skip/count mismatch: got %q, err=%v", string(data), err)
		}

		// 3. /dev/zero support
		outZero := filepath.Join(dir, "dd_zero.bin")
		_, code, _ = run("dd if=/dev/zero of=dd_zero.bin bs=16 count=4")
		if code != 0 {
			t.Fatalf("dd zero failed: code=%d", code)
		}
		data, err = os.ReadFile(outZero)
		if err != nil || len(data) != 64 {
			t.Errorf("dd zero len mismatch: got %d, err=%v", len(data), err)
		}

		// 4. Pipe streaming
		pipeOut, pcode, _ := run("echo hello | dd bs=2 count=2 status=none")
		if pcode != 0 || strings.TrimSpace(pipeOut) != "hell" {
			t.Errorf("dd pipe failed: code=%d out=%q", pcode, pipeOut)
		}
	})

	t.Run("wget", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/hello.txt":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("hello from wget server\n"))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer ts.Close()

		// 1. Download to file with -O
		out, code, _ := run(fmt.Sprintf("wget -O fetched.txt %s/hello.txt", ts.URL))
		if code != 0 {
			t.Fatalf("wget -O failed: code=%d out=%q", code, out)
		}
		data, err := os.ReadFile(filepath.Join(dir, "fetched.txt"))
		if err != nil || strings.TrimSpace(string(data)) != "hello from wget server" {
			t.Errorf("wget output mismatch: got %q, err=%v", string(data), err)
		}

		// 2. Download to stdout with -O -
		out, code, _ = run(fmt.Sprintf("wget -O - %s/hello.txt", ts.URL))
		if code != 0 || strings.TrimSpace(out) != "hello from wget server" {
			t.Errorf("wget -O - failed: code=%d out=%q", code, out)
		}

		// 3. Error on 404
		_, code, _ = run(fmt.Sprintf("wget %s/nonexistent", ts.URL))
		if code == 0 {
			t.Errorf("wget 404 expected non-zero, got %d", code)
		}
	})

	t.Run("netcat", func(t *testing.T) {
		// 1. Inbound data from server to nc client
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen failed: %v", err)
		}
		defer l.Close()

		go func() {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = conn.Write([]byte("hello from tcp listener\n"))
		}()

		host, port, _ := net.SplitHostPort(l.Addr().String())
		out, code, _ := run(fmt.Sprintf("nc %s %s", host, port))
		if code != 0 || !strings.Contains(out, "hello from tcp listener") {
			t.Errorf("nc client receive failed: code=%d out=%q", code, out)
		}

		// 2. Outbound data from nc client to server
		l2, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen failed: %v", err)
		}
		defer l2.Close()

		received := make(chan string, 1)
		go func() {
			conn, err := l2.Accept()
			if err != nil {
				received <- ""
				return
			}
			defer conn.Close()
			buf, _ := io.ReadAll(conn)
			received <- string(buf)
		}()

		host2, port2, _ := net.SplitHostPort(l2.Addr().String())
		_, code, _ = run(fmt.Sprintf("echo 'message to listener' | netcat %s %s", host2, port2))
		if code != 0 {
			t.Errorf("netcat send failed: code=%d", code)
		}

		select {
		case msg := <-received:
			if !strings.Contains(msg, "message to listener") {
				t.Errorf("listener got mismatch: %q", msg)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("timed out waiting for netcat payload")
		}
	})

	t.Run("scp", func(t *testing.T) {
		// 1. CLI flag validations through shell runner
		_, code, _ := run("scp")
		if code == 0 {
			t.Errorf("scp with no args expected non-zero, got %d", code)
		}
		_, code, _ = run("scp -t -f foo.txt")
		if code == 0 {
			t.Errorf("scp with both -t and -f expected non-zero, got %d", code)
		}

		// 2. Bidirectional transfer between source (-f) and sink (-t)
		scpFn, ok := LookupURootCommand("scp")
		if !ok {
			t.Fatalf("scp not registered")
		}

		srcPath := filepath.Join(dir, "scp_src.txt")
		dstPath := filepath.Join(dir, "scp_dst.txt")
		content := []byte("hello from scp bidirectional streaming protocol\n")
		if err := os.WriteFile(srcPath, content, 0o644); err != nil {
			t.Fatalf("failed to write scp source: %v", err)
		}

		// pipe1: source stdout -> sink stdin
		// pipe2: sink stdout -> source stdin
		r1, w1 := io.Pipe()
		r2, w2 := io.Pipe()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var srcCode, dstCode int
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			defer w1.Close()
			srcCode = scpFn(ctx, core.IO{Stdin: r2, Stdout: w1, Stderr: io.Discard}, "-f", srcPath)
		}()

		go func() {
			defer wg.Done()
			defer w2.Close()
			dstCode = scpFn(ctx, core.IO{Stdin: r1, Stdout: w2, Stderr: io.Discard}, "-t", dstPath)
		}()

		wg.Wait()

		if srcCode != 0 || dstCode != 0 {
			t.Fatalf("scp transfer failed: srcCode=%d dstCode=%d", srcCode, dstCode)
		}

		got, err := os.ReadFile(dstPath)
		if err != nil || !bytes.Equal(got, content) {
			t.Errorf("scp payload mismatch: got %q, err=%v", string(got), err)
		}
	})

	t.Run("sshd", func(t *testing.T) {
		// 1. Missing keys error handling
		_, code, _ := run("sshd -keys nonexistent_keys")
		if code == 0 {
			t.Errorf("sshd with missing keys expected non-zero, got %d", code)
		}

		// 2. Generate ephemeral RSA test keypair
		privKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rsa.GenerateKey failed: %v", err)
		}
		privPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(privKey),
		})
		privPath := filepath.Join(dir, "id_rsa_test")
		if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
			t.Fatalf("write priv key failed: %v", err)
		}

		sshPub, err := ssh.NewPublicKey(&privKey.PublicKey)
		if err != nil {
			t.Fatalf("ssh.NewPublicKey failed: %v", err)
		}
		pubBytes := ssh.MarshalAuthorizedKey(sshPub)
		keysPath := filepath.Join(dir, "authorized_keys_test")
		if err := os.WriteFile(keysPath, pubBytes, 0o644); err != nil {
			t.Fatalf("write auth keys failed: %v", err)
		}

		// Find a free port
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen failed: %v", err)
		}
		portStr := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
		_ = ln.Close()

		sshdFn, ok := LookupURootCommand("sshd")
		if !ok {
			t.Fatalf("sshd not registered")
		}

		serverCtx, cancelServer := context.WithCancel(context.Background())
		defer cancelServer()

		serverDone := make(chan int, 1)
		go func() {
			serverDone <- sshdFn(serverCtx, core.IO{
				Stdin:  strings.NewReader(""),
				Stdout: io.Discard,
				Stderr: io.Discard,
			}, "-keys", keysPath, "-privatekey", privPath, "-ip", "127.0.0.1", "-port", portStr)
		}()

		// Connect client
		signer, err := ssh.ParsePrivateKey(privPEM)
		if err != nil {
			t.Fatalf("ssh.ParsePrivateKey failed: %v", err)
		}
		clientCfg := &ssh.ClientConfig{
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         3 * time.Second,
		}

		targetAddr := net.JoinHostPort("127.0.0.1", portStr)
		var clt *ssh.Client
		for i := 0; i < 20; i++ {
			clt, err = ssh.Dial("tcp", targetAddr, clientCfg)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("failed to dial sshd: %v", err)
		}
		defer clt.Close()

		sess, err := clt.NewSession()
		if err != nil {
			t.Fatalf("NewSession failed: %v", err)
		}
		defer sess.Close()

		outBytes, err := sess.Output("echo hello sshd server")
		if err != nil {
			t.Fatalf("session.Output failed: %v", err)
		}
		if !strings.Contains(string(outBytes), "hello sshd server") {
			t.Errorf("output mismatch: %q", string(outBytes))
		}

		cancelServer()
		select {
		case c := <-serverDone:
			if c != 0 {
				t.Errorf("sshd exited with non-zero: %d", c)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("sshd did not shut down cleanly on cancel")
		}
	})

	t.Run("gosh", func(t *testing.T) {
		// 1. Inline command via -c
		out, code, err := run("gosh -c 'echo hello from gosh subshell'")
		if code != 0 || strings.TrimSpace(out) != "hello from gosh subshell" {
			t.Errorf("gosh -c failed: code=%d err=%v out=%q", code, err, out)
		}

		// 2. Variables and evaluation in subshell
		out, code, err = run("gosh -c 'FOO=kabibi; echo subshell=$FOO'")
		if code != 0 || strings.TrimSpace(out) != "subshell=kabibi" {
			t.Errorf("gosh var expansion failed: code=%d err=%v out=%q", code, err, out)
		}

		// 3. Script execution
		scriptPath := filepath.Join(dir, "test_gosh.sh")
		if err := os.WriteFile(scriptPath, []byte("echo line1\necho line2\n"), 0o644); err != nil {
			t.Fatalf("failed to write gosh script: %v", err)
		}
		out, code, err = run("gosh " + filepath.ToSlash(scriptPath))
		if code != 0 || strings.TrimSpace(out) != "line1\nline2" {
			t.Errorf("gosh script execution failed: code=%d err=%v out=%q", code, err, out)
		}

		// 4. Exit code propagation
		_, code, _ = run("gosh -c 'exit 5'")
		if code != 5 {
			t.Errorf("gosh exit status propagation failed: expected 5, got %d", code)
		}
	})

	t.Run("nohup", func(t *testing.T) {
		// 1. Missing command arguments -> exit code 127
		out, code, _ := run("nohup")
		if code != 127 {
			t.Errorf("nohup with no args expected 127, got code=%d out=%q", code, out)
		}

		// 2. Command execution in pipeline / subshell
		out, code, err := run("nohup echo 'running under nohup'")
		if code != 0 || strings.TrimSpace(out) != "running under nohup" {
			t.Errorf("nohup echo failed: code=%d err=%v out=%q", code, err, out)
		}

		// 3. Exit code propagation on failure
		_, code, _ = run("nohup false")
		if code == 0 {
			t.Errorf("nohup false expected non-zero, got %d", code)
		}
	})

	t.Run("ufs", func(t *testing.T) {
		// 1. Missing arguments -> exit code 1
		out, code, _ := run("ufs")
		if code != 1 {
			t.Errorf("ufs with no args expected 1, got code=%d out=%q", code, out)
		}

		// 2. Help flag -> exit code 0
		out, code, _ = run("ufs -help")
		if code != 0 {
			t.Errorf("ufs -help expected 0, got code=%d out=%q", code, out)
		}

		// 3. 9P server lifecycle and client file access
		ufsDir := filepath.Join(dir, "ufs_share")
		if err := os.MkdirAll(ufsDir, 0o755); err != nil {
			t.Fatalf("failed to create ufs share dir: %v", err)
		}
		testPayload := []byte("hello from 9p ufs share\n")
		if err := os.WriteFile(filepath.Join(ufsDir, "hello.txt"), testPayload, 0o644); err != nil {
			t.Fatalf("failed to write test file in ufs share: %v", err)
		}

		// Find a free TCP port
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen failed: %v", err)
		}
		portStr := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
		_ = ln.Close()

		ufsFn, ok := LookupURootCommand("ufs")
		if !ok {
			t.Fatalf("ufs command not registered")
		}

		serverCtx, cancelServer := context.WithCancel(context.Background())
		defer cancelServer()

		targetAddr := net.JoinHostPort("127.0.0.1", portStr)
		var serverStderr bytes.Buffer
		serverDone := make(chan int, 1)
		go func() {
			serverDone <- ufsFn(serverCtx, core.IO{
				Stdin:  strings.NewReader(""),
				Stdout: io.Discard,
				Stderr: &serverStderr,
			}, "-root", ufsDir, targetAddr)
		}()

		// Dial 9P server with retry
		var conn net.Conn
		for i := 0; i < 20; i++ {
			conn, err = net.Dial("tcp", targetAddr)
			if err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("failed to dial ufs at %s: %v (server stderr: %s)", targetAddr, err, serverStderr.String())
		}
		defer conn.Close()

		// Attach 9P client
		clt, err := p9.NewClient(conn)
		if err != nil {
			t.Fatalf("p9.NewClient failed: %v", err)
		}

		rootFile, err := clt.Attach("/")
		if err != nil {
			t.Fatalf("clt.Attach failed: %v", err)
		}
		_ = rootFile.Close()

		// Shutdown server cleanly
		cancelServer()
		select {
		case code := <-serverDone:
			if code != 0 {
				t.Errorf("ufs server exited with %d: stderr=%s", code, serverStderr.String())
			}
		case <-time.After(3 * time.Second):
			t.Errorf("ufs server did not stop after context cancel")
		}
	})
}

func TestURoot_Band3_SystemDiagnosticsProcessAndNetwork(t *testing.T) {
	dir, run := setupTestShell(t)

	t.Run("uname", func(t *testing.T) {
		// 1. Default uname (prints sysname)
		out, code, err := run("uname")
		if code != 0 || err != nil {
			t.Fatalf("uname failed: code=%d err=%v out=%s", code, err, out)
		}
		sysname := strings.TrimSpace(out)
		if sysname == "" {
			t.Fatalf("uname returned empty sysname")
		}

		// 2. uname -a (all fields)
		out, code, err = run("uname -a")
		if code != 0 || err != nil {
			t.Fatalf("uname -a failed: code=%d err=%v out=%s", code, err, out)
		}
		fields := strings.Fields(out)
		if len(fields) < 2 {
			t.Fatalf("uname -a expected >= 2 fields, got %v", fields)
		}

		// 3. uname -s (sysname matches default)
		outS, code, err := run("uname -s")
		if code != 0 || err != nil {
			t.Fatalf("uname -s failed: code=%d err=%v out=%s", code, err, outS)
		}
		if strings.TrimSpace(outS) != sysname {
			t.Errorf("uname -s %q != default uname %q", strings.TrimSpace(outS), sysname)
		}

		// 4. uname -m (machine)
		outM, code, err := run("uname -m")
		if code != 0 || err != nil {
			t.Fatalf("uname -m failed: code=%d err=%v out=%s", code, err, outM)
		}
		if strings.TrimSpace(outM) == "" {
			t.Errorf("uname -m returned empty machine")
		}

		// 5. uname -snrvm combined flags
		outCombined, code, err := run("uname -snrvm")
		if code != 0 || err != nil {
			t.Fatalf("uname -snrvm failed: code=%d err=%v out=%s", code, err, outCombined)
		}
		if len(strings.Fields(outCombined)) < 2 {
			t.Errorf("uname -snrvm returned too few fields: %s", outCombined)
		}

		// 6. uname -help
		_, code, _ = run("uname -help")
		if code != 0 {
			t.Errorf("uname -help expected 0, got %d", code)
		}

		// 7. uname with unknown flag
		_, code, _ = run("uname -bogusflagxyz")
		if code != 1 {
			t.Errorf("uname -bogusflagxyz expected 1, got %d", code)
		}
	})

	t.Run("id", func(t *testing.T) {
		// 1. Default id (prints uid=... gid=... groups=...)
		out, code, err := run("id")
		if code != 0 || err != nil {
			t.Fatalf("id failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "uid=") || !strings.Contains(out, "gid=") {
			t.Errorf("id output missing uid or gid: %q", out)
		}

		// 2. id -u (numeric UID)
		outU, code, err := run("id -u")
		if code != 0 || err != nil {
			t.Fatalf("id -u failed: code=%d err=%v out=%s", code, err, outU)
		}
		uidStr := strings.TrimSpace(outU)
		if _, err := strconv.Atoi(uidStr); err != nil {
			t.Errorf("id -u did not return integer UID: %q", uidStr)
		}

		// 3. id -un (username)
		outUN, code, err := run("id -u -n")
		if code != 0 || err != nil {
			t.Fatalf("id -u -n failed: code=%d err=%v out=%s", code, err, outUN)
		}
		if strings.TrimSpace(outUN) == "" {
			t.Errorf("id -u -n returned empty username")
		}

		// 4. id -g (numeric GID)
		outG, code, err := run("id -g")
		if code != 0 || err != nil {
			t.Fatalf("id -g failed: code=%d err=%v out=%s", code, err, outG)
		}
		gidStr := strings.TrimSpace(outG)
		if _, err := strconv.Atoi(gidStr); err != nil {
			t.Errorf("id -g did not return integer GID: %q", gidStr)
		}

		// 5. id -G (groups)
		outGroups, code, err := run("id -G")
		if code != 0 || err != nil {
			t.Fatalf("id -G failed: code=%d err=%v out=%s", code, err, outGroups)
		}
		if strings.TrimSpace(outGroups) == "" {
			t.Errorf("id -G returned empty")
		}

		// 6. Conflicting flags (e.g. -u and -g) -> exit code 1
		_, code, _ = run("id -u -g")
		if code != 1 {
			t.Errorf("id -u -g expected 1, got %d", code)
		}

		// 7. id -help -> exit code 0
		_, code, _ = run("id -help")
		if code != 0 {
			t.Errorf("id -help expected 0, got %d", code)
		}
	})

	t.Run("df", func(t *testing.T) {
		// 1. Default df (in KB)
		out, code, err := run("df")
		if code != 0 || err != nil {
			t.Fatalf("df failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "Filesystem") || !strings.Contains(out, "1K-blocks") {
			t.Errorf("df output missing expected 1K-blocks header: %q", out)
		}

		// 2. df -m (in MB)
		outM, code, err := run("df -m")
		if code != 0 || err != nil {
			t.Fatalf("df -m failed: code=%d err=%v out=%s", code, err, outM)
		}
		if !strings.Contains(outM, "1M-blocks") {
			t.Errorf("df -m missing 1M-blocks header: %q", outM)
		}

		// 3. df with specific directory argument
		outDir, code, err := run(fmt.Sprintf("df %q", os.TempDir()))
		if code != 0 || err != nil {
			t.Fatalf("df tempdir failed: code=%d err=%v out=%s", code, err, outDir)
		}
		if !strings.Contains(outDir, "Filesystem") {
			t.Errorf("df tempdir missing header: %q", outDir)
		}

		// 4. Conflicting flags (-k and -m) -> exit code 1
		_, code, _ = run("df -k -m")
		if code != 1 {
			t.Errorf("df -k -m expected 1, got %d", code)
		}

		// 5. df -help -> exit code 0
		_, code, _ = run("df -help")
		if code != 0 {
			t.Errorf("df -help expected 0, got %d", code)
		}
	})

	t.Run("free", func(t *testing.T) {
		// 1. Default free (in KB)
		out, code, err := run("free")
		if code != 0 || err != nil {
			t.Fatalf("free failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "Mem:") || !strings.Contains(out, "total") {
			t.Errorf("free output missing expected Mem: or total: %q", out)
		}

		// 2. free -m (in MB)
		outM, code, err := run("free -m")
		if code != 0 || err != nil {
			t.Fatalf("free -m failed: code=%d err=%v out=%s", code, err, outM)
		}
		if !strings.Contains(outM, "Mem:") {
			t.Errorf("free -m missing Mem: %q", outM)
		}

		// 3. free -h (human readable)
		outH, code, err := run("free -h")
		if code != 0 || err != nil {
			t.Fatalf("free -h failed: code=%d err=%v out=%s", code, err, outH)
		}
		if !strings.Contains(outH, "Mem:") {
			t.Errorf("free -h missing Mem: %q", outH)
		}

		// 4. free -json (JSON output)
		outJSON, code, err := run("free -json")
		if code != 0 || err != nil {
			t.Fatalf("free -json failed: code=%d err=%v out=%s", code, err, outJSON)
		}
		if !strings.Contains(outJSON, `"mem"`) {
			t.Errorf("free -json missing mem key: %q", outJSON)
		}

		// 5. Conflicting unit flags (-m and -g) -> exit code 1
		_, code, _ = run("free -m -g")
		if code != 1 {
			t.Errorf("free -m -g expected 1, got %d", code)
		}

		// 6. free -help -> exit code 0
		_, code, _ = run("free -help")
		if code != 0 {
			t.Errorf("free -help expected 0, got %d", code)
		}
	})

	t.Run("uptime", func(t *testing.T) {
		// 1. Default uptime
		out, code, err := run("uptime")
		if code != 0 || err != nil {
			t.Fatalf("uptime failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "up") || !strings.Contains(out, "loadaverage:") {
			t.Errorf("uptime output missing expected tokens: %q", out)
		}

		// 2. uptime -help -> exit code 0
		_, code, _ = run("uptime -help")
		if code != 0 {
			t.Errorf("uptime -help expected 0, got %d", code)
		}

		// 3. invalid flag -> exit code 1
		_, code, _ = run("uptime -unknownflagxyz")
		if code != 1 {
			t.Errorf("uptime -unknownflagxyz expected 1, got %d", code)
		}
	})

	t.Run("ps", func(t *testing.T) {
		// 1. Default ps (prints PID, TTY, TIME, CMD)
		out, code, err := run("ps")
		if code != 0 || err != nil {
			t.Fatalf("ps failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "PID") || !strings.Contains(out, "CMD") {
			t.Errorf("ps missing PID or CMD header: %q", out)
		}

		// 2. ps aux (prints PID, PGRP, SID, TTY, STAT, TIME, COMMAND)
		outAux, code, err := run("ps aux")
		if code != 0 || err != nil {
			t.Fatalf("ps aux failed: code=%d err=%v out=%s", code, err, outAux)
		}
		if !strings.Contains(outAux, "COMMAND") || !strings.Contains(outAux, "STAT") {
			t.Errorf("ps aux missing COMMAND or STAT: %q", outAux)
		}

		// 3. ps -A (select all)
		outA, code, err := run("ps -A")
		if code != 0 || err != nil {
			t.Fatalf("ps -A failed: code=%d err=%v out=%s", code, err, outA)
		}
		if !strings.Contains(outA, "PID") {
			t.Errorf("ps -A missing PID: %q", outA)
		}

		// 4. ps -help -> exit code 0
		_, code, _ = run("ps -help")
		if code != 0 {
			t.Errorf("ps -help expected 0, got %d", code)
		}
	})

	t.Run("pidof", func(t *testing.T) {
		// 1. pidof on current process (e.g. "go" or "shell.test" or "cmd")
		execName := filepath.Base(os.Args[0])
		out, code, err := run(fmt.Sprintf("pidof %s", execName))
		if code != 0 {
			// On Windows, also try without .exe extension
			if strings.HasSuffix(execName, ".exe") {
				out, code, err = run(fmt.Sprintf("pidof %s", strings.TrimSuffix(execName, ".exe")))
			}
		}
		if code != 0 || err != nil {
			t.Fatalf("pidof %s failed: code=%d err=%v out=%s", execName, code, err, out)
		}
		if len(strings.Fields(out)) == 0 {
			t.Errorf("pidof %s returned empty output", execName)
		}

		// 2. pidof -s (single PID)
		outSingle, code, err := run(fmt.Sprintf("pidof -s %s", execName))
		if code != 0 {
			if strings.HasSuffix(execName, ".exe") {
				outSingle, code, err = run(fmt.Sprintf("pidof -s %s", strings.TrimSuffix(execName, ".exe")))
			}
		}
		if code != 0 || err != nil {
			t.Fatalf("pidof -s failed: code=%d err=%v out=%s", code, err, outSingle)
		}
		if len(strings.Fields(outSingle)) != 1 {
			t.Errorf("pidof -s expected exactly 1 PID, got %v", strings.Fields(outSingle))
		}

		// 3. pidof nonexistent -> exit code 1
		_, code, _ = run("pidof nonexistentprogram12345")
		if code != 1 {
			t.Errorf("pidof nonexistent expected 1, got %d", code)
		}

		// 4. pidof -help -> exit code 0
		_, code, _ = run("pidof -help")
		if code != 0 {
			t.Errorf("pidof -help expected 0, got %d", code)
		}
	})

	t.Run("kill", func(t *testing.T) {
		// 1. kill -l (list signals)
		out, code, err := run("kill -l")
		if code != 0 || err != nil {
			t.Fatalf("kill -l failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "SIGTERM") || !strings.Contains(out, "SIGKILL") {
			t.Errorf("kill -l output missing SIGTERM/SIGKILL: %q", out)
		}

		// 2. kill with invalid signal -> exit code 1
		_, code, _ = run("kill --signal invalidSig999 12345")
		if code != 1 {
			t.Errorf("kill invalid signal expected 1, got %d", code)
		}

		// 3. kill without args -> exit code 1
		_, code, _ = run("kill")
		if code != 1 {
			t.Errorf("kill no args expected 1, got %d", code)
		}

		// 4. kill invalid pid -> exit code 1
		_, code, _ = run("kill -9 notapid")
		if code != 1 {
			t.Errorf("kill invalid pid expected 1, got %d", code)
		}
	})

	t.Run("sync", func(t *testing.T) {
		// 1. sync without args -> exit code 0
		out, code, err := run("sync")
		if code != 0 || err != nil {
			t.Fatalf("sync failed: code=%d err=%v out=%s", code, err, out)
		}

		// 2. sync with valid file -> exit code 0
		testFile := filepath.Join(dir, "sync_target.txt")
		_ = os.WriteFile(testFile, []byte("sync data"), 0o644)
		out, code, err = run("sync sync_target.txt")
		if code != 0 || err != nil {
			t.Fatalf("sync sync_target.txt failed: code=%d err=%v out=%s", code, err, out)
		}

		// 3. sync -d with valid file -> exit code 0
		out, code, err = run("sync -d sync_target.txt")
		if code != 0 || err != nil {
			t.Fatalf("sync -d failed: code=%d err=%v out=%s", code, err, out)
		}

		// 4. sync nonexistent file -> exit code 1
		_, code, _ = run("sync nonexistent_file_xyz_123")
		if code != 1 {
			t.Errorf("sync nonexistent expected 1, got %d", code)
		}
	})

	t.Run("tty", func(t *testing.T) {
		// 1. tty in pipeline or subshell -> exit code 0 or 1, prints output or device
		out, code, err := run("tty")
		if err != nil && code != 1 {
			t.Fatalf("tty failed: code=%d err=%v out=%s", code, err, out)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("tty returned empty output: %q", out)
		}

		// 2. tty -s -> silent mode
		out, code, err = run("tty -s")
		if err != nil && code != 1 {
			t.Fatalf("tty -s failed: code=%d err=%v out=%s", code, err, out)
		}
		if strings.TrimSpace(out) != "" {
			t.Errorf("tty -s expected empty output, got: %q", out)
		}

		// 3. tty -help -> exit code 0
		_, code, _ = run("tty -help")
		if code != 0 {
			t.Errorf("tty -help expected 0, got %d", code)
		}
	})

	t.Run("stty", func(t *testing.T) {
		// 1. Default stty -> exit code 0, outputs state
		out, code, err := run("stty")
		if code != 0 || err != nil {
			t.Fatalf("stty failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "rows:") || !strings.Contains(out, "cols:") {
			t.Errorf("stty output missing rows/cols: %q", out)
		}

		// 2. stty dump -> valid JSON
		outDump, code, err := run("stty dump")
		if code != 0 || err != nil {
			t.Fatalf("stty dump failed: code=%d err=%v out=%s", code, err, outDump)
		}
		if !strings.Contains(outDump, "{") {
			t.Errorf("stty dump expected JSON output: %q", outDump)
		}

		// 3. stty size -> exit code 0
		outSize, code, err := run("stty size")
		if code != 0 || err != nil {
			t.Fatalf("stty size failed: code=%d err=%v out=%s", code, err, outSize)
		}
		if len(strings.Fields(outSize)) == 0 {
			t.Errorf("stty size returned empty output: %q", outSize)
		}
	})

	t.Run("ping", func(t *testing.T) {
		// 1. ping -help -> exit code 0
		_, code, _ := run("ping -help")
		if code != 0 {
			t.Errorf("ping -help expected 0, got %d", code)
		}

		// 2. ping -V -> exit code 0
		out, code, err := run("ping -V")
		if code != 0 || err != nil {
			t.Fatalf("ping -V failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "u-root ping") {
			t.Errorf("ping -V expected 'u-root ping', got: %q", out)
		}

		// 3. ping without args -> exit code 1
		_, code, _ = run("ping")
		if code != 1 {
			t.Errorf("ping no args expected 1, got %d", code)
		}

		// 4. ping -c 1 127.0.0.1 -> exit code 0, prints response
		out, code, err = run("ping -c 1 127.0.0.1")
		if code != 0 || err != nil {
			t.Logf("ping -c 1 127.0.0.1 (code=%d, err=%v, out=%s)", code, err, out)
		} else {
			if !strings.Contains(out, "bytes from 127.0.0.1") {
				t.Errorf("ping expected bytes from 127.0.0.1, got: %q", out)
			}
		}
	})

	t.Run("ip", func(t *testing.T) {
		// 1. ip -help -> exit code 0
		_, code, _ := run("ip -help")
		if code != 0 {
			t.Errorf("ip -help expected 0, got %d", code)
		}

		// 2. ip link -> exit code 0
		out, code, err := run("ip link")
		if code != 0 || err != nil {
			t.Fatalf("ip link failed: code=%d err=%v out=%s", code, err, out)
		}
		if len(strings.TrimSpace(out)) == 0 {
			t.Errorf("ip link returned empty output")
		}

		// 3. ip addr / ip a -> exit code 0
		outAddr, code, err := run("ip a")
		if code != 0 || err != nil {
			t.Fatalf("ip a failed: code=%d err=%v out=%s", code, err, outAddr)
		}
		if !strings.Contains(outAddr, "inet") && !strings.Contains(outAddr, "mtu") {
			t.Errorf("ip a expected interface details, got: %q", outAddr)
		}

		// 4. ip route -> exit code 0
		_, code, err = run("ip route")
		if code != 0 || err != nil {
			t.Fatalf("ip route failed: code=%d err=%v", code, err)
		}

		// 5. ip unknown_cmd -> exit code 1
		_, code, _ = run("ip unknown_command_xyz")
		if code != 1 {
			t.Errorf("ip unknown command expected 1, got %d", code)
		}
	})

	t.Run("netstat", func(t *testing.T) {
		// 1. netstat -h -> exit code 0
		_, code, _ := run("netstat -h")
		if code != 0 {
			t.Errorf("netstat -h expected 0, got %d", code)
		}

		// 2. netstat -i -> exit code 0
		out, code, err := run("netstat -i")
		if code != 0 || err != nil {
			t.Fatalf("netstat -i failed: code=%d err=%v out=%s", code, err, out)
		}
		if !strings.Contains(out, "Iface") {
			t.Errorf("netstat -i expected Iface header, got: %q", out)
		}

		// 3. netstat -r -> exit code 0
		outRoute, code, err := run("netstat -r")
		if code != 0 || err != nil {
			t.Fatalf("netstat -r failed: code=%d err=%v out=%s", code, err, outRoute)
		}
		if !strings.Contains(outRoute, "Destination") {
			t.Errorf("netstat -r expected Destination header, got: %q", outRoute)
		}

		// 4. netstat -a -> exit code 0
		outSockets, code, err := run("netstat -a")
		if code != 0 || err != nil {
			t.Fatalf("netstat -a failed: code=%d err=%v out=%s", code, err, outSockets)
		}
		if !strings.Contains(outSockets, "Active Internet connections") && !strings.Contains(outSockets, "Proto") {
			t.Errorf("netstat -a expected socket table header, got: %q", outSockets)
		}
	})

	t.Run("pci", func(t *testing.T) {
		// 1. pci -h -> exit code 0
		_, code, _ := run("pci -h")
		if code != 0 {
			t.Errorf("pci -h expected 0, got %d", code)
		}

		// 2. pci -> exit code 0
		out, code, err := run("pci")
		if code != 0 || err != nil {
			t.Fatalf("pci failed: code=%d err=%v out=%s", code, err, out)
		}
		if len(strings.TrimSpace(out)) == 0 {
			t.Errorf("pci returned empty output")
		}

		// 3. pci -n -> exit code 0
		outNum, code, err := run("pci -n")
		if code != 0 || err != nil {
			t.Fatalf("pci -n failed: code=%d err=%v out=%s", code, err, outNum)
		}
		if len(strings.TrimSpace(outNum)) == 0 {
			t.Errorf("pci -n returned empty output")
		}
	})

	t.Run("lsmod", func(t *testing.T) {
		// 1. lsmod -help -> exit code 0
		_, code, _ := run("lsmod -help")
		if code != 0 {
			t.Errorf("lsmod -help expected 0, got %d", code)
		}

		// 2. lsmod -> runs safely without panic
		out, code, err := run("lsmod")
		if code == 0 {
			if !strings.Contains(out, "Module") {
				t.Errorf("lsmod expected Module header, got: %q", out)
			}
		} else {
			// On Windows without /proc/modules, clean error exit is expected
			t.Logf("lsmod exited with code %d (err=%v, out=%s)", code, err, out)
		}
	})

	t.Run("lsdrivers", func(t *testing.T) {
		// 1. lsdrivers -help -> exit code 0
		_, code, _ := run("lsdrivers -help")
		if code != 0 {
			t.Errorf("lsdrivers -help expected 0, got %d", code)
		}

		// 2. lsdrivers -> runs safely without panic
		out, code, err := run("lsdrivers")
		if code != 0 {
			// On non-Linux without /sys/bus, clean error return is expected
			t.Logf("lsdrivers exited with code %d (err=%v, out=%s)", code, err, out)
		}
	})

	t.Run("lsfd", func(t *testing.T) {
		// 1. lsfd -help -> exit code 0
		_, code, _ := run("lsfd -help")
		if code != 0 {
			t.Errorf("lsfd -help expected 0, got %d", code)
		}

		// 2. lsfd -> runs safely without panic
		out, code, err := run("lsfd")
		if code != 0 {
			// On non-Linux without /proc, clean error return is expected
			t.Logf("lsfd exited with code %d (err=%v, out=%s)", code, err, out)
		}
	})

	t.Run("lddfiles", func(t *testing.T) {
		// 1. lddfiles -help -> exit code 0
		_, code, _ := run("lddfiles -help")
		if code != 0 {
			t.Errorf("lddfiles -help expected 0, got %d", code)
		}

		// 2. lddfiles on nonexistent file -> returns non-zero error without panic
		out, code, _ := run("lddfiles definitely_nonexistent_binary_xyz")
		if code == 0 {
			t.Errorf("lddfiles on nonexistent binary expected non-zero code, got %d (out=%s)", code, out)
		}
	})

	t.Run("dmesg", func(t *testing.T) {
		// 1. dmesg -help -> exit code 0
		_, code, _ := run("dmesg -help")
		if code != 0 {
			t.Errorf("dmesg -help expected 0, got %d", code)
		}

		// 2. dmesg -> runs safely without panic
		out, code, err := run("dmesg")
		if code != 0 {
			// On non-Linux systems without kernel ring buffer access, clean error exit is expected
			t.Logf("dmesg exited with code %d (err=%v, out=%s)", code, err, out)
		}
	})
}
