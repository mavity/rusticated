package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// TargetInfo represents a CDP Target.TargetInfo descriptor.
type TargetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Attached bool   `json:"attached"`
}

// ConsoleEntry records a console.log/warn/error or uncaught exception from the page.
type ConsoleEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Text      string    `json:"text"`
}

// TabSession tracks an attached CDP session for a specific nickname-owned browser tab.
type TabSession struct {
	Nickname  string
	TargetID  string
	SessionID string

	mu          sync.Mutex
	LastMouseX  float64
	LastMouseY  float64
	HasMousePos bool
	LastAX      *AXSnapshot
	CurrentURL  string

	netTracker *NetworkTracker
	consoleMu  sync.Mutex
	consoleLog []ConsoleEntry
}

// RecordConsole appends a console message to the tab's rolling buffer.
func (t *TabSession) RecordConsole(level, text string) {
	t.consoleMu.Lock()
	defer t.consoleMu.Unlock()
	if len(t.consoleLog) >= 200 {
		t.consoleLog = t.consoleLog[1:]
	}
	t.consoleLog = append(t.consoleLog, ConsoleEntry{
		Timestamp: time.Now(),
		Level:     level,
		Text:      text,
	})
}

// ConsoleSnapshot returns a copy of recent console entries.
func (t *TabSession) ConsoleSnapshot() []ConsoleEntry {
	t.consoleMu.Lock()
	defer t.consoleMu.Unlock()
	out := make([]ConsoleEntry, len(t.consoleLog))
	copy(out, t.consoleLog)
	return out
}

// Manager manages the browser WebSocket connection and in-browser nickname-to-tab persistence.
//
// In-Browser Nickname Storage Architecture:
// Instead of storing tab mappings in external local files, Kabibi stores the nickname directly
// inside the browser using two complementary in-browser mechanisms:
//  1. A hidden in-browser registry target (`about:blank#__kabibi_registry__`) holding `window.__kabibi_tab_map = {nickname: targetId}`
//     and `window.name = "__kabibi_registry__:" + JSON.stringify(map)`.
//  2. Every claimed page target also gets `Page.addScriptToEvaluateOnNewDocument` + immediate evaluation setting
//     `window.__kabibi_nick = nickname` and `window.name = "__kabibi_nick:" + nickname`, plus a visual title badge
//     `[🤖 nickname]` so both CDP discovery and the human user can immediately identify tab ownership across
//     processes and cross-origin navigations.
type Manager struct {
	mu sync.Mutex
	// Serialize tab setup while leaving mu free during CDP calls; event callbacks need mu.
	ensureTabMu sync.Mutex
	profileDir  string
	wsURL       string
	conn        *Conn
	tabs        map[string]*TabSession
}

var (
	defaultManagerOnce sync.Once
	defaultManager     *Manager
)

// DefaultManager returns the process-wide Browser Manager singleton.
func DefaultManager() *Manager {
	defaultManagerOnce.Do(func() {
		defaultManager = NewManager("")
	})
	return defaultManager
}

// NewManager creates a new Manager using the specified profile directory (or DefaultProfileDir if empty).
func NewManager(profileDir string) *Manager {
	if profileDir == "" {
		profileDir = DefaultProfileDir()
	}
	return &Manager{
		profileDir: profileDir,
		tabs:       make(map[string]*TabSession),
	}
}

// SetWebSocketURL overrides the browser WebSocket endpoint (used in tests or custom remote debugging setups).
func (m *Manager) SetWebSocketURL(wsURL string) {
	m.ensureTabMu.Lock()
	defer m.ensureTabMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		_ = m.conn.Close()
		m.conn = nil
	}
	m.wsURL = wsURL
	m.tabs = make(map[string]*TabSession)
}

// Close closes the underlying CDP WebSocket connection (leaving the browser process itself running!).
func (m *Manager) Close() error {
	m.ensureTabMu.Lock()
	defer m.ensureTabMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		err := m.conn.Close()
		m.conn = nil
		return err
	}
	return nil
}

func (m *Manager) ensureConnLocked(ctx context.Context) (*Conn, error) {
	if m.conn != nil {
		select {
		case <-m.conn.Closed():
			m.conn = nil
			m.tabs = make(map[string]*TabSession)
		default:
			return m.conn, nil
		}
	}

	wsURL := m.wsURL
	if wsURL == "" {
		var err error
		wsURL, err = DiscoverOrLaunchBrowser(ctx, m.profileDir)
		if err != nil {
			return nil, err
		}
	}

	conn, err := DialWebSocket(ctx, wsURL)
	if err != nil && m.wsURL == "" {
		// Retry discovery once if cached port went stale
		wsURL, err = DiscoverOrLaunchBrowser(ctx, m.profileDir)
		if err != nil {
			return nil, err
		}
		conn, err = DialWebSocket(ctx, wsURL)
	}
	if err != nil {
		return nil, err
	}

	conn.Subscribe(m.handleCDPEvent)
	m.conn = conn
	return conn, nil
}

func (m *Manager) handleCDPEvent(evt Event) {
	if evt.SessionID == "" {
		return
	}
	m.mu.Lock()
	var tab *TabSession
	for _, t := range m.tabs {
		if t.SessionID == evt.SessionID {
			tab = t
			break
		}
	}
	m.mu.Unlock()
	if tab == nil {
		return
	}

	if strings.HasPrefix(evt.Method, "Network.") {
		tab.netTracker.HandleEvent(evt)
		return
	}

	switch evt.Method {
	case "Page.frameNavigated":
		var payload struct {
			Frame struct {
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
			} `json:"frame"`
		}
		if json.Unmarshal(evt.Params, &payload) == nil && payload.Frame.ParentID == "" && payload.Frame.URL != "" {
			tab.mu.Lock()
			tab.CurrentURL = payload.Frame.URL
			tab.mu.Unlock()
		}

	case "Runtime.consoleAPICalled":
		var payload struct {
			Type string `json:"type"`
			Args []struct {
				Value       any    `json:"value"`
				Description string `json:"description"`
			} `json:"args"`
		}
		if json.Unmarshal(evt.Params, &payload) == nil {
			var parts []string
			for _, a := range payload.Args {
				if a.Value != nil {
					parts = append(parts, fmt.Sprintf("%v", a.Value))
				} else if a.Description != "" {
					parts = append(parts, a.Description)
				}
			}
			tab.RecordConsole(payload.Type, strings.Join(parts, " "))
		}

	case "Runtime.exceptionThrown":
		var payload struct {
			ExceptionDetails struct {
				Text string `json:"text"`
			} `json:"exceptionDetails"`
		}
		if json.Unmarshal(evt.Params, &payload) == nil && payload.ExceptionDetails.Text != "" {
			tab.RecordConsole("exception", payload.ExceptionDetails.Text)
		}
	}
}

const registryURLMarker = "about:blank#__kabibi_registry__"

// EnsureTab resolves or creates the browser tab owned by `nickname` using in-browser storage.
func (m *Manager) EnsureTab(ctx context.Context, nickname string) (*Conn, *TabSession, error) {
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		return nil, nil, errors.New("nickname parameter is required to identify which browser tab this agent owns")
	}

	m.ensureTabMu.Lock()
	defer m.ensureTabMu.Unlock()

	m.mu.Lock()
	conn, err := m.ensureConnLocked(ctx)
	existing := m.tabs[nickname]
	m.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}

	// 1. Check in-memory session cache and verify target still exists in browser
	targets, err := getTargets(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	targetExists := make(map[string]TargetInfo, len(targets))
	for _, ti := range targets {
		if ti.Type == "page" {
			targetExists[ti.TargetID] = ti
		}
	}

	if existing != nil {
		if ti, alive := targetExists[existing.TargetID]; alive {
			existing.mu.Lock()
			if ti.URL != "" {
				existing.CurrentURL = ti.URL
			}
			existing.mu.Unlock()
			return conn, existing, nil
		}
		m.mu.Lock()
		if m.tabs[nickname] == existing {
			delete(m.tabs, nickname)
		}
		m.mu.Unlock()
	}

	// 2. Query in-browser registry target (`about:blank#__kabibi_registry__`) and page targets' `window.__kabibi_nick`
	targetID := m.findTargetInBrowser(ctx, conn, targets, nickname)

	// 3. If not found in browser, claim an initial blank unowned tab or create a fresh tab
	if targetID == "" {
		for _, ti := range targets {
			if ti.Type == "page" && ti.URL == "about:blank" && !strings.Contains(ti.Title, "[🤖") {
				// Check if this blank tab is unclaimed
				if owner := probeTargetNickname(ctx, conn, ti.TargetID); owner == "" {
					targetID = ti.TargetID
					break
				}
			}
		}
	}

	if targetID == "" {
		var createRes struct {
			TargetID string `json:"targetId"`
		}
		if err := conn.Call(ctx, "", "Target.createTarget", map[string]any{
			"url": "about:blank",
		}, &createRes); err != nil {
			return nil, nil, fmt.Errorf("create target for %q: %w", nickname, err)
		}
		targetID = createRes.TargetID
	}

	// 4. Attach flat session to targetID and enable required CDP domains
	var attachRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true,
	}, &attachRes); err != nil {
		return nil, nil, fmt.Errorf("attach to target %s: %w", targetID, err)
	}
	sid := attachRes.SessionID

	for _, domain := range []string{"Page.enable", "Runtime.enable", "DOM.enable", "Network.enable", "Accessibility.enable"} {
		_ = conn.Call(ctx, sid, domain, nil, nil)
	}

	// 5. Stamp nickname inside the tab via Page.addScriptToEvaluateOnNewDocument + immediate Runtime.evaluate,
	// and update the in-browser registry target (`about:blank#__kabibi_registry__`).
	stampScript := buildNicknameStampScript(nickname)
	_ = conn.Call(ctx, sid, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source": stampScript,
	}, nil)
	_ = conn.Call(ctx, sid, "Runtime.evaluate", map[string]any{
		"expression":    stampScript,
		"returnByValue": true,
	}, nil)

	m.saveTargetToBrowserRegistry(ctx, conn, targets, nickname, targetID)

	curURL := "about:blank"
	if ti, ok := targetExists[targetID]; ok && ti.URL != "" {
		curURL = ti.URL
	}

	tab := &TabSession{
		Nickname:   nickname,
		TargetID:   targetID,
		SessionID:  sid,
		CurrentURL: curURL,
		netTracker: NewNetworkTracker(conn, sid),
	}
	m.mu.Lock()
	m.tabs[nickname] = tab
	m.mu.Unlock()
	return conn, tab, nil
}

func getTargets(ctx context.Context, conn *Conn) ([]TargetInfo, error) {
	var res struct {
		TargetInfos []TargetInfo `json:"targetInfos"`
	}
	if err := conn.Call(ctx, "", "Target.getTargets", nil, &res); err != nil {
		return nil, err
	}
	return res.TargetInfos, nil
}

func buildNicknameStampScript(nickname string) string {
	nickJSON, _ := json.Marshal(nickname)
	return fmt.Sprintf(`(() => {
		const nick = %s;
		window.__kabibi_nick = nick;
		try {
			if (!window.name || !window.name.startsWith("__kabibi_nick:")) {
				window.name = "__kabibi_nick:" + nick;
			}
		} catch (e) {}
		const badge = "[🤖 " + nick + "] ";
		const applyBadge = () => {
			try {
				if (typeof document !== "undefined" && document.title !== undefined) {
					if (!document.title.startsWith("[🤖 ")) {
						document.title = badge + (document.title || nick);
					}
				}
			} catch (e) {}
		};
		applyBadge();
		if (typeof document !== "undefined" && document.addEventListener) {
			document.addEventListener("DOMContentLoaded", applyBadge, { once: true });
		}
	})()`, string(nickJSON))
}

func (m *Manager) findTargetInBrowser(ctx context.Context, conn *Conn, targets []TargetInfo, nickname string) string {
	pageSet := make(map[string]bool, len(targets))
	var regTargetID string
	for _, ti := range targets {
		if ti.Type == "page" {
			if ti.URL == registryURLMarker {
				regTargetID = ti.TargetID
				continue
			}
			pageSet[ti.TargetID] = true
			// Fast title badge check
			if strings.HasPrefix(ti.Title, "[🤖 "+nickname+"]") {
				return ti.TargetID
			}
		}
	}

	// Check the in-browser registry page if present
	if regTargetID != "" {
		if mappedID := readFromBrowserRegistry(ctx, conn, regTargetID, nickname); mappedID != "" && pageSet[mappedID] {
			return mappedID
		}
	}

	// Probe page targets for window.__kabibi_nick or window.name
	for _, ti := range targets {
		if ti.Type != "page" || ti.URL == registryURLMarker {
			continue
		}
		if owner := probeTargetNickname(ctx, conn, ti.TargetID); owner == nickname {
			return ti.TargetID
		}
	}
	return ""
}

func probeTargetNickname(ctx context.Context, conn *Conn, targetID string) string {
	var attachRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true,
	}, &attachRes); err != nil || attachRes.SessionID == "" {
		return ""
	}
	var evalRes struct {
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
	}
	expr := `(() => {
		if (typeof window.__kabibi_nick === "string" && window.__kabibi_nick) return window.__kabibi_nick;
		if (typeof window.name === "string" && window.name.startsWith("__kabibi_nick:")) {
			return window.name.slice("__kabibi_nick:".length);
		}
		return "";
	})()`
	_ = conn.Call(ctx, attachRes.SessionID, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	}, &evalRes)
	return evalRes.Result.Value
}

func readFromBrowserRegistry(ctx context.Context, conn *Conn, regTargetID, nickname string) string {
	var attachRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": regTargetID,
		"flatten":  true,
	}, &attachRes); err != nil || attachRes.SessionID == "" {
		return ""
	}
	nickJSON, _ := json.Marshal(nickname)
	expr := fmt.Sprintf(`(() => {
		try {
			const m = window.__kabibi_tab_map || (window.name ? JSON.parse(window.name) : {});
			return m[%s] || "";
		} catch (e) { return ""; }
	})()`, string(nickJSON))
	var evalRes struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	_ = conn.Call(ctx, attachRes.SessionID, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	}, &evalRes)
	return evalRes.Result.Value
}

func (m *Manager) saveTargetToBrowserRegistry(ctx context.Context, conn *Conn, targets []TargetInfo, nickname, targetID string) {
	var regTargetID string
	for _, ti := range targets {
		if ti.Type == "page" && ti.URL == registryURLMarker {
			regTargetID = ti.TargetID
			break
		}
	}
	if regTargetID == "" {
		var createRes struct {
			TargetID string `json:"targetId"`
		}
		if err := conn.Call(ctx, "", "Target.createTarget", map[string]any{
			"url": registryURLMarker,
		}, &createRes); err != nil {
			return
		}
		regTargetID = createRes.TargetID
	}

	var attachRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": regTargetID,
		"flatten":  true,
	}, &attachRes); err != nil || attachRes.SessionID == "" {
		return
	}

	nickJSON, _ := json.Marshal(nickname)
	tidJSON, _ := json.Marshal(targetID)
	expr := fmt.Sprintf(`(() => {
		try {
			let m = window.__kabibi_tab_map;
			if (!m) {
				try { m = window.name ? JSON.parse(window.name) : {}; } catch (e) { m = {}; }
			}
			m[%s] = %s;
			window.__kabibi_tab_map = m;
			window.name = JSON.stringify(m);
			document.title = "__kabibi_registry__";
		} catch (e) {}
	})()`, string(nickJSON), string(tidJSON))

	_ = conn.Call(ctx, attachRes.SessionID, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	}, nil)
}
