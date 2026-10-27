package shell

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var openableURLPattern = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s<>"']+`)
var oscSequencePattern = regexp.MustCompile(`\x1b\][^\x1b\x07]*(\x07|\x1b\\)`)

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

func (s *Shell) openLatestOutputURL() bool {
	target := s.latestOutputURL()
	if target == "" {
		return false
	}
	opener := s.opts.OpenURL
	if opener == nil {
		opener = openExternalURL
	}
	if err := opener(target); err != nil {
		s.reportTerminalError(fmt.Errorf("open URL %s: %w", target, err))
	}
	return true
}

func extractLastOpenableURL(text string) string {
	text = oscSequencePattern.ReplaceAllString(text, "")
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
