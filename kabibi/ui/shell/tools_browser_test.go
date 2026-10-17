package shell

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
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
	"testing"

	"ai"
	"github.com/mavity/rusticated/kabibi/tools/browser"
)

func startShellMockCDP(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("webserver doesn't support hijacking")
		}
		secKey := r.Header.Get("Sec-WebSocket-Key")
		h := sha1.Sum([]byte(secKey + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		accept := base64.StdEncoding.EncodeToString(h[:])

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
			var hdr [2]byte
			if _, err := io.ReadFull(rw.Reader, hdr[:]); err != nil {
				return
			}
			masked := (hdr[1] & 0x80) != 0
			length := uint64(hdr[1] & 0x7f)
			switch length {
			case 126:
				var ext [2]byte
				_, _ = io.ReadFull(rw.Reader, ext[:])
				length = uint64(binary.BigEndian.Uint16(ext[:]))
			case 127:
				var ext [8]byte
				_, _ = io.ReadFull(rw.Reader, ext[:])
				length = binary.BigEndian.Uint64(ext[:])
			}
			var mask [4]byte
			if masked {
				_, _ = io.ReadFull(rw.Reader, mask[:])
			}
			payload := make([]byte, length)
			_, _ = io.ReadFull(rw.Reader, payload)
			if masked {
				for i := range payload {
					payload[i] ^= mask[i%4]
				}
			}

			var req struct {
				ID        int64          `json:"id"`
				Method    string         `json:"method"`
				Params    map[string]any `json:"params"`
				SessionID string         `json:"sessionId"`
			}
			_ = json.Unmarshal(payload, &req)

			var res any = map[string]any{}
			switch req.Method {
			case "Target.getTargets":
				res = map[string]any{
					"targetInfos": []map[string]any{
						{"targetId": "t1", "type": "page", "title": "[🤖 main] Test", "url": "https://example.com"},
					},
				}
			case "Target.createTarget":
				res = map[string]any{"targetId": "t2"}
			case "Target.attachToTarget":
				res = map[string]any{"sessionId": "s1"}
			case "Runtime.evaluate":
				expr, _ := req.Params["expression"].(string)
				if strings.Contains(expr, "location.href") {
					res = map[string]any{
						"result": map[string]any{
							"value": map[string]any{"url": "https://example.com", "title": "Example", "vw": 1280, "vh": 800, "w": 640, "h": 480},
						},
					}
				} else if strings.Contains(expr, "scrollIntoView") {
					res = map[string]any{
						"result": map[string]any{
							"value": map[string]any{"ok": true, "x": 40, "y": 50, "w": 100, "h": 30, "desc": `button "Submit"`},
						},
					}
				} else {
					res = map[string]any{
						"result": map[string]any{"type": "string", "value": "eval_result_ok"},
					}
				}
			case "Accessibility.getFullAXTree":
				res = map[string]any{
					"nodes": []map[string]any{
						{
							"nodeId":   "1",
							"role":     map[string]any{"value": "RootWebArea"},
							"childIds": []string{"2"},
						},
						{
							"nodeId":           "2",
							"role":             map[string]any{"value": "button"},
							"name":             map[string]any{"value": "Submit"},
							"backendDOMNodeId": 1,
						},
					},
				}
			case "Page.captureScreenshot":
				img := image.NewRGBA(image.Rect(0, 0, 16, 16))
				img.Set(0, 0, color.RGBA{R: 255, A: 255})
				var b bytes.Buffer
				_ = png.Encode(&b, img)
				res = map[string]any{"data": base64.StdEncoding.EncodeToString(b.Bytes())}
			case "Browser.getVersion":
				res = map[string]any{"product": "Chrome/130.0"}
			}

			respBytes, _ := json.Marshal(map[string]any{
				"id":        req.ID,
				"sessionId": req.SessionID,
				"result":    res,
			})
			var frame bytes.Buffer
			frame.WriteByte(0x81)
			if len(respBytes) <= 125 {
				frame.WriteByte(byte(len(respBytes)))
			} else {
				frame.WriteByte(126)
				var ext [2]byte
				binary.BigEndian.PutUint16(ext[:], uint16(len(respBytes)))
				frame.Write(ext[:])
			}
			frame.Write(respBytes)
			_, _ = conn.Write(frame.Bytes())
		}
	}))
	t.Cleanup(func() {
		srv.Close()
		browser.DefaultManager().SetWebSocketURL("")
	})
	_ = bufio.NewReader
	_ = net.Dial
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	browser.DefaultManager().SetWebSocketURL(wsURL)
	return wsURL
}

func TestBrowserToolsAndWebShellBuiltins(t *testing.T) {
	startShellMockCDP(t)
	dir := t.TempDir()

	sh := New(ShellOptions{
		WorkingDir: dir,
	})
	defer sh.Close()

	tools := BuildKabibiTools(sh.ExecutionSession())
	ctx := context.Background()

	// Verify all 7 browser tools are registered and executable
	for _, name := range []string{
		"browser_navigate",
		"browser_ax",
		"browser_click",
		"browser_type",
		"browser_screenshot",
		"browser_eval",
		"browser_cdp",
	} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("missing expected browser tool %q", name)
		}
	}

	// 1. browser_navigate
	navOut, err := tools["browser_navigate"].Execute(ctx, ai.ToolCall{
		Name:         "browser_navigate",
		RawArguments: json.RawMessage(`{"nickname":"main","url":"https://example.com"}`),
	}, nil)
	if err != nil {
		t.Fatalf("browser_navigate failed: %v", err)
	}
	if !strings.Contains(navOut.(string), `button "Submit"`) {
		t.Fatalf("expected AX tree in browser_navigate output, got:\n%s", navOut.(string))
	}

	// 2. browser_ax with search
	axOut, err := tools["browser_ax"].Execute(ctx, ai.ToolCall{
		Name:         "browser_ax",
		RawArguments: json.RawMessage(`{"nickname":"main","search":"Submit"}`),
	}, nil)
	if err != nil {
		t.Fatalf("browser_ax failed: %v", err)
	}
	if !strings.Contains(axOut.(string), `button "Submit"`) {
		t.Fatalf("expected matched button in browser_ax output, got:\n%s", axOut.(string))
	}

	// 3. browser_click
	clickOut, err := tools["browser_click"].Execute(ctx, ai.ToolCall{
		Name:         "browser_click",
		RawArguments: json.RawMessage(`{"nickname":"main","target":"Submit"}`),
	}, nil)
	if err != nil {
		t.Fatalf("browser_click failed: %v", err)
	}
	if !strings.Contains(clickOut.(string), "[clicked button") {
		t.Fatalf("unexpected browser_click output:\n%s", clickOut.(string))
	}

	// 4. browser_type
	_, err = tools["browser_type"].Execute(ctx, ai.ToolCall{
		Name:         "browser_type",
		RawArguments: json.RawMessage(`{"nickname":"main","target":"Submit","text":"Hello!","press_enter":true}`),
	}, nil)
	if err != nil {
		t.Fatalf("browser_type failed: %v", err)
	}

	// 5. browser_eval
	evalOut, err := tools["browser_eval"].Execute(ctx, ai.ToolCall{
		Name:         "browser_eval",
		RawArguments: json.RawMessage(`{"nickname":"main","expression":"1 + 1"}`),
	}, nil)
	if err != nil {
		t.Fatalf("browser_eval failed: %v", err)
	}
	evalMap := evalOut.(map[string]any)
	if evalMap["value"] != "eval_result_ok" {
		t.Fatalf("unexpected browser_eval value: %v", evalMap)
	}

	// 6. browser_cdp
	cdpOut, err := tools["browser_cdp"].Execute(ctx, ai.ToolCall{
		Name:         "browser_cdp",
		RawArguments: json.RawMessage(`{"nickname":"main","method":"Browser.getVersion"}`),
	}, nil)
	if err != nil {
		t.Fatalf("browser_cdp failed: %v", err)
	}
	cdpMap := cdpOut.(map[string]any)
	if cdpMap["product"] != "Chrome/130.0" {
		t.Fatalf("unexpected browser_cdp output: %v", cdpMap)
	}

	// 7. browser_screenshot (with adorn_chrome=true)
	shotOut, err := tools["browser_screenshot"].Execute(ctx, ai.ToolCall{
		Name:         "browser_screenshot",
		RawArguments: json.RawMessage(`{"nickname":"main","save_path":"page.png","adorn_chrome":true}`),
	}, nil)
	if err != nil {
		t.Fatalf("browser_screenshot failed: %v", err)
	}
	shotMap := shotOut.(map[string]any)
	if _, err := os.Stat(filepath.Join(dir, "page.png")); err != nil {
		t.Fatalf("expected page.png on disk: %v (map=%v)", err, shotMap)
	}

	// 8. Verify `web` and `cdp` shell builtins work directly from Kabibi shell!
	sh.ExecuteLine(`web --nick main ax Submit`)
	blocks := sh.Blocks()
	last := blocks[len(blocks)-1]
	if !strings.Contains(last.Output.String(), `button "Submit"`) {
		t.Fatalf("expected `web ax Submit` shell builtin to output AX tree, got:\n%s", last.Output.String())
	}

	sh.ExecuteLine(`cdp --nick main Browser.getVersion`)
	blocks = sh.Blocks()
	last = blocks[len(blocks)-1]
	if !strings.Contains(last.Output.String(), "Chrome/130.0") {
		t.Fatalf("expected `cdp Browser.getVersion` shell builtin output, got:\n%s", last.Output.String())
	}
}
