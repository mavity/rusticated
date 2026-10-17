package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
	"unicode"
)

// ResolveTargetBounds resolves a human target specification (CSS selector, `role "name"` query,
// or accessible button/link/input caption) to its viewport bounding box (`Rect`), scrolling it
// into view if necessary.
func ResolveTargetBounds(ctx context.Context, conn *Conn, tab *TabSession, target string) (Rect, string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return Rect{}, "", errors.New("target parameter is required")
	}

	targetJSON, _ := json.Marshal(target)
	script := fmt.Sprintf(`(() => {
		const q = %s;
		const norm = (s) => (s || "").replace(/\s+/g, " ").trim().toLowerCase();
		const qNorm = norm(q);

		const isVisible = (el) => {
			if (!el || !el.getBoundingClientRect) return false;
			const st = window.getComputedStyle(el);
			if (st.display === "none" || st.visibility === "hidden" || parseFloat(st.opacity) === 0) return false;
			const r = el.getBoundingClientRect();
			return r.width > 0 && r.height > 0;
		};

		const labelOf = (el) => {
			if (!el) return "";
			const aria = el.getAttribute && (el.getAttribute("aria-label") || el.getAttribute("title") || el.getAttribute("placeholder") || el.getAttribute("alt"));
			if (aria) return norm(aria);
			if (el.id) {
				try {
					const lbl = document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
					if (lbl) return norm(lbl.innerText || lbl.textContent);
				} catch (e) {}
			}
			if (el.tagName === "INPUT" || el.tagName === "BUTTON") {
				if (el.value) return norm(el.value);
			}
			return norm(el.innerText || el.textContent || "");
		};

		let found = null;
		let desc = "";

		// 1. Try direct CSS selector if it looks like one or matches uniquely
		try {
			const el = document.querySelector(q);
			if (el && isVisible(el)) {
				found = el;
				desc = el.tagName.toLowerCase();
			}
		} catch (e) {}

		// 2. Parse optional role prefix e.g. 'button "Sign in"' or 'link "Pricing"'
		let roleFilter = "";
		let textQuery = qNorm;
		const roleMatch = q.match(/^([a-zA-Z0-9_-]+)\s+["'](.+)["']$/);
		if (roleMatch) {
			roleFilter = roleMatch[1].toLowerCase();
			textQuery = norm(roleMatch[2]);
		} else if ((q.startsWith('"') && q.endsWith('"')) || (q.startsWith("'") && q.endsWith("'"))) {
			textQuery = norm(q.slice(1, -1));
		}

		if (!found) {
			const interactiveSelector = 'button, a, input, select, textarea, summary, [role], [onclick], [tabindex], label, h1, h2, h3, li, td, th, span, div';
			const all = Array.from(document.querySelectorAll(interactiveSelector));
			const roleMatches = (el, rf) => {
				if (!rf) return true;
				const explicitRole = (el.getAttribute("role") || "").toLowerCase();
				const tag = el.tagName.toLowerCase();
				if (explicitRole === rf || tag === rf) return true;
				if (rf === "button" && (tag === "button" || (tag === "input" && ["button","submit","reset"].includes(el.type)))) return true;
				if (rf === "link" && tag === "a") return true;
				if ((rf === "textbox" || rf === "searchbox" || rf === "input") && (tag === "input" || tag === "textarea")) return true;
				if (rf === "checkbox" && tag === "input" && el.type === "checkbox") return true;
				if (rf === "combobox" && tag === "select") return true;
				return false;
			};

			// Pass A: Exact accessible label/text match on visible elements
			for (const el of all) {
				if (!isVisible(el) || !roleMatches(el, roleFilter)) continue;
				const lbl = labelOf(el);
				if (lbl === textQuery && lbl !== "") {
					found = el;
					desc = el.tagName.toLowerCase() + ' "' + lbl + '"';
					break;
				}
			}

			// Pass B: Substring match preferring deepest/smallest interactive element
			if (!found && textQuery !== "") {
				let bestArea = Infinity;
				for (const el of all) {
					if (!isVisible(el) || !roleMatches(el, roleFilter)) continue;
					const lbl = labelOf(el);
					if (lbl.includes(textQuery)) {
						const r = el.getBoundingClientRect();
						const area = r.width * r.height;
						if (area > 0 && area < bestArea) {
							bestArea = area;
							found = el;
							desc = el.tagName.toLowerCase() + ' "' + lbl.slice(0, 40) + '"';
						}
					}
				}
			}
		}

		if (!found) {
			return { ok: false, err: "element not found for target: " + q };
		}

		found.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
		const rect = found.getBoundingClientRect();
		return {
			ok: true,
			x: rect.left,
			y: rect.top,
			w: rect.width,
			h: rect.height,
			desc: desc
		};
	})()`, string(targetJSON))

	var evalRes struct {
		Result struct {
			Value struct {
				OK   bool    `json:"ok"`
				X    float64 `json:"x"`
				Y    float64 `json:"y"`
				W    float64 `json:"w"`
				H    float64 `json:"h"`
				Desc string  `json:"desc"`
				Err  string  `json:"err"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := conn.Call(ctx, tab.SessionID, "Runtime.evaluate", map[string]any{
		"expression":    script,
		"returnByValue": true,
		"awaitPromise":  true,
	}, &evalRes); err != nil {
		return Rect{}, "", err
	}
	v := evalRes.Result.Value
	if !v.OK {
		return Rect{}, "", errors.New(v.Err)
	}
	return Rect{X: v.X, Y: v.Y, W: v.W, H: v.H}, v.Desc, nil
}

// Point2D represents a 2D coordinate along a mouse trajectory.
type Point2D struct {
	X float64
	Y float64
}

// PickGaussianTargetPoint selects a human-like landing coordinate inside `box` using a
// truncated Gaussian distribution centered at the box midpoint (avoiding robotic dead-center
// `(x + w/2, y + h/2)` clicks and avoiding outer edges).
func PickGaussianTargetPoint(box Rect, rng *rand.Rand) (float64, float64) {
	cx, cy := box.Center()
	if box.W <= 4 || box.H <= 4 {
		return cx, cy
	}
	var normX, normY float64
	if rng != nil {
		normX = rng.NormFloat64()
		normY = rng.NormFloat64()
	} else {
		normX = rand.NormFloat64()
		normY = rand.NormFloat64()
	}
	// Clamp to [-1.8, +1.8] standard deviations and scale by 18% of width/height
	// Also ensure we are never exact dead-center (offset by at least 0.5px if rounded)
	normX = math.Max(-1.8, math.Min(1.8, normX))
	normY = math.Max(-1.8, math.Min(1.8, normY))
	if math.Abs(normX) < 0.08 {
		normX = 0.22
	}
	if math.Abs(normY) < 0.08 {
		normY = -0.19
	}
	tx := cx + normX*(box.W*0.18)
	ty := cy + normY*(box.H*0.18)
	return math.Round(tx*10) / 10, math.Round(ty*10) / 10
}

// GenerateBezierMousePath generates a curved cubic Bézier mouse trajectory from `(x0, y0)`
// to `(x1, y1)` with minimum-jerk velocity easing (slow start -> fast mid-flight -> deceleration
// near target) so anti-bot telemetry sees realistic `mouseMoved` history prior to `mousePressed`.
func GenerateBezierMousePath(x0, y0, x1, y1 float64, steps int) []Point2D {
	dist := math.Hypot(x1-x0, y1-y0)
	if steps <= 0 {
		steps = int(math.Max(6, math.Min(18, math.Round(dist/45.0))))
	}
	if steps < 4 {
		steps = 4
	}

	dx := x1 - x0
	dy := y1 - y0
	// Perpendicular normal for natural wrist arc
	nx := -dy
	ny := dx
	normLen := math.Hypot(nx, ny)
	if normLen > 0 {
		nx /= normLen
		ny /= normLen
	}

	arcMag := math.Min(90.0, dist*0.18)
	c1x := x0 + dx*0.28 + nx*arcMag*0.7
	c1y := y0 + dy*0.28 + ny*arcMag*0.7
	c2x := x0 + dx*0.74 + nx*arcMag*0.35
	c2y := y0 + dy*0.74 + ny*arcMag*0.35

	pts := make([]Point2D, 0, steps)
	for i := 1; i <= steps; i++ {
		u := float64(i) / float64(steps)
		// Minimum-jerk fifth-order polynomial time scaling: s(u) = 10u^3 - 15u^4 + 6u^5
		s := u * u * u * (10.0 - 15.0*u + 6.0*u*u)
		inv := 1.0 - s

		bx := inv*inv*inv*x0 + 3*inv*inv*s*c1x + 3*inv*s*s*c2x + s*s*s*x1
		by := inv*inv*inv*y0 + 3*inv*inv*s*c1y + 3*inv*s*s*c2y + s*s*s*y1
		pts = append(pts, Point2D{
			X: math.Round(bx*10) / 10,
			Y: math.Round(by*10) / 10,
		})
	}
	return pts
}

// HumaneClick moves the virtual mouse along a curved minimum-jerk trajectory to a Gaussian
// point inside `box`, hovers briefly, and dispatches `mousePressed` + `mouseReleased`.
func HumaneClick(ctx context.Context, conn *Conn, tab *TabSession, box Rect, button string) (float64, float64, error) {
	tab.mu.Lock()
	startX, startY := tab.LastMouseX, tab.LastMouseY
	if !tab.HasMousePos {
		startX, startY = 180.0, 140.0
	}
	tab.mu.Unlock()

	endX, endY := PickGaussianTargetPoint(box, nil)
	path := GenerateBezierMousePath(startX, startY, endX, endY, 0)

	for _, pt := range path {
		if err := conn.Call(ctx, tab.SessionID, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseMoved",
			"x":    pt.X,
			"y":    pt.Y,
		}, nil); err != nil {
			return endX, endY, err
		}
		sleepJitter(ctx, 4, 11)
	}

	tab.mu.Lock()
	tab.LastMouseX = endX
	tab.LastMouseY = endY
	tab.HasMousePos = true
	tab.mu.Unlock()

	// Brief pre-press hover settle (25-60ms)
	sleepJitter(ctx, 25, 60)

	cdpBtn := "left"
	clickCount := 1
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "right":
		cdpBtn = "right"
	case "middle":
		cdpBtn = "middle"
	case "double":
		cdpBtn = "left"
		clickCount = 2
	}

	for c := 1; c <= clickCount; c++ {
		if err := conn.Call(ctx, tab.SessionID, "Input.dispatchMouseEvent", map[string]any{
			"type":       "mousePressed",
			"x":          endX,
			"y":          endY,
			"button":     cdpBtn,
			"clickCount": c,
		}, nil); err != nil {
			return endX, endY, err
		}
		// Realistic physical switch hold time (45-95ms)
		sleepJitter(ctx, 45, 95)
		if err := conn.Call(ctx, tab.SessionID, "Input.dispatchMouseEvent", map[string]any{
			"type":       "mouseReleased",
			"x":          endX,
			"y":          endY,
			"button":     cdpBtn,
			"clickCount": c,
		}, nil); err != nil {
			return endX, endY, err
		}
		if c < clickCount {
			sleepJitter(ctx, 55, 110)
		}
	}

	return endX, endY, nil
}

// KeyChordEvent represents a single low-level CDP Input.dispatchKeyEvent step with realistic delay.
type KeyChordEvent struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	Code      string `json:"code"`
	Text      string `json:"text,omitempty"`
	Modifiers int    `json:"modifiers,omitempty"`
	DelayMs   int    `json:"delay_ms"`
}

type keySpec struct {
	Code    string
	Unshift string
	Shifted bool
	Row     int
	Col     int
	Hand    int // 0 = left, 1 = right
}

var qwertyKeyMap = map[rune]keySpec{
	'1': {"Digit1", "1", false, 0, 1, 0}, '!': {"Digit1", "1", true, 0, 1, 0},
	'2': {"Digit2", "2", false, 0, 2, 0}, '@': {"Digit2", "2", true, 0, 2, 0},
	'3': {"Digit3", "3", false, 0, 3, 0}, '#': {"Digit3", "3", true, 0, 3, 0},
	'4': {"Digit4", "4", false, 0, 4, 0}, '$': {"Digit4", "4", true, 0, 4, 0},
	'5': {"Digit5", "5", false, 0, 5, 0}, '%': {"Digit5", "5", true, 0, 5, 0},
	'6': {"Digit6", "6", false, 0, 6, 1}, '^': {"Digit6", "6", true, 0, 6, 1},
	'7': {"Digit7", "7", false, 0, 7, 1}, '&': {"Digit7", "7", true, 0, 7, 1},
	'8': {"Digit8", "8", false, 0, 8, 1}, '*': {"Digit8", "8", true, 0, 8, 1},
	'9': {"Digit9", "9", false, 0, 9, 1}, '(': {"Digit9", "9", true, 0, 9, 1},
	'0': {"Digit0", "0", false, 0, 10, 1}, ')': {"Digit0", "0", true, 0, 10, 1},
	'-': {"Minus", "-", false, 0, 11, 1}, '_': {"Minus", "-", true, 0, 11, 1},
	'=': {"Equal", "=", false, 0, 12, 1}, '+': {"Equal", "=", true, 0, 12, 1},
	'[': {"BracketLeft", "[", false, 1, 11, 1}, '{': {"BracketLeft", "[", true, 1, 11, 1},
	']': {"BracketRight", "]", false, 1, 12, 1}, '}': {"BracketRight", "]", true, 1, 12, 1},
	'\\': {"Backslash", "\\", false, 1, 13, 1}, '|': {"Backslash", "\\", true, 1, 13, 1},
	';': {"Semicolon", ";", false, 2, 10, 1}, ':': {"Semicolon", ";", true, 2, 10, 1},
	'\'': {"Quote", "'", false, 2, 11, 1}, '"': {"Quote", "'", true, 2, 11, 1},
	',': {"Comma", ",", false, 3, 8, 1}, '<': {"Comma", ",", true, 3, 8, 1},
	'.': {"Period", ".", false, 3, 9, 1}, '>': {"Period", ".", true, 3, 9, 1},
	'/': {"Slash", "/", false, 3, 10, 1}, '?': {"Slash", "/", true, 3, 10, 1},
	'`': {"Backquote", "`", false, 0, 0, 0}, '~': {"Backquote", "`", true, 0, 0, 0},
	' ': {"Space", " ", false, 4, 5, 0},
}

func lookupRuneKeySpec(r rune) keySpec {
	if sp, ok := qwertyKeyMap[r]; ok {
		return sp
	}
	lower := unicode.ToLower(r)
	shifted := unicode.IsUpper(r)
	const (
		row1 = "qwertyuiop"
		row2 = "asdfghjkl"
		row3 = "zxcvbnm"
	)
	if idx := strings.IndexRune(row1, lower); idx >= 0 {
		hand := 0
		if idx >= 5 {
			hand = 1
		}
		return keySpec{
			Code:    "Key" + strings.ToUpper(string(lower)),
			Unshift: string(lower),
			Shifted: shifted,
			Row:     1,
			Col:     idx + 1,
			Hand:    hand,
		}
	}
	if idx := strings.IndexRune(row2, lower); idx >= 0 {
		hand := 0
		if idx >= 5 {
			hand = 1
		}
		return keySpec{
			Code:    "Key" + strings.ToUpper(string(lower)),
			Unshift: string(lower),
			Shifted: shifted,
			Row:     2,
			Col:     idx + 1,
			Hand:    hand,
		}
	}
	if idx := strings.IndexRune(row3, lower); idx >= 0 {
		hand := 0
		if idx >= 5 {
			hand = 1
		}
		return keySpec{
			Code:    "Key" + strings.ToUpper(string(lower)),
			Unshift: string(lower),
			Shifted: shifted,
			Row:     3,
			Col:     idx + 1,
			Hand:    hand,
		}
	}
	return keySpec{
		Code:    "",
		Unshift: string(r),
		Shifted: shifted,
		Row:     2,
		Col:     5,
		Hand:    0,
	}
}

// PlanHumaneKeystrokes decomposes `text` into digraph-aware hardware keystroke events,
// including explicit Shift keyDown/keyUp chords for uppercase letters and shifted symbols
// (`A`, `*`, `@`, `?`, etc.) and cognitive lingers after punctuation.
func PlanHumaneKeystrokes(text string) []KeyChordEvent {
	var events []KeyChordEvent
	runes := []rune(text)
	var prevSpec *keySpec
	var prevRune rune

	for i, r := range runes {
		if r == '\n' || r == '\r' {
			events = append(events,
				KeyChordEvent{Type: "rawKeyDown", Key: "Enter", Code: "Enter", Text: "\r", DelayMs: 65},
				KeyChordEvent{Type: "keyUp", Key: "Enter", Code: "Enter", DelayMs: 240},
			)
			prevSpec = nil
			prevRune = r
			continue
		}

		sp := lookupRuneKeySpec(r)
		// Compute digraph flight time before pressing this key
		flightMs := 75
		if i > 0 && prevSpec != nil {
			if prevRune == '.' || prevRune == ',' || prevRune == '?' || prevRune == '!' || prevRune == ';' {
				// Cognitive linger after punctuation
				flightMs = 240 + (i*37)%180
			} else if r == ' ' {
				flightMs = 85 + (i*19)%45
			} else if prevSpec.Hand != sp.Hand {
				// Alternating-hand digraph: faster flight
				flightMs = 48 + (i*23)%38
			} else {
				// Same-hand digraph: distance-based flight
				dist := math.Hypot(float64(sp.Row-prevSpec.Row), float64(sp.Col-prevSpec.Col))
				flightMs = 110 + int(dist*18) + (i*17)%40
			}
		}

		holdMs := 52 + (i*13)%38
		charStr := string(r)

		if sp.Shifted {
			// Explicit Shift chord decomposition:
			// 1. Shift rawKeyDown -> 2. Key keyDown (modifiers=8) -> 3. Key keyUp -> 4. Shift keyUp
			events = append(events,
				KeyChordEvent{
					Type:      "rawKeyDown",
					Key:       "Shift",
					Code:      "ShiftLeft",
					Modifiers: 8,
					DelayMs:   30 + (i*7)%25,
				},
				KeyChordEvent{
					Type:      "keyDown",
					Key:       charStr,
					Code:      sp.Code,
					Text:      charStr,
					Modifiers: 8,
					DelayMs:   holdMs,
				},
				KeyChordEvent{
					Type:      "keyUp",
					Key:       charStr,
					Code:      sp.Code,
					Modifiers: 8,
					DelayMs:   20 + (i*5)%20,
				},
				KeyChordEvent{
					Type:      "keyUp",
					Key:       "Shift",
					Code:      "ShiftLeft",
					Modifiers: 0,
					DelayMs:   flightMs,
				},
			)
		} else {
			events = append(events,
				KeyChordEvent{
					Type:    "keyDown",
					Key:     charStr,
					Code:    sp.Code,
					Text:    charStr,
					DelayMs: holdMs,
				},
				KeyChordEvent{
					Type:    "keyUp",
					Key:     charStr,
					Code:    sp.Code,
					DelayMs: flightMs,
				},
			)
		}

		spCopy := sp
		prevSpec = &spCopy
		prevRune = r
	}
	return events
}

// HumaneType clicks/focuses the target element (if specified), optionally clears existing text,
// and dispatches the planned digraph-aware hardware keystroke sequence.
func HumaneType(ctx context.Context, conn *Conn, tab *TabSession, box *Rect, text string, clearFirst bool, pressEnter bool) error {
	if box != nil && box.Valid() {
		if _, _, err := HumaneClick(ctx, conn, tab, *box, "left"); err != nil {
			return err
		}
		sleepJitter(ctx, 40, 80)
	}

	if clearFirst {
		// Select all & Backspace via JS/input so works cross-platform (Ctrl+A vs Cmd+A)
		_ = conn.Call(ctx, tab.SessionID, "Runtime.evaluate", map[string]any{
			"expression": `(() => {
				const el = document.activeElement;
				if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA")) {
					el.select && el.select();
				}
			})()`,
		}, nil)
		_ = conn.Call(ctx, tab.SessionID, "Input.dispatchKeyEvent", map[string]any{
			"type": "rawKeyDown",
			"key":  "Backspace",
			"code": "Backspace",
		}, nil)
		sleepJitter(ctx, 30, 55)
		_ = conn.Call(ctx, tab.SessionID, "Input.dispatchKeyEvent", map[string]any{
			"type": "keyUp",
			"key":  "Backspace",
			"code": "Backspace",
		}, nil)
		sleepJitter(ctx, 40, 80)
	}

	chords := PlanHumaneKeystrokes(text)
	// Scale down delays when typing long passages so tools stay snappy while preserving rhythm
	delayScale := 1.0
	if len(text) > 40 {
		delayScale = 0.25
	} else if len(text) > 15 {
		delayScale = 0.5
	}

	for _, ev := range chords {
		params := map[string]any{
			"type": ev.Type,
			"key":  ev.Key,
		}
		if ev.Code != "" {
			params["code"] = ev.Code
		}
		if ev.Text != "" {
			params["text"] = ev.Text
			params["unmodifiedText"] = ev.Text
		}
		if ev.Modifiers != 0 {
			params["modifiers"] = ev.Modifiers
		}
		if err := conn.Call(ctx, tab.SessionID, "Input.dispatchKeyEvent", params, nil); err != nil {
			return err
		}
		d := int(float64(ev.DelayMs) * delayScale)
		if d > 0 {
			sleepMs(ctx, d)
		}
	}

	if pressEnter {
		sleepJitter(ctx, 60, 120)
		_ = conn.Call(ctx, tab.SessionID, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "rawKeyDown",
			"key":                   "Enter",
			"code":                  "Enter",
			"windowsVirtualKeyCode": 13,
			"nativeVirtualKeyCode":  13,
			"text":                  "\r",
			"unmodifiedText":        "\r",
		}, nil)
		sleepJitter(ctx, 45, 85)
		_ = conn.Call(ctx, tab.SessionID, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "keyUp",
			"key":                   "Enter",
			"code":                  "Enter",
			"windowsVirtualKeyCode": 13,
			"nativeVirtualKeyCode":  13,
		}, nil)
	}
	return nil
}

func sleepJitter(ctx context.Context, minMs, maxMs int) {
	if maxMs <= minMs {
		sleepMs(ctx, minMs)
		return
	}
	ms := minMs + rand.IntN(maxMs-minMs+1)
	sleepMs(ctx, ms)
}

func sleepMs(ctx context.Context, ms int) {
	if ms <= 0 {
		return
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
