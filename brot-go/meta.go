package main

import (
	_ "unsafe" // required for go:linkname
)

// MohabbatMeta holds the offsets and sizes injected by the patcher.
// Same struct, same magic, same layout as Rust.
type MohabbatMeta struct {
	Magic           [8]byte // "MOHABBAT"
	PoolLen         uint64
	WashmhostOffset uint64
	WashmhostLen    uint64
	PayloadOffset   uint64
	PayloadLen      uint64
	Reserved        uint64
}

//go:linkname mohabbatMeta mohabbat.Meta
var mohabbatMeta = MohabbatMeta{Magic: [8]byte{'M', 'O', 'H', 'A', 'B', 'B', 'A', 'T'}}
