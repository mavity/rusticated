//go:build !wasip1

package ai

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func remoteStreamRequest(
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
