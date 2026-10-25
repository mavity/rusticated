//go:build !wasip1

package ai

import (
	"context"
	"net/http"
)

func doRemoteHTTPRequest(ctx context.Context, client *http.Client, request remoteRequest) (remoteResponse, error) {
	return doStdHTTPRequest(ctx, client, request)
}

