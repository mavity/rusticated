//go:build !wasip1

package ai

import (
	"context"
	"fmt"
	"net/http"
)

func doRemoteHTTPRequest(ctx context.Context, client *http.Client, request remoteRequest) (remoteResponse, error) {
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
