// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testVideoID = "dmlkZW86dmVydGV4X2FpOnByb2plY3RzL3AvbG9jYXRpb25zL3VzLWNlbnRyYWwxL3B1Ymxpc2hlcnMvZ29vZ2xlL21vZGVscy92ZW8tMy4xLWxpdGUtZ2VuZXJhdGUtMDAxL29wZXJhdGlvbnMvYWJj"

func newManagedForTest(t *testing.T, handler http.HandlerFunc) *ManagedClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewManaged(Config{BaseURL: server.URL + "/v1", APIKey: "rlat_test", ModelID: "veo-3.1-lite-generate", APIModel: "veo-3.1-lite-generate-001", Driver: "reliant"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// The extra_body shape asserted here is LOAD-BEARING, and was verified against
// LiteLLM's real pipeline on 2026-10-05 (litellm/videos/utils.py
// get_optional_params_video_generation → VertexAIVideoConfig.
// transform_video_create_request). map_openai_params alone DROPS negativePrompt,
// generateAudio and the instance images, but the pipeline then merges
// extra_body over the mapped params: flat Veo keys land in `parameters` and the
// `instances` dict is merged into the Veo instance, so every field reaches
// Vertex. Moving these keys to the top level of the body, outside extra_body,
// would silently drop them.
func TestManagedSubmit_WireMapping(t *testing.T) {
	var got map[string]any
	var auth, path string
	client := newManagedForTest(t, func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.Method+" "+r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Write([]byte(`{"id":"` + testVideoID + `","object":"video","status":"processing","model":"veo-3.1-lite-generate-001"}`))
	})
	audio := false
	job, err := client.Submit(context.Background(), Request{
		Prompt: "a red ball", DurationSeconds: 6, Resolution: "1080p", AspectRatio: "9:16",
		NegativePrompt: "blur", Audio: &audio, StartFrame: &Image{Bytes: []byte("img"), MIMEType: "image/png"},
		References: []Image{{Bytes: []byte("ref"), MIMEType: "image/png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != testVideoID || job.Provider != "reliant" {
		t.Errorf("job = %+v", job)
	}
	if auth != "Bearer rlat_test" || path != "POST /v1/videos" {
		t.Errorf("auth=%q path=%q", auth, path)
	}
	if got["model"] != "veo-3.1-lite-generate-001" || got["prompt"] != "a red ball" || got["seconds"] != "6" || got["size"] != "1080x1920" {
		t.Errorf("body = %v", got)
	}
	extra := got["extra_body"].(map[string]any)
	if extra["negativePrompt"] != "blur" || extra["generateAudio"] != false || extra["aspectRatio"] != "9:16" || extra["resolution"] != "1080p" {
		t.Errorf("extra_body = %v", extra)
	}
	instance := extra["instances"].(map[string]any)
	if instance["image"].(map[string]any)["bytesBase64Encoded"] != "aW1n" || len(instance["referenceImages"].([]any)) != 1 {
		t.Errorf("instance = %v", instance)
	}
}

func TestManagedSize(t *testing.T) {
	for _, tc := range []struct{ res, aspect, want string }{
		{"720p", "16:9", "1280x720"}, {"720p", "9:16", "720x1280"}, {"1080p", "16:9", "1920x1080"},
		{"1080p", "9:16", "1080x1920"}, {"4k", "16:9", "3840x2160"}, {"", "", "1280x720"},
	} {
		if got := managedSize(tc.res, tc.aspect); got != tc.want {
			t.Errorf("managedSize(%q,%q) = %s, want %s", tc.res, tc.aspect, got, tc.want)
		}
	}
}

func TestManagedPoll_ResumesByLiteLLMID(t *testing.T) {
	var paths []string
	calls := 0
	client := newManagedForTest(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/content"):
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte("\x00\x00\x00\x18ftypmp42"))
		default:
			calls++
			status := "processing"
			if calls > 1 {
				status = "completed"
			}
			w.Write([]byte(`{"id":"` + testVideoID + `","status":"` + status + `"}`))
		}
	})
	job := Job{Provider: "reliant", ID: testVideoID}
	if resp, done, err := client.Poll(context.Background(), job); err != nil || done || resp != nil {
		t.Fatalf("first poll = %v %v %v", resp, done, err)
	}
	resp, done, err := client.Poll(context.Background(), job)
	if err != nil || !done || len(resp.Bytes) == 0 || resp.MIMEType != "video/mp4" || resp.Job.ID != testVideoID {
		t.Fatalf("second poll = %+v %v %v", resp, done, err)
	}
	want := []string{"/v1/videos/" + testVideoID, "/v1/videos/" + testVideoID, "/v1/videos/" + testVideoID + "/content"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestManagedPoll_FailedAndMissing(t *testing.T) {
	failing := newManagedForTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"x","status":"failed","error":{"message":"Unsupported output video duration 999 seconds"}}`))
	})
	_, _, err := failing.Poll(context.Background(), Job{ID: "x"})
	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != KindFailed || !strings.Contains(classified.Message, "Unsupported output video duration") {
		t.Errorf("failed poll err = %v", err)
	}
	missing := newManagedForTest(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	_, _, err = missing.Poll(context.Background(), Job{ID: "x"})
	if !errors.As(err, &classified) || classified.Kind != KindExpired {
		t.Errorf("404 poll err = %v", err)
	}
}

func TestManagedSubmit_OutOfCredit(t *testing.T) {
	attempts := 0
	client := newManagedForTest(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"You're out of Reliant credit. Add credit to your account to continue.","type":"insufficient_quota","code":"insufficient_quota","upgrade_url":"/billing/plans"}}`))
	})
	_, err := client.Submit(context.Background(), Request{Prompt: "x"})
	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != KindQuota || !strings.Contains(err.Error(), "out of Reliant credit") {
		t.Fatalf("err = %v", err)
	}
	if attempts != 1 {
		t.Errorf("an empty wallet was retried %d times", attempts)
	}
}
