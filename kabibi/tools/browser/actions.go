package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai"
)

// NavigateAction performs a `browser_navigate` transaction (open URL, back, forward, or reload),
// waits for Dual-Signal Settling, and returns the AX diff/tree + network summary.
func (m *Manager) NavigateAction(ctx context.Context, nickname, targetURL string, back, forward, reload bool) (string, error) {
	conn, tab, err := m.EnsureTab(ctx, nickname)
	if err != nil {
		return "", err
	}

	tab.mu.Lock()
	beforeAX := tab.LastAX
	tab.mu.Unlock()
	tab.netTracker.ResetWindow()

	sid := tab.SessionID
	switch {
	case reload:
		if err := conn.Call(ctx, sid, "Page.reload", nil, nil); err != nil {
			return "", err
		}
	case back || forward:
		var hist struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID  int    `json:"id"`
				URL string `json:"url"`
			} `json:"entries"`
		}
		if err := conn.Call(ctx, sid, "Page.getNavigationHistory", nil, &hist); err != nil {
			return "", err
		}
		targetIdx := hist.CurrentIndex
		if back {
			targetIdx--
		} else if forward {
			targetIdx++
		}
		if targetIdx < 0 || targetIdx >= len(hist.Entries) {
			return "", errors.New("navigation history out of bounds")
		}
		if err := conn.Call(ctx, sid, "Page.navigateToHistoryEntry", map[string]any{
			"entryId": hist.Entries[targetIdx].ID,
		}, nil); err != nil {
			return "", err
		}
	default:
		u := strings.TrimSpace(targetURL)
		if u == "" {
			return "", errors.New("url is required when back/forward/reload are not set")
		}
		if !strings.Contains(u, "://") && !strings.HasPrefix(u, "about:") && !strings.HasPrefix(u, "data:") {
			u = "https://" + u
		}
		if err := conn.Call(ctx, sid, "Page.navigate", map[string]any{
			"url": u,
		}, nil); err != nil {
			return "", err
		}
	}

	// Re-apply nickname stamp in case navigation created a new document
	stamp := buildNicknameStampScript(tab.Nickname)
	_ = conn.Call(ctx, sid, "Runtime.evaluate", map[string]any{
		"expression":    stamp,
		"returnByValue": true,
	}, nil)

	settled, err := WaitForSettle(ctx, conn, tab, 8000)
	if err != nil {
		return "", err
	}

	tab.mu.Lock()
	tab.LastAX = settled.Snapshot
	if settled.Snapshot.URL != "" {
		tab.CurrentURL = settled.Snapshot.URL
	}
	tab.mu.Unlock()

	return formatActionOutcome(tab.Nickname, beforeAX, settled), nil
}

// AXAction captures the current heuristically-trimmed AX tree for `nickname` and optionally
// runs the 4-tier progressive search funnel (Exact -> Regex -> Fuzzy -> EmbeddingGemma 2).
func (m *Manager) AXAction(ctx context.Context, nickname, search, rootSelector string, embedder ai.PartEmbedder) (string, error) {
	conn, tab, err := m.EnsureTab(ctx, nickname)
	if err != nil {
		return "", err
	}
	snap, err := CaptureAXSnapshot(ctx, conn, tab, rootSelector)
	if err != nil {
		return "", err
	}
	tab.mu.Lock()
	tab.LastAX = snap
	if snap.URL != "" {
		tab.CurrentURL = snap.URL
	}
	tab.mu.Unlock()

	hdr := fmt.Sprintf("[browser_ax on tab %q | url: %s | title: %q]", tab.Nickname, snap.URL, snap.Title)
	body := SearchAXSnapshot(snap, search, embedder)
	return hdr + "\n" + body, nil
}

// ClickAction resolves `target` (CSS selector, `role "name"`, or accessible caption),
// moves the mouse along a curved minimum-jerk trajectory to a Gaussian point inside the element,
// clicks, waits for Dual-Signal Settling, and returns the AX diff + network summary.
func (m *Manager) ClickAction(ctx context.Context, nickname, target, button string) (string, error) {
	conn, tab, err := m.EnsureTab(ctx, nickname)
	if err != nil {
		return "", err
	}
	beforeAX, _ := CaptureAXSnapshot(ctx, conn, tab, "")
	tab.netTracker.ResetWindow()

	box, desc, err := ResolveTargetBounds(ctx, conn, tab, target)
	if err != nil {
		return "", err
	}

	clickX, clickY, err := HumaneClick(ctx, conn, tab, box, button)
	if err != nil {
		return "", err
	}

	settled, err := WaitForSettle(ctx, conn, tab, 4000)
	if err != nil {
		return "", err
	}
	tab.mu.Lock()
	tab.LastAX = settled.Snapshot
	tab.mu.Unlock()

	lead := fmt.Sprintf("[clicked %s at (%.1f, %.1f)]", desc, clickX, clickY)
	return lead + "\n" + formatActionOutcome(tab.Nickname, beforeAX, settled), nil
}

// TypeAction resolves `target` (optional; if empty, types into currently focused element),
// types `text` with digraph-aware cadence and explicit Shift chord decomposition, waits for
// Dual-Signal Settling, and returns the AX diff + network summary.
func (m *Manager) TypeAction(ctx context.Context, nickname, target, text string, clearFirst, pressEnter bool) (string, error) {
	conn, tab, err := m.EnsureTab(ctx, nickname)
	if err != nil {
		return "", err
	}
	beforeAX, _ := CaptureAXSnapshot(ctx, conn, tab, "")
	tab.netTracker.ResetWindow()

	var boxPtr *Rect
	if strings.TrimSpace(target) != "" {
		box, _, err := ResolveTargetBounds(ctx, conn, tab, target)
		if err != nil {
			return "", err
		}
		boxPtr = &box
	}

	if err := HumaneType(ctx, conn, tab, boxPtr, text, clearFirst, pressEnter); err != nil {
		return "", err
	}

	settled, err := WaitForSettle(ctx, conn, tab, 4000)
	if err != nil {
		return "", err
	}
	tab.mu.Lock()
	tab.LastAX = settled.Snapshot
	tab.mu.Unlock()

	return formatActionOutcome(tab.Nickname, beforeAX, settled), nil
}

// EvalAction evaluates a JavaScript expression in the tab's page context (auto-awaiting Promises),
// waits for Dual-Signal Settling, and returns the result + AX diff + network summary.
func (m *Manager) EvalAction(ctx context.Context, nickname, expression string) (any, string, error) {
	conn, tab, err := m.EnsureTab(ctx, nickname)
	if err != nil {
		return nil, "", err
	}
	beforeAX, _ := CaptureAXSnapshot(ctx, conn, tab, "")
	tab.netTracker.ResetWindow()

	var evalRes struct {
		Result struct {
			Type        string `json:"type"`
			Value       any    `json:"value"`
			Description string `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := conn.Call(ctx, tab.SessionID, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}, &evalRes); err != nil {
		return nil, "", err
	}
	if evalRes.ExceptionDetails != nil {
		return nil, "", fmt.Errorf("js exception: %s", evalRes.ExceptionDetails.Text)
	}

	settled, err := WaitForSettle(ctx, conn, tab, 4000)
	if err != nil {
		return evalRes.Result.Value, "", nil
	}
	tab.mu.Lock()
	tab.LastAX = settled.Snapshot
	tab.mu.Unlock()

	val := evalRes.Result.Value
	if val == nil && evalRes.Result.Description != "" {
		val = evalRes.Result.Description
	}
	return val, formatActionOutcome(tab.Nickname, beforeAX, settled), nil
}

// RawCDPAction invokes an arbitrary raw CDP method (`method`, `params`) on the tab session
// (or browser root session if `browserLevel` is true).
func (m *Manager) RawCDPAction(ctx context.Context, nickname, method string, params map[string]any) (json.RawMessage, error) {
	conn, tab, err := m.EnsureTab(ctx, nickname)
	if err != nil {
		return nil, err
	}
	sid := tab.SessionID
	if strings.HasPrefix(method, "Target.") || strings.HasPrefix(method, "Browser.") {
		sid = ""
	}
	var raw json.RawMessage
	if err := conn.Call(ctx, sid, method, params, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// ScreenshotAction captures a screenshot of the tab Owned by `nickname` to `savePath`.
func (m *Manager) ScreenshotAction(ctx context.Context, nickname, savePath, target string, fullPage, adornChrome bool) (int, error) {
	conn, tab, err := m.EnsureTab(ctx, nickname)
	if err != nil {
		return 0, err
	}
	return CaptureScreenshot(ctx, conn, tab, savePath, target, fullPage, adornChrome)
}

func formatActionOutcome(nickname string, before *AXSnapshot, settled *SettleResult) string {
	if settled == nil {
		return ""
	}
	axDiff := DiffAXSnapshots(nickname, settled.ElapsedMs, before, settled.Snapshot)
	if settled.NetSummary != "" {
		return axDiff + "\n" + settled.NetSummary
	}
	return axDiff
}
