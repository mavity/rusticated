package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// TrackedRequest holds telemetry for a single network request captured during an action window.
type TrackedRequest struct {
	RequestID    string
	Method       string
	URL          string
	ResourceType string
	PostData     string
	Status       int
	MimeType     string
	EncodedBytes int64
	StartedAt    time.Time
	FinishedAt   time.Time
	Completed    bool
	Failed       bool
	ErrorText    string
	ResponseBody string
}

// NetworkTracker records CDP `Network.*` events for a specific tab session, supporting
// both per-action delta windows and dual-signal network quiescence detection.
type NetworkTracker struct {
	mu         sync.Mutex
	conn       *Conn
	sessionID  string
	requests   map[string]*TrackedRequest
	order      []string
	lastChange time.Time
}

// NewNetworkTracker creates a NetworkTracker bound to a tab's CDP session.
func NewNetworkTracker(conn *Conn, sessionID string) *NetworkTracker {
	return &NetworkTracker{
		conn:       conn,
		sessionID:  sessionID,
		requests:   make(map[string]*TrackedRequest),
		lastChange: time.Now(),
	}
}

// ResetWindow clears previously recorded requests before starting a new action transaction.
func (nt *NetworkTracker) ResetWindow() {
	if nt == nil {
		return
	}
	nt.mu.Lock()
	defer nt.mu.Unlock()
	nt.requests = make(map[string]*TrackedRequest)
	nt.order = nil
	nt.lastChange = time.Now()
}

// HandleEvent processes a `Network.*` CDP event.
func (nt *NetworkTracker) HandleEvent(evt Event) {
	if nt == nil {
		return
	}
	nt.mu.Lock()
	defer nt.mu.Unlock()

	now := time.Now()
	switch evt.Method {
	case "Network.requestWillBeSent":
		var p struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Request   struct {
				URL      string `json:"url"`
				Method   string `json:"method"`
				PostData string `json:"postData"`
			} `json:"request"`
		}
		if json.Unmarshal(evt.Params, &p) == nil && p.RequestID != "" {
			if strings.HasPrefix(p.Request.URL, "data:") {
				return
			}
			tr, exists := nt.requests[p.RequestID]
			if !exists {
				tr = &TrackedRequest{
					RequestID: p.RequestID,
					StartedAt: now,
				}
				nt.requests[p.RequestID] = tr
				nt.order = append(nt.order, p.RequestID)
			}
			tr.Method = p.Request.Method
			tr.URL = p.Request.URL
			tr.PostData = p.Request.PostData
			if p.Type != "" {
				tr.ResourceType = p.Type
			}
			nt.lastChange = now
		}

	case "Network.responseReceived":
		var p struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Response  struct {
				URL               string  `json:"url"`
				Status            int     `json:"status"`
				MimeType          string  `json:"mimeType"`
				EncodedDataLength float64 `json:"encodedDataLength"`
			} `json:"response"`
		}
		if json.Unmarshal(evt.Params, &p) == nil && p.RequestID != "" {
			tr := nt.requests[p.RequestID]
			if tr == nil {
				tr = &TrackedRequest{
					RequestID: p.RequestID,
					Method:    "GET",
					URL:       p.Response.URL,
					StartedAt: now,
				}
				nt.requests[p.RequestID] = tr
				nt.order = append(nt.order, p.RequestID)
			}
			tr.Status = p.Response.Status
			tr.MimeType = p.Response.MimeType
			if p.Type != "" {
				tr.ResourceType = p.Type
			}
			if p.Response.EncodedDataLength > 0 {
				tr.EncodedBytes = int64(p.Response.EncodedDataLength)
			}
			nt.lastChange = now
		}

	case "Network.loadingFinished":
		var p struct {
			RequestID         string  `json:"requestId"`
			EncodedDataLength float64 `json:"encodedDataLength"`
		}
		if json.Unmarshal(evt.Params, &p) == nil && p.RequestID != "" {
			if tr := nt.requests[p.RequestID]; tr != nil {
				tr.Completed = true
				tr.FinishedAt = now
				if p.EncodedDataLength > 0 {
					tr.EncodedBytes = int64(p.EncodedDataLength)
				}
				nt.lastChange = now
				if shouldFetchResponseBody(tr) && nt.conn != nil {
					reqID := tr.RequestID
					conn := nt.conn
					sid := nt.sessionID
					go nt.fetchBodyAsync(conn, sid, reqID)
				}
			}
		}

	case "Network.loadingFailed":
		var p struct {
			RequestID string `json:"requestId"`
			ErrorText string `json:"errorText"`
		}
		if json.Unmarshal(evt.Params, &p) == nil && p.RequestID != "" {
			if tr := nt.requests[p.RequestID]; tr != nil {
				tr.Completed = true
				tr.Failed = true
				tr.ErrorText = p.ErrorText
				tr.FinishedAt = now
				nt.lastChange = now
			}
		}
	}
}

func shouldFetchResponseBody(tr *TrackedRequest) bool {
	if tr == nil {
		return false
	}
	if tr.Status >= 400 {
		return true
	}
	rt := strings.ToLower(tr.ResourceType)
	if rt == "fetch" || rt == "xhr" {
		return true
	}
	if tr.Method != "" && tr.Method != "GET" && tr.Method != "HEAD" {
		return true
	}
	return false
}

func (nt *NetworkTracker) fetchBodyAsync(conn *Conn, sid, reqID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var res struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := conn.Call(ctx, sid, "Network.getResponseBody", map[string]any{
		"requestId": reqID,
	}, &res); err == nil && !res.Base64Encoded && res.Body != "" {
		nt.mu.Lock()
		if tr := nt.requests[reqID]; tr != nil {
			tr.ResponseBody = truncateInline(res.Body, 200)
		}
		nt.mu.Unlock()
	}
}

// ActiveFlightCount returns the number of currently in-flight Document/XHR/Fetch requests,
// ignoring long-lived streaming types (`EventSource`, `WebSocket`, `Media`) or requests open > 3s.
func (nt *NetworkTracker) ActiveFlightCount(now time.Time) (inFlight int, quietDuration time.Duration) {
	if nt == nil {
		return 0, time.Second
	}
	nt.mu.Lock()
	defer nt.mu.Unlock()

	for _, tr := range nt.requests {
		if tr.Completed {
			continue
		}
		rt := strings.ToLower(tr.ResourceType)
		if rt == "eventsource" || rt == "websocket" || rt == "media" || rt == "ping" {
			continue
		}
		if now.Sub(tr.StartedAt) > 3*time.Second {
			continue
		}
		inFlight++
	}
	return inFlight, now.Sub(nt.lastChange)
}

// SnapshotRequests returns a copy of all tracked requests in the current action window.
func (nt *NetworkTracker) SnapshotRequests() []TrackedRequest {
	if nt == nil {
		return nil
	}
	nt.mu.Lock()
	defer nt.mu.Unlock()
	out := make([]TrackedRequest, 0, len(nt.order))
	for _, id := range nt.order {
		if tr := nt.requests[id]; tr != nil {
			out = append(out, *tr)
		}
	}
	return out
}

const maxDisplayURLLen = 80

// TruncateURL shortens long URLs/paths so query strings or tokens do not flood the context window.
func TruncateURL(u string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = maxDisplayURLLen
	}
	if len(u) <= maxLen {
		return u
	}
	if maxLen <= 12 {
		return u[:maxLen]
	}
	head := (maxLen * 2) / 3
	tail := maxLen - head - 1
	return u[:head] + "…" + u[len(u)-tail:]
}

func truncateInline(s string, maxLen int) string {
	clean := collapseInlineSpaces(s)
	if len(clean) <= maxLen {
		return clean
	}
	return clean[:maxLen-1] + "…"
}

func parseOriginAndPath(rawURL string) (origin string, pathAndQuery string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", TruncateURL(rawURL, maxDisplayURLLen)
	}
	origin = strings.ToLower(u.Scheme + "://" + u.Host)
	pq := u.RequestURI()
	if pq == "" {
		pq = "/"
	}
	return origin, TruncateURL(pq, maxDisplayURLLen)
}

func isStaticAssetRequest(tr TrackedRequest) bool {
	if tr.Failed || tr.Status >= 400 || (tr.Status != 0 && tr.Status != 200 && tr.Status != 204 && tr.Status != 304) {
		return false
	}
	if tr.Method != "" && tr.Method != "GET" && tr.Method != "HEAD" {
		return false
	}
	rt := strings.ToLower(tr.ResourceType)
	switch rt {
	case "stylesheet", "image", "font", "script", "media", "manifest", "other":
		return true
	}
	lowerURL := strings.ToLower(tr.URL)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico", ".css", ".woff", ".woff2", ".ttf", ".js"} {
		if strings.HasSuffix(lowerURL, ext) || strings.Contains(lowerURL, ext+"?") {
			return true
		}
	}
	return false
}

// FormatNetworkSummary formats the recorded network requests from an action window into a
// compact, high-signal summary block:
//   - Strips the current page's origin from same-origin URLs (e.g. `/api/v2/cart`).
//   - Truncates any long URLs (`>80` chars).
//   - Collapses static 200 OK assets (`Image`, `Script`, `Stylesheet`, `Font`).
//   - Shows detailed method, status, duration, and truncated request/response bodies for same-origin
//     API/Fetch/XHR calls and any failed/non-200 requests.
//   - Groups requests to any external origin per origin, displaying at most 2 URLs per external
//     origin and summarizing the rest with `… (+N more)`.
func FormatNetworkSummary(pageURL string, reqs []TrackedRequest, windowMs int64) string {
	if len(reqs) == 0 {
		return ""
	}
	pageOrigin, _ := parseOriginAndPath(pageURL)

	var (
		staticCount       int
		staticBytes       int64
		sameOriginNotable []TrackedRequest

		extOrder  []string
		extGroups = make(map[string][]TrackedRequest)
	)

	for _, r := range reqs {
		reqOrigin, _ := parseOriginAndPath(r.URL)
		sameOrigin := (pageOrigin != "" && reqOrigin == pageOrigin) || reqOrigin == ""

		if sameOrigin && isStaticAssetRequest(r) {
			staticCount++
			staticBytes += r.EncodedBytes
			continue
		}

		if sameOrigin {
			sameOriginNotable = append(sameOriginNotable, r)
		} else {
			if _, seen := extGroups[reqOrigin]; !seen {
				extOrder = append(extOrder, reqOrigin)
			}
			extGroups[reqOrigin] = append(extGroups[reqOrigin], r)
		}
	}

	sort.Strings(extOrder)

	var lines []string
	lines = append(lines, fmt.Sprintf("[net: %d reqs in %dms]", len(reqs), windowMs))

	for _, r := range sameOriginNotable {
		_, pathStr := parseOriginAndPath(r.URL)
		method := r.Method
		if method == "" {
			method = "GET"
		}
		durMs := int64(0)
		if !r.FinishedAt.IsZero() && !r.StartedAt.IsZero() {
			durMs = r.FinishedAt.Sub(r.StartedAt).Milliseconds()
		}
		statusStr := fmt.Sprintf("%d", r.Status)
		if r.Failed {
			statusStr = "FAILED"
			if r.ErrorText != "" {
				statusStr += " (" + r.ErrorText + ")"
			}
		} else if r.Status == 0 {
			statusStr = "pending"
		}
		lines = append(lines, fmt.Sprintf("  %s %s -> %s (%dms)", method, pathStr, statusStr, durMs))
		if r.PostData != "" {
			lines = append(lines, "    req: "+truncateInline(r.PostData, 200))
		}
		if r.ResponseBody != "" {
			lines = append(lines, "    res: "+truncateInline(r.ResponseBody, 200))
		}
	}

	// External origin groups: max 2 URLs per origin, otherwise ellipsis and count
	for _, orig := range extOrder {
		group := extGroups[orig]
		lines = append(lines, fmt.Sprintf("  [origin %s: %d reqs]", orig, len(group)))
		limit := len(group)
		if limit > 2 {
			limit = 2
		}
		for i := 0; i < limit; i++ {
			r := group[i]
			_, pathStr := parseOriginAndPath(r.URL)
			method := r.Method
			if method == "" {
				method = "GET"
			}
			statusStr := fmt.Sprintf("%d", r.Status)
			if r.Failed {
				statusStr = "FAILED"
			} else if r.Status == 0 {
				statusStr = "200"
			}
			lines = append(lines, fmt.Sprintf("    %s %s -> %s", method, pathStr, statusStr))
			if (r.Status >= 400 || r.Failed) && r.ResponseBody != "" {
				lines = append(lines, "      res: "+truncateInline(r.ResponseBody, 160))
			}
		}
		if len(group) > 2 {
			lines = append(lines, fmt.Sprintf("    … (+%d more)", len(group)-2))
		}
	}

	if staticCount > 0 {
		if staticBytes > 0 {
			lines = append(lines, fmt.Sprintf("  GET [%d static assets -> 200 OK (%dKB)]", staticCount, staticBytes/1024))
		} else {
			lines = append(lines, fmt.Sprintf("  GET [%d static assets -> 200 OK]", staticCount))
		}
	}

	return strings.Join(lines, "\n")
}

// SettleResult holds the outcome of the Dual-Signal Settling Engine.
type SettleResult struct {
	Snapshot   *AXSnapshot
	ElapsedMs  int64
	Partial    bool
	NetSummary string
}

// WaitForSettle runs the Dual-Signal Settling loop after an action:
//   - Initial grace delay (180ms) so synchronous JS handlers and `fetch()` calls can start.
//   - Signal A (Network Quiescence): `inFlightDocumentOrXHR == 0` for a continuous 240ms quiet window.
//   - Signal B (AX/DOM Layout Quiescence): two consecutive AX snapshots (75ms apart) have identical structural hash.
//   - Shared deadline (`maxSettleMs`, e.g. 4000ms for interactions, 8000ms for navigation).
func WaitForSettle(ctx context.Context, conn *Conn, tab *TabSession, maxSettleMs int) (*SettleResult, error) {
	if maxSettleMs <= 0 {
		maxSettleMs = 4000
	}
	started := time.Now()
	deadline := started.Add(time.Duration(maxSettleMs) * time.Millisecond)

	// Initial grace delay so synchronous JS handlers & XHRs kick off
	sleepMs(ctx, 180)

	var lastSnap *AXSnapshot
	stableAXCount := 0

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		snap, err := CaptureAXSnapshot(ctx, conn, tab, "")
		if err == nil && snap != nil {
			if lastSnap != nil && lastSnap.StructuralHash == snap.StructuralHash && lastSnap.URL == snap.URL {
				stableAXCount++
			} else {
				stableAXCount = 0
			}
			lastSnap = snap
		}

		inFlight, quietDur := tab.netTracker.ActiveFlightCount(time.Now())
		if inFlight == 0 && quietDur >= 220*time.Millisecond && stableAXCount >= 1 {
			break
		}

		sleepMs(ctx, 75)
	}

	if lastSnap == nil {
		var err error
		lastSnap, err = CaptureAXSnapshot(ctx, conn, tab, "")
		if err != nil {
			return nil, err
		}
	}

	elapsedMs := time.Since(started).Milliseconds()
	reqs := tab.netTracker.SnapshotRequests()
	netSum := FormatNetworkSummary(lastSnap.URL, reqs, elapsedMs)

	return &SettleResult{
		Snapshot:   lastSnap,
		ElapsedMs:  elapsedMs,
		Partial:    time.Now().After(deadline),
		NetSummary: netSum,
	}, nil
}
