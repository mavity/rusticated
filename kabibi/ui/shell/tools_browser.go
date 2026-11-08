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

type webArgumentSpec struct {
	name     string
	path     bool
	choices  []webArgumentChoice
	variadic bool
}

type webArgumentChoice struct {
	value  string
	action string
}

type webOptionSpec struct {
	name   string
	action string
}

type webSubcommandSpec struct {
	name        string
	aliases     []string
	options     []webOptionSpec
	positionals []webArgumentSpec
}

type webBuiltinInvocation struct {
	nickname    string
	command     string
	positionals [][]string
	options     map[string]bool
	provided    bool
}

func (invocation webBuiltinInvocation) argument(index int) string {
	if index < 0 || index >= len(invocation.positionals) {
		return ""
	}
	return strings.Join(invocation.positionals[index], " ")
}

func (invocation webBuiltinInvocation) argumentCount(index int) int {
	if index < 0 || index >= len(invocation.positionals) {
		return 0
	}
	return len(invocation.positionals[index])
}

type webGlobalOptions struct {
	nickname         string
	args             []string
	nicknameExpected bool
}

type webArgumentState struct {
	positionals [][]string
	options     map[string]bool
	next        int
}

var webNicknameOptions = []string{"--nick", "-n"}

var webSubcommands = []webSubcommandSpec{
	{name: "open", aliases: []string{"nav", "navigate"}, positionals: []webArgumentSpec{
		{name: "url", choices: []webArgumentChoice{
			{value: "--back", action: "back"},
			{value: "--forward", action: "forward"},
			{value: "--reload", action: "reload"},
		}},
	}},
	{name: "ax", positionals: []webArgumentSpec{{name: "query", variadic: true}}},
	{name: "click", positionals: []webArgumentSpec{{name: "target", variadic: true}}},
	{name: "type", positionals: []webArgumentSpec{
		{name: "target"},
		{name: "text", variadic: true},
	}},
	{name: "shot", aliases: []string{"screenshot"}, options: []webOptionSpec{
		{name: "--adorn", action: "adorn"},
		{name: "--full", action: "full_page"},
	}, positionals: []webArgumentSpec{{name: "output", path: true}}},
	{name: "eval", positionals: []webArgumentSpec{{name: "expression", variadic: true}}},
	{name: "cdp", positionals: []webArgumentSpec{
		{name: "method"},
		{name: "json_params"},
	}},
	{name: "logs"},
}

var errWebSubcommandRequired = errors.New("web subcommand required")

func webSubcommand(name string) (webSubcommandSpec, bool) {
	for _, spec := range webSubcommands {
		if name == spec.name {
			return spec, true
		}
		for _, alias := range spec.aliases {
			if name == alias {
				return spec, true
			}
		}
	}
	return webSubcommandSpec{}, false
}

func webSubcommandNames() []string {
	var names []string
	for _, spec := range webSubcommands {
		names = append(names, spec.name)
		names = append(names, spec.aliases...)
	}
	return names
}

func webOptionNames(spec webSubcommandSpec) []string {
	names := make([]string, len(spec.options))
	for i, option := range spec.options {
		names[i] = option.name
	}
	return names
}

func webOption(spec webSubcommandSpec, name string) (webOptionSpec, bool) {
	for _, option := range spec.options {
		if option.name == name {
			return option, true
		}
	}
	return webOptionSpec{}, false
}

func parseWebGlobalOptions(args []string, allowIncomplete bool) webGlobalOptions {
	state := webGlobalOptions{nickname: "main"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isWebNicknameOption(arg) {
			if i+1 == len(args) && allowIncomplete {
				state.nicknameExpected = true
				return state
			}
			if i+1 < len(args) {
				state.nickname = args[i+1]
				i++
				continue
			}
		}
		if strings.HasPrefix(arg, "--nick=") {
			state.nickname = strings.TrimPrefix(arg, "--nick=")
			continue
		}
		state.args = append(state.args, arg)
	}
	return state
}

func isWebNicknameOption(arg string) bool {
	for _, option := range webNicknameOptions {
		if option == arg {
			return true
		}
	}
	return false
}

func parseWebArguments(spec webSubcommandSpec, args []string) webArgumentState {
	state := webArgumentState{
		positionals: make([][]string, len(spec.positionals)),
		options:     make(map[string]bool, len(spec.options)),
	}
	for _, arg := range args {
		if option, ok := webOption(spec, arg); ok {
			state.options[option.name] = true
			continue
		}
		if state.next >= len(spec.positionals) {
			if len(spec.positionals) == 0 || !spec.positionals[len(spec.positionals)-1].variadic {
				continue
			}
			state.next = len(spec.positionals) - 1
		}
		state.positionals[state.next] = append(state.positionals[state.next], arg)
		if !spec.positionals[state.next].variadic {
			state.next++
		}
	}
	return state
}

func parseWebBuiltinInvocation(cmdName string, args []string) (webBuiltinInvocation, error) {
	global := parseWebGlobalOptions(args, false)
	invocation := webBuiltinInvocation{nickname: global.nickname, provided: len(global.args) > 0}

	if cmdName == "cdp" {
		invocation.command = "cdp"
		spec, _ := webSubcommand("cdp")
		state := parseWebArguments(spec, global.args)
		invocation.positionals = state.positionals
		invocation.options = state.options
		return invocation, nil
	}
	if len(global.args) == 0 {
		return webBuiltinInvocation{}, errWebSubcommandRequired
	}
	spec, ok := webSubcommand(global.args[0])
	if !ok {
		return webBuiltinInvocation{}, fmt.Errorf("unknown subcommand %q", global.args[0])
	}
	invocation.command = spec.name
	state := parseWebArguments(spec, global.args[1:])
	invocation.positionals = state.positionals
	invocation.options = state.options
	invocation.provided = len(global.args) > 1
	return invocation, nil
}

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
		lead := ai.ToolLead(BrowserNavigateInput{}, fmt.Sprintf("[%s] %s", in.Nickname, label), 24)
		if feedback != nil {
			feedback(lead)
		}
		out, err := mgr.NavigateAction(ctx, in.Nickname, in.URL, in.Back, in.Forward, in.Reload)
		if feedback != nil {
			if err != nil {
				feedback(lead + "failed")
			} else {
				feedback(lead + "ok")
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
			embedder = tc.PartEmbedder()
		}
		label := in.Nickname
		if in.Search != "" {
			label += " ?" + in.Search
		}
		lead := ai.ToolLead(BrowserAXInput{}, label, 24)
		if feedback != nil {
			feedback(lead)
		}
		out, err := mgr.AXAction(ctx, in.Nickname, in.Search, in.RootSelector, embedder)
		if feedback != nil {
			if err != nil {
				feedback(lead + "failed")
			} else {
				feedback(lead + "ok")
			}
		}
		if err != nil {
			return nil, err
		}
		return truncateToolOutput(out), nil
	})

	tools["browser_click"] = ai.NewTool(func(ctx context.Context, in BrowserClickInput, feedback ai.LogFeedback) (any, error) {
		lead := ai.ToolLead(BrowserClickInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.Target), 24)
		if feedback != nil {
			feedback(lead)
		}
		out, err := mgr.ClickAction(ctx, in.Nickname, in.Target, in.Button)
		if feedback != nil {
			if err != nil {
				feedback(lead + "failed")
			} else {
				feedback(lead + "ok")
			}
		}
		if err != nil {
			return nil, err
		}
		return truncateToolOutput(out), nil
	})

	tools["browser_type"] = ai.NewTool(func(ctx context.Context, in BrowserTypeInput, feedback ai.LogFeedback) (any, error) {
		lead := ai.ToolLead(BrowserTypeInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.Target), 24)
		if feedback != nil {
			feedback(lead)
		}
		out, err := mgr.TypeAction(ctx, in.Nickname, in.Target, in.Text, in.ClearFirst, in.PressEnter)
		if feedback != nil {
			if err != nil {
				feedback(lead + "failed")
			} else {
				feedback(lead + "ok")
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
		resolved := resolveShellPath(shellInvocationDir(ctx, execSess), in.SavePath)
		lead := ai.ToolLead(BrowserScreenshotInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.SavePath), 24)
		if feedback != nil {
			feedback(lead)
		}
		nBytes, err := mgr.ScreenshotAction(ctx, in.Nickname, resolved, in.Target, in.FullPage, in.AdornChrome)
		if feedback != nil {
			if err != nil {
				feedback(lead + "failed")
			} else {
				feedback(lead + "saved")
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
		lead := ai.ToolLead(BrowserEvalInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.Expression), 24)
		if feedback != nil {
			feedback(lead)
		}
		val, delta, err := mgr.EvalAction(ctx, in.Nickname, in.Expression)
		if feedback != nil {
			if err != nil {
				feedback(lead + "failed")
			} else {
				feedback(lead + "ok")
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
		lead := ai.ToolLead(BrowserCDPInput{}, fmt.Sprintf("[%s] %s", in.Nickname, in.Method), 24)
		if feedback != nil {
			feedback(lead)
		}
		raw, err := mgr.RawCDPAction(ctx, in.Nickname, in.Method, in.Params)
		if feedback != nil {
			if err != nil {
				feedback(lead + "failed")
			} else {
				feedback(lead + "ok")
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
	invocation, err := parseWebBuiltinInvocation(cmdName, args)
	if err != nil {
		if cmdName != "cdp" && errors.Is(err, errWebSubcommandRequired) {
			fmt.Fprintln(hc.Stderr, "usage: web [--nick NAME] <open|ax|click|type|shot|eval|cdp|logs> [args...]")
		} else {
			fmt.Fprintf(hc.Stderr, "web: %v (expected %s)\n", err, strings.Join(webCanonicalSubcommandNames(), ", "))
		}
		return interp.ExitStatus(2)
	}
	nick := invocation.nickname
	sub := invocation.command

	if cmdName == "cdp" {
		method := invocation.argument(0)
		if invocation.argumentCount(0) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: cdp [--nick NAME] <Method> [json_params]")
			return interp.ExitStatus(2)
		}
		var params map[string]any
		if invocation.argumentCount(1) > 0 {
			rawParams := invocation.argument(1)
			if err := json.Unmarshal([]byte(rawParams), &params); err != nil {
				fmt.Fprintf(hc.Stderr, "cdp: invalid JSON params: %v\n", err)
				return interp.ExitStatus(2)
			}
		}
		raw, err := mgr.RawCDPAction(ctx, nick, method, params)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cdp: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, string(raw))
		return nil
	}

	switch sub {
	case "open":
		arg := invocation.argument(0)
		if invocation.argumentCount(0) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web open <url|--back|--forward|--reload>")
			return interp.ExitStatus(2)
		}
		spec, _ := webSubcommand(sub)
		action := ""
		for _, choice := range spec.positionals[0].choices {
			if choice.value == arg {
				action = choice.action
				break
			}
		}
		out, err := mgr.NavigateAction(ctx, nick, arg, action == "back", action == "forward", action == "reload")
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web open: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, out)
		return nil

	case "ax":
		query := invocation.argument(0)
		out, err := mgr.AXAction(ctx, nick, query, "", nil)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web ax: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, out)
		return nil

	case "click":
		target := invocation.argument(0)
		if invocation.argumentCount(0) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web click <target>")
			return interp.ExitStatus(2)
		}
		out, err := mgr.ClickAction(ctx, nick, target, "left")
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web click: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, out)
		return nil

	case "type":
		target, text := invocation.argument(0), invocation.argument(1)
		if invocation.argumentCount(0) == 0 || invocation.argumentCount(1) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web type <target> <text>")
			return interp.ExitStatus(2)
		}
		out, err := mgr.TypeAction(ctx, nick, target, text, false, false)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web type: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintln(hc.Stdout, out)
		return nil

	case "shot":
		if !invocation.provided {
			fmt.Fprintln(hc.Stderr, "usage: web shot <output.png> [--adorn] [--full]")
			return interp.ExitStatus(2)
		}
		outPath := invocation.argument(0)
		spec, _ := webSubcommand(sub)
		adorn := invocation.options[spec.options[0].name]
		full := invocation.options[spec.options[1].name]
		resolved := resolveShellPath(hc.Dir, outPath)
		nBytes, err := mgr.ScreenshotAction(ctx, nick, resolved, "", full, adorn)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "web shot: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintf(hc.Stdout, "saved %s (%d bytes)\n", resolved, nBytes)
		return nil

	case "eval":
		expression := invocation.argument(0)
		if invocation.argumentCount(0) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web eval <js_expression>")
			return interp.ExitStatus(2)
		}
		val, delta, err := mgr.EvalAction(ctx, nick, expression)
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
		method := invocation.argument(0)
		if invocation.argumentCount(0) == 0 {
			fmt.Fprintln(hc.Stderr, "usage: web cdp <Method> [json_params]")
			return interp.ExitStatus(2)
		}
		var params map[string]any
		if invocation.argumentCount(1) > 0 {
			rawParams := invocation.argument(1)
			if err := json.Unmarshal([]byte(rawParams), &params); err != nil {
				fmt.Fprintf(hc.Stderr, "web cdp: invalid JSON params: %v\n", err)
				return interp.ExitStatus(2)
			}
		}
		raw, err := mgr.RawCDPAction(ctx, nick, method, params)
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
		fmt.Fprintf(hc.Stderr, "web: unknown subcommand %q (expected %s)\n", sub, strings.Join(webCanonicalSubcommandNames(), ", "))
		return interp.ExitStatus(2)
	}
}

func webCanonicalSubcommandNames() []string {
	names := make([]string, len(webSubcommands))
	for i, spec := range webSubcommands {
		names[i] = spec.name
	}
	return names
}
