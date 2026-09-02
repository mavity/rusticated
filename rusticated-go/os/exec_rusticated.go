//go:build wasip1

package exec

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

var ErrNotFound = errors.New("rusticated: executable not found in $PATH")

func isWindowsLikeEnv() bool {
	pi := syscall.GetPlatformInfo()
	return strings.EqualFold(pi.OSName, "windows")
}

func splitPathList(value string) []string {
	if value == "" {
		return nil
	}
	if isWindowsLikeEnv() && strings.Contains(value, ";") {
		return strings.Split(value, ";")
	}
	if strings.Contains(value, ";") {
		return strings.Split(value, ";")
	}
	return strings.Split(value, ":")
}

func lookupEnvAnyCase(key string) (string, bool) {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func expandWindowsEnvVars(value string) string {
	if value == "" {
		return value
	}
	for i := 0; i < 8; i++ {
		changed := false
		for start := 0; start < len(value); {
			begin := strings.Index(value[start:], "%")
			if begin < 0 {
				break
			}
			begin += start
			end := strings.Index(value[begin+1:], "%")
			if end < 0 {
				break
			}
			end += begin + 1
			name := value[begin+1 : end]
			if name == "" {
				start = end + 1
				continue
			}
			if replacement, ok := lookupEnvAnyCase(name); ok {
				value = value[:begin] + replacement + value[end+1:]
				changed = true
				start = begin + len(replacement)
				continue
			}
			start = end + 1
		}
		if !changed {
			break
		}
	}
	return value
}

func joinPath(dir, file string) string {
	if dir == "" {
		return file
	}
	dir = strings.TrimRight(dir, "/\\")
	if dir == "" {
		return file
	}
	return dir + "\\" + file
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if isWindowsLikeEnv() {
		return true
	}
	return info.Mode()&0o111 != 0
}

func lookExtensions(path, dir string) (string, error) {
	if path == "" {
		return "", ErrNotFound
	}
	if strings.Contains(path, "/") || strings.Contains(path, "\\") {
		if isExecutableFile(path) {
			return path, nil
		}
		return "", ErrNotFound
	}
	if dir != "" {
		candidate := joinPath(dir, path)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	pathext := expandWindowsEnvVars(os.Getenv("PATHEXT"))
	if pathext == "" {
		pathext = ".COM;.EXE;.BAT;.CMD"
	}
	for _, ext := range strings.Split(pathext, ";") {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if ext[0] != '.' {
			ext = "." + ext
		}
		candidate := joinPath(dir, path+strings.ToLower(ext))
		if isExecutableFile(candidate) {
			return candidate, nil
		}
		candidate = joinPath(dir, path+strings.ToUpper(ext))
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", ErrNotFound
}

func lookPath(file string) (string, error) {
	if file == "" {
		return "", ErrNotFound
	}
	if strings.Contains(file, "/") || strings.Contains(file, "\\") {
		if isExecutableFile(file) {
			return file, nil
		}
		return "", ErrNotFound
	}

	if strings.Contains(file, ".") {
		for _, dir := range splitPathList(expandWindowsEnvVars(os.Getenv("PATH"))) {
			if dir == "" {
				continue
			}
			candidate := joinPath(dir, file)
			if isExecutableFile(candidate) {
				return candidate, nil
			}
		}
		if isExecutableFile(file) {
			return file, nil
		}
		return "", ErrNotFound
	}

	for _, dir := range splitPathList(expandWindowsEnvVars(os.Getenv("PATH"))) {
		if dir == "" {
			continue
		}
		if resolved, err := lookExtensions(file, dir); err == nil {
			return resolved, nil
		}
	}
	return "", ErrNotFound
}
