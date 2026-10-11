// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivers/videogen"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const (
	fakeGeminiKey      = "AIza-fake-key"
	fakeInteractionID  = "v1_omni_fake"
	fakeGeneratedFile  = "omniclip123"
	fakeOmniAPIModelID = "gemini-omni-1.1-flash"
)

// fakeGeminiAPI answers only the Interactions and Files endpoints, in the
// shapes ai.google.dev/gemini-api/docs/omni documents for them (the "REST"
// tabs, checked 2026-10-11). Anything else — Veo's predictLongRunning
// included — is an unexpected request.
type fakeGeminiAPI struct {
	*httptest.Server
	clip []byte
	// deliverURI makes the completed interaction name a Files API URI instead
	// of carrying the clip inline.
	deliverURI bool

	mu         sync.Mutex
	created    []map[string]any
	keys       []string
	gets       int
	unexpected []string
}

func newFakeGeminiAPI(t *testing.T, clip []byte, deliverURI bool) *fakeGeminiAPI {
	t.Helper()
	api := &fakeGeminiAPI{clip: clip, deliverURI: deliverURI}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1beta/interactions", api.createInteraction)
	mux.HandleFunc("GET /v1beta/interactions/{id}", api.getInteraction)
	mux.HandleFunc("GET /v1beta/files/{name}", api.file)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.unexpected = append(api.unexpected, r.Method+" "+r.URL.Path)
		api.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{
			"code": 404, "status": "NOT_FOUND", "message": "no such endpoint on the fake Gemini API: " + r.URL.Path,
		}})
	})
	api.Server = httptest.NewServer(mux)
	t.Cleanup(api.Close)
	return api
}

// fakeGeminiTraffic is what the fake saw, copied out under its lock: the
// handlers run on the server's goroutines.
type fakeGeminiTraffic struct {
	created    []map[string]any
	keys       []string
	unexpected []string
	gets       int
}

func (api *fakeGeminiAPI) traffic() fakeGeminiTraffic {
	api.mu.Lock()
	defer api.mu.Unlock()
	return fakeGeminiTraffic{
		created: slices.Clone(api.created), keys: slices.Clone(api.keys), unexpected: slices.Clone(api.unexpected), gets: api.gets,
	}
}

func (api *fakeGeminiAPI) recordKey(r *http.Request) {
	key := r.Header.Get("x-goog-api-key")
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	api.keys = append(api.keys, key)
}

func (api *fakeGeminiAPI) createInteraction(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": 400, "message": err.Error()}})
		return
	}
	api.mu.Lock()
	api.recordKey(r)
	api.created = append(api.created, body)
	api.mu.Unlock()
	// background=true: the id comes back at once, the render is polled.
	writeJSON(w, http.StatusOK, map[string]any{
		"id": fakeInteractionID, "status": "in_progress", "model": body["model"], "object": "interaction",
	})
}

func (api *fakeGeminiAPI) getInteraction(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != fakeInteractionID {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": 404, "message": "interaction not found"}})
		return
	}
	api.mu.Lock()
	api.recordKey(r)
	api.gets++
	gets := api.gets
	api.mu.Unlock()

	if gets == 1 {
		writeJSON(w, http.StatusOK, map[string]any{"id": fakeInteractionID, "status": "in_progress", "object": "interaction"})
		return
	}
	video := map[string]any{"type": "video", "mime_type": "video/mp4"}
	if api.deliverURI {
		video["uri"] = "http://" + r.Host + "/v1beta/files/" + fakeGeneratedFile + ":download?alt=media"
	} else {
		video["data"] = base64.StdEncoding.EncodeToString(api.clip)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"steps": []any{
			map[string]any{"type": "user_input", "content": []any{map[string]any{"type": "text", "text": "..."}}},
			map[string]any{"type": "thought", "content": []any{map[string]any{"type": "thought", "text": "..."}}},
			map[string]any{"type": "model_output", "content": []any{video}},
		},
		"id": fakeInteractionID, "status": "completed", "model": fakeOmniAPIModelID, "object": "interaction",
	})
}

// file serves files.get and files/{id}:download?alt=media.
func (api *fakeGeminiAPI) file(w http.ResponseWriter, r *http.Request) {
	api.mu.Lock()
	api.recordKey(r)
	api.mu.Unlock()
	name := r.PathValue("name")
	if name == fakeGeneratedFile+":download" && r.URL.Query().Get("alt") == "media" {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(api.clip)
		return
	}
	if name != fakeGeneratedFile {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": 404, "message": "file not found"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "files/" + fakeGeneratedFile, "mimeType": "video/mp4", "state": "ACTIVE",
		"uri":         "http://" + r.Host + "/v1beta/files/" + fakeGeneratedFile,
		"downloadUri": "http://" + r.Host + "/v1beta/files/" + fakeGeneratedFile + ":download?alt=media",
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// pinnedVideoClient builds the client a generate_video call pinned to selector
// gets for a user whose only provider is a Gemini API key: the same catalog
// resolution and the same config binding as ResolveVideoGenerator, pointed at
// baseURL instead of generativelanguage.googleapis.com.
func pinnedVideoClient(t *testing.T, selector, baseURL string) (VideoGenerator, *models.ResolvedModel) {
	t.Helper()
	resolved, err := models.MustGetRegistry().Resolve(
		models.ModelSelector{ID: selector, RequireOutputModality: models.ModalityVideo}, []string{"gemini"})
	require.NoError(t, err, "a chat pinned to %s must keep resolving", selector)
	client, err := newVideoGenClient(videoGenConfig(resolved, models.DriverConfig{
		DriverID: "gemini", Enabled: true, APIKey: fakeGeminiKey, BaseURL: baseURL,
	}))
	require.NoError(t, err)
	return client, resolved
}

// The Gemini API shuts the Veo 3.1 previews down on 2026-10-22 and names
// gemini-omni-1.1-flash as the replacement for all three. Omni is not a new
// model id on the same endpoint: Veo is models/{model}:predictLongRunning plus
// an operations poll, Omni is the Interactions API. So a chat, preset or
// workflow pinned to a Veo preview keeps generating video only if the
// retirement moves the id AND the move lands on the client that speaks
// Interactions. This drives that whole path — catalog, client dispatch and the
// SDK's HTTP — against a fake that serves only the documented Omni endpoints.
func TestRetiredVeoPreviewsGenerateThroughOmniInteractions(t *testing.T) {
	clip := append([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0xff, 0xfe}, make([]byte, 32)...)

	for _, veo := range veoModelIDs {
		t.Run(veo, func(t *testing.T) {
			api := newFakeGeminiAPI(t, clip, false)
			client, resolved := pinnedVideoClient(t, veo+"@gemini", api.URL)

			require.NotNil(t, resolved.Retirement, "%s@gemini must resolve AS a retirement, so the chat is told it moved", veo)
			assert.Equal(t, "2026-10-22", resolved.Retirement.RetiredOn)
			info := client.Info()
			assert.Equal(t, fakeOmniAPIModelID, info.ModelID)
			assert.Equal(t, fakeOmniAPIModelID, info.APIModel)
			assert.Equal(t, "gemini", info.Driver, "the move must not change whose key pays")

			ctx := context.Background()
			job, err := client.Submit(ctx, videogen.Request{
				Prompt: "a marble rolling down a track, single continuous shot", DurationSeconds: 8, Resolution: "720p", AspectRatio: "9:16",
			})
			require.NoError(t, err, "submit failed; requests the fake did not expect: %v", api.traffic().unexpected)
			assert.Equal(t, fakeInteractionID, job.ID)

			created := api.traffic().created
			require.Len(t, created, 1, "one POST /v1beta/interactions")
			body := created[0]
			assert.Equal(t, fakeOmniAPIModelID, body["model"])
			assert.Equal(t, "a marble rolling down a track, single continuous shot", body["input"], "text-to-video input is the bare prompt string")
			assert.Equal(t, true, body["background"], "create must return at once; the render is polled")
			assert.Equal(t, true, body["store"], "background execution requires store=true")
			format, ok := body["response_format"].(map[string]any)
			require.True(t, ok, "response_format must be an object: %v", body["response_format"])
			assert.Equal(t, "video", format["type"])
			assert.Equal(t, "9:16", format["aspect_ratio"])
			assert.Equal(t, "720p", format["resolution"])
			assert.Equal(t, "uri", format["delivery"])
			// The reference types duration only as "string"; this is Google's
			// JSON encoding of a Duration.
			assert.Equal(t, "8s", format["duration"])
			assert.NotContains(t, body, "negative_prompt", "Omni has no negative-prompt field")

			response, err := videogen.Wait(ctx, client, job, videogen.WaitOptions{Interval: time.Millisecond, Deadline: 10 * time.Second})
			require.NoError(t, err)
			assert.Equal(t, clip, response.Bytes, "the clip decoded from the documented steps[].content[] shape")
			assert.Equal(t, "video/mp4", response.MIMEType)
			assert.Equal(t, fakeOmniAPIModelID, response.ModelID)

			seen := api.traffic()
			assert.GreaterOrEqual(t, seen.gets, 2, "an in-progress interaction is polled until it completes")
			assert.Empty(t, seen.unexpected, "only the Interactions API may be called")
			for _, key := range seen.keys {
				assert.Equal(t, fakeGeminiKey, key, "every request carries the user's Gemini key")
			}
		})
	}
}

// URI delivery hands back a Files API URI; the client waits for the file to be
// ACTIVE and downloads it, as the guide's "Retrieving videos with an URI"
// section does.
func TestOmniInteractionsURIDeliveryDownloadsThroughTheFilesAPI(t *testing.T) {
	clip := []byte("omni-clip-bytes")
	api := newFakeGeminiAPI(t, clip, true)
	client, _ := pinnedVideoClient(t, fakeOmniAPIModelID+"@gemini", api.URL)

	ctx := context.Background()
	job, err := client.Submit(ctx, videogen.Request{Prompt: "a sunset"})
	require.NoError(t, err)
	response, err := videogen.Wait(ctx, client, job, videogen.WaitOptions{Interval: time.Millisecond, Deadline: 10 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, clip, response.Bytes)
	seen := api.traffic()
	assert.Empty(t, seen.unexpected)
	for _, key := range seen.keys {
		assert.Equal(t, fakeGeminiKey, key)
	}
}
