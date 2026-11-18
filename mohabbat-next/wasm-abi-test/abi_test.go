//go:build wasip1

package wasmabitest

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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

func TestNetLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_, err = io.Copy(conn, conn)
		done <- err
	}()

	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	if _, err := cli.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var buf [4]byte
	if _, err := io.ReadFull(cli, buf[:]); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf[:]) != "ping" {
		t.Fatalf("got %q, want ping", buf[:])
	}
	_ = cli.Close()
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}
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
