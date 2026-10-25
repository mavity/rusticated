package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai"
	"github.com/mavity/rusticated/kabibi/tools/browser"
	"mvdan.cc/sh/v3/interp"
)

type BrowserNavigateInput struct {
	_           struct{} `doc:"Open a URL or navigate history (back/forward/reload) in a nickname-owned browser tab, wait for network + DOM/AX settling, and return the AX tree diff and network activity summary." emoji:"🌐"`
	Nickname    string   `json:"nickname" doc:"Mandatory tab owner nickname (e.g. 'main', 'researcher', 'shopping') stored inside the browser"`
	URL         string   `json:"url" doc:"Always include this field; use an empty string only when using back, forward, or reload"`
	Back        bool     `json:"back,omitempty" doc:"If true, navigate back in tab history"`
	Forward     bool     `json:"forward,omitempty" doc:"If true, navigate forward in tab history"`
	Reload      bool     `json:"reload,omitempty" doc:"If true, reload the current page"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of this browser navigation"`
}

type BrowserAXInput struct {
	_            struct{} `doc:"Inspect the heuristically-trimmed Accessibility (AX) tree of a nickname-owned browser tab, with optional 4-tier progressive search (exact -> regex -> fuzzy -> EmbeddingGemma 2)." emoji:"🌳"`
	Nickname     string   `json:"nickname" doc:"Mandatory tab owner nickname"`
	Search       string   `json:"search,omitempty" doc:"Optional search query to filter the AX tree (exact -> regex -> fuzzy -> semantic)"`
	RootSelector string   `json:"root_selector,omitempty" doc:"Optional CSS selector scoping the root container"`
	Explanation  string   `json:"explanation,omitempty" doc:"Brief explanation of what is being inspected"`
}

type BrowserClickInput struct {
	_           struct{} `doc:"Click an element on a nickname-owned browser tab by human caption (e.g. 'Sign in', 'button \"Submit\"') or CSS selector using biomimetic Bézier mouse movement, then wait for settling and return the AX diff + network summary." emoji:"🖱️"`
	Nickname    string   `json:"nickname" doc:"Mandatory tab owner nickname"`
	Target      string   `json:"target" doc:"Element caption (e.g. 'Sign in', 'button \"Add to cart\"', 'link \"Docs\"') or CSS selector"`
	Button      string   `json:"button,omitempty" doc:"Mouse button: 'left' (default), 'right', 'middle', or 'double'"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why this element is being clicked"`
}

type BrowserTypeInput struct {
	_           struct{} `doc:"Type text into an input or element on a nickname-owned browser tab using digraph-aware keystroke cadence and hardware Shift chords, then wait for settling and return the AX diff + network summary." emoji:"⌨️"`
	Nickname    string   `json:"nickname" doc:"Mandatory tab owner nickname"`
	Target      string   `json:"target,omitempty" doc:"Element caption, placeholder, label, or CSS selector (if empty, types into currently focused element)"`
	Text        string   `json:"text" doc:"Text to type"`
	ClearFirst  bool     `json:"clear_first,omitempty" doc:"If true, clear existing input value before typing"`
	PressEnter  bool     `json:"press_enter,omitempty" doc:"If true, press Enter after typing"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of what is being typed"`
}

type BrowserScreenshotInput struct {
	_           struct{} `doc:"Capture a PNG screenshot of a nickname-owned browser tab (full viewport, full scrollable page, or specific element), optionally composited inside a skeuomorphic HTML/CSS browser window frame." emoji:"📸"`
	Nickname    string   `json:"nickname" doc:"Mandatory tab owner nickname"`
	SavePath    string   `json:"save_path" doc:"File path to save the PNG screenshot (relative to current shell cwd or absolute)"`
	Target      string   `json:"target,omitempty" doc:"Optional element caption or CSS selector to clip screenshot to"`
	FullPage    bool     `json:"full_page,omitempty" doc:"If true, capture the full scrollable page height"`
	AdornChrome bool     `json:"adorn_chrome,omitempty" doc:"If true, render a realistic skeuomorphic rounded browser frame with drop shadow and address bar"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of the screenshot"`
}

type BrowserEvalInput struct {
	_           struct{} `doc:"Evaluate a JavaScript expression in a nickname-owned browser tab (auto-awaiting Promises), wait for settling, and return the result along with the AX diff + network summary." emoji:"⚡"`
	Nickname    string   `json:"nickname" doc:"Mandatory tab owner nickname"`
	Expression  string   `json:"expression" doc:"JavaScript expression or IIFE to evaluate in the page"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why this script is being evaluated"`
}

type BrowserCDPInput struct {
	_           struct{}       `doc:"Invoke any raw Chrome DevTools Protocol (CDP) method on a nickname-owned browser tab or browser session." emoji:"🔧"`
	Nickname    string         `json:"nickname" doc:"Mandatory tab owner nickname"`
	Method      string         `json:"method" doc:"CDP method name (e.g. 'Network.getCookies', 'Page.bringToFront', 'DOM.getDocument')"`
	Params      map[string]any `json:"params,omitempty" doc:"Optional JSON object parameters for the CDP method"`
	Explanation string         `json:"explanation,omitempty" doc:"Brief explanation of the raw CDP call"`
}

func registerBrowserTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	mgr := browser.DefaultManager()

	tools["browser_navigate"] = ai.NewTool(func(ctx context.Context, in BrowserNavigateInput, feedback ai.LogFeedback) (any, error) {
		if feedback != nil {
			label := in.URL
			if label == "" {
				if in.Back {
					label = "back"
				} else if in.Forward {
					label = "forward"
				} else if in.Reload {
					label = "reload"
				}
			}
			feedback(ai.ToolLead(BrowserNavigateInput{}, fmt.Sprintf("[%s] %s", in.Nickname, label), 24))
		}
		out, err := mgr.NavigateAction(ctx, in.Nickname, in.URL, in.Back, in.Forward, in.Reload)
		if feedback != nil {
			if err != nil {
				feedback("failed")
			} else {
				feedback("ok")
			}
		}
		if err != nil {
			return nil, err
		}
		return truncateToolOutput(out), nil
	})

	tools["browser_ax"] = ai.NewSessionTool(func(ctx context.Context, tc *ai.ToolContext, in BrowserAXInput) (any, error) {
		var feedback ai.LogFeedback
		var embedder ai.PartEmbedder
		if tc != nil {
			feedback = tc.Feedback
			if tc.Session != nil {
				embedder = tc.Session.Embedder()
			}
		}
		if feedback != nil {
			lead := in.Nickname
			if in.Search != "" {
				lead += " ?" + in.Search
			}
			feedback(ai.ToolLead(BrowserAXInput{}, lead, 24))
		}
		out, err := mgr.AXAction(ctx, in.Nickname, in.Search, in.RootSelector, embedder)
		if feedback != nil {
			if err != nil {
				feedback("failed")
			} else {
				feedback("ok")
			}
		}
		if err != nil {
			return nil, err
		}
		return truncateToolOutput(out), nil
	})

	tools["browser_click"] = ai.NewTool(func(ctx context.Context, in BrowserClickInput, feedback ai.LogFeedback) (any, error) {
		if feedback != nil {
			feedback(ai.ToolLead(BrowserClickInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.Target), 24))
		}
		out, err := mgr.ClickAction(ctx, in.Nickname, in.Target, in.Button)
		if feedback != nil {
			if err != nil {
				feedback("failed")
			} else {
				feedback("ok")
			}
		}
		if err != nil {
			return nil, err
		}
		return truncateToolOutput(out), nil
	})

	tools["browser_type"] = ai.NewTool(func(ctx context.Context, in BrowserTypeInput, feedback ai.LogFeedback) (any, error) {
		if feedback != nil {
			feedback(ai.ToolLead(BrowserTypeInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.Target), 24))
		}
		out, err := mgr.TypeAction(ctx, in.Nickname, in.Target, in.Text, in.ClearFirst, in.PressEnter)
		if feedback != nil {
			if err != nil {
				feedback("failed")
			} else {
				feedback("ok")
			}
		}
		if err != nil {
			return nil, err
		}
		return truncateToolOutput(out), nil
	})

	tools["browser_screenshot"] = ai.NewTool(func(ctx context.Context, in BrowserScreenshotInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.SavePath) == "" {
			return nil, errors.New("save_path is required")
		}
		resolved := resolveShellPath(execSess.Dir(), in.SavePath)
		if feedback != nil {
			feedback(ai.ToolLead(BrowserScreenshotInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.SavePath), 24))
		}
		nBytes, err := mgr.ScreenshotAction(ctx, in.Nickname, resolved, in.Target, in.FullPage, in.AdornChrome)
		if feedback != nil {
			if err != nil {
				feedback("failed")
			} else {
				feedback("saved")
			}
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"path":         resolved,
			"bytes":        nBytes,
			"adorn_chrome": in.AdornChrome,
		}, nil
	})

	tools["browser_eval"] = ai.NewTool(func(ctx context.Context, in BrowserEvalInput, feedback ai.LogFeedback) (any, error) {
		if feedback != nil {
			feedback(ai.ToolLead(BrowserEvalInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.Expression), 24))
		}
		val, delta, err := mgr.EvalAction(ctx, in.Nickname, in.Expression)
		if feedback != nil {
			if err != nil {
				feedback("failed")
			} else {
				feedback("ok")
			}
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"value": val,
			"delta": truncateToolOutput(delta),
		}, nil
	})

	tools["browser_cdp"] = ai.NewTool(func(ctx context.Context, in BrowserCDPInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.Method) == "" {
			return nil, errors.New("method is required")
		}
		if feedback != nil {
			feedback(ai.ToolLead(BrowserCDPInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.Method), 24))
		}
		raw, err := mgr.RawCDPAction(ctx, in.Nickname, in.Method, in.Params)
		if feedback != nil {
			if err != nil {
				feedback("failed")
			} else {
				feedback("ok")
			}
		}
		if err != nil {
			return nil, err
		}
		var parsed any
		_ = json.Unmarshal(raw, &parsed)
		return parsed, nil
	})
}

// webBuiltinMiddleware exposes the `web` and `cdp` shell builtins so the human user
// or shell scripts can drive the exact same browser engine directly from the Kabibi prompt.
func webBuiltinMiddleware(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 || (args[0] != "web" && args[0] != "cdp") {
			return next(ctx, args)
		}
		return runWebBuiltin(ctx, args[0], args[1:])
	}
}

func runWebBuiltin(ctx context.Context, cmdName string, args []string) error {
	hc := interp.HandlerCtx(ctx)
	mgr := browser.DefaultManager()

	nick := "main"
	var rest []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--nick" || args[i] == "-n") && i+1 < len(args) {
			nick = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--nick=") {
			nick = strings.TrimPrefix(args[i], "--nick=")
			continue
		}
		rest = append(rest, args[i])
	}

	if cmdName == "cdp" {
		if len(rest) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: cdp [--nick NAME] <Method> [json_params]")
			return interp.ExitStatus(2)
		}
		var params map[string]any
		if len(rest) > 1 {
			if err := json.Unmarshal([]byte(rest[1]), &params); err != nil {
				fmt.Fprintf(hc.Stderr, "cdp: invalid JSON params: %v\n", err)
				return interp.ExitStatus(2)
			}
		}
		raw, err := mgr.RawCDPAction(ctx, nick, rest[0], params)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cdp: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, string(raw))
		return nil
	}

	if len(rest) == 0 {
		fmt.Fprintln(hc.Stderr, "usage: web [--nick NAME] <open|ax|click|type|shot|eval|cdp|logs> [args...]")
		return interp.ExitStatus(2)
	}

	sub := rest[0]
	subArgs := rest[1:]

	switch sub {
	case "open", "nav", "navigate":
		if len(subArgs) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web open <url|--back|--forward|--reload>")
			return interp.ExitStatus(2)
		}
		arg := subArgs[0]
		out, err := mgr.NavigateAction(ctx, nick, arg, arg == "--back", arg == "--forward", arg == "--reload")
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web open: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, out)
		return nil

	case "ax":
		query := strings.Join(subArgs, " ")
		out, err := mgr.AXAction(ctx, nick, query, "", nil)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web ax: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, out)
		return nil

	case "click":
		if len(subArgs) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web click <target>")
			return interp.ExitStatus(2)
		}
		target := strings.Join(subArgs, " ")
		out, err := mgr.ClickAction(ctx, nick, target, "left")
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web click: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, out)
		return nil

	case "type":
		if len(subArgs) < 2 {
			fmt.Fprintln(hc.Stderr, "usage: web type <target> <text>")
			return interp.ExitStatus(2)
		}
		out, err := mgr.TypeAction(ctx, nick, subArgs[0], strings.Join(subArgs[1:], " "), false, false)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web type: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, out)
		return nil

	case "shot", "screenshot":
		if len(subArgs) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web shot <output.png> [--adorn] [--full]")
			return interp.ExitStatus(2)
		}
		var outPath string
		adorn := false
		full := false
		for _, a := range subArgs {
			switch a {
			case "--adorn":
				adorn = true
			case "--full":
				full = true
			default:
				if outPath == "" {
					outPath = a
				}
			}
		}
		resolved := resolveShellPath(hc.Dir, outPath)
		nBytes, err := mgr.ScreenshotAction(ctx, nick, resolved, "", full, adorn)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web shot: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintf(hc.Stdout, "saved %s (%d bytes)\n", resolved, nBytes)
		return nil

	case "eval":
		if len(subArgs) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web eval <js_expression>")
			return interp.ExitStatus(2)
		}
		val, delta, err := mgr.EvalAction(ctx, nick, strings.Join(subArgs, " "))
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web eval: %v\n", err)
			return interp.ExitStatus(1)
		}
		b, _ := json.MarshalIndent(val, "", "  ")
		fmt.Fprintln(hc.Stdout, string(b))
		if delta != "" {
			fmt.Fprintln(hc.Stdout, delta)
		}
		return nil

	case "cdp":
		if len(subArgs) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web cdp <Method> [json_params]")
			return interp.ExitStatus(2)
		}
		var params map[string]any
		if len(subArgs) > 1 {
			if err := json.Unmarshal([]byte(subArgs[1]), &params); err != nil {
				fmt.Fprintf(hc.Stderr, "web cdp: invalid JSON params: %v\n", err)
				return interp.ExitStatus(2)
			}
		}
		raw, err := mgr.RawCDPAction(ctx, nick, subArgs[0], params)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web cdp: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, string(raw))
		return nil

	case "logs":
		_, tab, err := mgr.EnsureTab(ctx, nick)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web logs: %v\n", err)
			return interp.ExitStatus(1)
		}
		for _, entry := range tab.ConsoleSnapshot() {
			fmt.Fprintf(hc.Stdout, "[%s] %s: %s\n", entry.Timestamp.Format("15:04:05"), entry.Level, entry.Text)
		}
		return nil

	default:
		fmt.Fprintf(hc.Stderr, "web: unknown subcommand %q (expected open, ax, click, type, shot, eval, cdp, logs)\n", sub)
		return interp.ExitStatus(2)
	}
}
