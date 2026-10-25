package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var errUnsupportedRemoteAPI = errors.New("remote model does not support the requested API")

type remoteResponse struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
}

func remoteStatusError(status string, body []byte) error {
	message := fmt.Errorf("remote AI returned HTTP %s: %s", status, strings.TrimSpace(string(body)))
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error.Code == "unsupported_api_for_model" {
		return fmt.Errorf("%w: %w", errUnsupportedRemoteAPI, message)
	}
	return message
}

func remoteHTTPRequest(ctx context.Context, client *http.Client, request remoteRequest) (remoteResponse, error) {
	return doRemoteHTTPRequest(ctx, client, request)
}

func doStdHTTPRequest(ctx context.Context, client *http.Client, request remoteRequest) (remoteResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := newRemoteRequest(ctx, request)
	if err != nil {
		return remoteResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return remoteResponse{}, fmt.Errorf("remote AI request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := readRemoteResponse(resp.Body)
	if err != nil {
		return remoteResponse{}, fmt.Errorf("read remote AI response: %w", err)
	}
	return remoteResponse{StatusCode: resp.StatusCode, Status: resp.Status, Header: resp.Header.Clone(), Body: body}, nil
}

func readRemoteResponse(body io.Reader) ([]byte, error) {
	const maxResponseBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("remote AI response exceeded %d bytes", maxResponseBytes)
	}
	return data, nil
}

func newRemoteRequest(ctx context.Context, request remoteRequest) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, request.method, request.url, bytes.NewReader(request.body))
	if err != nil {
		return nil, fmt.Errorf("create remote AI request: %w", err)
	}
	for name, value := range request.headers {
		req.Header.Set(name, value)
	}
	return req, nil
}
