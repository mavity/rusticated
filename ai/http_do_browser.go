//go:build wasip1

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"ai/assets"
)

func doRemoteHTTPRequest(ctx context.Context, client *http.Client, request remoteRequest) (remoteResponse, error) {
	if err := ctx.Err(); err != nil {
		return remoteResponse{}, err
	}
	if assets.HostOS() != "browser" {
		return doStdHTTPRequest(ctx, client, request)
	}
	wire, err := json.Marshal(map[string]any{
		"method":  request.method,
		"url":     request.url,
		"headers": request.headers,
		"body":    string(request.body),
	})
	if err != nil {
		return remoteResponse{}, fmt.Errorf("encode browser HTTP request: %w", err)
	}
	script := "(async () => { const req = JSON.parse(" + strconv.Quote(string(wire)) + "); " +
		"const init = {method:req.method, headers:req.headers}; " +
		"if (req.method !== 'GET' && req.method !== 'HEAD') init.body = req.body; " +
		"const res = await fetch(req.url, init); const body = await res.text(); " +
		"if (new TextEncoder().encode(body).length > 1048576) throw new Error('remote AI response exceeded 1048576 bytes'); " +
		"return JSON.stringify({status:res.status, statusText:res.statusText, headers:Object.fromEntries(res.headers.entries()), body}); })()"
	raw, err := EvalJS(script)
	if err != nil {
		return remoteResponse{}, fmt.Errorf("browser fetch failed (network or CORS policy): %w", err)
	}
	var result struct {
		Status     int               `json:"status"`
		StatusText string            `json:"statusText"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return remoteResponse{}, fmt.Errorf("decode browser HTTP response: %w", err)
	}
	headers := make(http.Header, len(result.Headers))
	for name, value := range result.Headers {
		headers.Set(name, value)
	}
	return remoteResponse{
		StatusCode: result.Status,
		Status:     fmt.Sprintf("%d %s", result.Status, result.StatusText),
		Header:     headers,
		Body:       []byte(result.Body),
	}, nil
}

