package mohabbat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// jitDirName is the conventional subdirectory inside mohabbat that holds
// on-demand-hydrated, gate-flipped copies of third-party modules.
const jitDirName = "rusticated-jit"

// unixLikeBuildKeywords are Unix OS identifiers whose presence in a build
// constraint triggers a gate-flip to add wasip1.
var unixLikeBuildKeywords = []string{
	"unix", "linux", "darwin", "freebsd", "netbsd", "openbsd",
	"dragonfly", "solaris", "aix", "zos", "tamago",
}

// unixKeywordRe matches any Unix-like build keyword as a whole word.
var unixKeywordRe = regexp.MustCompile(`\b(unix|linux|darwin|freebsd|netbsd|openbsd|dragonfly|solaris|aix|zos|tamago)\b`)

// unixLikeSuffixes lists filename endings that correspond to actual Go GOOS
// values and are therefore excluded by Go's filename-based build constraints
// when GOOS=wasip1. These files need an overlay alias (_wasip1.go) to be
// visible to the wasip1 build.
//
// NOTE: _unix.go and _posix.go are intentionally NOT here. "unix" and
// "posix" are NOT Go GOOS values, so file names like foo_unix.go carry no
// implicit build constraint from the filename. Their build behaviour is
// governed entirely by their explicit //go:build tags, which flipGoFileTags
// handles directly.
var unixLikeSuffixes = []string{
	"_linux.go", "_darwin.go", "_freebsd.go",
	"_netbsd.go", "_openbsd.go", "_dragonfly.go", "_solaris.go", "_aix.go",
}

// jitTarget describes a module flagged for JIT hydration.
type jitTarget struct {
	module string // e.g. "github.com/u-root/u-root"
	jitDir string // absolute path to the JIT module directory
}

// depPatchResult holds the outputs of applyWasip1DepPatches.
type depPatchResult struct {
	overlayExtra map[string]string // additional overlay Replace entries for suffix-flip aliases
}

// applyWasip1DepPatches scans the project go.mod for replace directives whose
// target path contains the mohabbat rusticated-jit convention. For each one it
// finds, it hydrates (or reuses a valid cached copy of) the module from
// GOMODCACHE, applies generic build-tag gate flips, and returns the extra
// overlay Replace entries needed for suffix-flip aliases.
func applyWasip1DepPatches(ws, projectDir, goroot string) (*depPatchResult, error) {
	jitBase := filepath.Join(ws, "mohabbat", jitDirName)

	gomodPath := filepath.Join(projectDir, "go.mod")
	gomodContent, err := os.ReadFile(gomodPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", gomodPath, err)
	}

	targets := parseJitTargets(string(gomodContent), jitBase)
	if len(targets) == 0 {
		return &depPatchResult{}, nil
	}

	goBin := goBinFromRoot(goroot)
	gomodcache, err := getGoModCache(goBin)
	if err != nil {
		return nil, err
	}

	overlayExtra := map[string]string{}

	for _, target := range targets {
		fmt.Printf("🍆  wasip1 JIT: target module %s -> %s\n", target.module, target.jitDir)
		version, ok := modVersionFromGomod(string(gomodContent), target.module)
		if !ok {
			fmt.Printf("🍆  wasip1 JIT: %s not in go.mod require, skipping\n", target.module)
			continue
		}

		encoded := encodeModPath(target.module)
		srcDir := filepath.Join(gomodcache, encoded+"@"+version)
		if _, err := os.Stat(srcDir); err != nil {
			fmt.Printf("🍆  wasip1 JIT: %s@%s not in GOMODCACHE, skipping\n", target.module, version)
			continue
		}

		// Sync source files into the JIT directory.
		if err := syncDirToJit(srcDir, target.jitDir); err != nil {
			return nil, fmt.Errorf("sync %s: %w", target.module, err)
		}

		// Phase 1: Structural API translations using regast-poc.
		// These replace brittle strings.ReplaceAll with ast-aware transforms.
		commonPatches := []regastPatch{
			{pat: `⦃unix \. Major⦄`, repl: `syscall.Major`},
			{pat: `⦃unix \. Minor⦄`, repl: `syscall.Minor`},
		}
		if target.module == "github.com/u-root/u-root" {
			// For u-root we also flip the import to syscall.
			patches := append([]regastPatch{
				{pat: `⦃"golang.org/x/sys/unix"⦄`, repl: `"syscall"`},
			}, commonPatches...)

			_ = filepath.WalkDir(target.jitDir, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") {
					_ = applyRegastPatches(path, patches)
				}
				return nil
			})
		}

		// SPECIAL CASE: go-isatty
		if strings.HasSuffix(filepath.ToSlash(target.jitDir), "github.com/mattn/go-isatty") {
			for _, fname := range []string{
				"isatty_others.go", "isatty_bsd.go",
				"isatty_tcgets.go", "isatty_solaris.go", "isatty_plan9.go",
			} {
				p := filepath.Join(target.jitDir, fname)
				if data, err := os.ReadFile(p); err == nil {
					_ = writeFileIfChanged(p, []byte(addWasip1ExclusionToTag(string(data))))
				}
			}
			wasip1Src := "//go:build wasip1\n\npackage isatty\n\nimport \"syscall\"\n\n" +
				"// IsTerminal reports whether fd is a terminal, using rusticated-go's Isatty.\n" +
				"func IsTerminal(fd uintptr) bool {\n\treturn syscall.Isatty(fd)\n}\n\n" +
				"// IsCygwinTerminal always returns false on wasip1.\n" +
				"func IsCygwinTerminal(fd uintptr) bool {\n\treturn false\n}\n"
			_ = writeFileIfChanged(filepath.Join(target.jitDir, "isatty_rusticated_wasip1.go"), []byte(wasip1Src))
		}

		// SPECIAL CASE: termenv constants_solaris.go
		if strings.Contains(target.jitDir, "termenv") {
			solPath := filepath.Join(target.jitDir, "constants_solaris.go")
			if data, err := os.ReadFile(solPath); err == nil {
				content := string(data)
				if !strings.Contains(content, "//go:build") {
					_ = writeFileIfChanged(solPath, []byte("//go:build solaris\n\n"+content))
				}
			}
		}

		// SPECIAL CASE: golang.org/x/sys/unix — inject rusticated constants and types for wasip1.
		if strings.HasSuffix(filepath.ToSlash(target.jitDir), "golang.org/x/sys") {
			zerrorsPath := filepath.Join(target.jitDir, "unix", "zerrors_rusticated_wasip1.go")
			zerrorsContent := `// Code generated by mohabbat JIT. DO NOT EDIT.

//go:build wasip1

package unix

import (
	"syscall"
	"unsafe"
)

const (
	IGNBRK = 0x1
	BRKINT = 0x2
	IGNPAR = 0x4
	PARMRK = 0x8
	INPCK  = 0x10
	ISTRIP = 0x20
	INLCR  = 0x40
	IGNCR  = 0x80
	ICRNL  = 0x100
	IUCLC  = 0x200
	IXON   = 0x400
	IXANY  = 0x800
	IXOFF  = 0x1000
	IMAXBEL = 0x2000
	IUTF8  = 0x4000
	OPOST  = 0x1
	OLCUC  = 0x2
	ONLCR  = 0x4
	OCRNL  = 0x8
	ONOCR  = 0x10
	ONLRET = 0x20
	OFILL  = 0x40
	OFDEL  = 0x80
	B0     = 0x0
	B50    = 0x1
	B75    = 0x2
	B110   = 0x3
	B134   = 0x4
	B150   = 0x5
	B200   = 0x6
	B300   = 0x7
	B600   = 0x8
	B1200  = 0x9
	B1800  = 0xa
	B2400  = 0xb
	B4800  = 0xc
	B9600  = 0xd
	B19200 = 0xe
	B38400 = 0xf
	CSIZE  = 0x30
	CS5    = 0x0
	CS6    = 0x10
	CS7    = 0x20
	CS8    = 0x30
	CSTOPB = 0x40
	CREAD  = 0x80
	PARENB = 0x100
	PARODD = 0x200
	HUPCL  = 0x400
	CLOCAL = 0x800
	ISIG   = 0x1
	ICANON = 0x2
	ECHO   = 0x8
	ECHOE  = 0x10
	ECHOK  = 0x20
	ECHONL = 0x40
	NOFLSH = 0x80
	TOSTOP = 0x100
	IEXTEN = 0x8000
	VMIN   = 0x6
	VTIME  = 0x5
	TCGETS      = 0x5401
	TCSETS      = 0x5402
	TCSETSW     = 0x5403
	TCSETSF     = 0x5404
	TIOCGWINSZ  = 0x5413
	TIOCSWINSZ  = 0x5414
	TIOCGETA = 0x5401
	TIOCSETA = 0x5402
	TCGPGRP = 0x5414
	TIOCGPGRP = 0x5414
	SYS_IOCTL = 16
	ioctlReadTermios = 0x5401
	ioctlWriteTermios = 0x5402
)

func Read(fd int, p []byte) (n int, err error) { return syscall.Read(fd, p) }
func Write(fd int, p []byte) (n int, err error) { return syscall.Write(fd, p) }
func Close(fd int) error { return syscall.Close(fd) }

func Getpgrp() (pgrp int) { return 0 }

type FdSet struct { Bits [32]int32 }
func (s *FdSet) Set(fd int) { s.Bits[fd/31] |= (1 << (uint(fd) % 31)) }
func Select(nfd int, r *FdSet, w *FdSet, e *FdSet, timeout *Timeval) (n int, err error) { return 0, nil }
func NsecToTimeval(nsec int64) Timeval { return Timeval{Sec: nsec / 1e9, Usec: (nsec % 1e9) / 1e3} }

type Termios struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Line   uint8
	Cc     [32]uint8
	Ispeed uint32
	Ospeed uint32
}

type Winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

type Errno = syscall.Errno
const (
	EINVAL = syscall.EINVAL
	ENOTTY = syscall.ENOTTY
	EAGAIN = syscall.EAGAIN
	ENOENT = syscall.ENOENT
	EINTR  = syscall.EINTR
	Eintr  = syscall.EINTR
)
type Iovec struct { Base *byte; Len uint64 }
type Rlimit struct { Cur uint64; Max uint64 }
type Timeval struct { Sec int64; Usec int64 }
type Timespec struct { Sec int64; Nsec int64 }
var errorList = [...]struct {
	num  Errno
	name string
	desc string
}{}
var signalList = [...]struct {
	num  syscall.Signal
	name string
	desc string
}{}

type _Socklen uint32
type RawSockaddrInet4 struct {
	Family uint16
	Port   uint16
	Addr   [4]byte
	Zero   [8]uint8
}
type RawSockaddrInet6 struct {
	Family   uint16
	Port     uint16
	Flowinfo uint32
	Addr     [16]byte
	Scope_id uint32
}
type RawSockaddrUnix struct {
	Family uint16
	Path   [108]int8
}
type RawSockaddr struct {
	Family uint16
	Data   [14]int8
}
type RawSockaddrAny struct {
	Addr RawSockaddr
	Pad  [96]int8
}
type _RawSockaddrAny struct {
	Addr RawSockaddr
	Pad  [96]int8
}
type IPMreq struct {
	Multiaddr [4]byte
	Interface [4]byte
}
type IPv6Mreq struct {
	Multiaddr [16]byte
	Interface uint32
}
type IPv6MTUInfo struct {
	Addr RawSockaddrInet6
	Mtu  uint32
}
type ICMPv6Filter struct {
	Data [8]uint32
}
type Linger struct {
	Onoff  int32
	Linger int32
}

func itoa(v int) string {
	if v == 0 { return "0" }
	var buf [20]byte
	i := len(buf) - 1
	for v > 0 {
		buf[i] = byte(v%10) + '0'
		v /= 10
		i--
	}
	return string(buf[i+1:])
}

func ioctl(fd int, req uint, arg uintptr) error {
	_, _, err := RawSyscall(SYS_IOCTL, uintptr(fd), uintptr(req), arg)
	if err != 0 { return err }
	return nil
}
func ioctlPtr(fd int, req uint, arg unsafe.Pointer) error { return ioctl(fd, req, uintptr(arg)) }
func RawSyscall(trap, a1, a2, a3 uintptr) (r1, r2 uintptr, err Errno) {
	r1, r2, e := syscall.RawSyscall(trap, a1, a2, a3)
	return r1, r2, Errno(e)
}
`
			_ = writeFileIfChanged(zerrorsPath, []byte(zerrorsContent))
		}

		// SPECIAL CASE: u-root pkg/ls platform files sometimes lack build guards.
		if strings.HasSuffix(filepath.ToSlash(target.jitDir), "github.com/u-root/u-root/pkg/ls") {
			for _, f := range []string{"fileinfo_linux.go", "fileinfo_openbsd.go", "fileinfo_unix.go"} {
				p := filepath.Join(target.jitDir, f)
				if data, err := os.ReadFile(p); err == nil {
					content := string(data)
					if !strings.Contains(content, "//go:build") && !strings.Contains(content, "// +build") {
						tag := "!plan9 && !windows && !tamago"
						if f == "fileinfo_linux.go" {
							tag = "linux"
						} else if f == "fileinfo_openbsd.go" {
							tag = "openbsd"
						}
						_ = writeFileIfChanged(p, []byte("//go:build "+tag+"\n\n"+content))
					}
				}
			}
		}

		sfx, err := applyGateFlips(target.jitDir)
		if err != nil {
			return nil, fmt.Errorf("gate flip %s: %w", target.module, err)
		}
		for _, sf := range sfx {
			overlayExtra[sf[0]] = sf[1]
		}
	}

	return &depPatchResult{overlayExtra: overlayExtra}, nil
}

func parseJitTargets(gomodContent, jitBase string) []jitTarget {
	marker := "/rusticated-jit/"
	var targets []jitTarget
	inReplace := false
	for _, raw := range strings.Split(gomodContent, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if line == "replace (" {
			inReplace = true
			continue
		}
		if inReplace && line == ")" {
			inReplace = false
			continue
		}

		if inReplace {
			parts := strings.SplitN(line, " => ", 2)
			if len(parts) == 2 {
				replacePath := strings.TrimSpace(parts[1])
				if strings.Contains(filepath.ToSlash(replacePath), marker) {
					lhs := strings.Fields(parts[0])
					if len(lhs) > 0 {
						mod := lhs[0]
						jitDir := filepath.Join(jitBase, filepath.FromSlash(mod))
						targets = append(targets, jitTarget{module: mod, jitDir: jitDir})
					}
				}
			}
		} else if strings.HasPrefix(line, "replace ") {
			parts := strings.SplitN(line, " => ", 2)
			if len(parts) == 2 {
				replacePath := strings.TrimSpace(parts[1])
				if strings.Contains(filepath.ToSlash(replacePath), marker) {
					lhs := strings.Fields(strings.TrimPrefix(parts[0], "replace "))
					if len(lhs) > 0 {
						mod := lhs[0]
						jitDir := filepath.Join(jitBase, filepath.FromSlash(mod))
						targets = append(targets, jitTarget{module: mod, jitDir: jitDir})
					}
				}
			}
		}
	}
	return targets
}

func getGoModCache(goBin string) (string, error) {
	out, err := exec.Command(goBin, "env", "GOMODCACHE").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMODCACHE: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func writeFileIfChanged(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o644)
	return os.WriteFile(path, content, 0o644)
}

func syncDirToJit(srcDir, dstDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		dst := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return writeFileIfChanged(dst, data)
	})
}

func applyGateFlips(jitDir string) ([][2]string, error) {
	var flips [][2]string
	err := filepath.WalkDir(jitDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if virtual := suffixFlipTarget(path); virtual != "" {
			srcData, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := writeFileIfChanged(virtual, srcData); err != nil {
				return err
			}
			flips = append(flips, [2]string{canonPath(virtual), canonPath(path)})
		}
		return flipGoFileTags(path)
	})
	return flips, err
}

func suffixFlipTarget(filePath string) string {
	base := filepath.Base(filePath)
	dir := filepath.Dir(filePath)
	if strings.Contains(filepath.ToSlash(filePath), "golang.org/x/sys/unix") {
		return ""
	}
	for _, sfx := range unixLikeSuffixes {
		if strings.HasSuffix(base, sfx) {
			stem := base[:len(base)-len(sfx)]
			virtual := filepath.Join(dir, stem+"_wasip1.go")
			otherFiles := []string{stem + "_other.go", stem + "_stub.go", stem + "_stubs.go"}
			for _, of := range otherFiles {
				if _, err := os.Stat(filepath.Join(dir, of)); err == nil {
					return ""
				}
			}
			unixSibling := filepath.Join(dir, stem+"_unix.go")
			if _, err := os.Stat(unixSibling); err == nil {
				return ""
			}
			return virtual
		}
	}
	return ""
}

func flipGoFileTags(filePath string) error {
	base := filepath.Base(filePath)
	dir := filepath.Dir(filePath)

	if strings.Contains(filepath.ToSlash(filePath), "golang.org/x/sys/unix") {
		isGeneric := false
		// Only allow a very small set of files from x/sys/unix to be included for wasip1.
		// We provide the rest in zerrors_rusticated_wasip1.go.
		for _, g := range []string{
			"syscall.go", "ioctl_unsigned.go", "pagesize_unix.go",
			"env_unix.go", "mmap_unix.go", "race0.go",
		} {
			if base == g {
				isGeneric = true
				break
			}
		}
		if !isGeneric {
			return nil
		}
	}

	if strings.HasSuffix(base, "_unix.go") || base == "ioctl_unsigned.go" || base == "syscall_unix.go" {
		stem := base
		if strings.HasSuffix(base, "_unix.go") {
			stem = base[:len(base)-len("_unix.go")]
		} else {
			stem = strings.TrimSuffix(base, ".go")
		}

		// Don't exclude sisters if they are specialized unix helpers (like term_unix_other.go)
		// unless we are in x/sys/unix where we provide our own.
		if !strings.Contains(filePath, "golang.org/x/sys/unix") {
			// Actually, let's be more selective.
			// Only exclude if it's truly a generic "other" or "windows" file.
			sisters := []string{"_other.go", "_unsupported.go", "_stub.go", "_stubs.go", "_plan9.go", "_windows.go", "_wasm.go", "_tamago.go"}
			// If we are term_unix.go, we want to keep term_unix_other.go but exclude term_unix_bsd.go
			if base == "term_unix.go" {
				_ = actuallyExcludeWasip1(filepath.Join(dir, "term_unix_bsd.go"))
			}

			for _, s := range sisters {
				p := filepath.Join(dir, stem+s)
				if _, err := os.Stat(p); err == nil && p != filePath {
					_ = actuallyExcludeWasip1(p)
				}
			}
		} else {
			// In x/sys/unix, we ARE providing our own, so exclude more aggressively.
			sisters := []string{"_other.go", "_unsupported.go", "_stub.go", "_stubs.go", "_plan9.go", "_windows.go", "_wasm.go", "_tamago.go", "_bsd.go", "_unix_other.go", "_unix_bsd.go"}
			if base == "ioctl_unsigned.go" {
				sisters = append(sisters, "ioctl_signed.go")
			}
			for _, s := range sisters {
				p := filepath.Join(dir, stem+s)
				if s == "ioctl_signed.go" {
					p = filepath.Join(dir, "ioctl_signed.go")
				}
				if _, err := os.Stat(p); err == nil && p != filePath {
					_ = actuallyExcludeWasip1(p)
				}
			}
		}
	}

	// SPECIAL CASE: u-root/pkg/ls/fileinfo_unix.go vs fileinfo_tamago.go
	if strings.HasSuffix(filepath.ToSlash(filePath), "u-root/pkg/ls/fileinfo_unix.go") {
		dir := filepath.Dir(filePath)
		if _, err := os.Stat(filepath.Join(dir, "fileinfo_tamago.go")); err == nil {
			_ = actuallyExcludeWasip1(filepath.Join(dir, "fileinfo_tamago.go"))
		}
	}

	return actuallyFlipTags(filePath)
}

func actuallyFlipTags(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	changed := false
	maxLines := len(lines)
	if maxLines > 30 {
		maxLines = 30
	}
	for i := 0; i < maxLines; i++ {
		line := lines[i]
		if strings.HasPrefix(line, "//go:build ") || strings.HasPrefix(line, "// +build ") {
			// Only flip if it contains a positive match for a unix keyword
			matches := unixKeywordRe.FindAllStringIndex(line, -1)
			hasPositive := false
			for _, m := range matches {
				start := m[0]
				if start > 0 && line[start-1] == '!' {
					continue
				}
				hasPositive = true
				break
			}

			if hasPositive && !strings.Contains(line, "wasip1") {
				if strings.HasPrefix(line, "//go:build ") {
					lines[i] = strings.TrimSpace(line) + " || wasip1"
				} else {
					if strings.Contains(line, " ") {
						lines[i] = strings.TrimSpace(line) + " wasip1"
					} else {
						lines[i] = strings.TrimSpace(line) + ",wasip1"
					}
				}
				changed = true
			}
		}
	}
	if changed {
		return os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0644)
	}
	return nil
}

func actuallyExcludeWasip1(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	content := string(data)
	content = strings.ReplaceAll(content, " || wasip1", "")
	content = strings.ReplaceAll(content, "wasip1 || ", "")
	content = strings.ReplaceAll(content, ",wasip1", "")
	content = strings.ReplaceAll(content, "wasip1,", "")
	content = addWasip1ExclusionToTag(content)
	return os.WriteFile(filePath, []byte(content), 0644)
}

func addWasip1ExclusionToTag(content string) string {
	lines := strings.Split(content, "\n")
	found := false
	for i, line := range lines {
		if i > 30 {
			break
		}
		if strings.HasPrefix(line, "//go:build ") {
			if !strings.Contains(line, "wasip1") {
				lines[i] = strings.TrimSpace(line) + " && !wasip1"
			}
			found = true
		} else if strings.HasPrefix(line, "// +build ") {
			if !strings.Contains(line, "wasip1") {
				lines[i] = strings.TrimSpace(line) + ",!wasip1"
			}
			found = true
		}
	}
	if !found {
		return "//go:build !wasip1\n\n" + content
	}
	return strings.Join(lines, "\n")
}

func mergeOverlay(srcOverlay string, extra map[string]string, dstPath string) (string, error) {
	data, err := os.ReadFile(srcOverlay)
	if err != nil {
		return "", err
	}
	var overlay struct {
		Replace map[string]string `json:"Replace"`
	}
	if err := json.Unmarshal(data, &overlay); err != nil {
		return "", err
	}
	if overlay.Replace == nil {
		overlay.Replace = map[string]string{}
	}
	for k, v := range extra {
		overlay.Replace[k] = v
	}
	merged, _ := json.MarshalIndent(overlay, "", "  ")
	_ = os.WriteFile(dstPath, merged, 0644)
	return dstPath, nil
}

func modVersionFromGomod(content, mod string) (string, bool) {
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, mod+" ") || strings.HasPrefix(line, mod+"\t") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && strings.HasPrefix(parts[1], "v") {
				return parts[1], true
			}
		}
	}
	return "", false
}

func encodeModPath(mod string) string {
	var b strings.Builder
	for _, r := range mod {
		if unicode.IsUpper(r) {
			b.WriteRune('!')
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func canonPath(p string) string {
	abs, err := filepath.EvalSymlinks(p)
	if err != nil {
		abs = p
	}
	return filepath.ToSlash(cleanWindowsPath(abs))
}
