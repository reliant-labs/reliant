//go:build live

// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// bearerTransport adds an OAuth bearer token minted OUTSIDE this process. The
// token comes from the environment and is never logged or written anywhere.
type bearerTransport struct {
	token   string
	project string
	base    http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	r.Header.Set("X-Goog-User-Project", b.project)
	return b.base.RoundTrip(r)
}

// TestLiveVertexVeoLite renders ONE clip: veo-3.1-lite, 4s, 720p, no audio,
// about $0.20. It exercises the real Submit -> Wait -> Poll path of VeoClient
// against Vertex. Run manually:
//
//	VIDEOGEN_LIVE_TOKEN=... VIDEOGEN_LIVE_PROJECT=... go test -tags live -run TestLiveVertexVeoLite ./internal/llm/drivers/videogen/
func TestLiveVertexVeoLite(t *testing.T) {
	token, project := os.Getenv("VIDEOGEN_LIVE_TOKEN"), os.Getenv("VIDEOGEN_LIVE_PROJECT")
	if token == "" || project == "" {
		t.Skip("set VIDEOGEN_LIVE_TOKEN and VIDEOGEN_LIVE_PROJECT to run (spends about $0.20)")
	}
	httpClient := &http.Client{Transport: bearerTransport{token: token, project: project, base: http.DefaultTransport}}
	factory := func(ctx context.Context, cc *genai.ClientConfig) (*genai.Client, error) {
		return genai.NewClient(ctx, cc)
	}

	client, err := NewVeo(Config{
		ModelID: "veo-3.1-lite-generate", APIModel: "veo-3.1-lite-generate-001", Driver: "vertex-live-test",
		HTTPClient: httpClient, Vertex: &VertexConfig{Project: project, Location: "us-central1"},
	}, factory)
	if err != nil {
		t.Fatalf("NewVeo: %v", err)
	}

	audio := false
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	started := time.Now()
	job, err := client.Submit(ctx, Request{
		Prompt:          "A red ball bouncing once on a wooden floor, single continuous shot.",
		DurationSeconds: 4, Resolution: "720p", AspectRatio: "16:9", Audio: &audio,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Logf("submitted job=%s", job.ID)

	response, err := Wait(ctx, client, job, WaitOptions{Interval: 5 * time.Second})
	if err != nil {
		t.Fatalf("Wait: %v (job %s)", err, job.ID)
	}
	t.Logf("done in %s: %d bytes, %s", time.Since(started).Round(time.Second), len(response.Bytes), response.MIMEType)

	if len(response.Bytes) < 100*1024 {
		t.Errorf("clip is only %d bytes", len(response.Bytes))
	}
	if !bytes.Contains(response.Bytes[:min(64, len(response.Bytes))], []byte("ftyp")) {
		t.Errorf("no ftyp box in the first bytes: %q", response.Bytes[:min(16, len(response.Bytes))])
	}
	if !strings.HasPrefix(response.MIMEType, "video/") {
		t.Errorf("mime = %q", response.MIMEType)
	}
	if out := os.Getenv("VIDEOGEN_LIVE_OUT"); out != "" {
		if err := os.WriteFile(out, response.Bytes, 0o644); err != nil {
			t.Errorf("write %s: %v", out, err)
		}
	}
}
