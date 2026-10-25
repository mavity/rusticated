package shell

import (
	"net/url"
	"regexp"
	"strings"
)

var openableURLPattern = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s<>"']+`)

func (s *Shell) latestOutputURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.drainLiveOutputsLocked()
	for i := len(s.blocks) - 1; i >= 0; i-- {
		text := s.blocks[i].Output.String()
		if live := s.blocks[i].liveOutput; live != nil {
			text = string(live.raw)
		}
		if text == "" {
			continue
		}
		return extractLastOpenableURL(text)
	}
	return ""
}

func extractLastOpenableURL(text string) string {
	text = ansiRegex.ReplaceAllString(text, "")
	matches := openableURLPattern.FindAllString(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		candidate := strings.TrimRight(matches[i], ".,;:!?)]}*")
		if strings.HasPrefix(strings.ToLower(candidate), "www.") {
			candidate = "https://" + candidate
		}
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Host == "" {
			continue
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			continue
		}
		return candidate
	}
	return ""
}
