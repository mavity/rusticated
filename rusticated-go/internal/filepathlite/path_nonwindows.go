package filepathlite

import (
	"errors"
	"strings"
	"syscall"
)

var (
	Separator     byte = '/'
	ListSeparator byte = ':'
)

func init() {
	pi := syscall.GetPlatformInfo()
	Separator = pi.PathSeparator
	ListSeparator = pi.PathListSeparator
}

func CoreIsPathSeparator(c uint8) bool {
	return c == Separator || (Separator == '\\' && c == '/')
}

func CoreIsAbs(path string) bool {
	if isDriveAbsolutePath(path) {
		return true
	}
	l := CoreVolumeNameLen(path)
	if l > 0 {
		path = path[l:]
	}
	return len(path) > 0 && CoreIsPathSeparator(path[0])
}

func CoreVolumeNameLen(path string) int {
	if Separator == '\\' && len(path) >= 2 &&
		CoreIsPathSeparator(path[0]) && CoreIsPathSeparator(path[1]) {
		// A Windows UNC server/share prefix is a volume for path cleaning.
		separators := 0
		for i := 2; i < len(path); i++ {
			if CoreIsPathSeparator(path[i]) {
				separators++
				if separators == 2 {
					return i
				}
			}
		}
		return len(path)
	}
	if len(path) < 2 || path[1] != ':' {
		return 0
	}
	c := path[0]
	if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') {
		return 2
	}
	return 0
}

func isDriveAbsolutePath(path string) bool {
	offset := 0
	if len(path) >= 4 && path[0] == '/' && isDriveLetter(path[1]) && path[2] == ':' && (path[3] == '/' || path[3] == '\\') {
		offset = 1
	}
	return len(path) >= offset+3 && isDriveLetter(path[offset]) && path[offset+1] == ':' &&
		(path[offset+2] == '/' || path[offset+2] == '\\')
}

func isDriveLetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func canonicalDrivePath(path string) string {
	if path[0] == '/' {
		path = path[1:]
	}
	return "/" + strings.ReplaceAll(path, "\\", "/")
}

func CoreIsLocal(path string) bool {
	if CoreVolumeNameLen(path) > 0 {
		return false
	}
	return unixIsLocal(path)
}

func CoreLocalize(path string) (string, error) {
	for i := 0; i < len(path); i++ {
		if path[i] == 0 {
			return "", errors.New("invalid path")
		}
	}
	return path, nil
}

func CoreJoin(elem []string) string {
	if len(elem) == 0 {
		return ""
	}
	if Separator != '\\' {
		// Unix-like join
		var b strings.Builder
		for _, e := range elem {
			if e == "" {
				continue
			}
			if isDriveAbsolutePath(e) {
				b.Reset()
				b.WriteString(canonicalDrivePath(e))
				continue
			}
			if b.Len() > 0 {
				b.WriteByte(Separator)
			}
			b.WriteString(e)
		}
		if b.Len() == 0 {
			return ""
		}
		return Clean(b.String())
	}
	// Windows-like join (simplified)
	var b strings.Builder
	var lastChar byte
	for _, e := range elem {
		if e == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString(e)
			lastChar = e[len(e)-1]
			continue
		}
		if CoreIsPathSeparator(lastChar) {
			for len(e) > 0 && CoreIsPathSeparator(e[0]) {
				e = e[1:]
			}
		} else if lastChar != ':' {
			b.WriteByte(Separator)
		}
		if len(e) > 0 {
			b.WriteString(e)
			lastChar = e[len(e)-1]
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return Clean(b.String())
}

func postClean(out *lazybuf) {}
