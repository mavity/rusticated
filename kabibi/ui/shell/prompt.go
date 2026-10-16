package shell

import (
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\([a-zA-Z]`)

// PromptInfo contains the prompt string (with ANSI codes if any) and its printable display width.
type PromptInfo struct {
	Text  string
	Width int
}

// FormatPrompt evaluates a bash-style PS1 template in the context of cwd and env.
// If ps1 is empty, it uses the default fallback "\w$ ".
func FormatPrompt(ps1, cwd string, env map[string]string) PromptInfo {
	if ps1 == "" {
		ps1 = `\w$ `
	}
	return ExpandPS1(ps1, cwd, env, time.Now())
}

// ExpandPS1 expands bash prompt escape sequences in a PS1 string.
func ExpandPS1(ps1, cwd string, env map[string]string, now time.Time) PromptInfo {
	if cwd == "" {
		cwd = "."
	}

	home := lookupHome(env)

	var sb strings.Builder
	chars := []rune(ps1)
	n := len(chars)

	for i := 0; i < n; i++ {
		if chars[i] == '\\' && i+1 < n {
			i++
			switch chars[i] {
			case 'w':
				sb.WriteString(formatCwd(cwd, home, false))
			case 'W':
				sb.WriteString(formatCwd(cwd, home, true))
			case 'u':
				sb.WriteString(lookupUser(env))
			case 'h':
				sb.WriteString(lookupHost(false))
			case 'H':
				sb.WriteString(lookupHost(true))
			case '$':
				if isRoot() {
					sb.WriteRune('#')
				} else {
					sb.WriteRune('$')
				}
			case 'd':
				// e.g. "Wed Jun 30"
				sb.WriteString(now.Format("Mon Jan 02"))
			case 't':
				// 24-hour HH:MM:SS
				sb.WriteString(now.Format("15:04:05"))
			case 'T':
				// 12-hour HH:MM:SS
				sb.WriteString(now.Format("03:04:05"))
			case '@':
				// 12-hour am/pm
				sb.WriteString(now.Format("03:04 PM"))
			case 'A':
				// 24-hour HH:MM
				sb.WriteString(now.Format("15:04"))
			case 'e', 'E':
				sb.WriteRune('\x1b')
			case 'n':
				sb.WriteRune('\n')
			case 'r':
				sb.WriteRune('\r')
			case 'a':
				sb.WriteRune('\a')
			case '\\':
				sb.WriteRune('\\')
			case '[':
				sb.WriteString(`\[`)
			case ']':
				sb.WriteString(`\]`)
			default:
				// Unrecognized escape: output literal \X
				sb.WriteRune('\\')
				sb.WriteRune(chars[i])
			}
		} else {
			sb.WriteRune(chars[i])
		}
	}

	raw := sb.String()

	// Strip bash zero-width markers \[ and \] from display string
	display := strings.ReplaceAll(strings.ReplaceAll(raw, `\[`, ""), `\]`, "")

	// Calculate visible width by stripping ANSI escape sequences
	clean := ansiRegex.ReplaceAllString(display, "")
	// Also strip any leftover newlines/carriage returns if measuring last line width
	width := visualWidth(clean)

	return PromptInfo{
		Text:  display,
		Width: width,
	}
}

func visualWidth(s string) int {
	// If multi-line prompt, measure width of the last line
	if idx := strings.LastIndex(s, "\n"); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimRight(s, "\r")
	return runewidth.StringWidth(s)
}

func formatCwd(cwd, home string, basenameOnly bool) string {
	cleanCwd := filepath.Clean(cwd)

	if home != "" {
		cleanHome := filepath.Clean(home)
		if cleanCwd == cleanHome {
			return "~"
		}
		prefix := cleanHome + string(filepath.Separator)
		if strings.HasPrefix(strings.ToLower(cleanCwd), strings.ToLower(prefix)) {
			cleanCwd = "~" + cleanCwd[len(cleanHome):]
		}
	}

	// Normalise separators to forward slashes for prompt presentation consistency
	cleanCwd = filepath.ToSlash(cleanCwd)

	if basenameOnly {
		if cleanCwd == "~" {
			return "~"
		}
		base := filepath.Base(cleanCwd)
		if base == "." || base == "/" || base == "\\" {
			return cleanCwd
		}
		return base
	}

	return cleanCwd
}

func lookupHome(env map[string]string) string {
	if env != nil {
		if h, ok := env["HOME"]; ok && h != "" {
			return h
		}
		if h, ok := env["USERPROFILE"]; ok && h != "" {
			return h
		}
	}
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h := os.Getenv("USERPROFILE"); h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return ""
}

func lookupUser(env map[string]string) string {
	if env != nil {
		if u, ok := env["USER"]; ok && u != "" {
			return u
		}
		if u, ok := env["USERNAME"]; ok && u != "" {
			return u
		}
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		// On Windows, user.Current() might return DOMAIN\Username
		parts := strings.Split(u.Username, `\`)
		return parts[len(parts)-1]
	}
	return "user"
}

func lookupHost(full bool) string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	if !full {
		if dot := strings.IndexByte(h, '.'); dot > 0 {
			h = h[:dot]
		}
	}
	return h
}
