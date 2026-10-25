package browser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAXTree_TagWithoutAngleBrackets_ParentLexicalDedup_AndPreserves50Siblings(t *testing.T) {
	q := func(s string) rawAXValue {
		b, _ := json.Marshal(s)
		return rawAXValue{Value: b}
	}

	rawNodes := []rawAXNode{
		{
			NodeID:   "root",
			Role:     q("RootWebArea"),
			ChildIDs: []string{"hdr", "list_parent"},
		},
		{
			NodeID:           "hdr",
			Role:             q("generic"),
			Name:             q("Welcome to Kabibi Store"),
			BackendDOMNodeID: 10,
			ChildIDs:         []string{"hdr_h1", "hdr_sub"},
		},
		{
			NodeID:           "hdr_h1",
			Role:             q("heading"),
			Name:             q("Welcome to"),
			BackendDOMNodeID: 11,
		},
		{
			NodeID:           "hdr_sub",
			Role:             q("link"),
			Name:             q("Kabibi Store"),
			BackendDOMNodeID: 12,
		},
		{
			NodeID:           "list_parent",
			Role:             q("list"),
			BackendDOMNodeID: 20,
		},
	}

	domMap := map[int64]domNodeMeta{
		10: {Tag: "<header>", X: 10, Y: 10, W: 900, H: 60},
		11: {Tag: "<h1>", X: 16, Y: 18, W: 100, H: 24},
		12: {Tag: "<a>", X: 130, Y: 18, W: 110, H: 24},
		20: {Tag: "<ul>", X: 10, Y: 400, W: 600, H: 1500},
	}

	// Add 55 structurally identical sibling items to verify NO sibling repetition folding occurs!
	for i := 1; i <= 55; i++ {
		id := fmt.Sprintf("item_%d", i)
		bid := int64(100 + i)
		rawNodes[4].ChildIDs = append(rawNodes[4].ChildIDs, id)
		rawNodes = append(rawNodes, rawAXNode{
			NodeID:           id,
			Role:             q("listitem"),
			Name:             q(fmt.Sprintf("Product Entry #%02d", i)),
			BackendDOMNodeID: bid,
		})
		domMap[bid] = domNodeMeta{
			Tag: "li",
			X:   20,
			Y:   float64(400 + i*24),
			W:   150,
			H:   22,
		}
	}

	roots := BuildTrimmedAXTree(rawNodes, domMap, nil)
	lines, _, _ := FormatAXTreeLines(roots, 1280, 800)
	rendered := strings.Join(lines, "\n")

	// 1. HTML tag fallback must be `header` without angle brackets `<header>`
	if strings.Contains(rendered, "<header>") || strings.Contains(rendered, "<ul>") {
		t.Fatalf("expected tag names without angle brackets, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "header") {
		t.Fatalf("expected semantic tag 'header' in AX tree, got:\n%s", rendered)
	}

	// 2. Parent `header` originally had Name "Welcome to Kabibi Store", which lexically repeats
	// its children ("Welcome to" and "Kabibi Store"). Verify parent lost its redundant text
	// while children kept theirs!
	if strings.Contains(rendered, `header "Welcome to Kabibi Store"`) {
		t.Fatalf("expected parent redundant text to be deduplicated, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `heading "Welcome to"`) || !strings.Contains(rendered, `link "Kabibi Store"`) {
		t.Fatalf("expected children to retain their individual text, got:\n%s", rendered)
	}

	// 3. All 55 sibling list items must be retained (no repetition folding of siblings!)
	for i := 1; i <= 55; i++ {
		want := fmt.Sprintf(`listitem "Product Entry #%02d"`, i)
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing sibling %d (%s); all 55 data items must be preserved!\n%s", i, want, rendered)
		}
	}

	// 4. Verify NO `@e` or `#e` short ref IDs are emitted
	if strings.Contains(rendered, "[#e") || strings.Contains(rendered, "@e") {
		t.Fatalf("unexpected short ref IDs in AX output:\n%s", rendered)
	}

	// 5. Verify geometry jump between `link "Kabibi Store"` (Y=18) and `list` (Y=400) reflects
	// bounds on BOTH elements, and `link "Kabibi Store"` (110x24 matching 12 chars) omits WxH
	// whereas `list` (600x1500 with empty text) includes `600x1500`.
	if !strings.Contains(rendered, `link "Kabibi Store" (@130,18)`) {
		t.Fatalf("expected link before jump to show location-only (@130,18) since size matches text:\n%s", rendered)
	}
	if !strings.Contains(rendered, `list (@10,400 600x1500)`) {
		t.Fatalf("expected list after jump to show full bounds (@10,400 600x1500):\n%s", rendered)
	}
}

func TestNetworkSummary_ExternalOriginGroupingAndLongURLTruncation(t *testing.T) {
	now := time.Now()
	longQuery := strings.Repeat("abcdef0123456789", 8)
	reqs := []TrackedRequest{
		// Same-origin static assets -> collapsed
		{Method: "GET", URL: "https://shop.example.com/assets/app.js", ResourceType: "Script", Status: 200, EncodedBytes: 32768, Completed: true},
		{Method: "GET", URL: "https://shop.example.com/assets/logo.png", ResourceType: "Image", Status: 200, EncodedBytes: 16384, Completed: true},
		// Same-origin API call with long URL + payload
		{
			Method:       "POST",
			URL:          "https://shop.example.com/api/v2/checkout/session?token=" + longQuery,
			ResourceType: "Fetch",
			PostData:     `{"sku":"KB-99","qty":1}`,
			ResponseBody: `{"ok":true,"order_id":"ORD-77"}`,
			Status:       200,
			StartedAt:    now,
			FinishedAt:   now.Add(65 * time.Millisecond),
			Completed:    true,
		},
		// External origin with 4 requests -> must group per origin, show max 2 URLs, then `… (+2 more)`
		{Method: "GET", URL: "https://cdn.analytics.example.org/collect?e=pageview", Status: 200, Completed: true},
		{Method: "POST", URL: "https://cdn.analytics.example.org/collect?e=click", Status: 200, Completed: true},
		{Method: "POST", URL: "https://cdn.analytics.example.org/collect?e=scroll", Status: 200, Completed: true},
		{Method: "POST", URL: "https://cdn.analytics.example.org/collect?e=hover", Status: 200, Completed: true},
	}

	summary := FormatNetworkSummary("https://shop.example.com/cart", reqs, 210)

	if !strings.Contains(summary, "POST /api/v2/checkout/session?token=") || !strings.Contains(summary, "…") {
		t.Fatalf("expected same-origin API call with origin stripped and long URL truncated:\n%s", summary)
	}
	if !strings.Contains(summary, `req: {"sku":"KB-99","qty":1}`) || !strings.Contains(summary, `res: {"ok":true,"order_id":"ORD-77"}`) {
		t.Fatalf("expected request/response bodies in summary:\n%s", summary)
	}
	if !strings.Contains(summary, "[origin https://cdn.analytics.example.org: 4 reqs]") {
		t.Fatalf("expected external origin group header:\n%s", summary)
	}
	if !strings.Contains(summary, "… (+2 more)") {
		t.Fatalf("expected external origin with >2 URLs to truncate with '… (+2 more)':\n%s", summary)
	}
	if strings.Contains(summary, "e=scroll") || strings.Contains(summary, "e=hover") {
		t.Fatalf("expected 3rd and 4th external origin URLs to be omitted:\n%s", summary)
	}
	if !strings.Contains(summary, "GET [2 static assets -> 200 OK (48KB)]") {
		t.Fatalf("expected collapsed static asset counter:\n%s", summary)
	}
}

func TestBiomimeticMouseAndKeystrokePhysics(t *testing.T) {
	box := Rect{X: 200, Y: 300, W: 120, H: 40}
	cx, cy := box.Center()
	px, py := PickGaussianTargetPoint(box, nil)
	if px == cx && py == cy {
		t.Fatalf("PickGaussianTargetPoint landed on robotic dead-center (%v, %v)", px, py)
	}
	if px < box.X || px > box.X+box.W || py < box.Y || py > box.Y+box.H {
		t.Fatalf("PickGaussianTargetPoint (%v, %v) outside box %+v", px, py, box)
	}

	path := GenerateBezierMousePath(20, 20, px, py, 10)
	if len(path) != 10 {
		t.Fatalf("expected 10 Bezier steps, got %d", len(path))
	}
	if path[len(path)-1].X != px || path[len(path)-1].Y != py {
		t.Fatalf("Bezier path did not terminate at target (%v, %v): got %+v", px, py, path[len(path)-1])
	}

	// Verify explicit Shift chord decomposition for "Hi*!"
	chords := PlanHumaneKeystrokes("A*")
	// Both 'A' and '*' are shifted -> each decomposes into 4 events (Shift down, keyDown, keyUp, Shift up) = 8 events
	if len(chords) != 8 {
		t.Fatalf("expected 8 chord events for 'A*', got %d: %+v", len(chords), chords)
	}
	if chords[0].Key != "Shift" || chords[0].Type != "rawKeyDown" || chords[1].Key != "A" || chords[1].Modifiers != 8 || chords[3].Key != "Shift" || chords[3].Type != "keyUp" {
		t.Fatalf("unexpected Shift chord decomposition for 'A': %+v", chords[:4])
	}
	if chords[4].Key != "Shift" || chords[5].Key != "*" || chords[5].Code != "Digit8" || chords[5].Modifiers != 8 {
		t.Fatalf("unexpected Shift chord decomposition for '*': %+v", chords[4:])
	}
}

// mockCDPBrowser simulates a Chrome DevTools WebSocket server with in-browser target storage
// (`window.__kabibi_tab_map` & `window.__kabibi_nick`) so we can test the full Manager & 7 tools
// end-to-end deterministically.
type mockCDPBrowser struct {
	mu      sync.Mutex
	targets map[string]*mockTarget
	nextID  int
}

type mockTarget struct {
	ID       string
	Session  string
	URL      string
	Title    string
	Nickname string
	RegMap   map[string]string
	Clicked  bool
	Typed    string
}

func startMockCDPServer(t *testing.T, emitEventBeforeResponse ...bool) string {
	t.Helper()
	emitPageEventBeforeResponse := len(emitEventBeforeResponse) > 0 && emitEventBeforeResponse[0]
	b := &mockCDPBrowser{
		targets: make(map[string]*mockTarget),
	}
	// Initial blank tab
	b.targets["tab_1"] = &mockTarget{
		ID:      "tab_1",
		Session: "sess_1",
		URL:     "about:blank",
		Title:   "",
		RegMap:  make(map[string]string),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("webserver doesn't support hijacking")
		}
		secKey := r.Header.Get("Sec-WebSocket-Key")
		accept := computeWSKeyAccept(secKey)

		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()

		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
		_ = rw.Flush()

		for {
			_, payload, err := readMockWSFrame(rw.Reader)
			if err != nil {
				return
			}
			var req rpcRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				continue
			}
			res := b.handleRPC(req)
			if emitPageEventBeforeResponse && req.Method == "Page.enable" {
				eventBytes, _ := json.Marshal(map[string]any{
					"method":    "Page.frameNavigated",
					"sessionId": req.SessionID,
					"params": map[string]any{
						"frame": map[string]any{"url": "about:blank"},
					},
				})
				if err := writeMockWSFrame(conn, eventBytes); err != nil {
					return
				}
			}
			respBytes, _ := json.Marshal(map[string]any{
				"id":        req.ID,
				"sessionId": req.SessionID,
				"result":    res,
			})
			_ = writeMockWSFrame(conn, respBytes)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func (b *mockCDPBrowser) targetBySession(sid string) *mockTarget {
	for _, tgt := range b.targets {
		if tgt.Session == sid {
			return tgt
		}
	}
	return b.targets["tab_1"]
}

func (b *mockCDPBrowser) handleRPC(req rpcRequest) any {
	b.mu.Lock()
	defer b.mu.Unlock()

	paramsMap, _ := req.Params.(map[string]any)

	switch req.Method {
	case "Target.getTargets":
		var list []TargetInfo
		for _, tgt := range b.targets {
			list = append(list, TargetInfo{
				TargetID: tgt.ID,
				Type:     "page",
				Title:    tgt.Title,
				URL:      tgt.URL,
			})
		}
		return map[string]any{"targetInfos": list}

	case "Target.createTarget":
		b.nextID++
		id := fmt.Sprintf("tab_%d", b.nextID+1)
		sid := fmt.Sprintf("sess_%d", b.nextID+1)
		u, _ := paramsMap["url"].(string)
		b.targets[id] = &mockTarget{
			ID:      id,
			Session: sid,
			URL:     u,
			RegMap:  make(map[string]string),
		}
		return map[string]any{"targetId": id}

	case "Target.closeTarget":
		id, _ := paramsMap["targetId"].(string)
		delete(b.targets, id)
		return map[string]any{"success": true}

	case "Target.attachToTarget":
		id, _ := paramsMap["targetId"].(string)
		if tgt, ok := b.targets[id]; ok {
			return map[string]any{"sessionId": tgt.Session}
		}
		return map[string]any{"sessionId": "sess_1"}

	case "Page.navigate":
		tgt := b.targetBySession(req.SessionID)
		if u, ok := paramsMap["url"].(string); ok {
			if u == "https://navigation-error.invalid" {
				return map[string]any{
					"frameId":   "frame_1",
					"errorText": "net::ERR_NAME_NOT_RESOLVED",
				}
			}
			tgt.URL = u
			tgt.Title = "Kabibi Test Page"
		}
		return map[string]any{"frameId": "frame_1"}

	case "Runtime.evaluate":
		tgt := b.targetBySession(req.SessionID)
		expr, _ := paramsMap["expression"].(string)
		switch {
		case strings.Contains(expr, "window.__kabibi_tab_map = m"):
			// Registry save
			for _, other := range b.targets {
				if other.Nickname != "" {
					tgt.RegMap[other.Nickname] = other.ID
				}
			}
			return map[string]any{"result": map[string]any{"type": "undefined"}}

		case strings.Contains(expr, "const nick ="):
			// Stamp nickname in tab
			start := strings.Index(expr, `const nick = "`)
			if start >= 0 {
				rest := expr[start+len(`const nick = "`):]
				if end := strings.Index(rest, `"`); end >= 0 {
					nick := rest[:end]
					tgt.Nickname = nick
					tgt.Title = "[🤖 " + nick + "] " + tgt.Title
					for _, reg := range b.targets {
						if reg.URL == registryURLMarker {
							reg.RegMap[nick] = tgt.ID
						}
					}
				}
			}
			return map[string]any{"result": map[string]any{"type": "undefined"}}

		case strings.Contains(expr, "window.__kabibi_tab_map ||"):
			// Registry lookup
			for k, v := range tgt.RegMap {
				if strings.Contains(expr, `"`+k+`"`) {
					return map[string]any{"result": map[string]any{"type": "string", "value": v}}
				}
			}
			return map[string]any{"result": map[string]any{"type": "string", "value": ""}}

		case strings.Contains(expr, "window.__kabibi_nick ==="):
			return map[string]any{"result": map[string]any{"type": "string", "value": tgt.Nickname}}

		case strings.Contains(expr, "location.href"):
			return map[string]any{
				"result": map[string]any{
					"value": map[string]any{
						"url":   tgt.URL,
						"title": tgt.Title,
						"vw":    1280,
						"vh":    800,
						"w":     640,
						"h":     400,
					},
				},
			}

		case strings.Contains(expr, "scrollIntoView"):
			return map[string]any{
				"result": map[string]any{
					"value": map[string]any{
						"ok":   true,
						"x":    100,
						"y":    200,
						"w":    80,
						"h":    32,
						"desc": `button "Sign in"`,
					},
				},
			}

		default:
			return map[string]any{
				"result": map[string]any{
					"type":  "number",
					"value": 42,
				},
			}
		}

	case "Input.dispatchMouseEvent":
		tgt := b.targetBySession(req.SessionID)
		if paramsMap["type"] == "mousePressed" {
			tgt.Clicked = true
		}
		return map[string]any{}

	case "Input.dispatchKeyEvent":
		tgt := b.targetBySession(req.SessionID)
		if paramsMap["type"] == "keyDown" {
			if txt, ok := paramsMap["text"].(string); ok {
				tgt.Typed += txt
			}
		}
		return map[string]any{}

	case "Accessibility.getFullAXTree":
		tgt := b.targetBySession(req.SessionID)
		q := func(s string) rawAXValue {
			raw, _ := json.Marshal(s)
			return rawAXValue{Value: raw}
		}
		statusLabel := "Signed out"
		if tgt.Clicked {
			statusLabel = "Welcome User"
		}
		if tgt.Typed != "" {
			statusLabel += " (" + tgt.Typed + ")"
		}
		nodes := []rawAXNode{
			{NodeID: "1", Role: q("RootWebArea"), ChildIDs: []string{"2", "3"}},
			{NodeID: "2", Role: q("button"), Name: q("Sign in"), BackendDOMNodeID: 1},
			{NodeID: "3", Role: q("status"), Name: q(statusLabel), BackendDOMNodeID: 2},
		}
		return map[string]any{"nodes": nodes}

	case "Page.captureScreenshot":
		img := image.NewRGBA(image.Rect(0, 0, 32, 24))
		for y := 0; y < 24; y++ {
			for x := 0; x < 32; x++ {
				img.Set(x, y, color.RGBA{R: 40, G: 120, B: 220, A: 255})
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		return map[string]any{"data": base64.StdEncoding.EncodeToString(buf.Bytes())}

	default:
		return map[string]any{}
	}
}

func readMockWSFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode := hdr[0] & 0x0f
	masked := (hdr[1] & 0x80) != 0
	length := uint64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeMockWSFrame(w net.Conn, payload []byte) error {
	var buf bytes.Buffer
	buf.WriteByte(0x81)
	n := len(payload)
	if n <= 125 {
		buf.WriteByte(byte(n))
	} else if n <= 65535 {
		buf.WriteByte(126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		buf.Write(ext[:])
	} else {
		buf.WriteByte(127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		buf.Write(ext[:])
	}
	buf.Write(payload)
	_, err := w.Write(buf.Bytes())
	return err
}

func TestEnsureTabDoesNotDeadlockOnEventsDuringSetup(t *testing.T) {
	wsURL := startMockCDPServer(t, true)
	mgr := NewManager(t.TempDir())
	mgr.SetWebSocketURL(wsURL)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, _, err := mgr.EnsureTab(ctx, "researcher"); err != nil {
		t.Fatalf("EnsureTab() failed while CDP emitted an event before its response: %v", err)
	}
}

func TestNavigateActionRequiresURLBeforeBrowserLaunch(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetWebSocketURL("ws://127.0.0.1:1")

	_, err := mgr.NavigateAction(context.Background(), "researcher", "", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("NavigateAction() error = %v, want missing-URL error before connecting", err)
	}
}

func TestNavigateActionReportsNavigationError(t *testing.T) {
	wsURL := startMockCDPServer(t)
	mgr := NewManager(t.TempDir())
	mgr.SetWebSocketURL(wsURL)
	defer mgr.Close()

	_, err := mgr.NavigateAction(context.Background(), "researcher", "navigation-error.invalid", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "net::ERR_NAME_NOT_RESOLVED") {
		t.Fatalf("NavigateAction() error = %v, want CDP navigation error", err)
	}
}

func TestManager_InBrowserNicknamePersistence_AndFullActions(t *testing.T) {
	wsURL := startMockCDPServer(t)
	ctx := context.Background()

	mgr1 := NewManager(t.TempDir())
	mgr1.SetWebSocketURL(wsURL)
	defer mgr1.Close()

	// 1. Navigate with nickname "shopper"
	navOut, err := mgr1.NavigateAction(ctx, "shopper", "https://example.com/store", false, false, false)
	if err != nil {
		t.Fatalf("NavigateAction failed: %v", err)
	}
	if !strings.Contains(navOut, `button "Sign in"`) || !strings.Contains(navOut, `status "Signed out"`) {
		t.Fatalf("unexpected NavigateAction output:\n%s", navOut)
	}

	// 2. Click "Sign in" -> updates only 1 out of 2 nodes (<35% threshold for larger trees, or diff output)
	clickOut, err := mgr1.ClickAction(ctx, "shopper", "Sign in", "left")
	if err != nil {
		t.Fatalf("ClickAction failed: %v", err)
	}
	if !strings.Contains(clickOut, "Welcome User") {
		t.Fatalf("expected ClickAction to reflect updated status 'Welcome User':\n%s", clickOut)
	}

	// 3. Type "Hi!" with Shift chord decomposition
	typeOut, err := mgr1.TypeAction(ctx, "shopper", "Sign in", "Hi!", false, false)
	if err != nil {
		t.Fatalf("TypeAction failed: %v", err)
	}
	if !strings.Contains(typeOut, "Welcome User (Hi!)") {
		t.Fatalf("expected TypeAction to reflect typed text 'Hi!':\n%s", typeOut)
	}

	// 4. Simulate a second separate Kabibi instance attaching to the SAME browser without any local file state!
	// It must recover the "shopper" tab purely from in-browser storage (`about:blank#__kabibi_registry__` / `window.__kabibi_nick`).
	mgr2 := NewManager(t.TempDir())
	mgr2.SetWebSocketURL(wsURL)
	defer mgr2.Close()

	axOut, err := mgr2.AXAction(ctx, "shopper", "", "", nil)
	if err != nil {
		t.Fatalf("mgr2 AXAction failed: %v", err)
	}
	if !strings.Contains(axOut, "Welcome User (Hi!)") {
		t.Fatalf("expected second Manager instance to reattach to 'shopper' tab via in-browser storage, got:\n%s", axOut)
	}

	// 5. Screenshot with skeuomorphic HTML/CSS frame adornment
	shotPath := filepath.Join(t.TempDir(), "adorned.png")
	nBytes, err := mgr2.ScreenshotAction(ctx, "shopper", shotPath, "", false, true)
	if err != nil {
		t.Fatalf("ScreenshotAction failed: %v", err)
	}
	if nBytes <= 0 {
		t.Fatalf("expected positive PNG byte count, got %d", nBytes)
	}
	if _, err := os.Stat(shotPath); err != nil {
		t.Fatalf("expected screenshot file on disk: %v", err)
	}
}
