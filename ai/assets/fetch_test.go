package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (roundTripper testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return roundTripper(req)
}

func TestSelectWheelURLUsesHostCompatibleWheel(t *testing.T) {
	hostOS, hostArch := testRuntimeHost()
	want := testRuntimeWheelFilename()
	if want == "" {
		t.Skipf("no LiteRT-LM wheel is defined for %s/%s", hostOS, hostArch)
	}

	payload, err := json.Marshal(pypiResponse{
		Releases: map[string][]pypiArtifact{
			"1.0": {
				{Filename: want, URL: "https://example.test/" + want, Size: 123},
				{Filename: "litert_lm_api-1.0-cp311-cp311-any.whl", URL: "https://example.test/incompatible.whl", Size: 456},
			},
		},
	})
	if err != nil {
		t.Fatalf("encode PyPI fixture: %v", err)
	}
	const pypiURL = "https://pypi.example.test/project/json"
	client := &http.Client{
		Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet || req.URL.String() != pypiURL {
				t.Errorf("request = %s %s, want GET %s", req.Method, req.URL, pypiURL)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Request:    req,
			}, nil
		}),
	}

	url, filename, size, err := selectWheelURLWithClient(context.Background(), pypiURL, client)
	if err != nil {
		t.Fatalf("selectWheelURL() error = %v", err)
	}
	if filename != want {
		t.Fatalf("selected wheel = %q, want %q for %s/%s", filename, want, hostOS, hostArch)
	}
	if url != "https://example.test/"+want || size != 123 {
		t.Fatalf("selected artifact = (%q, %d), want (%q, 123)", url, size, "https://example.test/"+want)
	}
}
