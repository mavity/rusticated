package main

import (
	"encoding/binary"
	"testing"
)

func TestMinCallbackOverlappedSize(t *testing.T) {
	got := minCallbackOverlappedSize(2, 3)
	want := uint32(callbackOverlappedHeaderSize + 5*8)
	if got != want {
		t.Fatalf("minCallbackOverlappedSize() = %d, want %d", got, want)
	}
}

func TestCallbackCompletionCellRoundTrip(t *testing.T) {
	buf := make([]byte, minCallbackOverlappedSize(2, 2))
	cbID := uint64(11)
	invID := uint64(99)
	args := []uint64{7, 8}
	if !writeCallbackInvocationCell(buf, cbID, invID, args, 2) {
		t.Fatal("writeCallbackInvocationCell returned false")
	}
	if got := binary.LittleEndian.Uint64(buf[24:32]); got != cbID {
		t.Fatalf("cbID = %d, want %d", got, cbID)
	}
	resultOff := callbackOverlappedHeaderSize + len(args)*8
	binary.LittleEndian.PutUint64(buf[resultOff:resultOff+8], 123)
	binary.LittleEndian.PutUint64(buf[resultOff+8:resultOff+16], 456)

	gotInvID, gotResults, ok := readCallbackCompletionCell(buf, 2)
	if !ok {
		t.Fatal("readCallbackCompletionCell returned false")
	}
	if gotInvID != invID {
		t.Fatalf("invID = %d, want %d", gotInvID, invID)
	}
	if len(gotResults) != 2 || gotResults[0] != 123 || gotResults[1] != 456 {
		t.Fatalf("results = %#v, want [123 456]", gotResults)
	}
}

func TestReadCallbackCompletionCellRejectsShortBuffer(t *testing.T) {
	buf := make([]byte, callbackOverlappedHeaderSize-1)
	if _, _, ok := readCallbackCompletionCell(buf, 1); ok {
		t.Fatal("expected short buffer to be rejected")
	}
}
