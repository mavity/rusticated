package ai

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai/assets"
)

// ErrAuthCancelled is returned when an interactive setup or authentication flow
// is cancelled by the user (Ctrl+C, Esc, or explicit cancel option).
var ErrAuthCancelled = errors.New("AI authentication cancelled")

// TerminalSession is the narrow visual and input bottleneck passed to AI
// initialization and provider authentication flows. It models an in-process
// virtual PTY (backed by CommandTerminal in Shell TUI mode or stdio in CLI mode).
type TerminalSession interface {
	io.Reader
	io.Writer
	Size() (cols, rows int)
	IsTTY() bool
	SubscribeRawInput() (readKey func(ctx context.Context, p []byte) (int, error), release func())
}

// EmptyEnterHandler lets an interactive terminal perform an action instead of
// submitting an empty line. Non-TTY input deliberately does not use this hook.
type EmptyEnterHandler interface {
	HandleEmptyEnter() bool
}

// StdioTerminalSession wraps an io.Reader and io.Writer as a non-TTY (or simple line)
// TerminalSession for headless / one-shot CLI execution and deterministic unit tests.
type StdioTerminalSession struct {
	Reader io.Reader
	Writer io.Writer
	Cols   int
	Rows   int
	TTY    bool
}

func (s *StdioTerminalSession) Read(p []byte) (int, error) {
	if s == nil || s.Reader == nil {
		return 0, io.EOF
	}
	return s.Reader.Read(p)
}

func (s *StdioTerminalSession) Write(p []byte) (int, error) {
	if s == nil || s.Writer == nil {
		return len(p), nil
	}
	return s.Writer.Write(p)
}

func (s *StdioTerminalSession) Size() (int, int) {
	cols, rows := 80, 24
	if s != nil {
		if s.Cols > 0 {
			cols = s.Cols
		}
		if s.Rows > 0 {
			rows = s.Rows
		}
	}
	return cols, rows
}

func (s *StdioTerminalSession) IsTTY() bool {
	return s != nil && s.TTY
}

func (s *StdioTerminalSession) SubscribeRawInput() (func(context.Context, []byte) (int, error), func()) {
	readFn := func(ctx context.Context, p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return s.Read(p)
	}
	return readFn, func() {}
}

// PromptSelect renders an interactive menu over term. When term.IsTTY() is true,
// the user can navigate with Up/Down arrows (or j/k), press 1-9, Enter to confirm,
// or Esc/Ctrl+C to cancel. When term.IsTTY() is false, it renders a numbered prompt
// and reads a single line.
func PromptSelect(ctx context.Context, term TerminalSession, title, message string, options []string, defaultIdx int) (int, error) {
	if len(options) == 0 {
		return -1, errors.New("prompt select requires at least one option")
	}
	if defaultIdx < 0 || defaultIdx >= len(options) {
		defaultIdx = 0
	}
	if term == nil {
		return -1, errors.New("AI setup requires an active terminal session")
	}
	if !term.IsTTY() {
		return promptSelectLine(ctx, term, title, message, options, defaultIdx)
	}

	readKey, release := term.SubscribeRawInput()
	defer release()

	selected := defaultIdx
	renderedLines := 0
	render := func() error {
		var sb strings.Builder
		if renderedLines > 0 {
			fmt.Fprintf(&sb, "\r\x1b[%dA\x1b[J", renderedLines)
		}
		lines := 0
		if title != "" {
			sb.WriteString("\x1b[1;36m")
			sb.WriteString(title)
			sb.WriteString("\x1b[0m\n")
			lines++
		}
		if message != "" {
			for _, l := range strings.Split(message, "\n") {
				sb.WriteString(l)
				sb.WriteByte('\n')
				lines++
			}
		}
		for i, opt := range options {
			if i == selected {
				fmt.Fprintf(&sb, " \x1b[1;33m▸ [%d] %s\x1b[0m\n", i+1, opt)
			} else {
				fmt.Fprintf(&sb, "   [%d] %s\n", i+1, opt)
			}
			lines++
		}
		sb.WriteString("\x1b[2m(↑/↓ or 1-9 to select, Enter to confirm, Esc/Ctrl+C to cancel)\x1b[0m")
		renderedLines = lines
		_, err := io.WriteString(term, sb.String())
		return err
	}

	if err := render(); err != nil {
		return -1, err
	}

	var buf [16]byte
	for {
		n, err := readKey(ctx, buf[:])
		if err != nil {
			_, _ = io.WriteString(term, "\n")
			if errors.Is(err, context.Canceled) {
				return -1, ErrAuthCancelled
			}
			return -1, err
		}
		b := buf[:n]
		if len(b) == 0 {
			continue
		}
		switch {
		case b[0] == 3: // Ctrl+C
			_, _ = io.WriteString(term, "\n")
			return -1, ErrAuthCancelled
		case b[0] == '\r' || b[0] == '\n':
			_, _ = fmt.Fprintf(term, "\r\x1b[2K\x1b[32m✓ %s\x1b[0m\n", options[selected])
			return selected, nil
		case b[0] == 0x1b:
			if len(b) == 1 {
				_, _ = io.WriteString(term, "\n")
				return -1, ErrAuthCancelled
			}
			if len(b) >= 3 && b[1] == '[' {
				switch b[2] {
				case 'A': // Up
					selected = (selected - 1 + len(options)) % len(options)
					if err := render(); err != nil {
						return -1, err
					}
				case 'B': // Down
					selected = (selected + 1) % len(options)
					if err := render(); err != nil {
						return -1, err
					}
				}
			}
		case b[0] == 'k' || b[0] == 'K':
			selected = (selected - 1 + len(options)) % len(options)
			if err := render(); err != nil {
				return -1, err
			}
		case b[0] == 'j' || b[0] == 'J':
			selected = (selected + 1) % len(options)
			if err := render(); err != nil {
				return -1, err
			}
		case b[0] >= '1' && b[0] <= '9':
			idx := int(b[0] - '1')
			if idx < len(options) {
				selected = idx
				if err := render(); err != nil {
					return -1, err
				}
			}
		}
	}
}

func promptSelectLine(ctx context.Context, term TerminalSession, title, message string, options []string, defaultIdx int) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if title != "" {
		if _, err := fmt.Fprintln(term, title); err != nil {
			return -1, err
		}
	}
	if message != "" {
		if _, err := fmt.Fprintln(term, message); err != nil {
			return -1, err
		}
	}
	for i, opt := range options {
		if _, err := fmt.Fprintf(term, "  [%d] %s\n", i+1, opt); err != nil {
			return -1, err
		}
	}
	if _, err := fmt.Fprintf(term, "Choice [1-%d, default %d]: ", len(options), defaultIdx+1); err != nil {
		return -1, err
	}
	line, err := readLineFromTerm(ctx, term, false)
	if err != nil {
		return -1, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultIdx, nil
	}
	choice, convErr := strconv.Atoi(line)
	if convErr != nil || choice < 1 || choice > len(options) {
		return -1, fmt.Errorf("invalid choice %q; enter a number from 1 to %d", line, len(options))
	}
	return choice - 1, nil
}

// PromptConfirm asks a yes/no confirmation question in term.
func PromptConfirm(ctx context.Context, term TerminalSession, title, message string) (bool, error) {
	if term == nil {
		return false, errors.New("AI confirmation requires an active terminal session")
	}
	if term.IsTTY() {
		choice, err := PromptSelect(ctx, term, title, message, []string{"Yes, continue", "No, cancel"}, 0)
		if err != nil {
			return false, err
		}
		return choice == 0, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if title != "" {
		if _, err := fmt.Fprintln(term, title); err != nil {
			return false, err
		}
	}
	if _, err := fmt.Fprintf(term, "%s [y/N]: ", message); err != nil {
		return false, err
	}
	line, err := readLineFromTerm(ctx, term, false)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "", "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid confirmation %q; enter y or n", line)
	}
}

// PromptLine prompts for a line of text in term. When secret is true and term is interactive,
// typed characters are masked with '*'.
func PromptLine(ctx context.Context, term TerminalSession, title, prompt string, secret bool) (string, error) {
	if term == nil {
		return "", errors.New("AI input requires an active terminal session")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if title != "" {
		if _, err := fmt.Fprintf(term, "\x1b[1;36m%s\x1b[0m\n", title); err != nil {
			return "", err
		}
	}
	if prompt != "" {
		if _, err := fmt.Fprintf(term, "%s: ", prompt); err != nil {
			return "", err
		}
	}
	return readLineFromTerm(ctx, term, secret)
}

// PromptOpenURL lets an interactive terminal open the latest displayed URL on Enter.
func PromptOpenURL(ctx context.Context, term TerminalSession, prompt string) error {
	if term == nil {
		return errors.New("AI URL prompt requires an active terminal session")
	}
	if !term.IsTTY() {
		return nil
	}
	handler, ok := term.(EmptyEnterHandler)
	if !ok {
		return nil
	}
	if _, err := fmt.Fprintln(term, prompt); err != nil {
		return err
	}
	readKey, release := term.SubscribeRawInput()
	defer release()
	var buf [64]byte
	for {
		n, err := readKey(ctx, buf[:])
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return ErrAuthCancelled
			}
			return fmt.Errorf("read browser-open prompt: %w", err)
		}
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\n':
				if !handler.HandleEmptyEnter() {
					return errors.New("no authorization URL was available to open")
				}
				_, _ = fmt.Fprintln(term)
				return nil
			case 3, 0x1b:
				return ErrAuthCancelled
			}
		}
	}
}

func readLineFromTerm(ctx context.Context, term TerminalSession, secret bool) (string, error) {
	if !term.IsTTY() {
		var out []byte
		var b [1]byte
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			n, err := term.Read(b[:])
			if n > 0 {
				if b[0] == '\n' {
					break
				}
				if b[0] != '\r' {
					out = append(out, b[0])
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) && len(out) > 0 {
					break
				}
				if errors.Is(err, context.Canceled) {
					return "", ErrAuthCancelled
				}
				return "", fmt.Errorf("AI setup needs an interactive response from terminal input: %w", err)
			}
		}
		return strings.TrimSpace(string(out)), nil
	}

	readKey, release := term.SubscribeRawInput()
	defer release()

	var runes []rune
	var buf [64]byte
	for {
		n, err := readKey(ctx, buf[:])
		if err != nil {
			_, _ = io.WriteString(term, "\n")
			if errors.Is(err, context.Canceled) {
				return "", ErrAuthCancelled
			}
			return "", err
		}
		chunk := string(buf[:n])
		for i := 0; i < len(chunk); {
			b := chunk[i]
			switch {
			case b == 3: // Ctrl+C
				_, _ = io.WriteString(term, "\n")
				return "", ErrAuthCancelled
			case b == '\r' || b == '\n':
				if len(runes) == 0 {
					if handler, ok := term.(EmptyEnterHandler); ok && handler.HandleEmptyEnter() {
						i++
						continue
					}
				}
				_, _ = io.WriteString(term, "\n")
				return strings.TrimSpace(string(runes)), nil
			case b == 0x7f || b == 0x08: // Backspace
				if len(runes) > 0 {
					runes = runes[:len(runes)-1]
					_, _ = io.WriteString(term, "\b \b")
				}
				i++
			case b == 0x1b:
				if len(chunk) == 1 {
					_, _ = io.WriteString(term, "\n")
					return "", ErrAuthCancelled
				}
				// Skip ANSI escape sequence
				i++
				if i < len(chunk) && chunk[i] == '[' {
					i++
					for i < len(chunk) && (chunk[i] < 0x40 || chunk[i] > 0x7e) {
						i++
					}
					if i < len(chunk) {
						i++
					}
				}
			case b >= 0x20:
				r, width := decodeFirstRune(chunk[i:])
				runes = append(runes, r)
				if secret {
					_, _ = io.WriteString(term, "*")
				} else {
					_, _ = io.WriteString(term, string(r))
				}
				i += width
			default:
				i++
			}
		}
	}
}

func decodeFirstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

// RenderProgress writes a single-line carriage-return progress indicator to term.
func RenderProgress(term io.Writer, stage, details string, percent int) {
	if term == nil {
		return
	}
	if percent >= 0 {
		_, _ = fmt.Fprintf(term, "\r\x1b[2K[%s] %s (%d%%)", stage, details, percent)
	} else {
		_, _ = fmt.Fprintf(term, "\r\x1b[2K[%s] %s", stage, details)
	}
}

// GeneratePKCE generates an RFC 7636 S256 PKCE code_verifier and code_challenge.
func GeneratePKCE() (verifier string, challenge string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomHex(bytesLen int) (string, error) {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// LoopbackCallbackOptions configures the one-shot loopback OAuth redirect server.
type LoopbackCallbackOptions[T any] struct {
	ProviderName string
	Host         string
	Port         int
	Path         string
	RedirectHost string
	State        string
	Complete     func(r *http.Request, code string) (T, error)
}

// LoopbackCallbackServer manages a one-shot HTTP callback listener on 127.0.0.1.
type LoopbackCallbackServer[T any] struct {
	RedirectURI string
	listener    net.Listener
	server      *http.Server
	resultCh    chan loopbackResult[T]
	cancelOnce  sync.Once
	closeOnce   sync.Once
	connsMu     sync.Mutex
	conns       map[net.Conn]struct{}
	wakeAddr    string
}

type loopbackResult[T any] struct {
	value T
	err   error
	ok    bool
}

func oauthHTMLPage(title, heading, message string, isError bool) string {
	accent := "#4D9ABF"
	if isError {
		accent = "#F09082"
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>%s</title>
<style>
:root { color-scheme: dark; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
* { box-sizing: border-box; }
body { min-height: 100vh; min-height: 100svh; margin: 0; padding: 24px; display: grid; place-items: center; background: #101722; color: #f4f7fb; }
main { width: min(100%%, 460px); padding: 32px; border: 1px solid #334155; border-radius: 18px; background: #192433; box-shadow: 0 24px 70px #0008; text-align: center; }
.mark { width: 56px; height: 56px; margin: 0 auto 20px; display: grid; place-items: center; border-radius: 50%%; background: %s22; color: %s; font-size: 30px; font-weight: 700; }
h1 { margin: 0; color: %s; font-size: clamp(1.4rem, 5vw, 1.8rem); line-height: 1.25; }
p { margin: 14px 0 0; color: #c4cfdd; font-size: 1rem; line-height: 1.6; }
</style>
</head>
<body>
<main role="status" aria-live="polite">
<div class="mark" aria-hidden="true">%s</div>
<h1>%s</h1>
<p>%s</p>
</main>
</body>
</html>`,
		html.EscapeString(title), accent, accent, accent,
		map[bool]string{true: "!", false: "✓"}[isError],
		html.EscapeString(heading), html.EscapeString(message))
}

// StartLoopbackCallbackServer binds a local HTTP listener and handles a single OAuth redirect.
func StartLoopbackCallbackServer[T any](ctx context.Context, opts LoopbackCallbackOptions[T]) (*LoopbackCallbackServer[T], error) {
	if assets.HostOS() == "browser" {
		return nil, errors.New("loopback callback server is not used inside browser host")
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrAuthCancelled
	}
	host := opts.Host
	if host == "" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(opts.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, errors.New("loopback listener did not bind to TCP")
	}
	actualPort := tcpAddr.Port
	if actualPort == 0 && opts.Port > 0 {
		actualPort = opts.Port
	}
	if actualPort <= 0 {
		_ = ln.Close()
		return nil, errors.New("loopback listener requires a non-zero TCP port")
	}
	redirectHost := opts.RedirectHost
	if redirectHost == "" {
		redirectHost = host
	}
	redirectURI := fmt.Sprintf("http://%s:%d%s", redirectHost, actualPort, opts.Path)

	srv := &LoopbackCallbackServer[T]{
		RedirectURI: redirectURI,
		listener:    ln,
		resultCh:    make(chan loopbackResult[T], 1),
		conns:       make(map[net.Conn]struct{}),
		wakeAddr:    net.JoinHostPort(host, strconv.Itoa(actualPort)),
	}

	var claimed sync.Mutex
	isClaimed := false

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet || r.URL.Path != opts.Path {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, oauthHTMLPage(opts.ProviderName, "Not Found", "Callback route not found.", true))
			return
		}
		if opts.State != "" && r.URL.Query().Get("state") != opts.State {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, oauthHTMLPage(opts.ProviderName, "Authentication Failed", "State mismatch.", true))
			return
		}
		claimed.Lock()
		if isClaimed {
			claimed.Unlock()
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, oauthHTMLPage(opts.ProviderName, "Already Handled", "This sign-in has already been handled.", true))
			return
		}
		if errCode := r.URL.Query().Get("error"); errCode != "" {
			isClaimed = true
			claimed.Unlock()
			desc := r.URL.Query().Get("error_description")
			if desc == "" {
				desc = errCode
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, oauthHTMLPage(opts.ProviderName, opts.ProviderName+" authorization failed.", desc, true))
			srv.finish(loopbackResult[T]{err: fmt.Errorf("%s authorization failed: %s", opts.ProviderName, desc)})
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			claimed.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, oauthHTMLPage(opts.ProviderName, "Missing Code", "Missing authorization code.", true))
			return
		}
		isClaimed = true
		claimed.Unlock()

		val, completeErr := opts.Complete(r, code)
		if completeErr != nil {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, oauthHTMLPage(opts.ProviderName, opts.ProviderName+" sign-in failed.", completeErr.Error(), true))
			srv.finish(loopbackResult[T]{err: completeErr})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, oauthHTMLPage(opts.ProviderName, "Login completed successfully", "You are now signed in to "+opts.ProviderName+". You can close this page and return to Kabibi.", false))
		srv.finish(loopbackResult[T]{value: val, ok: true})
	})

	srv.server = &http.Server{
		Handler: mux,
		ConnState: func(c net.Conn, state http.ConnState) {
			srv.connsMu.Lock()
			defer srv.connsMu.Unlock()
			switch state {
			case http.StateNew:
				srv.conns[c] = struct{}{}
			case http.StateClosed, http.StateHijacked:
				delete(srv.conns, c)
			}
		},
	}

	go func() {
		if serveErr := srv.server.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			srv.finish(loopbackResult[T]{err: serveErr})
		}
	}()

	return srv, nil
}

func (s *LoopbackCallbackServer[T]) finish(res loopbackResult[T]) {
	select {
	case s.resultCh <- res:
	default:
	}
}

func (s *LoopbackCallbackServer[T]) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		// On the single-threaded Rusticated WASM runtime, Accept() blocks inside
		// env.net_accept on the host until either a client connects or handle_close
		// is called. Closing the listener first and only poking wakeAddr on non-wasip1
		// avoids a synchronous Dial timeout inside the WASM guest.
		_ = s.listener.Close()
		_ = s.server.Close()
		s.connsMu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.connsMu.Unlock()
	})
}

// WaitForCallbackOrManualInput races the optional loopback HTTP server against
// a manual paste prompt on term, cancelling whichever loses the race.
func WaitForCallbackOrManualInput[T any](
	ctx context.Context,
	term TerminalSession,
	callback *LoopbackCallbackServer[T],
	promptMessage string,
	parseManual func(input string) (T, error),
) (T, error) {
	var zero T
	if callback == nil {
		input, err := PromptLine(ctx, term, "", promptMessage, false)
		if err != nil {
			return zero, err
		}
		return parseManual(input)
	}
	defer callback.Close()

	manualCtx, cancelManual := context.WithCancel(ctx)
	defer cancelManual()

	type manualOut struct {
		val T
		err error
	}
	manualCh := make(chan manualOut, 1)
	go func() {
		input, err := PromptLine(manualCtx, term, "", promptMessage, false)
		if err != nil {
			manualCh <- manualOut{err: err}
			return
		}
		val, err := parseManual(input)
		manualCh <- manualOut{val: val, err: err}
	}()

	select {
	case <-ctx.Done():
		return zero, ErrAuthCancelled
	case cbRes := <-callback.resultCh:
		cancelManual()
		if cbRes.err != nil {
			return zero, cbRes.err
		}
		if cbRes.ok {
			_, _ = io.WriteString(term, "\n")
			return cbRes.value, nil
		}
		return zero, ErrAuthCancelled
	case mRes := <-manualCh:
		if mRes.err != nil {
			return zero, mRes.err
		}
		return mRes.val, nil
	}
}

func parseOAuthCodeAndState(input string) (code string, state string) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", ""
	}
	if parsed, err := url.Parse(value); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if c := parsed.Query().Get("code"); c != "" {
			return c, parsed.Query().Get("state")
		}
	}
	if idx := strings.Index(value, "code="); idx >= 0 {
		sub := value[idx:]
		if q, err := url.ParseQuery(sub); err == nil {
			if c := q.Get("code"); c != "" {
				return c, q.Get("state")
			}
		}
	}
	if before, after, ok := strings.Cut(value, "#"); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return value, ""
}

// FormatClickableURL wraps urlStr in an OSC-8 terminal hyperlink escape sequence
// so clicking the link in an OSC-8 terminal opens the full URL even if wrapped,
// and prints it at column 0 without leading indentation.
func FormatClickableURL(urlStr string) string {
	return fmt.Sprintf("\x1b]8;;%s\x1b\\\x1b[4;34m%s\x1b[0m\x1b]8;;\x1b\\", urlStr, urlStr)
}

// DeviceCodePollStatus represents the outcome of a single RFC 8628 poll attempt.
type DeviceCodePollStatus string

const (
	DevicePollPending  DeviceCodePollStatus = "pending"
	DevicePollSlowDown DeviceCodePollStatus = "slow_down"
	DevicePollFailed   DeviceCodePollStatus = "failed"
	DevicePollComplete DeviceCodePollStatus = "complete"
)

type DeviceCodePollResult[T any] struct {
	Status          DeviceCodePollStatus
	IntervalSeconds int
	Message         string
	Value           T
}

type DeviceCodePollOptions[T any] struct {
	IntervalSeconds  int
	ExpiresInSeconds int
	WaitBeforeFirst  bool
	Poll             func(ctx context.Context) (DeviceCodePollResult[T], error)
}

// PollDeviceCodeFlow implements RFC 8628 device-code polling with slow_down backoff
// and server-provided interval overrides (matching external/pi/packages/ai/src/auth/oauth/device-code.ts).
func PollDeviceCodeFlow[T any](ctx context.Context, opts DeviceCodePollOptions[T]) (T, error) {
	var zero T
	interval := time.Duration(opts.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if interval < time.Second {
		interval = time.Second
	}

	var deadline time.Time
	if opts.ExpiresInSeconds > 0 {
		deadline = time.Now().Add(time.Duration(opts.ExpiresInSeconds) * time.Second)
	}

	sleepAbortable := func(d time.Duration) error {
		if !deadline.IsZero() {
			rem := time.Until(deadline)
			if rem <= 0 {
				return errors.New("device flow timed out")
			}
			if d > rem {
				d = rem
			}
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ErrAuthCancelled
		case <-timer.C:
			return nil
		}
	}

	slowDowns := 0
	if opts.WaitBeforeFirst {
		if err := sleepAbortable(interval); err != nil {
			return zero, err
		}
	}

	for deadline.IsZero() || time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return zero, ErrAuthCancelled
		}
		res, err := opts.Poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return zero, ErrAuthCancelled
			}
			return zero, err
		}
		switch res.Status {
		case DevicePollComplete:
			return res.Value, nil
		case DevicePollFailed:
			if res.Message != "" {
				return zero, errors.New(res.Message)
			}
			return zero, errors.New("device code authorization failed")
		case DevicePollSlowDown:
			slowDowns++
			if res.IntervalSeconds > 0 {
				interval = time.Duration(res.IntervalSeconds) * time.Second
			} else {
				interval += 5 * time.Second
			}
			if interval < time.Second {
				interval = time.Second
			}
		case DevicePollPending:
		}
		if err := sleepAbortable(interval); err != nil {
			if errors.Is(err, ErrAuthCancelled) {
				return zero, err
			}
			break
		}
	}
	if slowDowns > 0 {
		return zero, errors.New("device flow timed out after slow_down responses (check system/VM clock sync)")
	}
	return zero, errors.New("device flow timed out")
}

// ReadScannerLine reads a single line from a shared *bufio.Reader.
func ReadScannerLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func decodeJSONMap(data []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
