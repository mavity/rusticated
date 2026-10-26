package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mavity/rusticated/platform"
)

const DefaultRemoteDebuggingPort = 9222

// FindBrowserBinary locates Chrome, Edge, Brave, Vivaldi, or Chromium on Windows, macOS, and Linux.
func FindBrowserBinary() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("KABIBI_BROWSER_BIN")); custom != "" {
		if info, err := os.Stat(custom); err == nil && !info.IsDir() {
			return custom, nil
		}
	}

	hostOS := platform.HostOS()
	if hostOS != "windows" {
		for _, name := range nativeBrowserNames(hostOS) {
			if resolved, err := exec.LookPath(name); err == nil {
				return resolved, nil
			}
		}
	}

	candidates := browserCandidatePaths(hostOS)
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}

	return "", fmt.Errorf(
		"no Chrome, Edge, or Chromium browser binary found for host OS %q; checked %v; set KABIBI_BROWSER_BIN if installed in a custom path",
		hostOS,
		candidates,
	)
}

func nativeBrowserNames(hostOS string) []string {
	if hostOS == "darwin" {
		return nil
	}
	return []string{
		"google-chrome-stable", "google-chrome", "microsoft-edge-stable",
		"microsoft-edge", "chromium-browser", "chromium", "brave-browser",
	}
}

// browserCandidatePaths returns filesystem candidates for the actual host OS.
// Keeping this separate makes platform selection testable without starting a browser.
func browserCandidatePaths(hostOS string) []string {
	if hostOS == "windows" {
		prefixes := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			driveEnd := strings.IndexByte(home, ':')
			if prefixes[0] == "" && driveEnd == 1 {
				prefixes[0] = filepath.Join(home[:driveEnd+1], "Program Files")
			}
			if prefixes[1] == "" && driveEnd == 1 {
				prefixes[1] = filepath.Join(home[:driveEnd+1], "Program Files (x86)")
			}
			if prefixes[2] == "" {
				prefixes[2] = filepath.Join(home, "AppData", "Local")
			}
		}
		relPaths := []string{
			`Google\Chrome\Application\chrome.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
			`Vivaldi\Application\vivaldi.exe`,
			`Chromium\Application\chrome.exe`,
		}
		candidates := make([]string, 0, len(prefixes)*len(relPaths))
		for _, rel := range relPaths {
			for _, pfx := range prefixes {
				if pfx != "" {
					candidates = append(candidates, filepath.Join(pfx, rel))
				}
			}
		}
		return candidates
	}

	if hostOS == "darwin" {
		home, _ := os.UserHomeDir()
		appRel := []string{
			`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,
			`/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge`,
			`/Applications/Chromium.app/Contents/MacOS/Chromium`,
			`/Applications/Brave Browser.app/Contents/MacOS/Brave Browser`,
		}
		candidates := append([]string(nil), appRel...)
		if home != "" {
			for _, rel := range appRel {
				candidates = append(candidates, filepath.Join(home, strings.TrimPrefix(rel, "/")))
			}
		}
		return candidates
	}

	return []string{
		"/opt/google/chrome/chrome", "/opt/microsoft/msedge/msedge",
		"/usr/bin/google-chrome-stable", "/usr/bin/google-chrome",
		"/usr/bin/microsoft-edge-stable", "/usr/bin/chromium-browser",
		"/usr/bin/chromium", "/snap/bin/chromium",
	}
}

// DefaultProfileDir returns the persistent user-data-dir for Kabibi's browser instance.
func DefaultProfileDir() string {
	if custom := strings.TrimSpace(os.Getenv("KABIBI_BROWSER_PROFILE")); custom != "" {
		return custom
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "kabibi-browser-profile")
}

type versionResponse struct {
	Browser              string `json:"Browser"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// ProbeBrowserWS queries http://127.0.0.1:<port>/json/version for an active browser WebSocket URL.
func ProbeBrowserWS(ctx context.Context, port int) (string, error) {
	if port <= 0 || port > 65535 {
		return "", errors.New("invalid port")
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var v versionResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	if v.WebSocketDebuggerURL == "" {
		return "", errors.New("empty webSocketDebuggerUrl")
	}
	return v.WebSocketDebuggerURL, nil
}

// ReadDevToolsActivePort reads <profileDir>/DevToolsActivePort if present.
func ReadDevToolsActivePort(profileDir string) (port int, wsPath string, err error) {
	data, err := os.ReadFile(filepath.Join(profileDir, "DevToolsActivePort"))
	if err != nil {
		return 0, "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return 0, "", errors.New("empty DevToolsActivePort")
	}
	port, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, "", err
	}
	if len(lines) > 1 {
		wsPath = strings.TrimSpace(lines[1])
	}
	return port, wsPath, nil
}

// DiscoverOrLaunchBrowser attaches to an already-running Kabibi browser instance or spawns a new
// detached browser process that survives Kabibi exit.
func DiscoverOrLaunchBrowser(ctx context.Context, profileDir string) (string, error) {
	if customWS := strings.TrimSpace(os.Getenv("KABIBI_BROWSER_WS")); customWS != "" {
		return customWS, nil
	}
	if profileDir == "" {
		profileDir = DefaultProfileDir()
	}
	_ = os.MkdirAll(profileDir, 0o755)

	// 1. Check DevToolsActivePort file first
	if port, wsPath, err := ReadDevToolsActivePort(profileDir); err == nil && port > 0 {
		if wsURL, err := ProbeBrowserWS(ctx, port); err == nil {
			return wsURL, nil
		}
		if wsPath != "" {
			candidate := fmt.Sprintf("ws://127.0.0.1:%d%s", port, wsPath)
			if conn, err := DialWebSocket(ctx, candidate); err == nil {
				_ = conn.Close()
				return candidate, nil
			}
		}
	}

	// 2. Check default debugging port 9222
	if wsURL, err := ProbeBrowserWS(ctx, DefaultRemoteDebuggingPort); err == nil {
		return wsURL, nil
	}

	// 3. Spawn a new detached browser process outside the job/session teardown cascade
	binPath, err := FindBrowserBinary()
	if err != nil {
		return "", err
	}

	// Remove stale DevToolsActivePort before launching
	_ = os.Remove(filepath.Join(profileDir, "DevToolsActivePort"))

	args := []string{
		"--user-data-dir=" + profileDir,
		fmt.Sprintf("--remote-debugging-port=%d", DefaultRemoteDebuggingPort),
		"--remote-allow-origins=*",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"about:blank",
	}
	if strings.EqualFold(os.Getenv("KABIBI_BROWSER_HEADLESS"), "1") || strings.EqualFold(os.Getenv("KABIBI_BROWSER_HEADLESS"), "true") {
		args = append(args[:len(args)-1], "--headless=new", "about:blank")
	}

	if err := startDetachedBrowserProcess(binPath, args); err != nil {
		return "", fmt.Errorf("launch browser %q: %w", binPath, err)
	}

	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		if port, _, err := ReadDevToolsActivePort(profileDir); err == nil && port > 0 {
			if wsURL, err := ProbeBrowserWS(ctx, port); err == nil {
				return wsURL, nil
			}
		}
		if wsURL, err := ProbeBrowserWS(ctx, DefaultRemoteDebuggingPort); err == nil {
			return wsURL, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return "", errors.New("timed out waiting for launched browser DevTools endpoint")
}
