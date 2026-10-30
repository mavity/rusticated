//go:build wasip1

package syscall

import (
	"bytes"
	"testing"
)

func TestReadRusticatedDirEntryWithUnknownKind(t *testing.T) {
	data := []byte{0, 'r', 'e', 'a', 'd', 'm', 'e', 0, 2, 's', 'r', 'c', 0}

	kind, name, consumed, ok := readRusticatedDirEntry(data)
	if !ok {
		t.Fatal("expected first directory entry to parse")
	}
	if kind != 0 {
		t.Fatalf("kind = %d, want unknown kind 0", kind)
	}
	if !bytes.Equal(name, []byte("readme")) {
		t.Fatalf("name = %q, want readme", name)
	}
	if consumed != 8 {
		t.Fatalf("consumed = %d, want 8", consumed)
	}

	kind, name, consumed, ok = readRusticatedDirEntry(data[consumed:])
	if !ok {
		t.Fatal("expected second directory entry to parse")
	}
	if kind != 2 || !bytes.Equal(name, []byte("src")) || consumed != 5 {
		t.Fatalf("second entry = (%d, %q, %d), want (2, src, 5)", kind, name, consumed)
	}
}

func TestReadRusticatedDirEntryKeepsIncompleteName(t *testing.T) {
	if _, _, _, ok := readRusticatedDirEntry([]byte{0, 'a', 'b'}); ok {
		t.Fatal("incomplete directory entry should remain buffered")
	}
}
