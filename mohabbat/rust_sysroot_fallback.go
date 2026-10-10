package mohabbat

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// This file implements the rustup-independent fallback described in RUSTUP.md.
//
// NON-NEGOTIABLE: nothing here runs on the happy path. Every function is only
// reached AFTER a normal `cargo`/`build-std` invocation has already failed and
// the failure was classified as a genuinely missing target std / rust-src.

type toolchainInfo struct {
	channel    string
	release    string
	commitHash string
	commitDate string
	host       string
	sysroot    string
}

// hasRustup reports whether a working `rustup` is on PATH.
func hasRustup() bool {
	return exec.Command("rustup", "--version").Run() == nil
}

// resolveToolchain reads the compiler identity as seen from dir (honoring the
// per-directory rustup-proxy resolution).
func resolveToolchain(dir string) (toolchainInfo, error) {
	vv := exec.Command("rustc", "-vV")
	vv.Dir = dir
	out, err := vv.Output()
	if err != nil {
		return toolchainInfo{}, fmt.Errorf("rustc -vV failed: %w", err)
	}
	var tc toolchainInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "release: "):
			tc.release = strings.TrimPrefix(line, "release: ")
			switch {
			case strings.Contains(tc.release, "-nightly"):
				tc.channel = "nightly"
			case strings.Contains(tc.release, "-beta"):
				tc.channel = "beta"
			default:
				tc.channel = "stable"
			}
		case strings.HasPrefix(line, "commit-hash: "):
			tc.commitHash = strings.TrimPrefix(line, "commit-hash: ")
		case strings.HasPrefix(line, "commit-date: "):
			tc.commitDate = strings.TrimPrefix(line, "commit-date: ")
		case strings.HasPrefix(line, "host: "):
			tc.host = strings.TrimPrefix(line, "host: ")
		}
	}

	sr := exec.Command("rustc", "--print", "sysroot")
	sr.Dir = dir
	sout, err := sr.Output()
	if err != nil {
		return toolchainInfo{}, fmt.Errorf("rustc --print sysroot failed: %w", err)
	}
	tc.sysroot = strings.TrimSpace(string(sout))
	return tc, nil
}

// rustTargetStdPresent reports whether the precompiled std for triple is
// already available to the compiler resolved from dir.
func rustTargetStdPresent(dir, triple string) bool {
	cmd := exec.Command("rustc", "--print", "target-libdir", "--target", triple)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	libdir := strings.TrimSpace(string(out))
	if libdir == "" {
		return false
	}
	entries, err := os.ReadDir(libdir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "libcore-") && strings.HasSuffix(e.Name(), ".rlib") {
			return true
		}
	}
	return false
}

// rustSrcPresent reports whether the rust-src component is available in sysroot.
func rustSrcPresent(sysroot string) bool {
	_, err := os.Stat(filepath.Join(sysroot, "lib", "rustlib", "src", "rust", "library", "core", "src", "lib.rs"))
	return err == nil
}

// ─── Channel manifest ─────────────────────────────────────────────────────────

type manifest struct{ lines []string }

func loadManifest(tc toolchainInfo) (*manifest, error) {
	local := filepath.Join(tc.sysroot, "lib", "rustlib", "multirust-channel-manifest.toml")
	if data, err := os.ReadFile(local); err == nil {
		fmt.Printf("🍆    using on-disk channel manifest\n")
		return &manifest{lines: strings.Split(string(data), "\n")}, nil
	}

	var url string
	switch tc.channel {
	case "stable", "beta":
		url = fmt.Sprintf("https://static.rust-lang.org/dist/channel-rust-%s.toml", tc.release)
	case "nightly":
		url = fmt.Sprintf("https://static.rust-lang.org/dist/%s/channel-rust-nightly.toml", tc.commitDate)
	default:
		return nil, fmt.Errorf("unsupported channel %q for remote manifest", tc.channel)
	}
	fmt.Printf("🍆    fetching channel manifest: %s\n", url)
	data, err := httpGet(url)
	if err != nil && tc.channel == "nightly" {
		// nightly dist-date can be off-by-one from commit-date; retry +1 day.
		if t, perr := time.Parse("2006-01-02", tc.commitDate); perr == nil {
			next := t.Add(24 * time.Hour).Format("2006-01-02")
			url2 := fmt.Sprintf("https://static.rust-lang.org/dist/%s/channel-rust-nightly.toml", next)
			fmt.Printf("🍆    retrying channel manifest (date skew): %s\n", url2)
			data, err = httpGet(url2)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("fetch channel manifest: %w", err)
	}
	return &manifest{lines: strings.Split(string(data), "\n")}, nil
}

// manifestArtifact returns the .tar.xz artifact (xz_url/xz_hash) for a package
// and target. For rust-src the target key is the literal "*".
func manifestArtifact(m *manifest, pkg, triple string) (url, sha256hex string, err error) {
	section := fmt.Sprintf("[pkg.%s.target.%s]", pkg, triple)
	if pkg == "rust-src" {
		section = `[pkg.rust-src.target."*"]`
	}
	inSection := false
	for _, raw := range m.lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			inSection = (line == section)
			continue
		}
		if !inSection {
			continue
		}
		// Deliberately read xz_url/xz_hash (the .tar.xz artifact), NOT url/hash
		// (which point at the .tar.gz and must not be used).
		if strings.HasPrefix(line, "xz_url = ") {
			url = strings.Trim(strings.TrimPrefix(line, "xz_url = "), "\"")
		} else if strings.HasPrefix(line, "xz_hash = ") {
			sha256hex = strings.Trim(strings.TrimPrefix(line, "xz_hash = "), "\"")
		}
		if url != "" && sha256hex != "" {
			return url, sha256hex, nil
		}
	}
	return "", "", fmt.Errorf("artifact not found in manifest for %s", section)
}

// ─── Download / verify / extract ──────────────────────────────────────────────

func httpGet(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// fetchAndVerify downloads url into cacheDir, verifying its SHA256 against the
// manifest value. A matching cached copy is reused; a mismatch is a hard error.
func fetchAndVerify(url, wantSHA, cacheDir string) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	filename := url[strings.LastIndex(url, "/")+1:]
	localPath := filepath.Join(cacheDir, filename)

	if data, err := os.ReadFile(localPath); err == nil {
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) == wantSHA {
			return localPath, nil
		}
		_ = os.Remove(localPath)
	}

	fmt.Printf("🍆    downloading %s\n", filename)
	data, err := httpGet(url)
	if err != nil {
		return "", err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != wantSHA {
		return "", fmt.Errorf("SHA256 mismatch for %s", filename)
	}
	if err := writeFileIfChanged(localPath, data); err != nil {
		return "", err
	}
	return localPath, nil
}

// extractTarXz unpacks a .tar.xz into destDir, rejecting path traversal.
func extractTarXz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	xr, err := xz.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(xr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if strings.Contains(hdr.Name, "..") {
			continue
		}
		target := filepath.Join(destDir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

// ─── Branch A: overlay sysroot for a missing precompiled target std ───────────

// composeOverlay builds a project-local overlay sysroot that links the present
// target dirs from the real sysroot and adds (as real dirs) the fetched
// addTargets. Returns the absolute overlay path for use with `--sysroot`.
func composeOverlay(tc toolchainInfo, overlayDir string, addTargets []string) (string, error) {
	rustlib := filepath.Join(overlayDir, "lib", "rustlib")
	if err := os.MkdirAll(rustlib, 0o755); err != nil {
		return "", err
	}

	realRustlib := filepath.Join(tc.sysroot, "lib", "rustlib")
	entries, err := os.ReadDir(realRustlib)
	if err != nil {
		return "", err
	}
	adding := map[string]bool{}
	for _, t := range addTargets {
		adding[t] = true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "src" || name == "components" || name == "etc" || adding[name] {
			continue
		}
		dst := filepath.Join(rustlib, name)
		_ = os.RemoveAll(dst)
		if err := linkDir(filepath.Join(realRustlib, name), dst); err != nil {
			return "", fmt.Errorf("link present target %s: %w", name, err)
		}
	}

	m, err := loadManifest(tc)
	if err != nil {
		return "", err
	}
	cacheDir := cacheDirFor(tc)
	for _, triple := range addTargets {
		url, sha, err := manifestArtifact(m, "rust-std", triple)
		if err != nil {
			return "", err
		}
		localPath, err := fetchAndVerify(url, sha, cacheDir)
		if err != nil {
			return "", err
		}
		extractDir := filepath.Join(cacheDir, "extract-std-"+triple)
		_ = os.RemoveAll(extractDir)
		if err := extractTarXz(localPath, extractDir); err != nil {
			return "", err
		}
		src, err := findInExtract(extractDir, filepath.Join("lib", "rustlib", triple))
		if err != nil {
			return "", fmt.Errorf("locate rustlib/%s in tarball: %w", triple, err)
		}
		dst := filepath.Join(rustlib, triple)
		_ = os.RemoveAll(dst)
		if err := copyDir(src, dst); err != nil {
			return "", err
		}
	}

	return filepath.Abs(overlayDir)
}

// ─── Branch B: RUST_SRC_PATH for a missing rust-src component ──────────────────

// rustSrcPathFromTarball fetches+extracts rust-src and returns the directory to
// hand to cargo via the RUST_SRC_PATH environment variable
// (<...>/lib/rustlib/src/rust/library).
func rustSrcPathFromTarball(tc toolchainInfo) (string, error) {
	m, err := loadManifest(tc)
	if err != nil {
		return "", err
	}
	url, sha, err := manifestArtifact(m, "rust-src", "*")
	if err != nil {
		return "", err
	}
	cacheDir := cacheDirFor(tc)
	localPath, err := fetchAndVerify(url, sha, cacheDir)
	if err != nil {
		return "", err
	}
	extractDir := filepath.Join(cacheDir, "extract-src")
	_ = os.RemoveAll(extractDir)
	if err := extractTarXz(localPath, extractDir); err != nil {
		return "", err
	}
	libDir, err := findInExtract(extractDir, filepath.Join("lib", "rustlib", "src", "rust", "library"))
	if err != nil {
		return "", fmt.Errorf("locate rust-src library dir in tarball: %w", err)
	}
	return filepath.Abs(libDir)
}

// remediateMissingRustSrc is invoked only after a `-Z build-std` build has
// already failed. It confirms the cause is a missing rust-src, then tries
// rustup first (if present) and falls back to fetching rust-src into the cache.
// It returns a RUST_SRC_PATH to set on the retried build, or "" when rustup
// supplied rust-src. A non-nil error means the failure was NOT a missing
// rust-src (caller should surface the original build error).
func remediateMissingRustSrc(dir string) (string, error) {
	tc, err := resolveToolchain(dir)
	if err != nil {
		return "", fmt.Errorf("resolve toolchain: %w", err)
	}
	if rustSrcPresent(tc.sysroot) {
		// rust-src is present, so the build failure is unrelated — do not remediate.
		return "", fmt.Errorf("rust-src already present; failure is unrelated")
	}

	if hasRustup() {
		fmt.Printf("🍆    rustup component add rust-src\n")
		addArgs := []string{"component", "add", "rust-src"}
		if rt := strings.TrimSpace(os.Getenv("RUSTUP_TOOLCHAIN")); rt != "" {
			addArgs = append(addArgs, "--toolchain", rt)
		}
		add := exec.Command("rustup", addArgs...)
		add.Stdout = os.Stdout
		add.Stderr = os.Stderr
		if add.Run() == nil {
			return "", nil
		}
		fmt.Printf("🍆    rustup component add failed; fetching rust-src\n")
	} else {
		fmt.Printf("🍆    rustup not found; fetching rust-src\n")
	}

	return rustSrcPathFromTarball(tc)
}

// ─── Shared helpers ───────────────────────────────────────────────────────────

func cacheDirFor(tc toolchainInfo) string {
	key := tc.release
	if tc.channel == "nightly" {
		key = tc.commitDate
	}
	return filepath.Join("target", "rust-fallback", key)
}

// findInExtract locates a path ending in suffix within an extracted tree whose
// top-level wrapper directory name is not known in advance (the dist tarballs
// nest an extra component folder, e.g. rust-std-nightly-<triple>/rust-std-<triple>/...).
func findInExtract(extractDir, suffix string) (string, error) {
	// Try common nesting depths explicitly, then glob as a fallback.
	for _, pat := range []string{
		filepath.Join(extractDir, "*", "*", suffix),
		filepath.Join(extractDir, "*", suffix),
		filepath.Join(extractDir, suffix),
	} {
		if matches, _ := filepath.Glob(pat); len(matches) > 0 {
			return matches[0], nil
		}
	}
	return "", fmt.Errorf("no path matching %q under %s", suffix, extractDir)
}

// linkDir creates a directory link from src to dst (symlink on unix; junction
// on Windows with a recursive-copy fallback).
func linkDir(src, dst string) error {
	if runtime.GOOS == "windows" {
		if exec.Command("cmd", "/c", "mklink", "/J", filepath.FromSlash(dst), filepath.FromSlash(src)).Run() == nil {
			return nil
		}
		return copyDir(src, dst)
	}
	return os.Symlink(src, dst)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
