//go:build wasip1

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"ai/assets"
)

const browserRemoteHTTPBootstrap = `
(() => {
  if (globalThis.__kabibi_ai_http) return;
  const streams = new Map();
  let nextId = 1;
  const push = (stream, item) => {
    const waiter = stream.waiters.shift();
    if (waiter) waiter(item);
    else stream.queue.push(item);
  };
  const parseSSE = (stream, line) => {
    if (line === '') {
      if (stream.data.length) {
        push(stream, {type: 'event', event: stream.event, data: stream.data.join('\n')});
      }
      stream.event = '';
      stream.data = [];
      return;
    }
    if (line.startsWith('event:')) stream.event = line.slice(6).trim();
    else if (line.startsWith('data:')) stream.data.push(line.slice(5).replace(/^ /, ''));
  };
  globalThis.__kabibi_ai_http = {
    start: (request) => {
      const id = nextId++;
      const stream = {queue: [], waiters: [], event: '', data: [], done: false, error: '', abort: new AbortController()};
      streams.set(id, stream);
      (async () => {
        try {
          const response = await fetch(request.url, {
            method: request.method,
            headers: request.headers,
            body: request.body,
            signal: stream.abort.signal
          });
          if (!response.ok) {
            const text = await response.text();
            stream.error = 'HTTP ' + response.status + ' ' + response.statusText + ': ' + text;
            return;
          }
          if (!response.body) throw new Error('response has no readable stream');
          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          let pending = '';
          while (true) {
            const {value, done} = await reader.read();
            if (done) break;
            pending += decoder.decode(value, {stream: true});
            const lines = pending.split(/\r?\n/);
            pending = lines.pop() || '';
            for (const line of lines) parseSSE(stream, line);
          }
          pending += decoder.decode();
          if (pending) parseSSE(stream, pending);
          parseSSE(stream, '');
        } catch (error) {
          if (!stream.abort.signal.aborted) {
            const message = String(error && error.message || error);
            stream.error = message.includes('fetch')
              ? 'browser fetch failed (network or CORS policy): ' + message
              : message;
          }
        } finally {
          stream.done = true;
          if (stream.error) push(stream, {type: 'error', error: stream.error});
          push(stream, {type: 'done'});
        }
      })();
      return id;
    },
    next: (id) => new Promise((resolve) => {
      const stream = streams.get(Number(id));
      if (!stream) return resolve({type: 'error', error: 'unknown stream'});
      if (stream.queue.length) return resolve(stream.queue.shift());
      if (stream.done) return resolve({type: 'done'});
      stream.waiters.push(resolve);
    }),
    cancel: (id) => {
      const stream = streams.get(Number(id));
      if (stream) stream.abort.abort();
      return '';
    },
    delete: (id) => { streams.delete(Number(id)); return ''; }
  };
})()
`

func remoteStreamRequest(
	ctx context.Context,
	client *http.Client,
	request remoteRequest,
	onEvent func(event, data string) error,
) error {
	if assets.HostOS() != "browser" {
		return stdRemoteStreamRequest(ctx, client, request, onEvent)
	}
	if _, err := EvalJS(browserRemoteHTTPBootstrap); err != nil {
		return fmt.Errorf("initialize browser AI HTTP transport: %w", err)
	}
	wire, err := json.Marshal(map[string]any{
		"method":  request.method,
		"url":     request.url,
		"headers": request.headers,
		"body":    string(request.body),
	})
	if err != nil {
		return fmt.Errorf("encode browser AI request: %w", err)
	}
	startResult, err := EvalJS("globalThis.__kabibi_ai_http.start(JSON.parse(" + strconv.Quote(string(wire)) + "))")
	if err != nil {
		return fmt.Errorf("start browser AI request: %w", err)
	}
	streamID := strings.TrimSpace(startResult)
	if streamID == "" {
		return fmt.Errorf("browser AI transport returned an empty stream ID")
	}
	defer func() {
		_, _ = EvalJS("globalThis.__kabibi_ai_http.delete(" + strconv.Quote(streamID) + ")")
	}()
	cancelled := make(chan struct{})
	defer close(cancelled)
	go func() {
		select {
		case <-ctx.Done():
			_, _ = EvalJS("globalThis.__kabibi_ai_http.cancel(" + strconv.Quote(streamID) + ")")
		case <-cancelled:
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		raw, err := EvalJS("(async () => JSON.stringify(await globalThis.__kabibi_ai_http.next(" + strconv.Quote(streamID) + ")))()")
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read browser AI stream: %w", err)
		}
		var item struct {
			Type  string `json:"type"`
			Event string `json:"event"`
			Data  string `json:"data"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return fmt.Errorf("decode browser AI stream event: %w", err)
		}
		switch item.Type {
		case "event":
			if err := onEvent(item.Event, item.Data); err != nil {
				return err
			}
		case "error":
			return fmt.Errorf("browser AI request failed: %s", item.Error)
		case "done":
			return nil
		default:
			return fmt.Errorf("browser AI transport returned unknown event type %q", item.Type)
		}
	}
}

func stdRemoteStreamRequest(
	ctx context.Context,
	client *http.Client,
	request remoteRequest,
	onEvent func(event, data string) error,
) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, request.method, request.url, bytes.NewReader(request.body))
	if err != nil {
		return fmt.Errorf("create remote AI request: %w", err)
	}
	for name, value := range request.headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("remote AI request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if readErr != nil {
			return fmt.Errorf("remote AI returned HTTP %s (read error body: %w)", resp.Status, readErr)
		}
		return remoteStatusError(resp.Status, body)
	}
	if mediaType := resp.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(mediaType), "text/event-stream") {
		return fmt.Errorf("remote AI returned unexpected content type %q", mediaType)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	var event string
	var data strings.Builder
	dispatch := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if err := onEvent(event, payload); err != nil {
			return err
		}
		event = ""
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(line, "data:"))
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read remote AI event stream: %w", err)
	}
	return dispatch()
}
