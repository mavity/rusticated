package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

// CaptureScreenshot captures a PNG screenshot of the tab Owned by `nickname` (optionally
// clipped to `target` or `fullPage`), and when `adornChrome` is true, renders a realistic
// CSS/HTML skeuomorphic browser frame (with multi-stop metallic gradients, traffic lights,
// rounded address pill, and soft drop shadow) via the browser's own layout/compositor engine.
func CaptureScreenshot(
	ctx context.Context,
	conn *Conn,
	tab *TabSession,
	savePath string,
	target string,
	fullPage bool,
	adornChrome bool,
) (int, error) {
	sid := tab.SessionID

	params := map[string]any{
		"format": "png",
	}
	if fullPage {
		params["captureBeyondViewport"] = true
	}

	if strings.TrimSpace(target) != "" {
		box, _, err := ResolveTargetBounds(ctx, conn, tab, target)
		if err != nil {
			return 0, err
		}
		params["clip"] = map[string]any{
			"x":      box.X,
			"y":      box.Y,
			"width":  box.W,
			"height": box.H,
			"scale":  1,
		}
	}

	var shotRes struct {
		Data string `json:"data"`
	}
	if err := conn.Call(ctx, sid, "Page.captureScreenshot", params, &shotRes); err != nil {
		return 0, err
	}

	pngBytes, err := base64.StdEncoding.DecodeString(shotRes.Data)
	if err != nil {
		return 0, err
	}

	if adornChrome && shotRes.Data != "" {
		if adorned, err := renderHTMLChromeFrame(ctx, conn, tab, shotRes.Data); err == nil && len(adorned) > 0 {
			pngBytes = adorned
		}
	}

	if savePath != "" {
		_ = os.MkdirAll(filepath.Dir(savePath), 0o755)
		if err := os.WriteFile(savePath, pngBytes, 0o644); err != nil {
			return 0, err
		}
	}
	return len(pngBytes), nil
}

// renderHTMLChromeFrame uses a transient browser tab and the browser's native CSS/HTML rendering
// engine to composite a realistic skeuomorphic window frame with rounded corners, glossy metallic
// toolbar, traffic-light indicators, address bar pill, and multi-layered drop shadow.
func renderHTMLChromeFrame(ctx context.Context, conn *Conn, tab *TabSession, rawBase64PNG string) ([]byte, error) {
	var pageInfo struct {
		Result struct {
			Value struct {
				URL   string  `json:"url"`
				Title string  `json:"title"`
				W     float64 `json:"w"`
				H     float64 `json:"h"`
			} `json:"value"`
		} `json:"result"`
	}
	_ = conn.Call(ctx, tab.SessionID, "Runtime.evaluate", map[string]any{
		"expression":    `({url: location.href, title: document.title, w: window.innerWidth || 1280, h: window.innerHeight || 800})`,
		"returnByValue": true,
	}, &pageInfo)

	dispURL := pageInfo.Result.Value.URL
	if dispURL == "" {
		dispURL = tab.CurrentURL
	}
	dispW := int(pageInfo.Result.Value.W)
	if dispW <= 0 {
		dispW = 1280
	}
	dispH := int(pageInfo.Result.Value.H)
	if dispH <= 0 {
		dispH = 800
	}

	var createRes struct {
		TargetID string `json:"targetId"`
	}
	if err := conn.Call(ctx, "", "Target.createTarget", map[string]any{
		"url": "about:blank",
	}, &createRes); err != nil {
		return nil, err
	}
	defer func() {
		_ = conn.Call(context.Background(), "", "Target.closeTarget", map[string]any{
			"targetId": createRes.TargetID,
		}, nil)
	}()

	var attachRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": createRes.TargetID,
		"flatten":  true,
	}, &attachRes); err != nil {
		return nil, err
	}
	frameSID := attachRes.SessionID
	_ = conn.Call(ctx, frameSID, "Page.enable", nil, nil)
	_ = conn.Call(ctx, frameSID, "Runtime.enable", nil, nil)

	pad := 56
	chromeH := 52
	canvasW := dispW + pad*2
	canvasH := dispH + chromeH + pad*2

	_ = conn.Call(ctx, frameSID, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             canvasW,
		"height":            canvasH,
		"deviceScaleFactor": 1,
		"mobile":            false,
	}, nil)

	markup := buildSkeuomorphicFrameHTML(rawBase64PNG, dispURL, tab.Nickname, dispW, dispH)
	markupJSON, _ := json.Marshal(markup)
	evalExpr := fmt.Sprintf(`new Promise((resolve) => {
		document.open();
		document.write(%s);
		document.close();
		const img = document.getElementById("shot");
		if (!img || img.complete) {
			requestAnimationFrame(() => resolve(true));
		} else {
			img.onload = () => requestAnimationFrame(() => resolve(true));
			img.onerror = () => resolve(false);
		}
	})`, string(markupJSON))

	_ = conn.Call(ctx, frameSID, "Runtime.evaluate", map[string]any{
		"expression":   evalExpr,
		"awaitPromise": true,
	}, nil)

	var adornedRes struct {
		Data string `json:"data"`
	}
	if err := conn.Call(ctx, frameSID, "Page.captureScreenshot", map[string]any{
		"format": "png",
	}, &adornedRes); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(adornedRes.Data)
}

func buildSkeuomorphicFrameHTML(b64PNG, urlStr, nickname string, w, h int) string {
	escURL := html.EscapeString(urlStr)
	escNick := html.EscapeString(nickname)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    width: 100vw;
    height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    background: radial-gradient(circle at 50%% 25%%, #2b3240 0%%, #141821 100%%);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  }
  .window {
    width: %dpx;
    border-radius: 14px;
    overflow: hidden;
    background: #1e1e1e;
    box-shadow:
      0 28px 70px rgba(0, 0, 0, 0.68),
      0 8px 22px rgba(0, 0, 0, 0.45),
      inset 0 1px 0 rgba(255, 255, 255, 0.45);
    border: 1px solid rgba(255, 255, 255, 0.18);
  }
  .titlebar {
    height: 52px;
    display: flex;
    align-items: center;
    padding: 0 16px;
    gap: 14px;
    background: linear-gradient(180deg, #e9ecf2 0%%, #cfd5e0 48%%, #b8c0ce 100%%);
    border-bottom: 1px solid #7d8594;
    box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.85);
  }
  .traffic {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .dot {
    width: 13px;
    height: 13px;
    border-radius: 50%%;
    position: relative;
    box-shadow: inset 0 1px 2px rgba(0,0,0,0.35), 0 1px 0 rgba(255,255,255,0.65);
  }
  .dot::after {
    content: "";
    position: absolute;
    top: 2px;
    left: 3px;
    width: 7px;
    height: 4px;
    border-radius: 50%%;
    background: linear-gradient(180deg, rgba(255,255,255,0.85), rgba(255,255,255,0.05));
  }
  .dot.red { background: radial-gradient(circle at 50%% 80%%, #ff7b72, #e0443e); border: 1px solid #b52a25; }
  .dot.yellow { background: radial-gradient(circle at 50%% 80%%, #ffe07d, #dea123); border: 1px solid #b38014; }
  .dot.green { background: radial-gradient(circle at 50%% 80%%, #7ce38b, #28c941); border: 1px solid #1b962e; }
  .nav-btns {
    display: flex;
    gap: 6px;
    color: #3a414e;
    font-size: 14px;
    font-weight: 600;
    text-shadow: 0 1px 0 rgba(255,255,255,0.75);
  }
  .nav-btn {
    width: 26px;
    height: 26px;
    border-radius: 6px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: linear-gradient(180deg, #f8f9fb 0%%, #dce1ea 100%%);
    border: 1px solid #9aa2b1;
    box-shadow: 0 1px 0 rgba(255,255,255,0.7), inset 0 1px 0 #fff;
  }
  .address {
    flex: 1;
    height: 30px;
    border-radius: 15px;
    background: linear-gradient(180deg, #eef1f6 0%%, #ffffff 35%%, #ffffff 100%%);
    border: 1px solid #8c95a6;
    box-shadow: inset 0 1px 3px rgba(0, 0, 0, 0.18), 0 1px 0 rgba(255, 255, 255, 0.8);
    display: flex;
    align-items: center;
    padding: 0 14px;
    font-size: 13px;
    color: #1f2430;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .lock {
    color: #2da44e;
    margin-right: 8px;
    font-size: 12px;
  }
  .badge {
    height: 26px;
    padding: 0 11px;
    border-radius: 13px;
    background: linear-gradient(180deg, #2d3748 0%%, #1a202c 100%%);
    color: #edf2f7;
    font-size: 12px;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 5px;
    border: 1px solid #4a5568;
    box-shadow: 0 1px 2px rgba(0,0,0,0.25), inset 0 1px 0 rgba(255,255,255,0.18);
  }
  .viewport {
    width: %dpx;
    height: %dpx;
    background: #fff;
    display: block;
  }
  .viewport img {
    width: 100%%;
    height: 100%%;
    object-fit: cover;
    display: block;
  }
</style>
</head>
<body>
  <div class="window">
    <div class="titlebar">
      <div class="traffic">
        <div class="dot red"></div>
        <div class="dot yellow"></div>
        <div class="dot green"></div>
      </div>
      <div class="nav-btns">
        <div class="nav-btn">&#9664;</div>
        <div class="nav-btn">&#9654;</div>
        <div class="nav-btn">&#8635;</div>
      </div>
      <div class="address"><span class="lock">&#128274;</span>%s</div>
      <div class="badge">&#129302; %s</div>
    </div>
    <div class="viewport">
      <img id="shot" src="data:image/png;base64,%s" />
    </div>
  </div>
</body>
</html>`, w, w, h, escURL, escNick, b64PNG)
}
