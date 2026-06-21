package mohabbat

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type BuildMetadata struct {
	Version  string
	Time     string
	Platform string
}

func GetBuildMetadata(ws string) BuildMetadata {
	version := "0.0.0-dev"
	cargoPath := filepath.Join(ws, "Cargo.toml")
	if data, err := os.ReadFile(cargoPath); err == nil {
		re := regexp.MustCompile(`(?m)^version\s*=\s*"([^"]+)"`)
		if m := re.FindStringSubmatch(string(data)); len(m) > 1 {
			version = m[1]
		}
	}

	return BuildMetadata{
		Version:  version,
		Time:     time.Now().UTC().Format(time.RFC3339),
		Platform: fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH),
	}
}

func upsertEnv(env []string, key, value string) []string {
	updated := make([]string, 0, len(env)+1)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], key) {
			continue
		}
		updated = append(updated, kv)
	}
	updated = append(updated, key+"="+value)
	return updated
}

func formatSize(n int64) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	l := len(s)
	for i, c := range s {
		out = append(out, byte(c))
		if (l-i-1)%3 == 0 && i != l-1 {
			out = append(out, ',')
		}
	}
	return string(out)
}

func uniqueStrings(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func IsProject(ws, dir string) bool {
	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(ws, dir)
	}
	if !fileExists(abs) {
		return false
	}
	return fileExists(filepath.Join(abs, "go.mod")) || fileExists(filepath.Join(abs, "Cargo.toml"))
}

func ResolveWorkspace(ws string) (string, error) {
	if ws != "" {
		return filepath.Abs(ws)
	}
	// Highest priority: when running inside a vegetable, MOHABBAT_VEGETABLE_PATH
	// points to the .bat file itself. Its directory is (or contains) the workspace root.
	if vegPath := os.Getenv("MOHABBAT_VEGETABLE_PATH"); vegPath != "" {
		dir := filepath.Dir(vegPath)
		for i := 0; i < 6; i++ {
			if _, err := os.Stat(filepath.Join(dir, "sysroot.toml")); err == nil {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	exe, err := os.Executable()
	if err == nil {
		// Walk up looking for sysroot.toml as the workspace root marker
		dir := filepath.Dir(exe)
		for i := 0; i < 6; i++ {
			if _, err := os.Stat(filepath.Join(dir, "sysroot.toml")); err == nil {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// Fallback: cwd
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Walk up from cwd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(cwd, "sysroot.toml")); err == nil {
			return cwd, nil
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			break
		}
		cwd = parent
	}
	return "", fmt.Errorf("could not locate workspace root (sysroot.toml not found)")
}

func Must(err error) {
	if err != nil {
		Die("%v", err)
	}
}

func Die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "🍆  error: "+format+"\n", a...)
	os.Exit(1)
}
