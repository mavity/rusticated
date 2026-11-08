package filelist

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const (
	embeddingCacheDirName = "semantic-v1"
	embeddingCacheMagic   = "KBLVEC01"
	embeddingRecipeFile   = "file-sample-v1:basename+newline+first-1500-bytes"
	embeddingRecipeName   = "filename-v1:basename"
)

type embeddingCache struct {
	dir string
}

func newEmbeddingCache() (*embeddingCache, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user cache directory: %w", err)
	}
	if root == "" {
		return nil, fmt.Errorf("resolve user cache directory: empty path")
	}
	return newEmbeddingCacheAt(filepath.Join(root, "kabibi", "filelist", embeddingCacheDirName))
}

func newEmbeddingCacheAt(dir string) (*embeddingCache, error) {
	if dir == "" {
		return nil, fmt.Errorf("resolve embedding cache directory: empty path")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create embedding cache directory: %w", err)
	}
	return &embeddingCache{dir: dir}, nil
}

func (c *embeddingCache) get(signature []byte, identity, kind, recipe string, dimension int) ([]float32, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	if dimension <= 0 {
		return nil, false, fmt.Errorf("invalid embedding dimension %d", dimension)
	}
	path := c.recordPath(signature, identity, kind, recipe, dimension)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open embedding cache record: %w", err)
	}
	defer file.Close()

	var header [44]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return nil, false, fmt.Errorf("read embedding cache header: %w", err)
	}
	if string(header[:8]) != embeddingCacheMagic {
		return nil, false, fmt.Errorf("invalid embedding cache format marker")
	}
	storedDimension := binary.LittleEndian.Uint32(header[8:12])
	if uint64(storedDimension) != uint64(dimension) {
		return nil, false, fmt.Errorf("embedding cache dimension %d does not match expected %d", storedDimension, dimension)
	}
	if uint64(storedDimension) > uint64((int(^uint(0)>>1)-len(header))/4) {
		return nil, false, fmt.Errorf("embedding cache dimension %d is too large", storedDimension)
	}
	raw := make([]byte, int(storedDimension)*4)
	if _, err := io.ReadFull(file, raw); err != nil {
		return nil, false, fmt.Errorf("read embedding cache vector: %w", err)
	}
	var extra [1]byte
	if n, err := io.ReadFull(file, extra[:]); n != 0 {
		return nil, false, fmt.Errorf("embedding cache record has trailing data")
	} else if err != io.EOF {
		return nil, false, fmt.Errorf("check embedding cache record: %w", err)
	}
	checksum := sha256.Sum256(raw)
	if !equalBytes(checksum[:], header[12:]) {
		return nil, false, fmt.Errorf("embedding cache checksum mismatch")
	}
	vector := make([]float32, storedDimension)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		if math.IsNaN(float64(vector[i])) || math.IsInf(float64(vector[i]), 0) {
			return nil, false, fmt.Errorf("embedding cache contains a non-finite value at index %d", i)
		}
	}
	return vector, true, nil
}

func (c *embeddingCache) put(signature []byte, identity, kind, recipe string, vector []float32) error {
	if c == nil {
		return nil
	}
	if len(vector) == 0 {
		return fmt.Errorf("cannot cache an empty embedding")
	}
	if uint64(len(vector)) > math.MaxUint32 || len(vector) > int(^uint(0)>>1)/4 {
		return fmt.Errorf("embedding dimension %d exceeds cache format limit", len(vector))
	}
	raw := make([]byte, len(vector)*4)
	for i, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("cannot cache non-finite embedding value at index %d", i)
		}
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(value))
	}
	checksum := sha256.Sum256(raw)
	var header [44]byte
	copy(header[:8], embeddingCacheMagic)
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(vector)))
	copy(header[12:], checksum[:])

	target := c.recordPath(signature, identity, kind, recipe, len(vector))
	temp, err := os.CreateTemp(c.dir, ".vector-*")
	if err != nil {
		return fmt.Errorf("create temporary embedding cache record: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set embedding cache record permissions: %w", err)
	}
	if err := writeCacheBytes(temp, header[:]); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write embedding cache header: %w", err)
	}
	if err := writeCacheBytes(temp, raw); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write embedding cache vector: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close embedding cache record: %w", err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		if _, ok, readErr := c.get(signature, identity, kind, recipe, len(vector)); ok && readErr == nil {
			return nil
		}
		return fmt.Errorf("publish embedding cache record: %w", err)
	}
	return nil
}

func (c *embeddingCache) recordPath(signature []byte, identity, kind, recipe string, dimension int) string {
	h := sha256.New()
	writeCacheField(h, signature)
	writeCacheField(h, []byte(identity))
	writeCacheField(h, []byte(kind))
	writeCacheField(h, []byte(recipe))
	var dim [4]byte
	binary.LittleEndian.PutUint32(dim[:], uint32(dimension))
	writeCacheField(h, dim[:])
	return filepath.Join(c.dir, hex.EncodeToString(h.Sum(nil))+".vec")
}

type byteWriter interface {
	Write([]byte) (int, error)
}

func writeCacheField(w byteWriter, value []byte) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

func writeCacheBytes(w io.Writer, value []byte) error {
	written, err := w.Write(value)
	if err != nil {
		return err
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

func entrySignature(path string, info fs.FileInfo) []byte {
	var signature []byte
	signature = append(signature, "entry-signature-v1"...)
	appendSignatureField(&signature, []byte(filepath.Base(path)))
	var fixed [8]byte
	binary.LittleEndian.PutUint64(fixed[:], uint64(info.Size()))
	signature = append(signature, fixed[:]...)
	binary.LittleEndian.PutUint32(fixed[:4], uint32(info.Mode()))
	signature = append(signature, fixed[:4]...)
	modTime := info.ModTime()
	binary.LittleEndian.PutUint64(fixed[:], uint64(modTime.Unix()))
	signature = append(signature, fixed[:]...)
	binary.LittleEndian.PutUint32(fixed[:4], uint32(modTime.Nanosecond()))
	signature = append(signature, fixed[:4]...)

	system := info.Sys()
	for _, fieldNames := range [][]string{
		{"Ctim", "Ctimespec", "ChangeTime", "ChangeTimespec"},
		{"Birthtimespec", "BirthTime", "Btim", "CreationTime", "CreateTime", "Createtime"},
	} {
		nanos, ok := optionalSystemTimestamp(system, fieldNames)
		if !ok {
			signature = append(signature, 0)
			continue
		}
		signature = append(signature, 1)
		binary.LittleEndian.PutUint64(fixed[:], uint64(nanos))
		signature = append(signature, fixed[:]...)
	}
	attributes, hasAttributes := optionalSystemUint32(system, []string{"FileAttributes", "Attributes"})
	if hasAttributes {
		signature = append(signature, 1)
		binary.LittleEndian.PutUint32(fixed[:4], attributes)
		signature = append(signature, fixed[:4]...)
	} else {
		signature = append(signature, 0)
	}
	return signature
}

func appendSignatureField(dst *[]byte, value []byte) {
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(value)))
	*dst = append(*dst, size[:]...)
	*dst = append(*dst, value...)
}

func optionalSystemTimestamp(system any, names []string) (int64, bool) {
	value := reflect.ValueOf(system)
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, false
	}
	for _, name := range names {
		field := value.FieldByName(name)
		if !field.IsValid() || !field.CanInterface() {
			continue
		}
		if timestamp, ok := timestampValue(field); ok {
			return timestamp, true
		}
	}
	return 0, false
}

func timestampValue(value reflect.Value) (int64, bool) {
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return 0, false
	}
	if value.CanInterface() {
		if timestamp, ok := value.Interface().(time.Time); ok {
			return timestamp.UnixNano(), true
		}
	}
	if value.Kind() != reflect.Struct {
		return 0, false
	}
	seconds, hasSeconds := integerField(value, "Sec", "Tv_sec", "Seconds")
	nanoseconds, hasNanoseconds := integerField(value, "Nsec", "Tv_nsec", "Nanoseconds")
	if hasSeconds && hasNanoseconds {
		return seconds*int64(time.Second) + nanoseconds, true
	}
	high, hasHigh := integerField(value, "HighDateTime")
	low, hasLow := integerField(value, "LowDateTime")
	if hasHigh && hasLow {
		ticks := (uint64(high) << 32) | uint64(uint32(low))
		const windowsToUnixEpoch100ns = uint64(116444736000000000)
		if ticks < windowsToUnixEpoch100ns {
			return 0, false
		}
		unixTicks := ticks - windowsToUnixEpoch100ns
		return int64(unixTicks/10_000_000*uint64(time.Second) + unixTicks%10_000_000*100), true
	}
	return 0, false
}

func integerField(value reflect.Value, names ...string) (int64, bool) {
	for _, name := range names {
		field := value.FieldByName(name)
		if !field.IsValid() || !field.CanInt() && !field.CanUint() {
			continue
		}
		if field.CanInt() {
			return field.Int(), true
		}
		return int64(field.Uint()), true
	}
	return 0, false
}

func optionalSystemUint32(system any, names []string) (uint32, bool) {
	value := reflect.ValueOf(system)
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, false
	}
	for _, name := range names {
		field := value.FieldByName(name)
		if field.IsValid() && field.CanUint() {
			return uint32(field.Uint()), true
		}
	}
	return 0, false
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func embeddingCacheIdentity(embedder interface{ CacheIdentity() string }) (string, bool) {
	if embedder == nil {
		return "", false
	}
	identity := strings.TrimSpace(embedder.CacheIdentity())
	return identity, identity != ""
}
