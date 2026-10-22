package main

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func runPathSetTimesTest(t *testing.T, env *HostEnv, mod *mockModule, path string, req pathTimesRequest) uint32 {
	t.Helper()
	const (
		pathPtr = uint32(0x100)
		ovPtr   = uint32(0x200)
	)
	if !mod.Memory().Write(pathPtr, []byte(path)) {
		t.Fatal("failed to write path to guest memory")
	}
	if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
		t.Fatal("failed to clear operation result")
	}
	env.sys_path_set_times(context.Background(), mod, []uint64{
		uint64(ovPtr), uint64(pathPtr), uint64(len(path)),
		uint64(req.atimeSec), uint64(req.atimeNsec),
		uint64(req.mtimeSec), uint64(req.mtimeNsec), uint64(req.flags),
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var result []byte
	for {
		env.Poll(ctx, mod)
		var ok bool
		result, ok = mod.Memory().Read(ovPtr, 24)
		if !ok {
			t.Fatal("failed to read operation result")
		}
		if binary.LittleEndian.Uint32(result)&1 != 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("timed out waiting for timestamp completion")
		}
	}
	if got := binary.LittleEndian.Uint32(result); got != 1 {
		t.Fatalf("operation flags = %d, want completion flag 1", got)
	}
	return binary.LittleEndian.Uint32(result[4:])
}

func getTestFileTimes(t *testing.T, path string, noFollow bool) (time.Time, time.Time) {
	t.Helper()
	var (
		info os.FileInfo
		err  error
	)
	if noFollow {
		info, err = os.Lstat(path)
	} else {
		info, err = os.Stat(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	stat := createAbiStatAt(info, path, noFollow)
	if stat.ValidMask&statValidAtime == 0 {
		t.Fatal("host stat did not provide atime")
	}
	return time.Unix(0, stat.AccessedNs), time.Unix(0, stat.ModifiedNs)
}

func assertTimeNear(t *testing.T, got, want time.Time) {
	t.Helper()
	if delta := got.Sub(want); delta < -time.Second || delta > time.Second {
		t.Fatalf("timestamp = %s, want approximately %s (delta %s)", got, want, delta)
	}
}

func TestSysPathSetTimes(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(4096)
	path := filepath.Join(t.TempDir(), "timestamps.txt")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	wantAtime := time.Unix(1_700_000_000, 123_456_700)
	wantMtime := time.Unix(1_700_000_100, 765_432_100)
	if got := runPathSetTimesTest(t, env, mod, path, pathTimesRequest{
		atimeSec: wantAtime.Unix(), atimeNsec: uint32(wantAtime.Nanosecond()),
		mtimeSec: wantMtime.Unix(), mtimeNsec: uint32(wantMtime.Nanosecond()),
		flags: pathTimesSetAtime | pathTimesSetMtime,
	}); got != wasiSuccess {
		t.Fatalf("setting timestamps returned WASI errno %d", got)
	}
	gotAtime, gotMtime := getTestFileTimes(t, path, false)
	assertTimeNear(t, gotAtime, wantAtime)
	assertTimeNear(t, gotMtime, wantMtime)

	wantMtime = time.Unix(1_700_000_200, 100)
	if got := runPathSetTimesTest(t, env, mod, path, pathTimesRequest{
		mtimeSec: wantMtime.Unix(), mtimeNsec: uint32(wantMtime.Nanosecond()),
		flags: pathTimesSetMtime,
	}); got != wasiSuccess {
		t.Fatalf("setting mtime while omitting atime returned WASI errno %d", got)
	}
	gotAtime, gotMtime = getTestFileTimes(t, path, false)
	assertTimeNear(t, gotAtime, wantAtime)
	assertTimeNear(t, gotMtime, wantMtime)

	nowBefore := time.Now().Add(-time.Second)
	if got := runPathSetTimesTest(t, env, mod, path, pathTimesRequest{
		flags: pathTimesAtimeNow | pathTimesMtimeNow,
	}); got != wasiSuccess {
		t.Fatalf("setting timestamps to now returned WASI errno %d", got)
	}
	gotAtime, gotMtime = getTestFileTimes(t, path, false)
	assertTimeNear(t, gotAtime, time.Now())
	assertTimeNear(t, gotMtime, time.Now())
	if gotAtime.Before(nowBefore) || gotMtime.Before(nowBefore) {
		t.Fatalf("now timestamps precede the request: atime=%s mtime=%s", gotAtime, gotMtime)
	}

	if got := runPathSetTimesTest(t, env, mod, path, pathTimesRequest{
		flags: pathTimesSetAtime | pathTimesAtimeNow,
	}); got != wasiEINVAL {
		t.Fatalf("contradictory timestamp flags returned WASI errno %d, want %d", got, wasiEINVAL)
	}
	if got := runPathSetTimesTest(t, env, mod, path, pathTimesRequest{
		mtimeNsec: 1_000_000_000, flags: pathTimesSetMtime,
	}); got != wasiEINVAL {
		t.Fatalf("invalid nanoseconds returned WASI errno %d, want %d", got, wasiEINVAL)
	}
}

func TestSysPathSetTimesNoFollowSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges on Windows")
	}
	env := NewHostEnv()
	mod := newMockModule(4096)
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	wantMtime := time.Unix(1_700_000_300, 987_654_300)
	if got := runPathSetTimesTest(t, env, mod, link, pathTimesRequest{
		mtimeSec: wantMtime.Unix(), mtimeNsec: uint32(wantMtime.Nanosecond()),
		flags: pathTimesSetMtime | pathTimesNoFollow,
	}); got != wasiSuccess {
		t.Fatalf("setting symlink mtime returned WASI errno %d", got)
	}
	_, gotLinkMtime := getTestFileTimes(t, link, true)
	assertTimeNear(t, gotLinkMtime, wantMtime)
	_, gotTargetMtime := getTestFileTimes(t, target, false)
	if gotTargetMtime.Equal(wantMtime) {
		t.Fatal("no-follow timestamp update changed the symlink target")
	}
}
