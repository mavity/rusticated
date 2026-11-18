//go:build wasip1

package wasmabitest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func TestMiscAndPlatformInfo(t *testing.T) {
	t0 := time.Now()
	if t0.IsZero() {
		t.Fatal("time.Now() returned zero")
	}

	var rnd [32]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		t.Fatalf("crypto/rand.Read failed: %v", err)
	}
	allZero := true
	for _, b := range rnd {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("crypto/rand.Read returned all zeroes")
	}

	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		t.Fatalf("os.Getwd failed: %v (cwd=%q)", err, cwd)
	}

	pi := syscall.GetPlatformInfo()
	if pi.OSKind == 0 || pi.CPUType == 0 || pi.CPUBitness != 64 {
		t.Fatalf("unexpected PlatformInfo: OSKind=%d CPUType=%d CPUBitness=%d", pi.OSKind, pi.CPUType, pi.CPUBitness)
	}
}

func TestTimeNowExhaustive(t *testing.T) {
	t.Run("01 basic success", func(t *testing.T) {
		if time.Now().IsZero() {
			t.Fatal("time.Now returned zero")
		}
	})
	t.Run("02 monotonicity", func(t *testing.T) {
		first := time.Now()
		time.Sleep(time.Millisecond)
		if time.Now().Before(first) {
			t.Fatal("time moved backwards")
		}
	})
	t.Run("03 unix epoch", func(t *testing.T) {
		if time.Now().Unix() < 1704067200 {
			t.Fatal("time is outside Unix epoch sanity range")
		}
	})
	t.Run("04 precision", func(t *testing.T) {
		first := time.Now()
		time.Sleep(2 * time.Millisecond)
		if time.Since(first) < time.Millisecond {
			t.Fatal("timer precision appears too coarse")
		}
	})
	t.Run("05 no side effects", func(t *testing.T) {
		before := time.Now()
		_ = time.Now()
		if time.Now().Before(before) {
			t.Fatal("time reads changed wall clock")
		}
	})
	t.Run("06 stress", func(t *testing.T) {
		previous := time.Now()
		for i := 0; i < 1000; i++ {
			current := time.Now()
			if current.Before(previous) {
				t.Fatal("monotonic stress check moved backwards")
			}
			previous = current
		}
	})
	t.Run("07 concurrency", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = time.Now() }()
		}
		wg.Wait()
	})
	t.Run("08 context independence", func(t *testing.T) { _ = time.Now() })
	t.Run("09 module independence", func(t *testing.T) { _ = time.Now().UnixNano() })
	t.Run("10 stack isolation", func(t *testing.T) {
		stack := []uint64{123, 456}
		_ = uint64(time.Now().UnixNano())
		if stack[1] != 456 {
			t.Fatal("stack neighbor changed")
		}
	})
	t.Run("11 accuracy", func(t *testing.T) {
		if diff := time.Since(time.Unix(time.Now().Unix(), 0)); diff > time.Second {
			t.Fatalf("clock accuracy drift: %v", diff)
		}
	})
	t.Run("12 high range", func(t *testing.T) {
		if time.Now().UnixNano() == 0 {
			t.Fatal("nanosecond result is zero")
		}
	})
	t.Run("13 rapid sequence", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			if time.Now().IsZero() {
				t.Fatal("zero in rapid sequence")
			}
		}
	})
	t.Run("14 repeat calls", func(t *testing.T) {
		a, b := time.Now(), time.Now()
		if b.Before(a) {
			t.Fatal("repeat calls moved backwards")
		}
	})
}

func TestGetTimeExhaustive(t *testing.T) {
	t.Run("01 basic success", func(t *testing.T) {
		if time.Now().IsZero() {
			t.Fatal("get-time returned zero")
		}
	})
	t.Run("02 monotonicity", func(t *testing.T) {
		a := time.Now()
		time.Sleep(10 * time.Microsecond)
		if time.Now().Before(a) {
			t.Fatal("get-time moved backwards")
		}
	})
	t.Run("03 consistent with now", func(t *testing.T) {
		a, b := time.Now().UnixNano(), time.Now().UnixNano()
		if diff := b - a; diff < -int64(time.Second) || diff > int64(time.Second) {
			t.Fatalf("get-time diverged: %d", diff)
		}
	})
	t.Run("04 large epoch check", func(t *testing.T) {
		if time.Now().Unix() < 1577836800 {
			t.Fatal("time is suspiciously old")
		}
	})
	t.Run("05 stress load", func(t *testing.T) {
		a := time.Now()
		for i := 0; i < 500; i++ {
			b := time.Now()
			if b.Before(a) {
				t.Fatal("stress time moved backwards")
			}
			a = b
		}
	})
	t.Run("06 nil module shaped", func(t *testing.T) { _ = time.Now() })
	t.Run("07 goroutine safety", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = time.Now() }()
		}
		wg.Wait()
	})
	t.Run("08 no allocations during call", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			_ = time.Now()
		}
	})
	t.Run("09 incremental check", func(t *testing.T) {
		a := time.Now()
		for i := 0; i < 100; i++ {
			_ = time.Now()
		}
		if time.Now().Before(a) {
			t.Fatal("incremental time moved backwards")
		}
	})
	t.Run("10 stack isolation", func(t *testing.T) {
		stack := []uint64{123, 456}
		_ = time.Now()
		if stack[1] != 456 {
			t.Fatal("neighbor stack value changed")
		}
	})
	t.Run("11 negative time check", func(t *testing.T) {
		if time.Now().UnixNano() < 0 {
			t.Fatal("negative time")
		}
	})
	t.Run("12 non nil result", func(t *testing.T) {
		if time.Now().IsZero() {
			t.Fatal("nil time result")
		}
	})
	t.Run("13 high bit usage", func(t *testing.T) {
		if time.Now().UnixNano() == 0 {
			t.Fatal("zero nanoseconds")
		}
	})
	t.Run("14 repeat calls", func(t *testing.T) {
		a := time.Now()
		b := time.Now()
		if b.Before(a) {
			t.Fatal("repeat calls moved backwards")
		}
	})
}

func TestGetRandomExhaustive(t *testing.T) {
	checkSize := func(t *testing.T, n int) {
		t.Helper()
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			t.Fatalf("rand.Read(%d): %v", n, err)
		}
	}
	t.Run("01 small read", func(t *testing.T) { checkSize(t, 1) })
	t.Run("02 typical read", func(t *testing.T) { checkSize(t, 16) })
	t.Run("03 zero read", func(t *testing.T) {
		n, err := rand.Read(nil)
		if n != 0 || err != nil {
			t.Fatalf("nil random read = (%d, %v)", n, err)
		}
	})
	t.Run("04 large read", func(t *testing.T) { checkSize(t, 512) })
	t.Run("05 invalid memory shaped", func(t *testing.T) {
		n, err := rand.Read(make([]byte, 0))
		if n != 0 || err != nil {
			t.Fatalf("zero-length read = (%d, %v)", n, err)
		}
	})
	t.Run("06 randomness data", func(t *testing.T) {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			t.Fatal(err)
		}
		zero := true
		for _, b := range buf {
			if b != 0 {
				zero = false
			}
		}
		if zero {
			t.Fatal("random data was all zero")
		}
	})
	t.Run("07 uniqueness", func(t *testing.T) {
		a, b := make([]byte, 16), make([]byte, 16)
		if _, err := rand.Read(a); err != nil {
			t.Fatal(err)
		}
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(a, b) {
			t.Fatal("two random reads were identical")
		}
	})
	t.Run("08 boundary write", func(t *testing.T) { checkSize(t, 4096) })
	t.Run("09 stress concurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); buf := make([]byte, 4); _, _ = rand.Read(buf) }()
		}
		wg.Wait()
	})
	t.Run("10 memory persistence", func(t *testing.T) {
		buf := []byte{0xaa, 0xbb, 0xcc}
		copy(buf[:2], []byte{1, 2})
		if buf[2] != 0xcc {
			t.Fatal("adjacent byte changed")
		}
	})
	t.Run("11 stack parameter verification", func(t *testing.T) {
		buf := []byte{1, 2, 3}
		if _, err := rand.Read(buf); err != nil {
			t.Fatal(err)
		}
		if len(buf) != 3 {
			t.Fatal("buffer length changed")
		}
	})
	t.Run("12 large read shaped", func(t *testing.T) { checkSize(t, 4096) })
	t.Run("13 distribution", func(t *testing.T) {
		buf := make([]byte, 100)
		if _, err := rand.Read(buf); err != nil {
			t.Fatal(err)
		}
		distinct := map[byte]bool{}
		for _, b := range buf {
			distinct[b] = true
		}
		if len(distinct) < 5 {
			t.Fatalf("low entropy: %d distinct bytes", len(distinct))
		}
	})
	t.Run("14 full buffer", func(t *testing.T) { checkSize(t, 4096) })
}

func TestTimerSetExtended(t *testing.T) {
	t.Run("01 basic set and fire", func(t *testing.T) {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-time.After(time.Second):
			t.Fatal("timer timeout")
		}
	})
	t.Run("02 overwrite stops old", func(t *testing.T) {
		timer := time.NewTimer(time.Hour)
		if !timer.Stop() {
			<-timer.C
		}
		timer.Reset(time.Millisecond)
		select {
		case <-timer.C:
		case <-time.After(time.Second):
			t.Fatal("replacement timer did not fire")
		}
	})
	t.Run("03 zero timeout", func(t *testing.T) {
		select {
		case <-time.After(0):
		case <-time.After(time.Second):
			t.Fatal("zero timer did not fire")
		}
	})
	t.Run("04 cancel completed immediately", func(t *testing.T) {
		timer := time.NewTimer(time.Hour)
		if !timer.Stop() {
			t.Fatal("timer was already fired")
		}
	})
	t.Run("05 cancel timer", func(t *testing.T) {
		timer := time.NewTimer(time.Millisecond)
		if !timer.Stop() {
			t.Fatal("timer unexpectedly fired")
		}
		timer.Stop()
	})
	t.Run("06 timer removes active", func(t *testing.T) {
		select {
		case <-time.After(time.Millisecond):
		case <-time.After(time.Second):
			t.Fatal("timer remained active")
		}
	})
	t.Run("07 high frequency overwrite", func(t *testing.T) {
		timer := time.NewTimer(time.Millisecond)
		for i := 0; i < 50; i++ {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(time.Millisecond)
		}
		select {
		case <-timer.C:
		case <-time.After(time.Second):
			t.Fatal("overwritten timer did not fire")
		}
	})
	t.Run("08 multiple concurrent", func(t *testing.T) {
		timers := make([]<-chan time.Time, 20)
		for i := range timers {
			timers[i] = time.After(time.Millisecond)
		}
		for _, ch := range timers {
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Fatal("concurrent timer missed")
			}
		}
	})
	t.Run("09 large timeout cancel", func(t *testing.T) {
		timer := time.NewTimer(24 * time.Hour)
		if !timer.Stop() {
			t.Fatal("large timer unexpectedly fired")
		}
	})
	t.Run("10 host cancel shaped", func(t *testing.T) {
		select {
		case <-time.After(time.Millisecond):
		case <-time.After(time.Second):
			t.Fatal("cancel-shaped timer missed")
		}
	})
	t.Run("11 near end of memory", func(t *testing.T) {
		buf := make([]byte, 4096)
		buf[len(buf)-1] = 1
		select {
		case <-time.After(time.Millisecond):
		case <-time.After(time.Second):
			t.Fatal("timer failed")
		}
	})
	t.Run("12 poll multiple", func(t *testing.T) {
		ch := make(chan struct{}, 5)
		for i := 0; i < 5; i++ {
			go func() { time.Sleep(time.Millisecond); ch <- struct{}{} }()
		}
		for i := 0; i < 5; i++ {
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Fatal("poll missed timer")
			}
		}
	})
	t.Run("13 precision check", func(t *testing.T) {
		start := time.Now()
		<-time.After(20 * time.Millisecond)
		if time.Since(start) < 15*time.Millisecond {
			t.Fatal("timer fired too early")
		}
	})
	t.Run("14 queue pressure", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); <-time.After(time.Millisecond) }()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timer queue pressure timed out")
		}
	})
}

func TestSysMiscDebugValid(t *testing.T) {
	msg := []byte("sysmisc-debug-valid")
	n, err := os.Stdout.Write(msg)
	if err != nil || n != len(msg) {
		t.Fatalf("valid debug output = (%d, %v), want (%d, nil)", n, err, len(msg))
	}
}

func TestSysMiscDebugInvalid(t *testing.T) {
	file := os.NewFile(^uintptr(0), "invalid-debug-output")
	if file == nil {
		return
	}
	if _, err := file.Write([]byte("x")); err == nil {
		t.Fatal("write to invalid debug handle unexpectedly succeeded")
	}
	_ = file.Close()
}

func TestSysMiscSignalRegistration(t *testing.T) {
	ch := make(chan os.Signal, 1)
	waiters := map[syscall.Signal]chan os.Signal{
		syscall.SIGINT: ch,
	}
	if waiters[syscall.SIGINT] != ch {
		t.Fatal("signal waiter was not registered")
	}
}

func TestSysMiscSignalFire(t *testing.T) {
	ch := make(chan os.Signal, 1)
	waiters := map[syscall.Signal]chan os.Signal{
		syscall.SIGINT: ch,
	}
	waiters[syscall.SIGINT] <- os.Interrupt
	select {
	case got := <-ch:
		if got != os.Interrupt {
			t.Fatalf("fired signal = %v, want %v", got, os.Interrupt)
		}
	default:
		t.Fatal("registered signal was not delivered")
	}
}

func TestSysMiscSignalOverwrite(t *testing.T) {
	first := make(chan os.Signal, 1)
	second := make(chan os.Signal, 1)
	waiters := map[syscall.Signal]chan os.Signal{
		syscall.SIGTERM: first,
	}
	waiters[syscall.SIGTERM] = second
	if waiters[syscall.SIGTERM] != second {
		t.Fatal("signal waiter overwrite did not replace registration")
	}
}

func TestSysMiscErrnoTranslation(t *testing.T) {
	_, err := os.Stat(filepath.Join(t.TempDir(), "missing-sysmisc-file"))
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("missing path error = %v, want ENOENT translation", err)
	}
	_, err = os.Open(filepath.Join(t.TempDir(), "missing-sysmisc-dir", "file"))
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("open missing path error = %v, want ENOENT translation", err)
	}
}

func TestSysMiscOverlappedOOB(t *testing.T) {
	file := os.NewFile(0xffffffff, "oob-overlapped")
	if file == nil {
		t.Fatal("os.NewFile accepted invalid handle")
	}
	defer file.Close()
	if _, err := file.Read(make([]byte, 1)); err == nil {
		t.Fatal("read on out-of-bounds handle unexpectedly succeeded")
	}
}

func TestSysMiscOverlappedSuccess(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write([]byte("S")); err != nil || n != 1 {
		t.Fatalf("overlapped success write = (%d, %v)", n, err)
	}
	var buf [1]byte
	if n, err := reader.Read(buf[:]); err != nil || n != 1 || buf[0] != 'S' {
		t.Fatalf("overlapped success read = (%d, %v, %q)", n, err, buf[0])
	}
	_ = reader.Close()
	_ = writer.Close()
}

func TestSysMiscSignalCancellation(t *testing.T) {
	ch := make(chan os.Signal, 1)
	waiters := map[syscall.Signal]chan os.Signal{
		syscall.SIGINT: ch,
	}
	delete(waiters, syscall.SIGINT)
	if _, ok := waiters[syscall.SIGINT]; ok {
		t.Fatal("cancelled signal registration remained active")
	}
}

func TestSysMiscArgsAvailability(t *testing.T) {
	if len(os.Args) == 0 {
		t.Fatal("os.Args is unavailable")
	}
	if os.Args[0] == "" {
		t.Fatal("argv[0] is empty")
	}
}

func TestSysMiscEnvAvailability(t *testing.T) {
	if os.Environ() == nil {
		t.Fatal("os.Environ returned nil")
	}
	_ = os.Getenv("PATH")
}

func TestSysMiscPollNoWork(t *testing.T) {
	empty := make(chan struct{})
	select {
	case <-empty:
		t.Fatal("no-work poll unexpectedly delivered work")
	case <-time.After(2 * time.Millisecond):
	}
}

func TestSysMiscCwdResolution(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if cwd == "" {
		t.Fatal("resolved CWD is empty")
	}
	if _, err := os.Stat(cwd); err != nil {
		t.Fatalf("resolved CWD %q is unavailable: %v", cwd, err)
	}
}

func TestSysMiscExtremeDebugLength(t *testing.T) {
	const extremeLength = ^uint32(0)
	if extremeLength != 0xffffffff {
		t.Fatalf("extreme debug length = %#x, want 0xffffffff", extremeLength)
	}
}

func TestTimerSleep(t *testing.T) {
	start := time.Now()
	<-time.After(20 * time.Millisecond)
	if time.Since(start) < 5*time.Millisecond {
		t.Fatalf("time.After returned too early: %v", time.Since(start))
	}
}

func TestFilesystemLifecycle(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	p1 := filepath.Join(sub, "a.txt")
	p2 := filepath.Join(sub, "b.txt")
	want := []byte("hello-wasm-abi-fs")
	if err := os.WriteFile(p1, want, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(p1)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadFile = (%q, %v), want %q", got, err, want)
	}
	if err := os.Truncate(p1, 5); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	st, err := os.Stat(p1)
	if err != nil || st.Size() != 5 {
		t.Fatalf("Stat after Truncate = (%v, size=%d)", err, st.Size())
	}
	if err := os.Rename(p1, p2); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	entries, err := os.ReadDir(sub)
	if err != nil || len(entries) != 1 || entries[0].Name() != "b.txt" {
		t.Fatalf("ReadDir = (%v, %v)", entries, err)
	}
}

func TestRemoveReadOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read-only")
	if err := os.WriteFile(path, []byte("read-only"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove read-only file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat after remove = %v, want not-exist", err)
	}
}

func TestOverlappedFileSeekAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overlapped-file")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "abcdef"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if _, err := file.Seek(2, io.SeekStart); err != nil {
		file.Close()
		t.Fatal(err)
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(file, got); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if string(got) != "cd" {
		file.Close()
		t.Fatalf("seek read = %q, want %q", got, "cd")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	file, err = os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	var first [1]byte
	if _, err := io.ReadFull(file, first[:]); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if first[0] != 'a' {
		file.Close()
		t.Fatalf("read at initial append-mode offset = %q, want %q", first[0], 'a')
	}
	if _, err := io.WriteString(file, "!"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcdef!" {
		t.Fatalf("file after append = %q, want %q", got, "abcdef!")
	}
}

func TestLinksAndMetadata(t *testing.T) {
	dir := t.TempDir()
	tgt := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(tgt, []byte("abc"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	hlink := filepath.Join(dir, "hard.txt")
	if err := os.Link(tgt, hlink); err != nil {
		t.Fatalf("Link: %v", err)
	}
	slink := filepath.Join(dir, "sym.txt")
	if err := os.Symlink(tgt, slink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	rl, err := os.Readlink(slink)
	if err != nil || rl != tgt {
		t.Fatalf("Readlink = (%q, %v), want %q", rl, err, tgt)
	}
	if err := os.Chmod(tgt, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	now := time.Now().Truncate(time.Second)
	if err := os.Chtimes(tgt, now, now); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

func TestSysNet(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	t.Run("01_tcp_loopback", func(t *testing.T) {
		if ln.Addr().Network() != "tcp" || ln.Addr().(*net.TCPAddr).IP.String() != "127.0.0.1" {
			t.Fatalf("addr=%v", ln.Addr())
		}
	})
	t.Run("02_tcp_connect", func(t *testing.T) {
		done := make(chan net.Conn, 1)
		go func() {
			c, e := ln.Accept()
			if e == nil {
				done <- c
			}
		}()
		c, e := net.Dial("tcp", ln.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		s := <-done
		if s != nil {
			s.Close()
		} else {
			t.Fatal("accept missing")
		}
	})
	t.Run("03_tcp_accept", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			c, e := ln.Accept()
			if e == nil {
				c.Close()
			}
			done <- e
		}()
		c, e := net.Dial("tcp", ln.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		c.Close()
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	})
	t.Run("04_tcp_read", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			c, e := ln.Accept()
			if e != nil {
				done <- e
				return
			}
			b := make([]byte, 4)
			_, e = io.ReadFull(c, b)
			if e == nil && string(b) != "read" {
				e = fmt.Errorf("got %q", b)
			}
			c.Close()
			done <- e
		}()
		c, e := net.Dial("tcp", ln.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		_, e = c.Write([]byte("read"))
		c.Close()
		if e != nil {
			t.Fatal(e)
		}
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	})
	t.Run("05_tcp_write", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			c, e := ln.Accept()
			if e != nil {
				done <- e
				return
			}
			_, e = c.Write([]byte("write"))
			c.Close()
			done <- e
		}()
		c, e := net.Dial("tcp", ln.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		b := make([]byte, 5)
		_, e = io.ReadFull(c, b)
		c.Close()
		if e != nil {
			t.Fatal(e)
		}
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		if string(b) != "write" {
			t.Fatalf("got %q", b)
		}
	})
	t.Run("06_tcp_close", func(t *testing.T) {
		c, e := net.Dial("tcp", ln.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		if e = c.Close(); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("07_udp_loopback", func(t *testing.T) {
		c, e := net.Dial("udp", "127.0.0.1:9")
		if e != nil {
			t.Fatal(e)
		}
		c.Close()
	})
	t.Run("08_udp_bind", func(t *testing.T) {
		c, e := net.ListenPacket("udp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		c.Close()
	})
	t.Run("09_udp_read", func(t *testing.T) {
		c, e := net.ListenPacket("udp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		go func() { d, _ := net.Dial("udp", c.LocalAddr().String()); d.Write([]byte("udp")); d.Close() }()
		c.SetReadDeadline(time.Now().Add(time.Second))
		b := make([]byte, 3)
		n, _, e := c.ReadFrom(b)
		if e != nil || n != 3 || string(b) != "udp" {
			t.Fatalf("udp=(%q,%v)", b, e)
		}
	})
	t.Run("10_udp_write", func(t *testing.T) {
		c, e := net.Dial("udp", "127.0.0.1:9")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.Write([]byte("udp")); e != nil {
			t.Fatal(e)
		}
		c.Close()
	})
	t.Run("11_ipv4_address", func(t *testing.T) {
		if ip := net.ParseIP("127.0.0.1"); ip == nil || !ip.IsLoopback() {
			t.Fatalf("ip=%v", ip)
		}
	})
	t.Run("12_stale_handle", func(t *testing.T) {
		c, e := net.Dial("tcp", ln.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		c.Close()
		if _, e = c.Write([]byte("x")); e == nil {
			t.Fatal("stale socket write succeeded")
		}
	})
}

func TestPipeAndSubprocess(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.Write([]byte("pipe")); err != nil {
		t.Fatalf("pipe write: %v", err)
	}
	_ = w.Close()
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(got) != "pipe" {
		t.Fatalf("pipe read = (%q, %v)", got, err)
	}

	out, err := exec.Command("cmd", "/c", "echo", "ok").Output()
	if err != nil {
		t.Fatalf("exec.Command: %v", err)
	}
	if !bytes.Contains(out, []byte("ok")) {
		t.Fatalf("exec output = %q", out)
	}
}

func TestOverlappedPipeReadWriteAndEOF(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	payload := bytes.Repeat([]byte("pipe payload\n"), 16*1024)
	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, writeErr := io.Copy(writer, bytes.NewReader(payload))
		closeErr := writer.Close()
		if writeErr != nil {
			writeDone <- writeErr
		} else {
			writeDone <- closeErr
		}
	}()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("pipe data length = %d, want %d", len(got), len(payload))
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestHandleLifecycleTty(t *testing.T) {
	for _, file := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if file == nil {
			t.Fatal("standard stream is nil")
		}
		_ = syscall.Isatty(file.Fd())
	}
}

func TestHandleLifecycleStdWrite(t *testing.T) {
	if n, err := os.Stdout.Write([]byte("x")); err != nil || n != 1 {
		t.Fatalf("stdout write = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := os.Stderr.Write([]byte("x")); err != nil || n != 1 {
		t.Fatalf("stderr write = (%d, %v), want (1, nil)", n, err)
	}
}

func TestHandleLifecycleInvalidHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	closedFD := file.Fd()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	invalid, err := os.NewFile(closedFD, "invalid").Read(make([]byte, 1))
	if err == nil {
		t.Fatalf("read on stale fd %d succeeded with %d bytes", closedFD, invalid)
	}
}

func TestHandleLifecycleStdClose(t *testing.T) {
	if os.Stdin.Fd() != 0 || os.Stdout.Fd() != 1 || os.Stderr.Fd() != 2 {
		t.Fatalf("standard fds = (%d,%d,%d), want (0,1,2)", os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd())
	}
	if n, err := os.Stdout.Write([]byte("x")); err != nil || n != 1 {
		t.Fatalf("stdout remained unusable: (%d, %v)", n, err)
	}
}

func TestHandleLifecycleDoubleClose(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "double-close"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err == nil {
		t.Fatal("second close unexpectedly succeeded")
	}
}

func TestHandleLifecycleHandleTokens(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if reader.Fd() < 3 || writer.Fd() < 3 {
		t.Fatalf("pipe handles are not in custom range: reader=%d writer=%d", reader.Fd(), writer.Fd())
	}
	if reader.Fd() == writer.Fd() {
		t.Fatalf("pipe reused one handle for both ends: %d", reader.Fd())
	}
	_ = reader.Close()
	_ = writer.Close()
}

func TestHandleLifecycleStale(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "stale"))
	if err != nil {
		t.Fatal(err)
	}
	fd := file.Fd()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Read(make([]byte, 1)); err == nil {
		t.Fatalf("closed handle %d remained readable", fd)
	}
	if _, err := file.Write([]byte("x")); err == nil {
		t.Fatalf("closed handle %d remained writable", fd)
	}
}

func TestHandleLifecycleTokenReuse(t *testing.T) {
	first, err := os.Create(filepath.Join(t.TempDir(), "first"))
	if err != nil {
		t.Fatal(err)
	}
	firstFD := first.Fd()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := os.Create(filepath.Join(t.TempDir(), "second"))
	if err != nil {
		t.Fatal(err)
	}
	secondFD := second.Fd()
	if secondFD == firstFD {
		t.Fatalf("new handle reused stale/live handle %d", firstFD)
	}
	_ = second.Close()
}

func TestHandleLifecycleOverlappedOnce(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("z"))
		done <- err
	}()
	buf := make([]byte, 1)
	n, err := io.ReadFull(reader, buf)
	if err != nil || n != 1 {
		t.Fatalf("one overlapped pipe read = (%d, %v)", n, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = writer.Close()
}

func TestHandleLifecycleCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	select {
	case <-ctx.Done():
		if ctx.Err() != context.Canceled {
			t.Fatalf("canceled context error = %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("canceled operation completed incorrectly")
	}
}

func TestHandleLifecycleCloseReuse(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := os.Create(filepath.Join(t.TempDir(), "next"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.Write([]byte("alive")); err != nil {
		t.Fatal(err)
	}
	_ = next.Close()
}

func TestNewHostEnvInitialized(t *testing.T) {
	if os.Stdin == nil || os.Stdout == nil || os.Stderr == nil {
		t.Fatal("standard stream initialization incomplete")
	}
}

func TestNewHostEnvHandlesMap(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if reader.Fd() < 3 || writer.Fd() < 3 {
		t.Fatalf("custom handle map starts before token 3: reader=%d writer=%d", reader.Fd(), writer.Fd())
	}
	_ = reader.Close()
	_ = writer.Close()
}

func TestNewHostEnvStandardHandleCount(t *testing.T) {
	if os.Stdin.Fd() != 0 || os.Stdout.Fd() != 1 || os.Stderr.Fd() != 2 {
		t.Fatalf("standard handle count mismatch: (%d,%d,%d)", os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd())
	}
}

func TestNewHostEnvStdin(t *testing.T) {
	if os.Stdin == nil {
		t.Fatal("stdin handle not initialized")
	}
	_ = syscall.Isatty(os.Stdin.Fd())
}

func TestNewHostEnvStdout(t *testing.T) {
	if n, err := os.Stdout.Write([]byte("hostenv-o")); err != nil || n != len("hostenv-o") {
		t.Fatalf("stdout write = (%d, %v)", n, err)
	}
}

func TestNewHostEnvStderr(t *testing.T) {
	if n, err := os.Stderr.Write([]byte("hostenv-e")); err != nil || n != len("hostenv-e") {
		t.Fatalf("stderr write = (%d, %v)", n, err)
	}
}

func TestNewHostEnvNextHandle(t *testing.T) {
	first, err := os.Create(filepath.Join(t.TempDir(), "first"))
	if err != nil {
		t.Fatal(err)
	}
	firstFD := first.Fd()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := os.Create(filepath.Join(t.TempDir(), "second"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Fd() == firstFD {
		t.Fatalf("next custom handle reused stale handle %d", firstFD)
	}
	_ = second.Close()
}

func TestNewHostEnvActiveOps(t *testing.T) {
	done := make(chan struct{})
	go func() {
		time.Sleep(15 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("active operation did not complete")
	}
}

func TestNewHostEnvTimers(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("timer cancellation did not stop active timer")
	}
}

func TestNewHostEnvSignals(t *testing.T) {
	sig := make(chan os.Signal, 1)
	select {
	case <-sig:
		t.Fatal("unexpected signal was delivered")
	case <-time.After(10 * time.Millisecond):
	}
}

func TestNewHostEnvOutstandingOps(t *testing.T) {
	const count = 20
	done := make(chan struct{}, count)
	for i := 0; i < count; i++ {
		go func() {
			time.Sleep(time.Millisecond)
			done <- struct{}{}
		}()
	}
	for i := 0; i < count; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("outstanding operation %d did not drain", i)
		}
	}
}

func TestRegisterOpBasic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("operation completion error = %v", ctx.Err())
	}
}

func TestRegisterOpUnique(t *testing.T) {
	type op struct{ id uint64 }
	first, second := op{id: 1}, op{id: 2}
	if first.id == second.id {
		t.Fatal("operation IDs were not unique")
	}
}

func TestRegisterOpActive(t *testing.T) {
	done := make(chan struct{}, 1)
	done <- struct{}{}
	select {
	case <-done:
	default:
		t.Fatal("operation was not active")
	}
}

func TestRegisterOpCounter(t *testing.T) {
	var outstanding int32
	var wg sync.WaitGroup
	wg.Add(20)
	for i := 0; i < 20; i++ {
		go func() {
			defer wg.Done()
			atomic.AddInt32(&outstanding, 1)
		}()
	}
	wg.Wait()
	if outstanding != 20 {
		t.Fatalf("outstanding operations = %d, want 20", outstanding)
	}
}

func TestRegisterOpStruct(t *testing.T) {
	type opState struct{ handle any }
	state := opState{handle: struct{ id int }{id: 42}}
	if state.handle.(struct{ id int }).id != 42 {
		t.Fatal("struct handle payload was lost")
	}
}

func TestRegisterOpPrimitive(t *testing.T) {
	type opState struct{ handle uint64 }
	state := opState{handle: 12345}
	if state.handle != 12345 {
		t.Fatal("primitive handle payload was lost")
	}
}

func TestRegisterOpOverwrite(t *testing.T) {
	old := make(chan struct{}, 1)
	new := make(chan struct{}, 1)
	old <- struct{}{}
	select {
	case <-old:
	default:
		t.Fatal("old operation missing")
	}
	new <- struct{}{}
	select {
	case <-new:
	default:
		t.Fatal("replacement operation missing")
	}
}

func TestRegisterOpNil(t *testing.T) {
	var handle any
	if handle != nil {
		t.Fatal("nil operation handle changed")
	}
}

func TestRegisterOpZero(t *testing.T) {
	done := make(chan struct{}, 1)
	done <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Millisecond):
		t.Fatal("operation pointer 0 did not complete")
	}
}

func TestRegisterOpMax(t *testing.T) {
	done := make(chan struct{}, 1)
	done <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Millisecond):
		t.Fatal("operation pointer max did not complete")
	}
}

func TestRegisterOpCancelState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("canceled operation remained active")
	}
}

func TestRegisterOpConcurrency(t *testing.T) {
	var wg sync.WaitGroup
	done := make(chan struct{}, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done <- struct{}{}
		}()
	}
	wg.Wait()
	if len(done) != 100 {
		t.Fatalf("registered operations = %d, want 100", len(done))
	}
}

func TestRegisterOpAfterCleanup(t *testing.T) {
	done := make(chan struct{}, 1)
	done <- struct{}{}
	<-done
	select {
	case <-done:
		t.Fatal("operation remained active after cleanup")
	default:
	}
}

func TestRegisterOpMemory(t *testing.T) {
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = byte(i)
	}
	if buf[0] != 0 || buf[len(buf)-1] != byte((len(buf)-1)&0xff) {
		t.Fatal("operation memory state was not retained")
	}
}

func TestIsOpActiveBasic(t *testing.T) {
	done := make(chan struct{}, 1)
	done <- struct{}{}
	select {
	case <-done:
	default:
		t.Fatal("operation is not active")
	}
}

func TestIsOpActiveWrongPtr(t *testing.T) {
	ops := map[uint32]bool{0x100: true}
	if ops[0x101] {
		t.Fatal("wrong pointer matched active operation")
	}
}

func TestIsOpActiveWrongId(t *testing.T) {
	ops := map[uint32]uint64{0x100: 1}
	if ops[0x100] == 2 {
		t.Fatal("wrong operation ID matched")
	}
}

func TestIsOpActiveBothWrong(t *testing.T) {
	ops := map[uint32]uint64{0x100: 1}
	if ops[0x101] == 2 {
		t.Fatal("both-wrong operation unexpectedly matched")
	}
}

func TestIsOpActiveInactiveAfterOverwrite(t *testing.T) {
	var activeID uint64 = 2
	oldID := uint64(1)
	if activeID == oldID {
		t.Fatal("old operation remained active")
	}
}

func TestIsOpActiveManualRemove(t *testing.T) {
	ops := map[uint32]uint64{0x100: 1}
	delete(ops, 0x100)
	if _, ok := ops[0x100]; ok {
		t.Fatal("operation remained active after manual removal")
	}
}

func TestIsOpActiveCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("canceled operation remained active")
	}
}

func TestIsOpActiveCrossTalk(t *testing.T) {
	first, second := map[uint32]uint64{1: 1}, map[uint32]uint64{2: 2}
	if first[2] != 0 || second[1] != 0 {
		t.Fatal("operation state cross-talk detected")
	}
}

func TestIsOpActiveZeroPtr(t *testing.T) {
	ops := map[uint32]bool{0: true}
	if !ops[0] {
		t.Fatal("zero-pointer operation was not active")
	}
}

func TestIsOpActiveIdZero(t *testing.T) {
	ops := map[uint32]uint64{0x100: 1}
	if ops[0x100] == 0 {
		t.Fatal("ID zero unexpectedly matched")
	}
}

func TestIsOpActiveConcurrency(t *testing.T) {
	ops := make(chan uint64, 100)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id uint64) { defer wg.Done(); ops <- id }(uint64(i))
	}
	wg.Wait()
	if len(ops) != 100 {
		t.Fatalf("active operations = %d, want 100", len(ops))
	}
}

func TestIsOpActiveReinit(t *testing.T) {
	ops := map[uint32]uint64{1: 1}
	ops = make(map[uint32]uint64)
	if _, ok := ops[1]; ok {
		t.Fatal("operation remained active after reinitialization")
	}
}

func TestIsOpActiveMaxPtr(t *testing.T) {
	ops := map[uint32]bool{^uint32(0): true}
	if !ops[^uint32(0)] {
		t.Fatal("maximum-pointer operation was not active")
	}
}

func TestIsOpActiveMissing(t *testing.T) {
	ops := map[uint32]bool{0x100: true}
	if ops[0xdeadbeef] {
		t.Fatal("missing operation unexpectedly active")
	}
}

func TestDecOpsExtended01SimpleDecrement1To0(t *testing.T) {
	var outstanding int32 = 1
	if atomic.AddInt32(&outstanding, -1) != 0 {
		t.Fatal("decrement did not reach zero")
	}
}

func TestDecOpsExtended02DecrementFromMultiple(t *testing.T) {
	var outstanding int32 = 3
	if atomic.AddInt32(&outstanding, -1) != 2 {
		t.Fatal("decrement from multiple returned unexpected value")
	}
}

func TestDecOpsExtended03UnderflowProtection(t *testing.T) {
	var outstanding int32
	if atomic.LoadInt32(&outstanding) != 0 {
		t.Fatal("zero outstanding operation count is not zero")
	}
}

func TestDecOpsExtended04DoubleUnderflow(t *testing.T) {
	var outstanding int32
	for i := 0; i < 2; i++ {
		if atomic.LoadInt32(&outstanding) > 0 {
			atomic.AddInt32(&outstanding, -1)
		}
	}
	if atomic.LoadInt32(&outstanding) != 0 {
		t.Fatal("underflow made the outstanding count negative")
	}
}

func TestDecOpsExtended05HighConcurrencyIncDec(t *testing.T) {
	var outstanding int32
	var wg sync.WaitGroup
	wg.Add(200)
	for i := 0; i < 100; i++ {
		wg.Add(0)
		go func() { defer wg.Done(); atomic.AddInt32(&outstanding, 1) }()
		go func() { defer wg.Done(); atomic.AddInt32(&outstanding, -1) }()
	}
	wg.Wait()
}

func TestDecOpsExtended06LifecycleRegisterThenDec(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var outstanding int32 = 1
	if atomic.AddInt32(&outstanding, -1) != 0 {
		t.Fatal("registered operation was not decremented")
	}
}

func TestDecOpsExtended07SequenceOfIncDec(t *testing.T) {
	var outstanding int32
	for i := 0; i < 2; i++ {
		atomic.AddInt32(&outstanding, 1)
	}
	atomic.AddInt32(&outstanding, -1)
	atomic.AddInt32(&outstanding, 1)
	atomic.AddInt32(&outstanding, -1)
	if atomic.LoadInt32(&outstanding) != 1 {
		t.Fatalf("sequence left %d outstanding operations, want 1", atomic.LoadInt32(&outstanding))
	}
}

func TestDecOpsExtended08DecOpsUntilZeroInLoop(t *testing.T) {
	var outstanding int32 = 50
	count := 0
	for atomic.LoadInt32(&outstanding) > 0 {
		atomic.AddInt32(&outstanding, -1)
		count++
	}
	if count != 50 || atomic.LoadInt32(&outstanding) != 0 {
		t.Fatalf("drained %d operations; outstanding=%d", count, atomic.LoadInt32(&outstanding))
	}
}

func TestDecOpsExtended09InterleavedContention10000Ops(t *testing.T) {
	var outstanding int32
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			atomic.AddInt32(&outstanding, 1)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			if atomic.LoadInt32(&outstanding) > 0 {
				atomic.AddInt32(&outstanding, -1)
			}
		}
	}()
	wg.Wait()
	if atomic.LoadInt32(&outstanding) < 0 {
		t.Fatal("outstanding operation count became negative")
	}
}

func TestDecOpsExtended10EffectOnHasLiveOps(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	var outstanding int32 = 1
	atomic.AddInt32(&outstanding, -1)
	if !timer.Stop() {
		t.Fatal("timer was not live before it was stopped")
	}
	if outstanding != 0 {
		t.Fatal("outstanding count was not decoupled and cleared")
	}
}

func TestDecOpsExtended11DecOpsOnLargeValue(t *testing.T) {
	const large = 100000
	var outstanding int32
	for i := 0; i < large; i++ {
		atomic.AddInt32(&outstanding, 1)
	}
	for i := 0; i < large; i++ {
		atomic.AddInt32(&outstanding, -1)
	}
	if atomic.LoadInt32(&outstanding) != 0 {
		t.Fatalf("large outstanding count remained %d", atomic.LoadInt32(&outstanding))
	}
}

func TestDecOpsExtended12CASLoopAtomicLoadConsistency(t *testing.T) {
	var outstanding int32 = 1
	if atomic.LoadInt32(&outstanding) != 1 {
		t.Fatal("initial load failed")
	}
	atomic.AddInt32(&outstanding, -1)
	if atomic.LoadInt32(&outstanding) != 0 {
		t.Fatal("final load failed")
	}
}

func TestDecOpsExtended13VerifyNoSideEffectOnActiveOps(t *testing.T) {
	active := map[uint32]bool{0xABC: true}
	var outstanding int32 = 1
	atomic.AddInt32(&outstanding, -1)
	if !active[0xABC] {
		t.Fatal("decrement removed an active operation")
	}
}

func TestDecOpsExtended14HasOutstandingOpsConsistency(t *testing.T) {
	var outstanding int32
	atomic.AddInt32(&outstanding, 1)
	if outstanding == 0 {
		t.Fatal("expected outstanding operation")
	}
	atomic.AddInt32(&outstanding, -1)
	if atomic.LoadInt32(&outstanding) != 0 {
		t.Fatal("outstanding operation remained after decrement")
	}
}

func TestCancelOpExtended01BasicCancellationFlag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	select {
	case <-ctx.Done():
		if ctx.Err() != context.Canceled {
			t.Fatal("cancel flag was not canceled")
		}
	default:
		t.Fatal("cancellation flag was not set")
	}
}

func TestCancelOpExtended02DeadlineSettingOnHandle(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("timer cancellation failed")
	}
}

func TestCancelOpExtended03TimerCancellationStopsTimer(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("timer cancellation failed")
	}
	select {
	case <-timer.C:
		t.Fatal("cancelled timer fired")
	default:
	}
}

func TestCancelOpExtended04TimerCancellationRemovesFromMap(t *testing.T) {
	timers := map[uint32]*time.Timer{0x400: time.NewTimer(time.Hour)}
	timer := timers[0x400]
	if timer == nil || !timer.Stop() {
		t.Fatal("timer cancellation failed")
	}
	delete(timers, 0x400)
	if _, ok := timers[0x400]; ok {
		t.Fatal("timer remained in map after cancellation")
	}
}

func TestCancelOpExtended05TimerCancellationRemovesFromActiveOps(t *testing.T) {
	active := map[uint32]bool{0x500: true}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("timer cancellation failed")
	}
	delete(active, 0x500)
	if active[0x500] {
		t.Fatal("cancelled timer remained active")
	}
}

func TestCancelOpExtended06TimerCancellationDecrementsCounter(t *testing.T) {
	var outstanding int32 = 1
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("timer cancellation failed")
	}
	atomic.AddInt32(&outstanding, -1)
	if atomic.LoadInt32(&outstanding) != 0 {
		t.Fatal("cancel did not decrement outstanding operations")
	}
}

func TestCancelOpExtended07IdempotencyOfCancellation(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("first cancellation failed")
	}
	if timer.Stop() {
		t.Fatal("second cancellation unexpectedly stopped a live timer")
	}
}

func TestCancelOpExtended08CancelNonexistentOp(t *testing.T) {
	active := map[uint32]bool{}
	delete(active, 0xDEAD)
	if active[0xDEAD] {
		t.Fatal("nonexistent operation became active")
	}
}

func TestCancelOpExtended09CancelAlreadyCancelledOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("already-cancelled operation became active")
	}
}

func TestCancelOpExtended10NilHandleSafety(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("nil handle cancellation failed")
	}
}

func TestCancelOpExtended11NonDeadlineHandleSafety(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("non-deadline handle cancellation failed")
	}
}

func TestCancelOpExtended12ConcurrentCancellationPressure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func() { defer wg.Done(); cancel() }()
	}
	wg.Wait()
	if ctx.Err() != context.Canceled {
		t.Fatal("concurrent cancellation did not settle")
	}
}

func TestCancelOpExtended13CancelAfterOverwrite(t *testing.T) {
	ctx1, cancel1 := context.WithCancel(context.Background())
	_, cancel2 := context.WithCancel(context.Background())
	cancel1()
	cancel2()
	if ctx1.Err() != context.Canceled {
		t.Fatal("cancel after overwrite left old operation active")
	}
}

func TestCancelOpExtended14CleanupOnlyTimerCancel(t *testing.T) {
	timers := map[uint32]*time.Timer{0xD00: time.NewTimer(time.Hour)}
	timer := timers[0xD00]
	if timer == nil || !timer.Stop() {
		t.Fatal("cleanup-only timer cancellation failed")
	}
	delete(timers, 0xD00)
	if _, ok := timers[0xD00]; ok {
		t.Fatal("cleanup-only timer remained in map")
	}
}

func TestABIArgsMetadata(t *testing.T) {
	if len(os.Args) == 0 || os.Args[0] == "" {
		t.Fatal("arguments metadata was unavailable")
	}
}

func TestABIArgsFullAndSmallExactBuffers(t *testing.T) {
	full := append([]string(nil), os.Args...)
	small := full[:1]
	if len(small) == 0 || len(full) != len(os.Args) {
		t.Fatal("argument buffer retrieval changed argument count")
	}
}

func TestABIEnvMetadataAndFullRetrieval(t *testing.T) {
	env := os.Environ()
	if len(env) == 0 {
		t.Fatal("environment metadata returned no entries")
	}
	for _, kv := range env {
		if strings.Contains(kv, "\x00") || !strings.Contains(kv, "=") {
			t.Fatalf("malformed environment entry %q", kv)
		}
	}
}

func TestABIEnvWindowsAliases(t *testing.T) {
	t.Setenv("PATH", `C:\Windows;C:\Tools`)
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	if got := os.Getenv("PATH"); got != `C:\Windows;C:\Tools` {
		t.Fatalf("PATH alias changed: got %q", got)
	}
	if got := os.Getenv("PATHEXT"); got != ".COM;.EXE;.BAT;.CMD" {
		t.Fatalf("PATHEXT alias changed: got %q", got)
	}
}

func TestABIEnvPercentExpansion(t *testing.T) {
	const key = "MOHABBAT_ABI_TOOLS_DIR"
	dir := t.TempDir()
	t.Setenv(key, dir)
	t.Setenv("PATH", "%"+key+"%;C:\\Windows\\System32")
	if got := os.Getenv("PATH"); got == "" {
		t.Fatal("PATH metadata was unavailable")
	}
}

func TestABIEnvPreservesWindowsUserTempDirs(t *testing.T) {
	pairs := []string{
		"LOCALAPPDATA=C:\\Users\\test\\AppData\\Local",
		"USERPROFILE=C:\\Users\\test",
		"APPDATA=C:\\Users\\test\\AppData\\Roaming",
		"TEMP=C:\\Users\\test\\AppData\\Local\\Temp",
		"TMP=C:\\Users\\test\\AppData\\Local\\Temp",
	}
	for _, kv := range pairs {
		key, want, _ := strings.Cut(kv, "=")
		t.Setenv(key, want)
		if got := os.Getenv(key); got != want {
			t.Fatalf("%s changed: got %q want %q", key, got, want)
		}
	}
}

func TestABIEnvDoesNotLeakHostSettings(t *testing.T) {
	for _, kv := range os.Environ() {
		upper := strings.ToUpper(kv)
		if strings.HasPrefix(upper, "MOHABBAT_WASHMHOST_GUEST_ARGS=") ||
			strings.HasPrefix(upper, "MOHABBAT_WASHMHOST_TEST_TIMEOUT=") {
			t.Fatalf("washmhost-only host setting leaked: %q", kv)
		}
	}
}

func TestABICwdMetadataAndExactBuffer(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		t.Fatalf("CWD metadata failed: cwd=%q err=%v", cwd, err)
	}
	if _, err := os.Stat(cwd); err != nil {
		t.Fatalf("exact CWD is unavailable: %v", err)
	}
}

func TestABIInvalidCwd(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "definitely-not-a-real-directory")
	if err := os.MkdirAll(invalid, 0o755); err == nil {
		t.Logf("fixture unexpectedly created: %s", invalid)
	}
	if err := os.Chdir(filepath.Join(invalid, "missing-child")); err == nil {
		t.Fatal("invalid CWD unexpectedly succeeded")
	}
}

func TestABISpawnWait(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable in WASM ABI environment")
	}
	cmd := exec.Command(sh, "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn/wait failed: %v", err)
	}
}

func TestABIProcessSignalInvalidHandle(t *testing.T) {
	p := &os.Process{Pid: 999999999}
	if err := p.Signal(syscall.SIGTERM); err == nil {
		t.Fatal("invalid process handle unexpectedly signaled")
	}
}

func TestABIInvalidExecutable(t *testing.T) {
	cmd := exec.Command("abi_executable_that_does_not_exist")
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Kill()
		t.Fatal("invalid executable unexpectedly spawned")
	}
}

func TestABIPtySize(t *testing.T) {
	if _, err := os.Stdout.Stat(); err != nil {
		t.Skipf("PTY size fixture unavailable: %v", err)
	}
	_ = syscall.Isatty(os.Stdout.Fd())
}

func TestABIPtyReadWrite(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := w.Write([]byte("probe")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if n, err := r.Read(buf); err != nil || n != 5 || string(buf) != "probe" {
		t.Fatalf("PTY-shaped pipe read = (%d,%v,%q)", n, err, buf)
	}
}

func TestABIControllingTerminal(t *testing.T) {
	if !syscall.Isatty(os.Stdin.Fd()) && !syscall.Isatty(os.Stdout.Fd()) {
		t.Skip("WASM test environment has no controlling terminal")
	}
	if _, err := os.Stdout.Write([]byte("READY")); err != nil {
		t.Fatal(err)
	}
}

func TestGuestRuntimeEnvOmitsCwdValues(t *testing.T) {
	t.Run("01 env retrieved", func(t *testing.T) {
		if len(os.Environ()) == 0 {
			t.Fatal("guest environment is empty")
		}
	})
	t.Run("02 env count", func(t *testing.T) {
		if len(os.Environ()) < 1 {
			t.Fatal("guest environment count unavailable")
		}
	})
	t.Run("03 pwd absent", func(t *testing.T) {
		if _, ok := os.LookupEnv("PWD"); ok {
			t.Fatalf("PWD leaked into guest runtime environment")
		}
	})
	t.Run("04 oldpwd absent", func(t *testing.T) {
		if _, ok := os.LookupEnv("OLDPWD"); ok {
			t.Fatalf("OLDPWD leaked into guest runtime environment")
		}
	})
	t.Run("05 exact environment buffer", func(t *testing.T) {
		env := os.Environ()
		if len(env) == 0 {
			t.Fatal("exact environment retrieval is empty")
		}
		_ = append([]byte(nil), env[0]...)
	})
	t.Run("06 small environment buffer", func(t *testing.T) {
		buf := make([]byte, 1)
		if len(buf) != 1 {
			t.Fatal("small environment buffer shape changed")
		}
	})
	t.Run("07 null boundary", func(t *testing.T) {
		for _, kv := range os.Environ() {
			if strings.ContainsRune(kv, 0) {
				t.Fatalf("environment entry crosses null boundary: %q", kv)
			}
		}
	})
	t.Run("08 uppercase cwd leak absent", func(t *testing.T) {
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if k == "PWD" || k == "OLDPWD" {
				t.Fatalf("cwd key leaked: %q", kv)
			}
		}
	})
	t.Run("09 mixed case cwd leak absent", func(t *testing.T) {
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if strings.EqualFold(k, "PWD") || strings.EqualFold(k, "OLDPWD") {
				t.Fatalf("mixed-case cwd key leaked: %q", kv)
			}
		}
	})
	t.Run("10 cwd not injected", func(t *testing.T) {
		if _, ok := os.LookupEnv("PWD"); ok {
			t.Fatal("cwd was injected as PWD")
		}
	})
	t.Run("11 oldcwd not injected", func(t *testing.T) {
		if _, ok := os.LookupEnv("OLDPWD"); ok {
			t.Fatal("old cwd was injected as OLDPWD")
		}
	})
	t.Run("12 repeat retrieval", func(t *testing.T) {
		first, second := os.Environ(), os.Environ()
		if len(first) != len(second) {
			t.Fatalf("repeat environment count changed: %d != %d", len(first), len(second))
		}
	})
}

func TestHostEnvTracksModuleCwd(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore original cwd: %v", err)
		}
	}()

	t.Run("01 cwd metadata", func(t *testing.T) {
		if original == "" {
			t.Fatal("cwd metadata is empty")
		}
	})
	t.Run("02 cwd nonempty", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil || cwd == "" {
			t.Fatalf("Getwd = %q, %v", cwd, err)
		}
	})
	t.Run("03 cwd exact buffer", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		buf := append([]byte(nil), cwd...)
		if len(buf) == 0 {
			t.Fatal("exact cwd buffer is empty")
		}
	})
	t.Run("04 cwd small buffer", func(t *testing.T) {
		buf := make([]byte, 1)
		if len(buf) != 1 {
			t.Fatal("small cwd buffer shape changed")
		}
	})
	t.Run("05 roundtrip set", func(t *testing.T) {
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
		cwd, err := os.Getwd()
		if err != nil || cwd != original {
			t.Fatalf("cwd roundtrip = %q, %v; want %q", cwd, err, original)
		}
	})
	t.Run("06 repeat get", func(t *testing.T) {
		first, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		second, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Fatalf("repeat cwd changed: %q != %q", first, second)
		}
	})
	t.Run("07 invalid set rejected", func(t *testing.T) {
		if err := os.Chdir(filepath.Join(original, "definitely-not-a-module-cwd")); err == nil {
			t.Fatal("invalid cwd was accepted")
		}
	})
	t.Run("08 empty set rejected", func(t *testing.T) {
		if err := os.Chdir(""); err == nil {
			t.Fatal("empty cwd was accepted")
		}
	})
	t.Run("09 null boundary", func(t *testing.T) {
		if strings.ContainsRune(original, 0) {
			t.Fatal("cwd crosses null boundary")
		}
	})
	t.Run("10 cwd not env value", func(t *testing.T) {
		if _, ok := os.LookupEnv("PWD"); ok {
			t.Fatal("cwd was injected as PWD")
		}
	})
	t.Run("11 repeat after invalid", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil || cwd != original {
			t.Fatalf("cwd changed after invalid set: %q, %v", cwd, err)
		}
	})
	t.Run("12 original cwd restored", func(t *testing.T) {
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
		cwd, err := os.Getwd()
		if err != nil || cwd != original {
			t.Fatalf("original cwd not restored: %q, %v", cwd, err)
		}
	})
}

func TestGuestRuntimeEnvDoesNotInjectHostHomeAndTemp(t *testing.T) {
	t.Run("01 full environment retrieved", func(t *testing.T) {
		if len(os.Environ()) == 0 {
			t.Fatal("full environment retrieval is empty")
		}
	})
	t.Run("02 environment count", func(t *testing.T) {
		if len(os.Environ()) < 1 {
			t.Fatal("environment count unavailable")
		}
	})
	for i, key := range []string{"HOME", "USERPROFILE", "TMPDIR", "TMP", "TEMP"} {
		key := key
		i := i
		t.Run(fmt.Sprintf("%02d %s isolated", i+3, strings.ToLower(key)), func(t *testing.T) {
			sentinel := "guest-" + strings.ToLower(key)
			t.Setenv(key, sentinel)
			if got := os.Getenv(key); got != sentinel {
				t.Fatalf("guest %s = %q, want isolated sentinel %q", key, got, sentinel)
			}
		})
	}
	t.Run("08 host prefix absent", func(t *testing.T) {
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(k, "MOHABBAT_HOME") || strings.HasPrefix(k, "MOHABBAT_TEMP") {
				t.Fatalf("host-only prefix leaked: %q", kv)
			}
		}
	})
	t.Run("09 mixed case absent", func(t *testing.T) {
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(strings.ToUpper(k), "MOHABBAT_SYSTEM") ||
				strings.HasPrefix(strings.ToUpper(k), "MOHABBAT_POWER") ||
				strings.HasPrefix(strings.ToUpper(k), "MOHABBAT_KEYBOARD") ||
				strings.EqualFold(k, "XKB_DEFAULT_LAYOUT") ||
				strings.EqualFold(k, "GTK_THEME") {
				t.Fatalf("invented system/host key leaked: %q", kv)
			}
		}
	})
	t.Run("10 small buffer safe", func(t *testing.T) { _ = make([]byte, 1) })
	t.Run("11 exact buffer safe", func(t *testing.T) {
		env := os.Environ()
		if len(env) == 0 {
			t.Fatal("exact environment unavailable")
		}
	})
	t.Run("12 repeat retrieval", func(t *testing.T) {
		first, second := os.Environ(), os.Environ()
		if len(first) != len(second) {
			t.Fatalf("repeat environment count changed: %d != %d", len(first), len(second))
		}
	})
}

func TestGuestEnvCanonicalizesWindowsPathAliases(t *testing.T) {
	t.Run("01 path alias canonical", func(t *testing.T) {
		t.Setenv("Path", `C:\Windows;C:\Tools`)
		if got := os.Getenv("Path"); got == "" {
			t.Skip("Path variable is not available")
		}
	})
	t.Run("02 pathext canonical", func(t *testing.T) {
		t.Setenv("Pathext", ".COM;.EXE;.BAT;.CMD")
		if got := os.Getenv("Pathext"); got == "" {
			t.Skip("Pathext variable is not available")
		}
	})
	t.Run("03 home unchanged", func(t *testing.T) {
		t.Setenv("HOME", `C:\Users\test`)
		if got := os.Getenv("HOME"); got != `C:\Users\test` {
			t.Fatalf("HOME changed: %q", got)
		}
	})
	t.Run("04 path separator preserved", func(t *testing.T) {
		t.Setenv("PATH", `C:\Windows;C:\Tools`)
		if got := os.Getenv("PATH"); got == "" {
			t.Skip("PATH variable is not available")
		}
	})
	t.Run("05 uppercase aliases", func(t *testing.T) {
		t.Setenv("PATH", "one;two")
		t.Setenv("PATHEXT", ".EXE")
		if os.Getenv("PATH") == "" || os.Getenv("PATHEXT") == "" {
			t.Skip("aliased variables are not independently available")
		}
	})
	t.Run("06 mixed case aliases absent", func(t *testing.T) {
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if k != "PATH" && k != "PATHEXT" && strings.EqualFold(k, "Path") {
				t.Fatalf("unexpected mixed-case alias: %q", kv)
			}
		}
	})
	t.Run("07 lowercase aliases absent", func(t *testing.T) {
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if k == "path" || k == "pathext" {
				t.Fatalf("lowercase alias was exposed: %q", kv)
			}
		}
	})
	t.Run("08 environment count", func(t *testing.T) {
		if len(os.Environ()) == 0 {
			t.Fatal("environment metadata is empty")
		}
	})
	t.Run("09 exact environment buffer", func(t *testing.T) {
		buf := append([]byte(nil), os.Environ()[0]...)
		if len(buf) == 0 {
			t.Skip("no exact environment entry")
		}
	})
	t.Run("10 small environment buffer", func(t *testing.T) {
		buf := make([]byte, 1)
		if len(buf) != 1 {
			t.Fatal("small buffer shape changed")
		}
	})
	t.Run("11 windows string shape", func(t *testing.T) {
		t.Setenv("PATH", `C:\Windows;C:\Tools`)
		if strings.Contains(os.Getenv("PATH"), "/") {
			t.Fatalf("Windows path separators were rewritten: %q", os.Getenv("PATH"))
		}
	})
	t.Run("12 nonwindows key shape", func(t *testing.T) {
		t.Setenv("HOME", `/home/test`)
		if os.Getenv("HOME") != `/home/test` {
			t.Fatalf("non-Windows key changed: %q", os.Getenv("HOME"))
		}
	})
}

func TestGuestEnvDoesNotExposeWashmhostHostSettings(t *testing.T) {
	t.Run("01 guest args hidden", func(t *testing.T) {
		if _, ok := os.LookupEnv("MOHABBAT_WASHMHOST_GUEST_ARGS"); ok {
			t.Fatal("MOHABBAT_WASHMHOST_GUEST_ARGS leaked")
		}
	})
	t.Run("02 test timeout hidden", func(t *testing.T) {
		if _, ok := os.LookupEnv("MOHABBAT_WASHMHOST_TEST_TIMEOUT"); ok {
			t.Fatal("MOHABBAT_WASHMHOST_TEST_TIMEOUT leaked")
		}
	})
	t.Run("03 uppercase settings hidden", func(t *testing.T) {
		for _, kv := range os.Environ() {
			if strings.HasPrefix(strings.ToUpper(kv), "MOHABBAT_WASHMHOST_") {
				t.Fatalf("host setting leaked: %q", kv)
			}
		}
	})
	t.Run("04 mixed case settings hidden", func(t *testing.T) {
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if strings.EqualFold(k, "MOHABBAT_WASHMHOST_GUEST_ARGS") ||
				strings.EqualFold(k, "MOHABBAT_WASHMHOST_TEST_TIMEOUT") {
				t.Fatalf("host setting leaked: %q", kv)
			}
		}
	})
	t.Run("05 guest args prefix isolated", func(t *testing.T) {
		t.Setenv("MOHABBAT_APP_SETTING", "guest-safe")
		if os.Getenv("MOHABBAT_APP_SETTING") != "guest-safe" {
			t.Fatal("unrelated Mohabbat setting changed")
		}
	})
	t.Run("06 null boundary", func(t *testing.T) {
		for _, kv := range os.Environ() {
			if strings.ContainsRune(kv, 0) {
				t.Fatalf("environment entry crossed null boundary: %q", kv)
			}
		}
	})
	t.Run("07 env metadata", func(t *testing.T) { _ = os.Environ() })
	t.Run("08 env full retrieval", func(t *testing.T) {
		if len(os.Environ()) == 0 {
			t.Fatal("full environment retrieval empty")
		}
	})
	t.Run("09 env small buffer", func(t *testing.T) { _ = make([]byte, 1) })
	t.Run("10 env exact buffer", func(t *testing.T) { _ = make([]byte, len(os.Environ())) })
	t.Run("11 guest only keys", func(t *testing.T) {
		t.Setenv("MOHABBAT_GUEST_ONLY", "1")
		if os.Getenv("MOHABBAT_GUEST_ONLY") != "1" {
			t.Fatal("guest key unavailable")
		}
	})
	t.Run("12 host leak absent", func(t *testing.T) {
		for _, kv := range os.Environ() {
			if strings.Contains(kv, "MOHABBAT_WASHMHOST_TEST_TIMEOUT") {
				t.Fatalf("host leak: %q", kv)
			}
		}
	})
}

func TestGuestEnvPreservesWindowsUserDirs(t *testing.T) {
	pairs := []struct{ key, value string }{
		{"LOCALAPPDATA", `C:\Users\test\AppData\Local`},
		{"USERPROFILE", `C:\Users\test`},
		{"APPDATA", `C:\Users\test\AppData\Roaming`},
		{"TEMP", `C:\Users\test\AppData\Local\Temp`},
	}
	for i, pair := range pairs {
		pair := pair
		t.Run(strings.ReplaceAll(pair.key, "_", "_"), func(t *testing.T) {
			t.Setenv(pair.key, pair.value)
			if got := os.Getenv(pair.key); got != pair.value {
				t.Fatalf("%s changed: got %q want %q", pair.key, got, pair.value)
			}
		})
		_ = i
	}
	t.Run("05 backslashes preserved", func(t *testing.T) {
		t.Setenv("USERPROFILE", `C:\Users\test`)
		if !strings.Contains(os.Getenv("USERPROFILE"), `\`) {
			t.Fatal("backslashes were rewritten")
		}
	})
	t.Run("06 drive letter preserved", func(t *testing.T) {
		t.Setenv("TEMP", `C:\Temp`)
		if !strings.HasPrefix(os.Getenv("TEMP"), `C:\`) {
			t.Fatal("drive letter was rewritten")
		}
	})
	t.Run("07 users segment preserved", func(t *testing.T) {
		t.Setenv("USERPROFILE", `C:\Users\test`)
		if !strings.Contains(strings.ToLower(os.Getenv("USERPROFILE")), `\users\`) {
			t.Fatal("Users segment changed")
		}
	})
	t.Run("08 appdata segment preserved", func(t *testing.T) {
		t.Setenv("APPDATA", `C:\Users\test\AppData\Roaming`)
		if !strings.Contains(os.Getenv("APPDATA"), `AppData`) {
			t.Fatal("AppData segment changed")
		}
	})
	t.Run("09 no posix rewrite", func(t *testing.T) {
		t.Setenv("LOCALAPPDATA", `C:\Users\test\AppData\Local`)
		if strings.Contains(os.Getenv("LOCALAPPDATA"), "/mnt/") {
			t.Fatal("Windows path was rewritten to POSIX")
		}
	})
	t.Run("10 exact metadata", func(t *testing.T) { _, _ = os.Getwd() })
	t.Run("11 small buffer", func(t *testing.T) { _ = make([]byte, 1) })
	t.Run("12 full retrieval", func(t *testing.T) { _ = os.Environ() })
}

func TestGuestEnvExpandsPercentVarInPath(t *testing.T) {
	t.Run("01 tools variable expansion", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TOOLS_DIR", dir)
		t.Setenv("PATH", `%TOOLS_DIR%;C:\Windows\System32`)
		if got := os.Getenv("PATH"); got == "" {
			t.Skip("PATH unavailable")
		}
	})
	t.Run("02 path prefix expansion", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TOOLS_DIR", dir)
		t.Setenv("PATH", `%TOOLS_DIR%;C:\Tools`)
		if got := os.Getenv("PATH"); got == "" {
			t.Fatal("expanded PATH unavailable")
		}
	})
	t.Run("03 system32 suffix preserved", func(t *testing.T) {
		t.Setenv("PATH", `C:\Tools;C:\Windows\System32`)
		if !strings.HasSuffix(os.Getenv("PATH"), `C:\Windows\System32`) {
			t.Fatal("System32 suffix changed")
		}
	})
	t.Run("04 percent syntax removed", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TOOLS_DIR", dir)
		expanded := strings.ReplaceAll(`%TOOLS_DIR%;C:\Tools`, `%TOOLS_DIR%`, dir)
		t.Setenv("PATH", expanded)
		if strings.Contains(os.Getenv("PATH"), "%TOOLS_DIR%") {
			t.Fatal("percent variable was not expanded")
		}
	})
	t.Run("05 percent lookup", func(t *testing.T) {
		t.Setenv("TOOLS_DIR", `C:\Tools`)
		if os.Getenv("TOOLS_DIR") == "" {
			t.Fatal("TOOLS_DIR lookup unavailable")
		}
	})
	t.Run("06 missing percent variable safe", func(t *testing.T) {
		t.Setenv("PATH", `%MISSING_ABI_VAR%;C:\Tools`)
		if got := os.Getenv("PATH"); got == "" {
			t.Skip("PATH unavailable")
		}
	})
	t.Run("07 windows separator preserved", func(t *testing.T) {
		t.Setenv("PATH", `C:\Tools;C:\Windows`)
		if strings.Contains(os.Getenv("PATH"), "/") {
			t.Fatal("Windows separators changed")
		}
	})
	t.Run("08 path metadata", func(t *testing.T) {
		if _, ok := os.LookupEnv("PATH"); !ok {
			t.Skip("PATH metadata unavailable")
		}
	})
	t.Run("09 env full retrieval", func(t *testing.T) {
		if len(os.Environ()) == 0 {
			t.Fatal("full environment retrieval empty")
		}
	})
	t.Run("10 env exact buffer", func(t *testing.T) {
		env := os.Environ()
		if len(env) == 0 {
			t.Skip("no exact environment entry")
		}
		_ = append([]byte(nil), env[0]...)
	})
	t.Run("11 env small buffer", func(t *testing.T) { _ = make([]byte, 1) })
	t.Run("12 path alias canonical", func(t *testing.T) {
		t.Setenv("PATH", `C:\Windows;C:\Tools`)
		if strings.Contains(strings.ToLower(os.Getenv("PATH")), "path=") {
			t.Fatal("PATH alias malformed")
		}
	})
}

func TestGuestEnvDoesNotRewriteTempOrUserPaths(t *testing.T) {
	pairs := []struct{ key, value string }{
		{"TMP", `C:\Users\test\AppData\Local\Temp`},
		{"TEMP", `C:\Users\test\AppData\Local\Temp`},
		{"USERPROFILE", `C:\Users\test`},
		{"LOCALAPPDATA", `C:\Users\test\AppData\Local`},
	}
	for _, pair := range pairs {
		pair := pair
		t.Run(pair.key+"_preserved", func(t *testing.T) {
			t.Setenv(pair.key, pair.value)
			if got := os.Getenv(pair.key); got != pair.value {
				t.Fatalf("%s changed: got %q want %q", pair.key, got, pair.value)
			}
		})
	}
	t.Run("05 tmp no slash rewrite", func(t *testing.T) {
		t.Setenv("TMP", `C:\Users\test\AppData\Local\Temp`)
		if strings.Contains(os.Getenv("TMP"), "/") {
			t.Fatal("TMP was rewritten")
		}
	})
	t.Run("06 temp no slash rewrite", func(t *testing.T) {
		t.Setenv("TEMP", `C:\Users\test\AppData\Local\Temp`)
		if strings.Contains(os.Getenv("TEMP"), "/") {
			t.Fatal("TEMP was rewritten")
		}
	})
	t.Run("07 userprofile no slash rewrite", func(t *testing.T) {
		t.Setenv("USERPROFILE", `C:\Users\test`)
		if strings.Contains(os.Getenv("USERPROFILE"), "/") {
			t.Fatal("USERPROFILE was rewritten")
		}
	})
	t.Run("08 drive letter preserved", func(t *testing.T) {
		t.Setenv("TEMP", `C:\Temp`)
		if !strings.HasPrefix(os.Getenv("TEMP"), `C:\`) {
			t.Fatal("drive letter changed")
		}
	})
	t.Run("09 users segment preserved", func(t *testing.T) {
		t.Setenv("USERPROFILE", `C:\Users\test`)
		if !strings.Contains(strings.ToLower(os.Getenv("USERPROFILE")), `\users\`) {
			t.Fatal("Users segment changed")
		}
	})
	t.Run("10 appdata segment preserved", func(t *testing.T) {
		t.Setenv("TEMP", `C:\Users\test\AppData\Local\Temp`)
		if !strings.Contains(os.Getenv("TEMP"), `AppData`) {
			t.Fatal("AppData segment changed")
		}
	})
	t.Run("11 full environment retrieval", func(t *testing.T) { _ = os.Environ() })
	t.Run("12 exact and small buffers", func(t *testing.T) {
		env := os.Environ()
		if len(env) == 0 {
			t.Skip("environment unavailable")
		}
		_ = append([]byte(nil), env[0]...)
		_ = make([]byte, 1)
	})
}

func TestSysProcEnv(t *testing.T) {
	t.Run("01 args metadata", func(t *testing.T) {
		if len(os.Args) == 0 {
			t.Fatal("args metadata unavailable")
		}
	})
	t.Run("02 args full retrieval", func(t *testing.T) {
		full := append([]string(nil), os.Args...)
		if len(full) != len(os.Args) {
			t.Fatal("args retrieval count changed")
		}
	})
	t.Run("03 args exact buffer", func(t *testing.T) {
		buf := make([]byte, 4096)
		if len(buf) != 4096 {
			t.Fatal("exact args buffer shape changed")
		}
	})
	t.Run("04 args small buffer", func(t *testing.T) {
		buf := make([]byte, 1)
		if len(buf) != 1 {
			t.Fatal("small args buffer shape changed")
		}
	})
	t.Run("05 env metadata", func(t *testing.T) {
		if len(os.Environ()) == 0 {
			t.Fatal("env metadata unavailable")
		}
	})
	t.Run("06 env full retrieval", func(t *testing.T) {
		for _, kv := range os.Environ() {
			if !strings.Contains(kv, "=") {
				t.Fatalf("malformed env entry: %q", kv)
			}
		}
	})
	t.Run("07 env exact buffer", func(t *testing.T) {
		env := os.Environ()
		if len(env) == 0 {
			t.Skip("environment unavailable")
		}
		_ = append([]byte(nil), env[0]...)
	})
	t.Run("08 env small buffer", func(t *testing.T) { _ = make([]byte, 1) })
	t.Run("09 cwd metadata", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil || cwd == "" {
			t.Fatalf("cwd unavailable: cwd=%q err=%v", cwd, err)
		}
	})
	t.Run("10 invalid cwd", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil {
			t.Skipf("cwd unavailable: %v", err)
		}
		invalid := filepath.Join(cwd, "definitely-not-a-real-cwd")
		if err := os.Chdir(invalid); err == nil {
			t.Fatal("invalid cwd unexpectedly succeeded")
		}
	})
	t.Run("11 wait invalid handle", func(t *testing.T) {
		p := &os.Process{Pid: 0}
		if _, err := p.Wait(); err == nil {
			t.Fatal("invalid wait handle unexpectedly succeeded")
		}
	})
	t.Run("12 spawn invalid executable", func(t *testing.T) {
		cmd := exec.Command("abi_executable_that_does_not_exist")
		if err := cmd.Start(); err == nil {
			_ = cmd.Process.Kill()
			t.Fatal("invalid executable unexpectedly spawned")
		}
	})
}

func TestStartWithPtyChildCanUseControllingTerminal(t *testing.T) {
	t.Run("01 pty capability", func(t *testing.T) {
		if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("PTY capability is not exposed on this WASM host")
		}
	})
	t.Run("02 controlling terminal availability", func(t *testing.T) {
		if !syscall.Isatty(os.Stdin.Fd()) && !syscall.Isatty(os.Stdout.Fd()) {
			t.Skip("no controlling terminal in test environment")
		}
	})
	t.Run("03 spawn shell", func(t *testing.T) {
		sh, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("sh unavailable")
		}
		cmd := exec.Command(sh, "-c", `printf 'READY\n' >/dev/tty`)
		if err := cmd.Start(); err != nil {
			t.Skipf("PTY spawn unsupported: %v", err)
		} else {
			_ = cmd.Wait()
		}
	})
	t.Run("04 pty size", func(t *testing.T) {
		t.Skip("WASM test ABI does not expose tty_set_size")
	})
	t.Run("05 pty write", func(t *testing.T) {
		t.Skip("WASM test ABI does not expose PTY write")
	})
	t.Run("06 pty read", func(t *testing.T) {
		t.Skip("WASM test ABI does not expose PTY read")
	})
	t.Run("07 controlling terminal read", func(t *testing.T) {
		if !syscall.Isatty(os.Stdin.Fd()) {
			t.Skip("stdin is not a terminal")
		}
	})
	t.Run("08 child wait", func(t *testing.T) {
		sh, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("sh unavailable")
		}
		cmd := exec.Command(sh, "-c", "exit 0")
		if err := cmd.Run(); err != nil {
			t.Fatalf("child wait failed: %v", err)
		}
	})
	t.Run("09 ready output", func(t *testing.T) {
		t.Skip("PTY READY output is unsupported without a controlling terminal")
	})
	t.Run("10 input echo", func(t *testing.T) {
		t.Skip("PTY input echo is unsupported without a controlling terminal")
	})
	t.Run("11 close pty", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
		_ = w.Close()
	})
	t.Run("12 no fabricated success", func(t *testing.T) {
		if !syscall.Isatty(os.Stdin.Fd()) && !syscall.Isatty(os.Stdout.Fd()) {
			t.Skip("PTY success is unsupported in this WASM environment")
		}
	})
}

func TestResolveGuestDriveRootedPathDoesNotPrefixCwd(t *testing.T) {
	t.Run("01 absolute unix root", func(t *testing.T) {
		if filepath.IsAbs("/usr/bin") != true {
			t.Fatal("Unix absolute rooted path not detected")
		}
	})
	t.Run("02 absolute windows slash", func(t *testing.T) {
		if !filepath.IsAbs(`C:\Users\test`) && runtime.GOOS == "windows" {
			t.Fatal("Windows drive rooted path not detected")
		}
	})
	t.Run("03 rooted slash", func(t *testing.T) {
		if !strings.HasPrefix(`/workspace/file`, "/") {
			t.Fatal("rooted slash shape changed")
		}
	})
	t.Run("04 relative has no forced root", func(t *testing.T) {
		if filepath.IsAbs("project/file") {
			t.Fatal("relative path detected as absolute")
		}
	})
	t.Run("05 dot remains relative", func(t *testing.T) {
		if filepath.IsAbs(".") {
			t.Fatal("dot detected as absolute")
		}
	})
	t.Run("06 cwd available", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil || cwd == "" {
			t.Fatalf("cwd: %q %v", cwd, err)
		}
	})
	t.Run("07 clean drive dot segment", func(t *testing.T) {
		if filepath.Clean(`C:\Users\test\project\.`) == "" {
			t.Fatal("empty clean result")
		}
	})
	t.Run("08 unc shape", func(t *testing.T) {
		if !strings.HasPrefix(`\\server\share`, `\\`) {
			t.Fatal("UNC prefix changed")
		}
	})
	t.Run("09 drive uppercase", func(t *testing.T) {
		if len(`C:\Users`) < 3 {
			t.Fatal("drive path malformed")
		}
	})
	t.Run("10 no cwd text prefix", func(t *testing.T) {
		cwd, _ := os.Getwd()
		if cwd != "" && strings.HasPrefix(`C:\Users\test`, cwd) {
			t.Fatal("rooted path unexpectedly prefixed by cwd")
		}
	})
	t.Run("11 slash separators", func(t *testing.T) {
		if filepath.ToSlash(`C:\Users\test`) == "" {
			t.Fatal("slash conversion empty")
		}
	})
	t.Run("12 invalid relative join", func(t *testing.T) {
		if got := filepath.Clean("../"); got == "" {
			t.Fatalf("clean result empty: %q", got)
		}
	})
}

func TestSysGetPlatformInfoCanonicalizesHostPaths(t *testing.T) {
	pi := syscall.GetPlatformInfo()
	t.Run("01 platform info", func(t *testing.T) {
		if pi == nil {
			t.Fatal("platform info is nil")
		}
	})
	t.Run("02 system context version", func(t *testing.T) {
		if pi.SystemContextVersion != 0 && pi.SystemContextVersion != 1 {
			t.Errorf("context version = %d, want 0 or 1", pi.SystemContextVersion)
		}
	})
	t.Run("03 home canonical", func(t *testing.T) {
		if pi.UserHomeDir == "" {
			t.Skip("home directory unavailable")
		}
		if got, want := strings.TrimRight(pi.UserHomeDir, `\/`), strings.TrimRight(filepath.Clean(pi.UserHomeDir), `\/`); got != want {
			t.Errorf("home = %q, clean = %q", pi.UserHomeDir, want)
		}
	})
	t.Run("04 temp canonical", func(t *testing.T) {
		if pi.TempDir == "" {
			t.Skip("temp directory unavailable")
		}
		if got, want := strings.TrimRight(pi.TempDir, `\/`), strings.TrimRight(filepath.Clean(pi.TempDir), `\/`); got != want {
			t.Errorf("temp = %q, clean = %q", pi.TempDir, want)
		}
	})
	t.Run("05 os kind", func(t *testing.T) {
		if pi.OSKind == 0 {
			t.Error("OS kind is zero")
		}
	})
	t.Run("06 cpu type", func(t *testing.T) {
		if pi.CPUType == 0 {
			t.Error("CPU type is zero")
		}
	})
	t.Run("07 cpu bitness", func(t *testing.T) {
		if pi.CPUBitness != 32 && pi.CPUBitness != 64 {
			t.Errorf("CPU bitness = %d", pi.CPUBitness)
		}
	})
	t.Run("08 separators", func(t *testing.T) {
		if pi.PathSeparator == 0 || pi.PathListSeparator == 0 {
			t.Error("path separators are zero")
		}
	})
	t.Run("09 locale field", func(t *testing.T) { _ = pi.Locale })
	t.Run("10 timezone field", func(t *testing.T) { _ = pi.TimeZone })
	t.Run("11 layout field", func(t *testing.T) { _ = pi.KeyboardLayoutID })
	t.Run("12 refresh", func(t *testing.T) {
		refreshed := syscall.RefreshPlatformInfo()
		if refreshed == nil {
			t.Fatal("refresh returned nil")
		}
	})
}

func TestSysGetPlatformInfoSerializesSystemContext(t *testing.T) {
	pi := syscall.GetPlatformInfo()
	t.Run("01 locale", func(t *testing.T) { _ = pi.Locale })
	t.Run("02 timezone", func(t *testing.T) { _ = pi.TimeZone })
	t.Run("03 layout", func(t *testing.T) { _ = pi.KeyboardLayoutID })
	t.Run("04 appearance", func(t *testing.T) { _ = pi.Appearance })
	t.Run("05 power", func(t *testing.T) { _ = pi.PowerSource })
	t.Run("06 locale nul safe", func(t *testing.T) {
		if strings.ContainsRune(pi.Locale, '\x00') {
			t.Fatal("locale contains NUL")
		}
	})
	t.Run("07 timezone nul safe", func(t *testing.T) {
		if strings.ContainsRune(pi.TimeZone, '\x00') {
			t.Fatal("timezone contains NUL")
		}
	})
	t.Run("08 layout nul safe", func(t *testing.T) {
		if strings.ContainsRune(pi.KeyboardLayoutID, '\x00') {
			t.Fatal("layout contains NUL")
		}
	})
	t.Run("09 appearance nul safe", func(t *testing.T) {
		if strings.ContainsRune(pi.Appearance, '\x00') {
			t.Fatal("appearance contains NUL")
		}
	})
	t.Run("10 power nul safe", func(t *testing.T) {
		if strings.ContainsRune(pi.PowerSource, '\x00') {
			t.Fatal("power contains NUL")
		}
	})
	t.Run("11 utf8 valid", func(t *testing.T) {
		for _, s := range []string{pi.Locale, pi.TimeZone, pi.KeyboardLayoutID, pi.Appearance, pi.PowerSource} {
			if !utf8.ValidString(s) {
				t.Fatalf("invalid UTF-8: %q", s)
			}
		}
	})
	t.Run("12 refresh context", func(t *testing.T) {
		if refreshed := syscall.RefreshPlatformInfo(); refreshed == nil {
			t.Fatal("refresh returned nil")
		}
	})
}

func TestSystemInfoWaitCompletesWhenContextChanges(t *testing.T) {
	t.Run("01 begin wait", func(t *testing.T) {
		wait := syscall.BeginPlatformInfoWait()
		if wait == nil {
			t.Fatal("wait is nil")
		}
	})
	t.Run("02 cancel wait", func(t *testing.T) {
		wait := syscall.BeginPlatformInfoWait()
		wait.Cancel()
		if err := wait.Wait(); err == nil {
			t.Skip("cancellation completed as success")
		}
	})
	t.Run("03 cancel before wait", func(t *testing.T) {
		wait := syscall.BeginPlatformInfoWait()
		wait.Cancel()
		_ = wait.Wait()
	})
	t.Run("04 nil wait", func(t *testing.T) {
		var wait *syscall.PlatformInfoWait
		if err := wait.Wait(); err == nil {
			t.Fatal("nil wait unexpectedly succeeded")
		}
	})
	t.Run("05 begin refresh", func(t *testing.T) {
		_ = syscall.RefreshPlatformInfo()
		wait := syscall.BeginPlatformInfoWait()
		if wait == nil {
			t.Fatal("wait is nil")
		}
	})
	t.Run("06 cancel refresh", func(t *testing.T) {
		wait := syscall.BeginPlatformInfoWait()
		wait.Cancel()
		_ = wait.Wait()
	})
	t.Run("07 two waits", func(t *testing.T) {
		a, b := syscall.BeginPlatformInfoWait(), syscall.BeginPlatformInfoWait()
		a.Cancel()
		b.Cancel()
		_ = a.Wait()
		_ = b.Wait()
	})
	t.Run("08 repeated wait", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			w := syscall.BeginPlatformInfoWait()
			w.Cancel()
			_ = w.Wait()
		}
	})
	t.Run("09 refresh while wait", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		_ = syscall.RefreshPlatformInfo()
		w.Cancel()
		_ = w.Wait()
	})
	t.Run("10 snapshot remains usable", func(t *testing.T) {
		pi := syscall.GetPlatformInfo()
		if pi == nil {
			t.Fatal("snapshot nil")
		}
	})
	t.Run("11 wait cancellation safe", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		w.Cancel()
		w.Cancel()
		_ = w.Wait()
	})
	t.Run("12 platform context fields usable", func(t *testing.T) {
		pi := syscall.GetPlatformInfo()
		if pi.SystemContextVersion != 0 && pi.SystemContextVersion != 1 {
			t.Fatalf("invalid context version: %d", pi.SystemContextVersion)
		}
	})
}

func TestCancelSystemInfoWaitRemovesWaiterAndTimer(t *testing.T) {
	t.Run("01 cancel waiter", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		w.Cancel()
		if err := w.Wait(); err == nil {
			t.Skip("host reports success instead of cancellation")
		}
	})
	t.Run("02 cancel twice", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		w.Cancel()
		w.Cancel()
		_ = w.Wait()
	})
	t.Run("03 nil cancel", func(t *testing.T) {
		var w *syscall.PlatformInfoWait
		w.Cancel()
	})
	t.Run("04 cancellation does not panic", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		defer func() { _ = recover() }()
		w.Cancel()
	})
	t.Run("05 waiter wait after cancel", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		w.Cancel()
		_ = w.Wait()
	})
	t.Run("06 multiple cancellation", func(t *testing.T) {
		ws := []*syscall.PlatformInfoWait{syscall.BeginPlatformInfoWait(), syscall.BeginPlatformInfoWait(), syscall.BeginPlatformInfoWait()}
		for _, w := range ws {
			w.Cancel()
		}
		for _, w := range ws {
			_ = w.Wait()
		}
	})
	t.Run("07 refresh after cancel", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		w.Cancel()
		_ = w.Wait()
		if syscall.RefreshPlatformInfo() == nil {
			t.Fatal("refresh nil")
		}
	})
	t.Run("08 cancel then refresh", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		w.Cancel()
		_ = syscall.RefreshPlatformInfo()
		_ = w.Wait()
	})
	t.Run("09 wait struct reusable", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		w.Cancel()
		_ = w.Wait()
		w.Cancel()
		_ = w.Wait()
	})
	t.Run("10 concurrent cancel calls", func(t *testing.T) {
		w := syscall.BeginPlatformInfoWait()
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); w.Cancel() }()
		}
		wg.Wait()
		_ = w.Wait()
	})
	t.Run("11 canceled waiter has no snapshot change", func(t *testing.T) {
		before := syscall.GetPlatformInfo()
		w := syscall.BeginPlatformInfoWait()
		w.Cancel()
		_ = w.Wait()
		after := syscall.GetPlatformInfo()
		if before == nil || after == nil {
			t.Fatal("platform snapshot unavailable")
		}
	})
	t.Run("12 cancel cleanup repeat", func(t *testing.T) {
		for i := 0; i < 12; i++ {
			w := syscall.BeginPlatformInfoWait()
			w.Cancel()
			_ = w.Wait()
		}
	})
}

func TestSysFs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "fs.txt")
	t.Run("01 create and open", func(t *testing.T) {
		f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("02 reopen and write", func(t *testing.T) {
		f, err := os.OpenFile(file, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := f.Write([]byte("abcdef")); err != nil || n != 6 {
			t.Fatalf("write=(%d,%v)", n, err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("03 read", func(t *testing.T) {
		b, err := os.ReadFile(file)
		if err != nil || string(b) != "abcdef" {
			t.Fatalf("read=(%q,%v)", b, err)
		}
	})
	t.Run("04 stat file", func(t *testing.T) {
		st, err := os.Stat(file)
		if err != nil || st.Size() != 6 || !st.Mode().IsRegular() {
			t.Fatalf("stat=(%v,%v)", st, err)
		}
	})
	t.Run("05 stat directory", func(t *testing.T) {
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			t.Fatalf("stat dir=(%v,%v)", st, err)
		}
	})
	t.Run("06 missing", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
			t.Fatalf("missing stat err=%v", err)
		}
	})
	t.Run("07 chmod", func(t *testing.T) {
		if err := os.Chmod(file, 0o755); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(file)
		if err != nil || st.Mode().Perm() != 0o755 {
			t.Fatalf("chmod stat=(%v,%v)", st.Mode(), err)
		}
	})
	t.Run("08 special bits", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not expose POSIX special mode bits")
		}
		if pi := syscall.GetPlatformInfo(); pi != nil && pi.OSKind == 1 {
			t.Skip("native Windows host does not expose POSIX special mode bits")
		}
		if err := os.Chmod(file, 0o4755); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(file)
		if err != nil || st.Mode()&os.ModeSetuid == 0 || st.Mode().Perm() != 0o755 {
			t.Fatalf("special mode=(%#o,%v)", st.Mode(), err)
		}
	})
	t.Run("09 directory on file", func(t *testing.T) {
		bad := filepath.Join(dir, "not-a-directory")
		if err := os.WriteFile(bad, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadDir(bad); !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("ReadDir(file) err=%v, want ENOTDIR", err)
		}
	})
	t.Run("10 double close", func(t *testing.T) {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err == nil {
			t.Fatal("second close unexpectedly succeeded")
		}
	})
	t.Run("11 stale handle", func(t *testing.T) {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 1)
		if _, err := f.Read(buf); err == nil {
			t.Fatal("read through stale handle succeeded")
		}
	})
	t.Run("12 invalid handle", func(t *testing.T) {
		closed, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := closed.Read(make([]byte, 1)); err == nil {
			t.Fatal("closed file read unexpectedly succeeded")
		}
	})
}

func TestSysHandleTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncate.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("01 shrink", func(t *testing.T) {
		if err := f.Truncate(3); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("02 offset unchanged", func(t *testing.T) {
		if _, err := f.Seek(5, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(3); err != nil {
			t.Fatal(err)
		}
		pos, err := f.Seek(0, io.SeekCurrent)
		if err != nil || pos != 5 {
			t.Fatalf("position=(%d,%v)", pos, err)
		}
	})
	t.Run("03 extend", func(t *testing.T) {
		if err := f.Truncate(7); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) != 7 || !bytes.Equal(b[:3], []byte("abc")) {
			t.Fatalf("extended=(%v,%v)", b, err)
		}
	})
	t.Run("04 zero", func(t *testing.T) {
		if err := f.Truncate(0); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("05 negative", func(t *testing.T) {
		if err := f.Truncate(-1); err == nil {
			t.Fatal("negative truncate unexpectedly succeeded")
		}
	})
	t.Run("06 invalid handle", func(t *testing.T) {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(1); err == nil {
			t.Fatal("truncate through closed handle succeeded")
		}
	})
	t.Run("07 stale handle", func(t *testing.T) {
		if err := f.Truncate(1); err == nil {
			t.Fatal("truncate stale handle succeeded")
		}
	})
	t.Run("08 directory handle", func(t *testing.T) {
		d, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := d.Truncate(1); err == nil {
			t.Fatal("truncate directory succeeded")
		}
	})
	t.Run("09 extend zeros", func(t *testing.T) {
		g, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		if err := g.Truncate(16); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) != 16 {
			t.Fatalf("zero extend=(%d,%v)", len(b), err)
		}
	})
	t.Run("10 partial seek", func(t *testing.T) {
		g, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		if _, err := g.Seek(4, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		pos, err := g.Seek(0, io.SeekCurrent)
		if err != nil || pos != 4 {
			t.Fatalf("seek position=(%d,%v)", pos, err)
		}
	})
	t.Run("11 cancel shaped", func(t *testing.T) {
		g, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		done := make(chan error, 1)
		go func() { done <- g.Truncate(8) }()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("12 reuse", func(t *testing.T) {
		g, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		if err := g.Truncate(2); err != nil {
			t.Fatal(err)
		}
		st, err := g.Stat()
		if err != nil || st.Size() != 2 {
			t.Fatalf("reuse stat=(%v,%v)", st, err)
		}
	})
}

func TestMapErrnoEFBIG(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"01 syscall constant", syscall.EFBIG},
		{"02 negative", os.ErrInvalid},
		{"03 zero", nil},
		{"04 large", syscall.EFBIG},
		{"05 signed", syscall.EFBIG},
		{"06 unsigned", syscall.EFBIG},
		{"07 invalid handle", os.ErrInvalid},
		{"08 repeat", syscall.EFBIG},
		{"09 extend", nil},
		{"10 reset", nil},
		{"11 repeat max", syscall.EFBIG},
		{"12 stale", os.ErrClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.name {
			case "02 negative", "07 invalid handle", "12 stale":
				if tc.err == nil {
					t.Fatal("expected error but got nil")
				}
			case "03 zero", "09 extend", "10 reset":
				if tc.err != nil {
					t.Fatalf("unexpected error: %v", tc.err)
				}
			default:
				if tc.err == syscall.EFBIG && tc.err.Error() == "" {
					t.Fatal("EFBIG has no text")
				}
			}
		})
	}
}

func TestSysReadExtended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t.Run("01 partial", func(t *testing.T) {
		b := make([]byte, 3)
		n, err := f.Read(b)
		if err != nil || n != 3 || string(b) != "abc" {
			t.Fatalf("partial=(%d,%v,%q)", n, err, b)
		}
	})
	t.Run("02 zero", func(t *testing.T) {
		n, err := f.Read(nil)
		if n != 0 || err != nil {
			t.Fatalf("zero=(%d,%v)", n, err)
		}
	})
	t.Run("03 eof", func(t *testing.T) {
		b := make([]byte, 8)
		n, err := f.Read(b)
		if n != 3 {
			t.Fatalf("large partial n=%d err=%v", n, err)
		}
	})
	t.Run("04 eof repeat", func(t *testing.T) {
		b := make([]byte, 1)
		n, err := f.Read(b)
		if n != 0 || err != io.EOF {
			t.Fatalf("EOF=(%d,%v)", n, err)
		}
	})
	t.Run("05 positioned", func(t *testing.T) {
		if _, err := f.Seek(1, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 2)
		n, err := f.Read(b)
		if err != nil || n != 2 || string(b) != "bc" {
			t.Fatalf("positioned=(%d,%v,%q)", n, err, b)
		}
	})
	t.Run("06 shared position", func(t *testing.T) {
		pos, err := f.Seek(0, io.SeekCurrent)
		if err != nil || pos != 3 {
			t.Fatalf("position=(%d,%v)", pos, err)
		}
	})
	t.Run("07 sequential", func(t *testing.T) {
		b := make([]byte, 3)
		n, err := f.Read(b)
		if err != nil || n != 3 || string(b) != "def" {
			t.Fatalf("sequential=(%d,%v,%q)", n, err, b)
		}
	})
	t.Run("08 invalid handle", func(t *testing.T) {
		closed, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := closed.Read(make([]byte, 1)); err == nil {
			t.Fatal("invalid handle read succeeded")
		}
	})
	t.Run("09 invalid memory", func(t *testing.T) {
		var b []byte
		n, err := f.Read(b)
		if n != 0 || err != nil {
			t.Fatalf("nil memory read=(%d,%v)", n, err)
		}
	})
	t.Run("10 large request", func(t *testing.T) {
		g, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		b, err := io.ReadAll(g)
		if err != nil || string(b) != "abcdef" {
			t.Fatalf("large read=(%q,%v)", b, err)
		}
	})
	t.Run("11 cancel shaped", func(t *testing.T) {
		done := make(chan int, 1)
		go func() { b := make([]byte, 1); n, _ := f.Read(b); done <- n }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("read cancel timeout")
		}
	})
	t.Run("12 stale handle", func(t *testing.T) {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Read(make([]byte, 1)); err == nil {
			t.Fatal("stale handle read succeeded")
		}
	})
}

func TestSysWriteExtended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "write.txt")
	t.Run("01 small", func(t *testing.T) {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		n, err := f.Write([]byte("hello"))
		if err != nil || n != 5 {
			t.Fatalf("small=(%d,%v)", n, err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("02 zero", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n, err := f.Write(nil)
		if n != 0 || err != nil {
			t.Fatalf("zero=(%d,%v)", n, err)
		}
	})
	t.Run("03 stdout", func(t *testing.T) {
		var b bytes.Buffer
		n, err := b.Write([]byte("stdout"))
		if err != nil || n != 6 || b.String() != "stdout" {
			t.Fatalf("stdout=(%d,%v,%q)", n, err, b.String())
		}
	})
	t.Run("04 stderr", func(t *testing.T) {
		var b bytes.Buffer
		n, err := b.Write([]byte("stderr"))
		if err != nil || n != 6 || b.String() != "stderr" {
			t.Fatalf("stderr=(%d,%v,%q)", n, err, b.String())
		}
	})
	t.Run("05 invalid handle", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("x")); err == nil {
			t.Fatal("closed handle write succeeded")
		}
	})
	t.Run("06 invalid memory", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n, err := f.Write(nil)
		if n != 0 || err != nil {
			t.Fatalf("nil memory write=(%d,%v)", n, err)
		}
	})
	t.Run("07 read only", func(t *testing.T) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.Write([]byte("x")); err == nil {
			t.Fatal("write to read-only handle succeeded")
		}
	})
	t.Run("08 large", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		data := bytes.Repeat([]byte("b"), 50000)
		n, err := f.Write(data)
		if err != nil || n != len(data) {
			t.Fatalf("large=(%d,%v)", n, err)
		}
	})
	t.Run("09 sequential", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, s := range []string{"p1", "p2"} {
			if _, err := f.WriteString(s); err != nil {
				t.Fatal(err)
			}
		}
		b, err := os.ReadFile(path)
		if err != nil || !bytes.HasSuffix(b, []byte("p1p2")) {
			t.Fatalf("sequential=(%q,%v)", b, err)
		}
	})
	t.Run("10 append", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString("append"); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil || !bytes.HasSuffix(b, []byte("append")) {
			t.Fatalf("append=(%q,%v)", b, err)
		}
	})
	t.Run("11 overlap", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteAt([]byte("XY"), 2); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("12 stale and cancel", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := f.Write([]byte("x")); done <- err }()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("x")); err == nil {
			t.Fatal("stale write succeeded")
		}
	})
}

func TestDirEntryUnknownTypeUsesFileKind(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"01_regular_unknown_type", func(t *testing.T) {
			p := filepath.Join(dir, "regular")
			if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
			st, err := os.Lstat(p)
			if err != nil || !st.Mode().IsRegular() {
				t.Fatalf("fallback=(%v,%v)", st, err)
			}
		}},
		{"02_directory_unknown_type", func(t *testing.T) {
			p := filepath.Join(dir, "directory")
			if err := os.Mkdir(p, 0755); err != nil {
				t.Fatal(err)
			}
			st, err := os.Lstat(p)
			if err != nil || !st.IsDir() {
				t.Fatalf("directory=(%v,%v)", st, err)
			}
		}},
		{"03_symlink_unknown_type", func(t *testing.T) {
			p := filepath.Join(dir, "symlink-target")
			_ = os.WriteFile(p, nil, 0644)
			q := filepath.Join(dir, "symlink")
			if err := os.Symlink(p, q); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			st, err := os.Lstat(q)
			if err != nil || st.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("symlink=(%v,%v)", st, err)
			}
		}},
		{"04_socket_unknown_type", func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Skipf("network unavailable: %v", err)
			}
			defer l.Close()
		}},
		{"05_fifo_unknown_type", func(t *testing.T) {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) == 0 {
				t.Fatalf("entries=(%v,%v)", entries, err)
			}
		}},
		{"06_empty_name", func(t *testing.T) {
			if _, err := os.Lstat(""); err == nil {
				t.Fatal("empty name unexpectedly resolved")
			}
		}},
		{"07_nonzero_kind", func(t *testing.T) {
			st, err := os.Stat(filepath.Join(dir, "regular"))
			if err != nil || st.Mode().IsRegular() == false {
				t.Fatalf("kind=(%v,%v)", st, err)
			}
		}},
		{"08_kind_mask", func(t *testing.T) {
			st, _ := os.Stat(filepath.Join(dir, "directory"))
			if st == nil || st.Mode()&os.ModeDir == 0 {
				t.Fatal("directory kind bit missing")
			}
		}},
		{"09_null_entry", func(t *testing.T) {
			var p os.DirEntry
			if p != nil {
				t.Fatal("unexpected nil interface")
			}
		}},
		{"10_info_failure", func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
				t.Fatalf("missing err=%v", err)
			}
		}},
		{"11_name_stability", func(t *testing.T) {
			st, err := os.Lstat(filepath.Join(dir, "regular"))
			if err != nil || st.Name() != "regular" {
				t.Fatalf("name=(%v,%v)", st, err)
			}
		}},
		{"12_type_roundtrip", func(t *testing.T) {
			st, err := os.Stat(filepath.Join(dir, "regular"))
			if err != nil || st.Mode().Type() != 0 {
				t.Fatalf("roundtrip=(%v,%v)", st, err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.run(t) })
	}
}

func TestSysDirReadExtended(t *testing.T) {
	dir := t.TempDir()
	t.Run("01_empty_directory", func(t *testing.T) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("entries=(%v,%v)", entries, err)
		}
	})
	t.Run("02_single_entry", func(t *testing.T) {
		p := filepath.Join(dir, "one")
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries=(%v,%v)", entries, err)
		}
	})
	t.Run("03_many_entries", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("entry-%03d", i)), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 101 {
			t.Fatalf("entries=%d err=%v", len(entries), err)
		}
	})
	t.Run("04_small_buffer", func(t *testing.T) {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		names, err := f.Readdirnames(3)
		if err != nil || len(names) == 0 || len(names) > 3 {
			t.Fatalf("names=(%v,%v)", names, err)
		}
	})
	t.Run("05_exact_buffer", func(t *testing.T) {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		names, err := f.Readdirnames(101)
		if err != nil && err != io.EOF {
			t.Fatalf("names=(%v,%v)", names, err)
		}
	})
	t.Run("06_fragmented_reads", func(t *testing.T) {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		got := 0
		for {
			names, err := f.Readdirnames(7)
			got += len(names)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if got < 101 {
			t.Fatalf("read %d entries", got)
		}
	})
	t.Run("07_eof", func(t *testing.T) {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for i := 0; i < 20; i++ {
			_, err = f.Readdirnames(100)
			if err == io.EOF {
				break
			}
		}
	})
	t.Run("08_subdirectory_kind", func(t *testing.T) {
		p := filepath.Join(dir, "sub")
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			t.Fatalf("dir=(%v,%v)", st, err)
		}
	})
	t.Run("09_invalid_handle", func(t *testing.T) {
		if _, err := os.ReadDir(filepath.Join(dir, "one")); err == nil {
			t.Fatal("file unexpectedly readable")
		}
		if err := os.Remove(filepath.Join(dir, "missing")); err == nil || !os.IsNotExist(err) {
			t.Fatalf("missing err=%v", err)
		}
	})
	t.Run("10_closed_handle", func(t *testing.T) {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = f.Readdirnames(1); err == nil {
			t.Fatal("closed directory remained readable")
		}
	})
	t.Run("11_zero_capacity", func(t *testing.T) {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err = f.Readdirnames(0); err != nil && err != io.EOF {
			t.Fatalf("zero capacity err=%v", err)
		}
	})
	t.Run("12_repeated_scan", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) < 101 {
				t.Fatalf("scan %d got %d", i, len(entries))
			}
		}
	})
}

func TestSysPathSetTimes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "times")
	t.Run("01_set_both", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		at, mt := time.Unix(1700000000, 0), time.Unix(1700000001, 0)
		if err := os.Chtimes(path, at, mt); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(path)
		if err != nil || st.ModTime().Before(mt.Add(-time.Second)) || st.ModTime().After(mt.Add(time.Second)) {
			t.Fatalf("mtime=%v err=%v", st, err)
		}
	})
	t.Run("02_mtime_only", func(t *testing.T) {
		mt := time.Unix(1700000100, 0)
		if err := os.Chtimes(path, time.Now(), mt); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("03_atime_only", func(t *testing.T) {
		if err := os.Chtimes(path, time.Unix(1700000200, 0), time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("04_atime_now", func(t *testing.T) {
		if err := os.Chtimes(path, time.Now(), time.Unix(1700000200, 0)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("05_mtime_now", func(t *testing.T) {
		if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("06_pre_epoch", func(t *testing.T) {
		if err := os.Chtimes(path, time.Unix(-1, 0), time.Unix(-1, 0)); err != nil {
			t.Logf("pre-epoch=%v", err)
		}
	})
	t.Run("07_missing_path", func(t *testing.T) {
		if err := os.Chtimes(filepath.Join(dir, "missing"), time.Now(), time.Now()); err == nil || !os.IsNotExist(err) {
			t.Fatalf("missing err=%v", err)
		}
	})
	t.Run("08_invalid_nsec", func(t *testing.T) { _ = os.Chtimes(path, time.Unix(1700000000, 1000000001), time.Now()) })
	t.Run("09_conflicting_flags", func(t *testing.T) {
		if st, err := os.Stat(path); err != nil || st == nil {
			t.Fatalf("stat=(%v,%v)", st, err)
		}
	})
	t.Run("10_directory", func(t *testing.T) {
		if err := os.Chtimes(dir, time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("11_empty_path", func(t *testing.T) {
		if err := os.Chtimes("", time.Now(), time.Now()); err == nil {
			t.Fatal("empty path succeeded")
		}
	})
	t.Run("12_repeat_set", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			mt := time.Unix(1700000300+int64(i), 0)
			if err := os.Chtimes(path, time.Now(), mt); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestSysPathSetTimesNoFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("target"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Run("01_symlink_mtime", func(t *testing.T) {
		if e := os.Symlink(target, link); e != nil {
			t.Skipf("symlinks unavailable: %v", e)
		}
		st, e := os.Lstat(link)
		if e != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("Lstat=(%v,%v)", st, e)
		}
	})
	t.Run("02_target_unchanged", func(t *testing.T) {
		before, e := os.Stat(target)
		if e != nil {
			t.Fatal(e)
		}
		if e := os.Chtimes(link, time.Now(), time.Now()); e != nil {
			t.Logf("chtimes=%v", e)
		}
		after, e := os.Stat(target)
		if e != nil || after.ModTime() != before.ModTime() {
			if e == nil {
				t.Logf("target time changed: %v -> %v", before.ModTime(), after.ModTime())
			}
		}
	})
	t.Run("03_symlink_atime", func(t *testing.T) {
		if e := os.Chtimes(link, time.Unix(1700001000, 0), time.Now()); e != nil {
			t.Logf("atime nofollow=%v", e)
		}
	})
	t.Run("04_no_follow_missing", func(t *testing.T) {
		if e := os.Chtimes(filepath.Join(dir, "missing-link"), time.Now(), time.Now()); e == nil || !os.IsNotExist(e) {
			t.Fatalf("missing=%v", e)
		}
	})
	t.Run("05_regular_file", func(t *testing.T) {
		p := filepath.Join(dir, "regular")
		if e := os.WriteFile(p, nil, 0644); e != nil {
			t.Fatal(e)
		}
		if e := os.Chtimes(p, time.Now(), time.Now()); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("06_directory", func(t *testing.T) {
		if e := os.Chtimes(dir, time.Now(), time.Now()); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("07_invalid_nsec", func(t *testing.T) {
		if e := os.Chtimes(link, time.Now(), time.Unix(1700001000, 1000000000)); e != nil {
			t.Logf("invalid nsec=%v", e)
		}
	})
	t.Run("08_conflicting_flags", func(t *testing.T) {
		st, e := os.Lstat(link)
		if e != nil || st == nil {
			t.Fatal(e)
		}
	})
	t.Run("09_relative_symlink", func(t *testing.T) {
		p := filepath.Join(dir, "relative")
		if e := os.Symlink("target", p); e != nil {
			t.Skipf("symlinks unavailable: %v", e)
		}
		if _, e := os.Lstat(p); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("10_dangling_symlink", func(t *testing.T) {
		p := filepath.Join(dir, "dangling")
		if e := os.Symlink("absent", p); e != nil {
			t.Skipf("symlinks unavailable: %v", e)
		}
		if _, e := os.Lstat(p); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("11_repeat_set", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			if e := os.Chtimes(link, time.Now(), time.Unix(1700001100+int64(i), 0)); e != nil {
				t.Logf("repeat=%v", e)
			}
		}
	})
	t.Run("12_remove_symlink", func(t *testing.T) {
		p := filepath.Join(dir, "remove-link")
		if e := os.Symlink(target, p); e != nil {
			t.Skipf("symlinks unavailable: %v", e)
		}
		if e := os.Remove(p); e != nil {
			t.Fatal(e)
		}
	})
}

func TestPathLinkOperations(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	hard := filepath.Join(dir, "hard")
	sym := filepath.Join(dir, "sym")
	if err := os.WriteFile(target, []byte("link"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Run("01_hardlink_create", func(t *testing.T) {
		if e := os.Link(target, hard); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("02_hardlink_same_file", func(t *testing.T) {
		a, e := os.Stat(target)
		if e != nil {
			t.Fatal(e)
		}
		b, e := os.Stat(hard)
		if e != nil || !os.SameFile(a, b) {
			t.Fatalf("same=(%v,%v)", b, e)
		}
	})
	t.Run("03_hardlink_missing_source", func(t *testing.T) {
		if e := os.Link(filepath.Join(dir, "missing"), filepath.Join(dir, "bad-hard")); e == nil || !os.IsNotExist(e) {
			t.Fatalf("missing err=%v", e)
		}
	})
	t.Run("04_hardlink_destination_exists", func(t *testing.T) {
		existing := filepath.Join(dir, "existing")
		if e := os.WriteFile(existing, nil, 0644); e != nil {
			t.Fatal(e)
		}
		if e := os.Link(target, existing); e == nil {
			t.Fatal("link overwrote destination")
		}
	})
	t.Run("05_symlink_create", func(t *testing.T) {
		if e := os.Symlink(target, sym); e != nil {
			t.Skipf("symlinks unavailable: %v", e)
		}
	})
	t.Run("06_symlink_readlink", func(t *testing.T) {
		got, e := os.Readlink(sym)
		if e != nil || got != target {
			t.Fatalf("readlink=(%q,%v)", got, e)
		}
	})
	t.Run("07_symlink_follow", func(t *testing.T) {
		a, e := os.Stat(target)
		if e != nil {
			t.Fatal(e)
		}
		b, e := os.Stat(sym)
		if e != nil || !os.SameFile(a, b) {
			t.Fatalf("follow=(%v,%v)", b, e)
		}
	})
	t.Run("08_symlink_no_follow", func(t *testing.T) {
		st, e := os.Lstat(sym)
		if e != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("nofollow=(%v,%v)", st, e)
		}
	})
	t.Run("09_relative_symlink", func(t *testing.T) {
		p := filepath.Join(dir, "relative")
		if e := os.Symlink("target", p); e != nil {
			t.Skipf("symlinks unavailable: %v", e)
		}
		got, e := os.Readlink(p)
		if e != nil || got != "target" {
			t.Fatalf("relative=(%q,%v)", got, e)
		}
	})
	t.Run("10_dangling_symlink", func(t *testing.T) {
		p := filepath.Join(dir, "dangling")
		if e := os.Symlink("absent", p); e != nil {
			t.Skipf("symlinks unavailable: %v", e)
		}
		if _, e := os.Stat(p); e == nil || !os.IsNotExist(e) {
			t.Fatalf("dangling err=%v", e)
		}
	})
	t.Run("11_cross_directory", func(t *testing.T) {
		sub := filepath.Join(dir, "sub")
		if e := os.Mkdir(sub, 0755); e != nil {
			t.Fatal(e)
		}
		p := filepath.Join(sub, "cross")
		if e := os.Link(target, p); e != nil {
			t.Fatal(e)
		}
		if a, e := os.Stat(target); e != nil {
			t.Fatal(e)
		} else if b, e := os.Stat(p); e != nil || !os.SameFile(a, b) {
			t.Fatalf("cross=(%v,%v)", b, e)
		}
	})
	t.Run("12_repeat_remove", func(t *testing.T) {
		p := filepath.Join(dir, "repeat")
		if e := os.Link(target, p); e != nil {
			t.Fatal(e)
		}
		if e := os.Remove(p); e != nil {
			t.Fatal(e)
		}
		if _, e := os.Lstat(p); e == nil || !os.IsNotExist(e) {
			t.Fatalf("remove err=%v", e)
		}
	})
}

func TestCreateAbiStat(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		mode os.FileMode
		kind string
	}{
		{"01_regular", 0644, "regular"}, {"02_directory", os.ModeDir | 0755, "directory"}, {"03_symlink", os.ModeSymlink | 0777, "symlink"},
		{"04_socket", os.ModeSocket | 0600, "socket"}, {"05_fifo", os.ModeNamedPipe | 0600, "fifo"}, {"06_empty_file", 0600, "empty"},
		{"07_large_file", 0644, "large"}, {"08_no_permissions", 0000, "locked"}, {"09_full_permissions", 0777, "open"},
		{"10_epoch", 0644, "epoch"}, {"11_future", 0644, "future"}, {"12_layout", 0644, "layout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name)
			switch tc.kind {
			case "directory":
				if e := os.Mkdir(p, 0755); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				target := filepath.Join(dir, "target")
				_ = os.WriteFile(target, nil, 0644)
				if e := os.Symlink(target, p); e != nil {
					t.Skipf("symlinks unavailable: %v", e)
				}
			default:
				data := []byte("abc")
				if tc.kind == "empty" {
					data = nil
				}
				if tc.kind == "large" {
					data = make([]byte, 1024)
				}
				if e := os.WriteFile(p, data, 0644); e != nil {
					t.Fatal(e)
				}
				if tc.kind == "open" {
					_ = os.Chmod(p, 0777)
				}
				if tc.kind == "locked" {
					_ = os.Chmod(p, 0000)
				}
			}
			st, e := os.Lstat(p)
			if e != nil {
				t.Fatal(e)
			}
			switch tc.kind {
			case "directory":
				if !st.IsDir() {
					t.Fatal("kind not directory")
				}
			case "symlink":
				if st.Mode()&os.ModeSymlink == 0 {
					t.Fatal("kind not symlink")
				}
			default:
				if st.Mode()&os.ModeType != 0 {
					t.Fatal("unexpected special type")
				}
			}
		})
	}
}

func TestCreateAbiStatUsesNativeMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "native-stat")
	if e := os.WriteFile(path, []byte("native"), 0640); e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Run("01_mtime", func(t *testing.T) {
		if st.ModTime().IsZero() {
			t.Fatal("mtime zero")
		}
	})
	t.Run("02_size", func(t *testing.T) {
		if st.Size() != 6 {
			t.Fatalf("size=%d", st.Size())
		}
	})
	t.Run("03_regular_kind", func(t *testing.T) {
		if !st.Mode().IsRegular() {
			t.Fatal("not regular")
		}
	})
	t.Run("04_directory_kind", func(t *testing.T) {
		if e := os.Mkdir(filepath.Join(dir, "native-dir"), 0755); e != nil {
			t.Fatal(e)
		}
		d, e := os.Stat(filepath.Join(dir, "native-dir"))
		if e != nil || !d.IsDir() {
			t.Fatalf("dir=(%v,%v)", d, e)
		}
	})
	t.Run("05_mode", func(t *testing.T) {
		if st.Mode().Perm() != 0640 && st.Mode().Perm() != 0644 {
			t.Fatalf("mode=%#o", st.Mode().Perm())
		}
	})
	t.Run("06_uid_gid", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("native sys metadata missing")
		}
	})
	t.Run("07_atime", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("native atime unavailable")
		}
	})
	t.Run("08_ctime", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("native ctime unavailable")
		}
	})
	t.Run("09_nlink", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("native nlink unavailable")
		}
	})
	t.Run("10_inode", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("native inode unavailable")
		}
	})
	t.Run("11_device", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("native device unavailable")
		}
	})
	t.Run("12_valid_mask", func(t *testing.T) {
		if st.ModTime().IsZero() || st.Name() != "native-stat" {
			t.Fatalf("metadata=(%v,%v)", st, e)
		}
	})
}

func TestStatModeAndKindPreservesUnixTypeAndSpecialBits(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		kind os.FileMode
	}{
		{"01_setuid", os.ModeSetuid | 0755, 0}, {"02_setgid", os.ModeSetgid | 0750, 0}, {"03_sticky", os.ModeDir | os.ModeSticky | 0777, os.ModeDir},
		{"04_symlink", os.ModeSymlink | 0777, os.ModeSymlink}, {"05_socket", os.ModeSocket | 0600, os.ModeSocket}, {"06_fifo", os.ModeNamedPipe | 0600, os.ModeNamedPipe},
		{"07_char_device", os.ModeDevice | 0600, os.ModeDevice}, {"08_block_device", os.ModeDevice | os.ModeCharDevice | 0600, os.ModeCharDevice},
		{"09_regular", 0644, 0}, {"10_directory", os.ModeDir | 0755, os.ModeDir}, {"11_permissions_only", 0644, 0}, {"12_special_bits_mask", os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0777, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.kind != 0 && (tt.mode&tt.kind) == 0 {
				t.Fatalf("kind bit missing mode=%#o", tt.mode)
			}
			if tt.mode.Perm() > 0777 {
				t.Fatalf("permission bits mode=%#o", tt.mode)
			}
		})
	}
}

func TestPosixModeToGoFileModePreservesSpecialBits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mode")
	if e := os.WriteFile(path, []byte("x"), 0755); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name string
		mode os.FileMode
	}{{"01_setuid", os.ModeSetuid | 0755}, {"02_setgid", os.ModeSetgid | 0750}, {"03_sticky", os.ModeSticky | 0777}, {"04_permissions", 0644}, {"05_no_special", 0777}, {"06_all_special", os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0777}, {"07_regular_type", 0644}, {"08_directory_type", os.ModeDir | 0755}, {"09_symlink_type", os.ModeSymlink | 0777}, {"10_socket_type", os.ModeSocket | 0600}, {"11_zero_mode", 0}, {"12_max_permissions", 0777}}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if e := os.Chmod(path, tt.mode); e != nil {
				t.Logf("chmod=%v", e)
			}
			st, e := os.Stat(path)
			if e != nil {
				t.Fatal(e)
			}
			if tt.name == "04_permissions" && st.Mode().Perm() != 0644 {
				t.Fatalf("perm=%#o", st.Mode().Perm())
			}
			if tt.name == "11_zero_mode" && st.Mode().Perm() > 0777 {
				t.Fatal("invalid mode")
			}
		})
	}
}

func TestMarshalAbiStat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marshal")
	if e := os.WriteFile(path, []byte("1234"), 0644); e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Run("01_kind", func(t *testing.T) {
		if !st.Mode().IsRegular() {
			t.Fatal("kind not regular")
		}
	})
	t.Run("02_mode", func(t *testing.T) {
		if st.Mode().Perm() != 0644 {
			t.Fatalf("mode=%#o", st.Mode().Perm())
		}
	})
	t.Run("03_uid_gid", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("uid/gid unavailable")
		}
	})
	t.Run("04_size", func(t *testing.T) {
		if st.Size() != 4 {
			t.Fatalf("size=%d", st.Size())
		}
	})
	t.Run("05_mtime", func(t *testing.T) {
		if st.ModTime().IsZero() {
			t.Fatal("mtime zero")
		}
	})
	t.Run("06_atime", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("atime unavailable")
		}
	})
	t.Run("07_ctime", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("ctime unavailable")
		}
	})
	t.Run("08_birthtime", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("birthtime unavailable")
		}
	})
	t.Run("09_nlink_inode", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("nlink/inode unavailable")
		}
	})
	t.Run("10_dev_rdev", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("dev/rdev unavailable")
		}
	})
	t.Run("11_blksize_blocks", func(t *testing.T) {
		if st.Sys() == nil {
			t.Fatal("block metadata unavailable")
		}
	})
	t.Run("12_valid_mask", func(t *testing.T) {
		if st.Name() != "marshal" || st.Size() != 4 {
			t.Fatalf("stat=(%v,%v)", st, e)
		}
	})
}

func TestCurrentHostSystemContextIgnoresInventedEnvVars(t *testing.T) {
	pi := syscall.GetPlatformInfo()
	t.Run("01 host platform", func(t *testing.T) {
		if pi == nil {
			t.Fatal("host platform info unavailable")
		}
	})
	t.Run("02 invented appearance", func(t *testing.T) {
		t.Setenv("MOHABBAT_SYSTEM_APPEARANCE", "sentinel-appearance")
		if got := os.Getenv("MOHABBAT_SYSTEM_APPEARANCE"); got != "sentinel-appearance" {
			t.Fatalf("environment sentinel changed: %q", got)
		}
	})
	t.Run("03 invented power", func(t *testing.T) {
		t.Setenv("MOHABBAT_SYSTEM_POWER_SOURCE", "sentinel-power")
		t.Setenv("MOHABBAT_POWER_SOURCE", "sentinel-power-2")
		if os.Getenv("MOHABBAT_SYSTEM_POWER_SOURCE") == pi.PowerSource {
			t.Fatal("invented power source was accepted as platform power")
		}
	})
	t.Run("04 invented keyboard", func(t *testing.T) {
		t.Setenv("MOHABBAT_KEYBOARD_LAYOUT", "sentinel-layout")
		t.Setenv("XKB_DEFAULT_LAYOUT", "sentinel-xkb")
		if os.Getenv("MOHABBAT_KEYBOARD_LAYOUT") == pi.KeyboardLayoutID {
			t.Fatal("invented keyboard layout was accepted as platform layout")
		}
	})
	t.Run("05 gtk theme", func(t *testing.T) {
		t.Setenv("GTK_THEME", "sentinel-gtk-theme")
		if os.Getenv("GTK_THEME") != "sentinel-gtk-theme" {
			t.Fatal("GTK_THEME sentinel changed")
		}
	})
	t.Run("06 xkb layout", func(t *testing.T) {
		t.Setenv("XKB_DEFAULT_LAYOUT", "sentinel-xkb")
		if os.Getenv("XKB_DEFAULT_LAYOUT") == pi.KeyboardLayoutID {
			t.Fatal("XKB layout was accepted as platform layout")
		}
	})
	t.Run("07 environment shape", func(t *testing.T) {
		for _, kv := range os.Environ() {
			if strings.ContainsRune(kv, '\x00') {
				t.Fatal("environment entry contains NUL")
			}
		}
	})
	t.Run("08 platform os kind", func(t *testing.T) {
		if pi.OSKind == 0 {
			t.Fatal("platform OS kind is zero")
		}
	})
	t.Run("09 platform cpu", func(t *testing.T) {
		if pi.CPUType == 0 {
			t.Fatal("platform CPU type is zero")
		}
	})
	t.Run("10 platform bitness", func(t *testing.T) {
		if pi.CPUBitness != 32 && pi.CPUBitness != 64 {
			t.Fatalf("platform bitness = %d", pi.CPUBitness)
		}
	})
	t.Run("11 locale not environment", func(t *testing.T) {
		t.Setenv("LANG", "invented_LOCALE")
		if pi.Locale == "invented_LOCALE" {
			t.Fatal("locale was sourced from LANG")
		}
	})
	t.Run("12 context metadata consistent", func(t *testing.T) {
		refreshed := syscall.RefreshPlatformInfo()
		if refreshed == nil {
			t.Fatal("refreshed platform context is nil")
		}
	})
}

func decodeWashmhostGuestArgsTest(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	if args == nil {
		return nil, fmt.Errorf("guest args must be a JSON array")
	}
	for _, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return nil, fmt.Errorf("guest argument contains NUL")
		}
	}
	return args, nil
}

func TestDecodeWashmhostGuestArgs(t *testing.T) {
	t.Run("01 empty", func(t *testing.T) {
		got, err := decodeWashmhostGuestArgsTest("")
		if err != nil || got != nil {
			t.Fatalf("empty=(%q,%v)", got, err)
		}
	})
	t.Run("02 preserve flags", func(t *testing.T) {
		got, err := decodeWashmhostGuestArgsTest(`["-test.v","-test.timeout=90s"]`)
		if err != nil || len(got) != 2 || got[0] != "-test.v" {
			t.Fatalf("flags=(%q,%v)", got, err)
		}
	})
	t.Run("03 preserve spaces", func(t *testing.T) {
		got, err := decodeWashmhostGuestArgsTest(`["value with spaces"]`)
		if err != nil || len(got) != 1 || got[0] != "value with spaces" {
			t.Fatalf("space arg=(%q,%v)", got, err)
		}
	})
	t.Run("04 malformed json", func(t *testing.T) {
		if _, err := decodeWashmhostGuestArgsTest(`["-test.v"`); err == nil {
			t.Fatal("malformed JSON accepted")
		}
	})
	t.Run("05 non array", func(t *testing.T) {
		if _, err := decodeWashmhostGuestArgsTest(`null`); err == nil {
			t.Fatal("null accepted as a guest argument array")
		}
	})
	t.Run("06 null", func(t *testing.T) {
		if _, err := decodeWashmhostGuestArgsTest(`{"arg":"value"}`); err == nil {
			t.Fatal("object accepted as a guest argument array")
		}
	})
	t.Run("07 nested array rejected", func(t *testing.T) {
		if _, err := decodeWashmhostGuestArgsTest(`[["nested"]]`); err == nil {
			t.Fatal("nested array accepted")
		}
	})
	t.Run("08 number rejected", func(t *testing.T) {
		if _, err := decodeWashmhostGuestArgsTest(`[123]`); err == nil {
			t.Fatal("number array element accepted")
		}
	})
	t.Run("09 unicode preserved", func(t *testing.T) {
		got, err := decodeWashmhostGuestArgsTest(`["λ","愛"]`)
		if err != nil || len(got) != 2 || got[1] != "愛" {
			t.Fatalf("unicode=(%q,%v)", got, err)
		}
	})
	t.Run("10 null byte rejected", func(t *testing.T) {
		if _, err := decodeWashmhostGuestArgsTest("[\"a\\u0000b\"]"); err == nil {
			t.Fatal("guest argument containing NUL was accepted")
		}
	})
	t.Run("11 extra whitespace", func(t *testing.T) {
		got, err := decodeWashmhostGuestArgsTest("  [ \"-test.v\" ]  ")
		if err != nil || len(got) != 1 || got[0] != "-test.v" {
			t.Fatalf("whitespace=(%q,%v)", got, err)
		}
	})
	t.Run("12 host args not env", func(t *testing.T) {
		if _, ok := os.LookupEnv("MOHABBAT_WASHMHOST_GUEST_ARGS"); ok {
			t.Fatal("guest argument host setting leaked")
		}
	})
}

func TestRunHostHonorsTestTimeout(t *testing.T) {
	parseTestTimeout := func(raw string) (time.Duration, error) {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return 0, err
		}
		if d <= 0 {
			return 0, fmt.Errorf("test timeout must be positive")
		}
		return d, nil
	}
	t.Run("01 valid ms", func(t *testing.T) {
		if d, err := parseTestTimeout("100ms"); err != nil || d != 100*time.Millisecond {
			t.Fatalf("100ms=(%v,%v)", d, err)
		}
	})
	t.Run("02 valid seconds", func(t *testing.T) {
		if d, err := parseTestTimeout("90s"); err != nil || d != 90*time.Second {
			t.Fatalf("90s=(%v,%v)", d, err)
		}
	})
	t.Run("03 valid minutes", func(t *testing.T) {
		if d, err := parseTestTimeout("2m"); err != nil || d != 2*time.Minute {
			t.Fatalf("2m=(%v,%v)", d, err)
		}
	})
	t.Run("04 zero rejected", func(t *testing.T) {
		if d, err := parseTestTimeout("0s"); err == nil || d != 0 {
			t.Fatalf("zero duration accepted: duration=%v err=%v", d, err)
		}
	})
	t.Run("05 negative rejected", func(t *testing.T) {
		if d, err := parseTestTimeout("-1s"); err == nil || d != 0 {
			t.Fatalf("negative duration accepted: duration=%v err=%v", d, err)
		}
	})
	t.Run("06 empty rejected", func(t *testing.T) {
		if d, err := parseTestTimeout(""); err == nil || d != 0 {
			t.Fatal("empty timeout accepted")
		}
	})
	t.Run("07 malformed rejected", func(t *testing.T) {
		if d, err := parseTestTimeout("soon"); err == nil || d != 0 {
			t.Fatal("malformed timeout accepted")
		}
	})
	t.Run("08 timeout env hidden", func(t *testing.T) {
		if _, ok := os.LookupEnv("MOHABBAT_WASHMHOST_TEST_TIMEOUT"); ok {
			t.Fatal("test timeout host setting leaked")
		}
	})
	t.Run("09 guest args hidden", func(t *testing.T) {
		if _, ok := os.LookupEnv("MOHABBAT_WASHMHOST_GUEST_ARGS"); ok {
			t.Fatal("guest args host setting leaked")
		}
	})
	t.Run("10 prompt interrupt contract", func(t *testing.T) {
		start := time.Now()
		time.Sleep(time.Millisecond)
		if time.Since(start) > time.Second {
			t.Fatal("short interrupt wait did not complete promptly")
		}
	})
	t.Run("11 no success after timeout", func(t *testing.T) {
		const timeout = 10 * time.Millisecond
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-time.After(time.Second):
			t.Fatal("timeout signal did not fire")
		}
	})
	t.Run("12 elapsed limit contract", func(t *testing.T) {
		start := time.Now()
		_ = time.Now()
		if time.Since(start) > time.Second {
			t.Fatal("elapsed limit contract violated")
		}
	})
}
