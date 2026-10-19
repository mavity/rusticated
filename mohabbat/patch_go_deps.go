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

	regast "mohabbat/mohabbat/regast"
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
		version, ok := modVersionFromGomod(string(gomodContent), target.module)
		if !ok {
			fmt.Printf("🍆  wasip1 JIT: %s not in go.mod require, skipping\n", target.module)
			continue
		}

		encoded := encodeModPath(target.module)
		srcDir := filepath.Join(gomodcache, encoded+"@"+version)
		if _, err := os.Stat(srcDir); err != nil {
			fmt.Printf("🍆  wasip1 JIT: downloading %s@%s\n", target.module, version)
			dlCmd := exec.Command(goBin, "mod", "download", target.module+"@"+version)
			if out, dlErr := dlCmd.CombinedOutput(); dlErr != nil {
				fmt.Printf("🍆  wasip1 JIT: failed to download %s@%s: %v\n%s\n", target.module, version, dlErr, string(out))
				continue
			}
			if _, err := os.Stat(srcDir); err != nil {
				if _, err := os.Stat(srcDir); err != nil {
					fmt.Printf("🍆 wasip1 JIT: %s@%s not in GOMODCACHE, skipping\n", target.module, version)
					continue
				}

				fmt.Printf("🍆 wasip1 JIT: downloading %s@%s\n", target.module, version)
				dlCmd := exec.Command(goBin, "mod", "download", target.module+"@"+version)
				if out, dlErr := dlCmd.CombinedOutput(); dlErr != nil {
					fmt.Printf("🍆 wasip1 JIT: failed to download %s@%s: %v\n%s\n", target.module, version, dlErr, string(out))
					continue
				}
			}

			if _, err := os.Stat(srcDir); err != nil {
				fmt.Printf("🍆 wasip1 JIT: %s@%s not in GOMODCACHE, skipping\n", target.module, version)
				continue
			}
		}

		// Load source files from srcDir into memory, apply all patches and gate flips
		// in memory, and flush each resulting file to target.jitDir once via writeFileIfChanged.
		files, err := loadDirIntoMemory(srcDir)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", target.module, err)
		}

		// Phase 1: Structural API translations using regast.
		var patches []regastPatch
		patches = append(patches, regastPatch{pat: `⦅unix \. Major⦆`, repl: `syscall.Major`})
		patches = append(patches, regastPatch{pat: `⦅unix \. Minor⦆`, repl: `syscall.Minor`})
		if target.module == "github.com/u-root/u-root" {
			patches = append(patches, regastPatch{pat: `⦅"golang.org/x/sys/unix"⦆`, repl: `"syscall"`})
		}

		for rel, data := range files {
			if strings.HasSuffix(rel, ".go") {
				if patched, err := patchGoStr(string(data), patches); err == nil {
					files[rel] = []byte(patched)
				}
			}
		}

		if target.module == "github.com/go-git/go-git/v5" {
			addGoGitWasip1Compatibility(files)
		}

		if target.module == "github.com/cyphar/filepath-securejoin" {
			if err := patchFilepathSecurejoinInMemory(files); err != nil {
				return nil, fmt.Errorf("patch %s: %w", target.module, err)
			}
		}

		// SPECIAL CASE: go-isatty
		if strings.HasSuffix(filepath.ToSlash(target.jitDir), "github.com/mattn/go-isatty") {
			for _, fname := range []string{
				"isatty_others.go", "isatty_bsd.go",
				"isatty_tcgets.go", "isatty_tiocgwinsz.go",
				"isatty_solaris.go", "isatty_plan9.go",
			} {
				if data, ok := files[fname]; ok {
					files[fname] = []byte(addWasip1ExclusionToTag(string(data)))
				}
			}
			wasip1Src := "//go:build wasip1\n\npackage isatty\n\nimport \"syscall\"\n\n" +
				"// IsTerminal reports whether fd is a terminal, using rusticated-go's Isatty.\n" +
				"func IsTerminal(fd uintptr) bool {\n\treturn syscall.Isatty(fd)\n}\n\n" +
				"// IsCygwinTerminal always returns false on wasip1.\n" +
				"func IsCygwinTerminal(fd uintptr) bool {\n\treturn false\n}\n"
			files["isatty_rusticated_wasip1.go"] = []byte(wasip1Src)
		}

		// SPECIAL CASE: termenv constants_solaris.go
		if strings.Contains(target.jitDir, "termenv") {
			if data, ok := files["constants_solaris.go"]; ok {
				content := string(data)
				if !strings.Contains(content, "//go:build") {
					files["constants_solaris.go"] = []byte("//go:build solaris\n\n" + content)
				}
			}
			patchTermenvColorProfileInMemory(files)
		}

		// SPECIAL CASE: golang.org/x/sys/unix — inject rusticated constants and types for wasip1.
		if strings.HasSuffix(filepath.ToSlash(target.jitDir), "golang.org/x/sys") {
			if err := removeStaleXSysCPUAliases(target.jitDir, files); err != nil {
				return nil, fmt.Errorf("clean stale x/sys/cpu aliases: %w", err)
			}
			zerrorsRel := "unix/zerrors_rusticated_wasip1.go"
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
			files[zerrorsRel] = []byte(zerrorsContent)
		}

		// SPECIAL CASE: u-root pkg/ls platform files sometimes lack build guards.
		if strings.HasSuffix(filepath.ToSlash(target.jitDir), "github.com/u-root/u-root") {
			for _, f := range []string{"fileinfo_linux.go", "fileinfo_openbsd.go", "fileinfo_unix.go"} {
				rel := "pkg/ls/" + f
				if data, ok := files[rel]; ok {
					content := string(data)
					if !strings.Contains(content, "//go:build") && !strings.Contains(content, "// +build") {
						tag := "!plan9 && !windows && !tamago"
						if f == "fileinfo_linux.go" {
							tag = "linux"
						} else if f == "fileinfo_openbsd.go" {
							tag = "openbsd"
						}
						files[rel] = []byte("//go:build " + tag + "\n\n" + content)
					}
				}
			}
			fsRel := "pkg/ls/filestring_unix.go"
			if data, ok := files[fsRel]; ok {
				content := string(data)
				if strings.Contains(content, "syscall.Major") {
					content = strings.ReplaceAll(content, "syscall.Major(fi.Rdev)", "uint32((fi.Rdev>>24)&0xff)")
					content = strings.ReplaceAll(content, "syscall.Minor(fi.Rdev)", "uint32(fi.Rdev&0xffffff)")
					content = strings.ReplaceAll(content, "\t\"syscall\"\n", "")
					files[fsRel] = []byte(content)
				}
			}
		}

		var sfx [][2]string
		if target.module != "github.com/go-git/go-git/v5" && target.module != "github.com/mattn/go-isatty" {
			sfx = applyGateFlipsInMemory(target.jitDir, files)
		}
		for _, sf := range sfx {
			overlayExtra[sf[0]] = sf[1]
		}

		changedCount, err := flushJitFilesIfChanged(target.jitDir, files)
		if err != nil {
			return nil, fmt.Errorf("flush %s: %w", target.module, err)
		}
		if changedCount > 0 {
			fmt.Printf("🍆  wasip1 JIT: target module %s -> %s (%d files updated)\n", target.module, target.jitDir, changedCount)
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
	return writeFileIfChangedMode(path, content, 0o644)
}

func writeFileIfChangedMode(path string, content []byte, perm os.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_ = os.Chmod(path, perm)
	return os.WriteFile(path, content, perm)
}

func loadDirIntoMemory(srcDir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	return files, err
}

func flushJitFilesIfChanged(dstDir string, files map[string][]byte) (int, error) {
	// Clean up any leftover stamp file if present from an older run.
	_ = os.Remove(filepath.Join(dstDir, ".mohabbat-jit-stamp.json"))

	changed := 0
	for rel, content := range files {
		dst := filepath.Join(dstDir, filepath.FromSlash(rel))
		if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, content) {
			continue
		}
		if err := writeFileIfChanged(dst, content); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func applyGateFlipsInMemory(jitDir string, files map[string][]byte) [][2]string {
	var keys []string
	for rel := range files {
		if strings.HasSuffix(rel, ".go") {
			keys = append(keys, rel)
		}
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	var flips [][2]string
	for _, rel := range keys {
		if virtualRel := suffixFlipTargetInMemory(jitDir, rel, files); virtualRel != "" {
			files[virtualRel] = append([]byte(nil), files[rel]...)
			virtualAbs := filepath.Join(jitDir, filepath.FromSlash(virtualRel))
			origAbs := filepath.Join(jitDir, filepath.FromSlash(rel))
			flips = append(flips, [2]string{canonPath(virtualAbs), canonPath(origAbs)})
		}
		flipGoFileTagsInMemory(jitDir, rel, files)
	}
	return flips
}

func joinSlashRel(dir, base string) string {
	if dir == "." || dir == "" {
		return base
	}
	return dir + "/" + base
}

func suffixFlipTargetInMemory(jitDir, rel string, files map[string][]byte) string {
	fullSlash := filepath.ToSlash(filepath.Join(jitDir, filepath.FromSlash(rel)))
	if strings.Contains(fullSlash, "golang.org/x/sys/unix") {
		return ""
	}
	if isXSysCPUFile(jitDir, rel) {
		return ""
	}
	base := pathBaseSlash(rel)
	dir := pathDirSlash(rel)
	for _, sfx := range unixLikeSuffixes {
		if strings.HasSuffix(base, sfx) {
			stem := base[:len(base)-len(sfx)]
			otherFiles := []string{stem + "_other.go", stem + "_stub.go", stem + "_stubs.go", stem + "_unix.go"}
			for _, of := range otherFiles {
				if _, ok := files[joinSlashRel(dir, of)]; ok {
					return ""
				}
			}
			return joinSlashRel(dir, stem+"_wasip1.go")
		}
	}
	return ""
}

func pathBaseSlash(rel string) string {
	if idx := strings.LastIndexByte(rel, '/'); idx >= 0 {
		return rel[idx+1:]
	}
	return rel
}

func pathDirSlash(rel string) string {
	if idx := strings.LastIndexByte(rel, '/'); idx >= 0 {
		return rel[:idx]
	}
	return ""
}

func flipGoFileTagsInMemory(jitDir, rel string, files map[string][]byte) {
	fullSlash := filepath.ToSlash(filepath.Join(jitDir, filepath.FromSlash(rel)))
	base := pathBaseSlash(rel)
	dir := pathDirSlash(rel)

	if isXSysCPUFile(jitDir, rel) || strings.Contains(fullSlash, "github.com/mattn/go-isatty") {
		return
	}

	if strings.Contains(fullSlash, "golang.org/x/sys/unix") {
		isGeneric := false
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
			return
		}
	}

	if strings.HasSuffix(base, "_unix.go") || base == "ioctl_unsigned.go" || base == "syscall_unix.go" {
		stem := base
		if strings.HasSuffix(base, "_unix.go") {
			stem = base[:len(base)-len("_unix.go")]
		} else {
			stem = strings.TrimSuffix(base, ".go")
		}

		if !strings.Contains(fullSlash, "golang.org/x/sys/unix") {
			sisters := []string{"_other.go", "_unsupported.go", "_stub.go", "_stubs.go", "_plan9.go", "_windows.go", "_wasm.go", "_tamago.go"}
			if base == "term_unix.go" {
				bsdRel := joinSlashRel(dir, "term_unix_bsd.go")
				if data, ok := files[bsdRel]; ok {
					files[bsdRel] = []byte(addWasip1ExclusionToTag(string(data)))
				}
			}
			for _, s := range sisters {
				sisterRel := joinSlashRel(dir, stem+s)
				if sisterRel != rel {
					if data, ok := files[sisterRel]; ok {
						files[sisterRel] = []byte(addWasip1ExclusionToTag(string(data)))
					}
				}
			}
		} else {
			sisters := []string{"_other.go", "_unsupported.go", "_stub.go", "_stubs.go", "_plan9.go", "_windows.go", "_wasm.go", "_tamago.go", "_bsd.go", "_unix_other.go", "_unix_bsd.go"}
			if base == "ioctl_unsigned.go" {
				sisters = append(sisters, "ioctl_signed.go")
			}
			for _, s := range sisters {
				sisterRel := joinSlashRel(dir, stem+s)
				if s == "ioctl_signed.go" {
					sisterRel = joinSlashRel(dir, "ioctl_signed.go")
				}
				if sisterRel != rel {
					if data, ok := files[sisterRel]; ok {
						files[sisterRel] = []byte(addWasip1ExclusionToTag(string(data)))
					}
				}
			}
		}
	}

	if strings.HasSuffix(fullSlash, "u-root/pkg/ls/fileinfo_unix.go") {
		tamagoRel := joinSlashRel(dir, "fileinfo_tamago.go")
		if data, ok := files[tamagoRel]; ok {
			files[tamagoRel] = []byte(addWasip1ExclusionToTag(string(data)))
		}
	}

	if data, ok := files[rel]; ok {
		files[rel] = []byte(flipTagsStr(string(data)))
	}
}

func isXSysCPUFile(jitDir, rel string) bool {
	fullSlash := filepath.ToSlash(filepath.Join(jitDir, filepath.FromSlash(rel)))
	return strings.Contains(fullSlash, "golang.org/x/sys/cpu/")
}

func removeStaleXSysCPUAliases(jitDir string, files map[string][]byte) error {
	for rel, source := range files {
		if !strings.HasPrefix(rel, "cpu/") {
			continue
		}
		base := pathBaseSlash(rel)
		dir := pathDirSlash(rel)
		for _, suffix := range unixLikeSuffixes {
			if !strings.HasSuffix(base, suffix) {
				continue
			}
			stem := base[:len(base)-len(suffix)]
			aliasRel := joinSlashRel(dir, stem+"_wasip1.go")
			if _, exists := files[aliasRel]; exists {
				continue
			}
			aliasPath := filepath.Join(jitDir, filepath.FromSlash(aliasRel))
			alias, err := os.ReadFile(aliasPath)
			if os.IsNotExist(err) || (err == nil && !bytes.Equal(alias, source)) {
				continue
			}
			if err != nil {
				return err
			}
			if err := os.Remove(aliasPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func patchFilepathSecurejoinInMemory(files map[string][]byte) error {
	const old = "strings.IndexRune(remainingPath, filepath.Separator)"
	const patched = "strings.IndexByte(remainingPath, filepath.Separator)"
	data, ok := files["join.go"]
	if !ok {
		return fmt.Errorf("join.go is missing")
	}
	content := string(data)
	if strings.Count(content, old) != 1 {
		return fmt.Errorf("expected exactly one filepath separator scan, found %d", strings.Count(content, old))
	}
	files["join.go"] = []byte(strings.Replace(content, old, patched, 1))
	return nil
}

func addGoGitWasip1Compatibility(files map[string][]byte) {
	files["worktree_rusticated_wasip1.go"] = []byte(`//go:build wasip1

package git

func isSymlinkWindowsNonAdmin(error) bool { return false }
`)
}

func flipTagsStr(content string) string {
	re, err := regast.Compile(`(?m)^//(?:go:|\s*\+)build.*[\r\n]+\s*`)
	if err != nil {
		return content
	}
	return re.ReplaceAllStringFunc(content, func(m string) string {
		matches := unixKeywordRe.FindAllStringIndex(m, -1)
		hasPositive := false
		for _, match := range matches {
			start := match[0]
			if start > 0 && m[start-1] == '!' {
				continue
			}
			hasPositive = true
			break
		}
		if hasPositive && !strings.Contains(m, "wasip1") {
			if strings.HasPrefix(m, "//go:build ") {
				return strings.TrimSpace(m) + " || wasip1\n\n"
			}
			if strings.Contains(m, " ") {
				return strings.TrimSpace(m) + " wasip1\n\n"
			}
			return strings.TrimSpace(m) + ",wasip1\n\n"
		}
		return m
	})
}

func patchTermenvColorProfileInMemory(files map[string][]byte) {
	replaceInMap := func(name string, repls [][2]string) {
		data, ok := files[name]
		if !ok {
			return
		}
		content := string(data)
		for _, r := range repls {
			if strings.Contains(content, r[0]) {
				content = strings.Replace(content, r[0], r[1], 1)
			}
		}
		files[name] = []byte(content)
	}

	replaceInMap("termenv.go", [][2]string{
		{
			"\tif len(o.environ.Getenv(\"CI\")) > 0 {\n\t\treturn false\n\t}\n\tif f, ok := o.Writer().(*os.File); ok {",
			"\tif f, ok := o.Writer().(*os.File); ok {",
		},
	})
	replaceInMap("termenv_unix.go", [][2]string{
		{
			"\tif !o.isTTY() {\n\t\treturn Ascii\n\t}\n\n\tif o.environ.Getenv(\"GOOGLE_CLOUD_SHELL\") == \"true\" {",
			"\tif o.environ.Getenv(\"GOOGLE_CLOUD_SHELL\") == \"true\" {",
		},
		{
			"\tif strings.Contains(term, \"ansi\") {\n\t\treturn ANSI\n\t}\n\n\treturn Ascii\n}",
			"\tif strings.Contains(term, \"ansi\") {\n\t\treturn ANSI\n\t}\n\n\treturn TrueColor\n}",
		},
	})
	replaceInMap("termenv_windows.go", [][2]string{
		{
			"\tif !o.isTTY() {\n\t\treturn Ascii\n\t}\n\n\tif o.environ.Getenv(\"ConEmuANSI\") == \"ON\" {",
			"\tif o.environ.Getenv(\"ConEmuANSI\") == \"ON\" {",
		},
	})
}

func addWasip1ExclusionToTag(content string) string {
	// Clean up any existing wasip1 additions first
	reWasip1 := regexp.MustCompile(`\s*(\|\||,)\s*wasip1|wasip1\s*(\|\||,)\s*|\s*(\|\||,)?\s*\bwasip1\b`)

	re, err := regast.Compile(`(?m)^//(?:go:|\s*\+)build.*[\r\n]+\s*`)
	if err != nil {
		return content
	}
	found := false
	newContent := re.ReplaceAllStringFunc(content, func(m string) string {
		found = true
		if strings.Contains(m, "wasip1") {
			m = reWasip1.ReplaceAllString(m, "")
		}
		if !strings.Contains(m, "!wasip1") {
			if strings.HasPrefix(m, "//go:build ") {
				inner := strings.TrimSpace(strings.TrimPrefix(m, "//go:build "))
				return fmt.Sprintf("//go:build (%s) && !wasip1\n\n", inner)
			}
			// // +build style
			return strings.TrimSpace(m) + ",!wasip1\n\n"
		}
		return m
	})

	if !found {
		return "//go:build !wasip1\n\n" + content
	}
	return newContent
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

	// Sort keys to ensure deterministic JSON output so we don't bust the Go build cache
	var keys []string
	for k := range overlay.Replace {
		keys = append(keys, k)
	}
	// A simple bubble sort since we just need it to be deterministic and it's small
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	var buf bytes.Buffer
	buf.WriteString("{\n  \"Replace\": {\n")
	for i, k := range keys {
		v := overlay.Replace[k]
		kJSON, _ := json.Marshal(k)
		vJSON, _ := json.Marshal(v)
		buf.WriteString("    ")
		buf.Write(kJSON)
		buf.WriteString(": ")
		buf.Write(vJSON)
		if i < len(keys)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("  }\n}\n")

	_ = writeFileIfChanged(dstPath, buf.Bytes())
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
