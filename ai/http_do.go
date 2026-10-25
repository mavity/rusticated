package ai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
)

type remoteResponse struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
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
	const maxResponseBytes = 1 << 20
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
