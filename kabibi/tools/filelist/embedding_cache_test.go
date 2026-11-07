package filelist

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai"
)

type cacheTestEmbedder struct {
	calls atomic.Int32
}

func (e *cacheTestEmbedder) CacheIdentity() string {
	return "test-provider:test-model:v1"
}

func (e *cacheTestEmbedder) Embed(parts []ai.PromptPart) ([]float32, error) {
	e.calls.Add(1)
	return []float32{1, 0}, nil
}

func TestEmbeddingCachePersistsAndScopesRecords(t *testing.T) {
	cache := &embeddingCache{dir: t.TempDir()}
	signature := []byte("signature")
	vector := []float32{0.25, -0.5, 0.75}
	if err := cache.put(signature, "provider:model", "content", "recipe-v1", vector); err != nil {
		t.Fatalf("put(): %v", err)
	}

	reopened := &embeddingCache{dir: cache.dir}
	got, ok, err := reopened.get(signature, "provider:model", "content", "recipe-v1", len(vector))
	if err != nil {
		t.Fatalf("get(): %v", err)
	}
	if !ok || !reflect.DeepEqual(got, vector) {
		t.Fatalf("get() = %v, %v; want %v, true", got, ok, vector)
	}
	for _, key := range []struct {
		identity  string
		kind      string
		recipe    string
		dimension int
	}{
		{"other:model", "content", "recipe-v1", len(vector)},
		{"provider:model", "filename", "recipe-v1", len(vector)},
		{"provider:model", "content", "recipe-v2", len(vector)},
		{"provider:model", "content", "recipe-v1", len(vector) + 1},
	} {
		_, found, err := reopened.get(signature, key.identity, key.kind, key.recipe, key.dimension)
		if err != nil {
			t.Fatalf("get with key %+v: %v", key, err)
		}
		if found {
			t.Fatalf("get with incompatible key %+v unexpectedly hit", key)
		}
	}

	entries, err := os.ReadDir(cache.dir)
	if err != nil {
		t.Fatalf("ReadDir(): %v", err)
	}
	if len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".vec" {
		t.Fatalf("cache entries = %v, want one binary vector record", entries)
	}
	record, err := os.ReadFile(filepath.Join(cache.dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if bytes.Contains(record, []byte("source contents")) || bytes.Contains(record, []byte(".json")) {
		t.Fatalf("record contains source content or JSON data")
	}
	if !bytes.Equal(record[44:48], []byte{0x00, 0x00, 0x80, 0x3e}) {
		t.Fatalf("vector is not encoded as little-endian float32: %x", record[44:48])
	}
}

func TestNewEmbeddingCacheUsesUserCacheDirectory(t *testing.T) {
	userCache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir(): %v", err)
	}
	cache, err := newEmbeddingCache()
	if err != nil {
		t.Fatalf("newEmbeddingCache(): %v", err)
	}
	want := filepath.Join(userCache, "kabibi", "filelist", embeddingCacheDirName)
	if cache.dir != want {
		t.Fatalf("cache directory = %q, want %q", cache.dir, want)
	}
}

func TestEmbeddingCacheRejectsCorruptRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "marker",
			mutate: func(record []byte) []byte {
				record[0] ^= 0xff
				return record
			},
		},
		{
			name: "checksum",
			mutate: func(record []byte) []byte {
				record[len(record)-1] ^= 0xff
				return record
			},
		},
		{
			name: "trailing bytes",
			mutate: func(record []byte) []byte {
				return append(record, 1)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := &embeddingCache{dir: t.TempDir()}
			signature := []byte("signature")
			if err := cache.put(signature, "model", "content", "recipe", []float32{1}); err != nil {
				t.Fatalf("put(): %v", err)
			}
			recordPath := cache.recordPath(signature, "model", "content", "recipe", 1)
			record, err := os.ReadFile(recordPath)
			if err != nil {
				t.Fatalf("read record: %v", err)
			}
			if err := os.WriteFile(recordPath, test.mutate(record), 0o600); err != nil {
				t.Fatalf("corrupt record: %v", err)
			}
			_, ok, err := cache.get(signature, "model", "content", "recipe", 1)
			if err == nil || ok {
				t.Fatalf("get() = hit %v, error %v; want an explicit corruption error", ok, err)
			}
		})
	}
}

func TestEmbeddingCacheRejectsInvalidVectors(t *testing.T) {
	cache := &embeddingCache{dir: t.TempDir()}
	for _, vector := range [][]float32{nil, {float32(math.NaN())}, {float32(math.Inf(1))}} {
		if err := cache.put([]byte("sig"), "model", "filename", "recipe", vector); err == nil {
			t.Fatalf("put(%v) unexpectedly succeeded", vector)
		}
	}
}

func TestEntrySignatureUsesEntryFieldsAndOptionalTimes(t *testing.T) {
	base := signatureTestInfo{
		name: "same.go",
		size: 12,
		mode: 0o644,
		mod:  time.Unix(100, 25),
		sys: signatureTestSys{
			Ctim:          signatureTestTimespec{Sec: 11, Nsec: 4},
			Birthtimespec: signatureTestTimespec{Sec: 7, Nsec: 9},
		},
	}
	baseSys := base.sys.(signatureTestSys)
	want := entrySignature(filepath.Join("a", "same.go"), base)
	if !bytes.Equal(want, entrySignature(filepath.Join("b", "same.go"), base)) {
		t.Fatal("directory path unexpectedly affects the entry signature")
	}
	for name, changed := range map[string]signatureTestInfo{
		"basename":   {name: "other.go", size: base.size, mode: base.mode, mod: base.mod, sys: base.sys},
		"size":       {name: base.name, size: base.size + 1, mode: base.mode, mod: base.mod, sys: base.sys},
		"mode":       {name: base.name, size: base.size, mode: 0o755, mod: base.mod, sys: base.sys},
		"mtime":      {name: base.name, size: base.size, mode: base.mode, mod: base.mod.Add(time.Nanosecond), sys: base.sys},
		"ctime":      {name: base.name, size: base.size, mode: base.mode, mod: base.mod, sys: signatureTestSys{Ctim: signatureTestTimespec{Sec: 12, Nsec: 4}, Birthtimespec: baseSys.Birthtimespec}},
		"birth":      {name: base.name, size: base.size, mode: base.mode, mod: base.mod, sys: signatureTestSys{Ctim: baseSys.Ctim, Birthtimespec: signatureTestTimespec{Sec: 8, Nsec: 9}}},
		"attributes": {name: base.name, size: base.size, mode: base.mode, mod: base.mod, sys: signatureTestSys{Ctim: baseSys.Ctim, Birthtimespec: baseSys.Birthtimespec, FileAttributes: 0x20}},
	} {
		if bytes.Equal(want, entrySignature(filepath.Join("a", changed.name), changed)) {
			t.Errorf("%s change did not change the signature", name)
		}
	}
	withoutOptional := base
	withoutOptional.sys = struct{}{}
	withOptional := base
	withOptional.sys = struct {
		Ctim signatureTestTimespec
	}{Ctim: signatureTestTimespec{Sec: 11, Nsec: 4}}
	if bytes.Equal(entrySignature("same.go", withoutOptional), entrySignature("same.go", withOptional)) {
		t.Fatal("valid optional timestamp did not change the signature")
	}
}

func TestCachedCandidateEmbeddingReusesVectors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("sample contents"), 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(): %v", err)
	}
	entry := fileEntry{relPath: "sample.txt", absPath: path, size: info.Size()}
	cache := &embeddingCache{dir: t.TempDir()}
	embedder := &cacheTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	progress := &progressTracker{}

	for range 2 {
		vector, err := cachedCandidateEmbedding(context.Background(), entry, ai.PromptPart{Role: ai.RoleModel, Text: "sample.txt"}, "filename", embeddingRecipeName, scheduler, cache, embedder.CacheIdentity(), 2, progress)
		if err != nil {
			t.Fatalf("cachedCandidateEmbedding(): %v", err)
		}
		if !reflect.DeepEqual(vector, []float32{1, 0}) {
			t.Fatalf("vector = %v", vector)
		}
	}
	if got := embedder.calls.Load(); got != 1 {
		t.Fatalf("embed calls = %d, want one candidate embedding", got)
	}
}

func TestCachedContentCandidateUsesCacheBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.txt")
	if err := os.WriteFile(path, []byte("sample contents"), 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(): %v", err)
	}
	entry := fileEntry{relPath: "content.txt", absPath: path, size: info.Size()}
	cache := &embeddingCache{dir: t.TempDir()}
	signature := entrySignature(path, info)
	if err := cache.put(signature, "test-provider:test-model:v1", "content", embeddingRecipeFile, []float32{1, 0}); err != nil {
		t.Fatalf("put(): %v", err)
	}
	embedder := &cacheTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	vector, err := cachedCandidateEmbedding(context.Background(), entry, ai.PromptPart{}, "content", embeddingRecipeFile, scheduler, cache, embedder.CacheIdentity(), 2, &progressTracker{})
	if err != nil {
		t.Fatalf("cachedCandidateEmbedding(): %v", err)
	}
	if !reflect.DeepEqual(vector, []float32{1, 0}) {
		t.Fatalf("vector = %v", vector)
	}
	if got := embedder.calls.Load(); got != 0 {
		t.Fatalf("embed calls = %d, want cache hit without candidate embedding", got)
	}
}

func TestEmbeddingCacheWriteFailureDoesNotDiscardSearchVector(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("sample contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := fileEntry{relPath: "sample.txt", absPath: path, size: info.Size()}
	blockingFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := &embeddingCache{dir: filepath.Join(blockingFile, "cache")}
	progress := &progressTracker{}
	embedder := &cacheTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()

	vector, err := cachedCandidateEmbedding(context.Background(), entry, ai.PromptPart{Role: ai.RoleModel, Text: "sample.txt"}, "filename", embeddingRecipeName, scheduler, cache, embedder.CacheIdentity(), 2, progress)
	if err != nil {
		t.Fatalf("cachedCandidateEmbedding(): %v", err)
	}
	if len(vector) != 2 {
		t.Fatalf("vector length = %d, want 2", len(vector))
	}
	if warnings := progress.warningMessages(); len(warnings) != 1 || !strings.Contains(warnings[0], "Could not persist") {
		t.Fatalf("cache warnings = %v, want explicit write warning", warnings)
	}
}

func TestCacheIdentityDisablesUnidentifiedEmbedders(t *testing.T) {
	identity, ok := embeddingCacheIdentity(&cacheTestEmbedder{})
	if !ok || identity != "test-provider:test-model:v1" {
		t.Fatalf("embeddingCacheIdentity() = %q, %v", identity, ok)
	}
	if _, ok := embeddingCacheIdentity(unidentifiedEmbedder{}); ok {
		t.Fatal("unidentified embedder enabled persistent cache")
	}
}

type unidentifiedEmbedder struct{}

func (unidentifiedEmbedder) CacheIdentity() string { return "" }

type signatureTestInfo struct {
	name string
	size int64
	mode os.FileMode
	mod  time.Time
	sys  any
}

func (i signatureTestInfo) Name() string       { return i.name }
func (i signatureTestInfo) Size() int64        { return i.size }
func (i signatureTestInfo) Mode() os.FileMode  { return i.mode }
func (i signatureTestInfo) ModTime() time.Time { return i.mod }
func (i signatureTestInfo) IsDir() bool        { return i.mode.IsDir() }
func (i signatureTestInfo) Sys() any           { return i.sys }

type signatureTestSys struct {
	Ctim           signatureTestTimespec
	Birthtimespec  signatureTestTimespec
	Atim           signatureTestTimespec
	FileAttributes uint32
}

type signatureTestTimespec struct {
	Sec  int64
	Nsec int64
}

func TestEntrySignatureExcludesAccessTime(t *testing.T) {
	first := signatureTestInfo{name: "x", size: 2, mode: 0o600, mod: time.Unix(1, 2), sys: signatureTestSys{Atim: signatureTestTimespec{Sec: 3}}}
	second := first
	second.sys = signatureTestSys{Atim: signatureTestTimespec{Sec: 99}}
	if !bytes.Equal(entrySignature("x", first), entrySignature("x", second)) {
		t.Fatal("access time unexpectedly affects entry signature")
	}
}

func TestEntrySignatureContainsLengthDelimitedBasename(t *testing.T) {
	a := entrySignature("ab", signatureTestInfo{name: "ab", size: 3, mode: 0o600, mod: time.Unix(1, 0)})
	b := entrySignature("a", signatureTestInfo{name: "a", size: 2, mode: 0o600, mod: time.Unix(1, 0)})
	if bytes.Equal(a, b) {
		t.Fatal("different basename/size combinations collided")
	}
	if binary.LittleEndian.Uint32(a[len("entry-signature-v1"):]) != 2 {
		t.Fatal("basename is not length-prefixed")
	}
}

func TestEntrySignatureDoesNotInspectSourceContents(t *testing.T) {
	info := signatureTestInfo{name: "same.txt", size: 4, mode: 0o600, mod: time.Unix(1, 2)}
	firstContent := []byte("aaaa")
	secondContent := []byte("bbbb")
	if bytes.Equal(firstContent, secondContent) {
		t.Fatal("test contents must differ")
	}
	if len(firstContent) != len(secondContent) {
		t.Fatal("test contents must have equal sizes")
	}
	first := entrySignature("same.txt", info)
	second := entrySignature("same.txt", info)
	if !bytes.Equal(first, second) {
		t.Fatal("identical metadata signatures did not match")
	}
	cache := &embeddingCache{dir: t.TempDir()}
	if err := cache.put(first, "model", "content", "recipe", []float32{1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.get(second, "model", "content", "recipe", 1); err != nil || !ok {
		t.Fatalf("same-signature cache lookup = hit %v, error %v; want a metadata-only hit", ok, err)
	}
}

func TestEmbeddingCacheReadFailureIsNotAMiss(t *testing.T) {
	cache := &embeddingCache{dir: t.TempDir()}
	signature := []byte("signature")
	if err := cache.put(signature, "model", "filename", "recipe", []float32{1}); err != nil {
		t.Fatalf("put(): %v", err)
	}
	path := cache.recordPath(signature, "model", "filename", "recipe", 1)
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(): %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir(): %v", err)
	}
	_, found, err := cache.get(signature, "model", "filename", "recipe", 1)
	if err == nil || found {
		t.Fatalf("get() = hit %v, error %v; want a read failure", found, err)
	}
}
