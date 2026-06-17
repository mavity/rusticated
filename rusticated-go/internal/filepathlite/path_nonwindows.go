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
	l := CoreVolumeNameLen(path)
	if l > 0 {
		path = path[l:]
	} else if Separator == '\\' {
		return false
	}
	return len(path) > 0 && CoreIsPathSeparator(path[0])
}

func CoreVolumeNameLen(path string) int {
	if Separator != '\\' || len(path) < 2 || path[1] != ':' {
		return 0
	}
	c := path[0]
	if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') {
		return 2
	}
	return 0
}

func CoreIsLocal(path string) bool {
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
			if b.Len() > 0 {
				b.WriteByte(Separator)
			}
			b.WriteString(e)
		}
		return b.String()
	}
	// Windows-like join (simplified)
	var b strings.Builder
	for _, e := range elem {
		if e == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString(e)
			continue
		}
		if CoreIsAbs(e) {
			b.Reset()
			b.WriteString(e)
			continue
		}
		last := b.String()
		if !CoreIsPathSeparator(last[len(last)-1]) && !strings.HasSuffix(last, ":") {
			b.WriteByte(Separator)
		}
		b.WriteString(e)
	}
	return b.String()
}

func postClean(out *lazybuf) {}
