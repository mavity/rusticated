package mohabbat

// Modern Five scope: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64.
// Slot order is contractual - Zone A and patcher both depend on it.
var slots = []slot{
	{name: "node", goos: "js", goarch: "wasm"},
	{name: "linux-amd64", goos: "linux", goarch: "amd64", shCase: "x86_64-Linux"},
	{name: "linux-arm64", goos: "linux", goarch: "arm64", shCase: "aarch64-Linux"},
	{name: "darwin-amd64", goos: "darwin", goarch: "amd64", shCase: "x86_64-Darwin"},
	{name: "darwin-arm64", goos: "darwin", goarch: "arm64", shCase: "arm64-Darwin"},
	{name: "win-amd64", goos: "windows", goarch: "amd64", winArch: "AMD64"},
	{name: "win-arm64", goos: "windows", goarch: "arm64", winArch: "ARM64"},
}

type slot struct {
	name      string
	goos      string
	goarch    string
	shCase    string // matches "$(uname -m)-$(uname -s)"
	winArch   string // matches %PROCESSOR_ARCHITECTURE%
	jsTextLen int
}

const mohabbatMagic = "MOHABBAT"

// MohabbatMeta layout: 8-byte magic + 6*u64 = 56 bytes
type mohabbatMeta struct {
	PoolLen         uint64
	WashmhostOffset uint64
	WashmhostLen    uint64
	PayloadOffset   uint64
	PayloadLen      uint64
	Reserved        uint64
}

// prebuildFn is set by prebuild.go on native (!wasip1) builds via init().
// On WASM builds it remains nil; ModeBuild falls back to subprocess invocation.
var prebuildFn func(ws string) error
