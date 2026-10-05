// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/apierrors"
	gaos "google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
)

type fakeInteractions struct {
	created   []operations.CreateInteractionRequest
	createErr []error
	createRes *gaos.Interaction
	getRes    []*gaos.Interaction
	getErr    error
	gets      int
	cancelled []string
}

func (f *fakeInteractions) Create(_ context.Context, r operations.CreateInteractionRequest, _ ...operations.Option) (*operations.CreateInteractionResponse, error) {
	f.created = append(f.created, r)
	if n := len(f.created); n <= len(f.createErr) && f.createErr[n-1] != nil {
		return nil, f.createErr[n-1]
	}
	return &operations.CreateInteractionResponse{Interaction: f.createRes}, nil
}

func (f *fakeInteractions) Get(_ context.Context, _ operations.GetInteractionByIDRequest, _ ...operations.Option) (*operations.GetInteractionByIDResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	r := f.getRes[min(f.gets, len(f.getRes)-1)]
	f.gets++
	return &operations.GetInteractionByIDResponse{Interaction: r}, nil
}

func (f *fakeInteractions) Cancel(_ context.Context, r operations.CancelInteractionByIDRequest, _ ...operations.Option) (*operations.CancelInteractionByIDResponse, error) {
	f.cancelled = append(f.cancelled, r.ID)
	return &operations.CancelInteractionByIDResponse{}, nil
}

type fakeOmniFiles struct {
	state genai.FileState
	data  []byte
}

func (f *fakeOmniFiles) Get(context.Context, string, *genai.GetFileConfig) (*genai.File, error) {
	return &genai.File{State: f.state}, nil
}

func (f *fakeOmniFiles) Download(context.Context, genai.DownloadURI, *genai.DownloadFileConfig) ([]byte, error) {
	return f.data, nil
}

func omniConfig() Config {
	return Config{ModelID: "gemini-omni-1.1-flash", APIModel: "gemini-omni-1.1-flash", Driver: "gemini"}
}

func strp(s string) *string { return &s }

// bodyJSON renders the request exactly as it goes on the wire.
func bodyJSON(t *testing.T, r operations.CreateInteractionRequest) map[string]any {
	t.Helper()
	raw, err := json.Marshal(r.Body)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestOmniSubmit_WireShapeForTextToVideo(t *testing.T) {
	f := &fakeInteractions{createRes: &gaos.Interaction{ID: strp("v1_abc")}}
	c := newOmniWithFakes(omniConfig(), f, nil)
	job, err := c.Submit(context.Background(), Request{Prompt: "a marble rolling, single continuous shot", DurationSeconds: 5, Resolution: "720p", AspectRatio: "9:16"})
	require.NoError(t, err)
	assert.Equal(t, Job{Provider: "omni", ID: "v1_abc"}, job)

	body := bodyJSON(t, f.created[0])
	assert.Equal(t, "gemini-omni-1.1-flash", body["model"])
	assert.Equal(t, "a marble rolling, single continuous shot", body["input"])
	assert.Equal(t, true, body["background"], "create returns the id at once; the render is polled")
	assert.Equal(t, true, body["store"], "store=true is what makes edit and background work")
	format := body["response_format"].(map[string]any)
	assert.Equal(t, "video", format["type"])
	assert.Equal(t, "uri", format["delivery"])
	assert.Equal(t, "720p", format["resolution"])
	assert.Equal(t, "9:16", format["aspect_ratio"])
	assert.Equal(t, "5s", format["duration"])
	assert.NotContains(t, body, "previous_interaction_id")
	assert.NotContains(t, body, "generation_config", "text-to-video leaves the task to the model")
}

func TestOmniSubmit_EditFromBecomesPreviousInteractionIDAndResendsFormat(t *testing.T) {
	f := &fakeInteractions{createRes: &gaos.Interaction{ID: strp("v1_second")}}
	_, err := newOmniWithFakes(omniConfig(), f, nil).Submit(context.Background(), Request{Prompt: "make it slower", EditOf: "v1_first", Resolution: "720p"})
	require.NoError(t, err)

	body := bodyJSON(t, f.created[0])
	assert.Equal(t, "v1_first", body["previous_interaction_id"])
	assert.Equal(t, "make it slower", body["input"], "an edit sends only the change")
	format, ok := body["response_format"].(map[string]any)
	require.True(t, ok, "response_format is NOT inherited across turns and must be re-sent")
	assert.Equal(t, "video", format["type"])
}

func TestOmniSubmit_ImagesBecomeContentAndSetTheTask(t *testing.T) {
	f := &fakeInteractions{createRes: &gaos.Interaction{ID: strp("v1_img")}}
	c := newOmniWithFakes(omniConfig(), f, nil)
	png := Image{Bytes: []byte{1, 2, 3}, MIMEType: "image/png"}
	_, err := c.Submit(context.Background(), Request{Prompt: "animate", StartFrame: &png})
	require.NoError(t, err)
	body := bodyJSON(t, f.created[0])
	input := body["input"].([]any)
	require.Len(t, input, 2)
	first := input[0].(map[string]any)
	assert.Equal(t, "image", first["type"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte{1, 2, 3}), first["data"])
	assert.Equal(t, "image/png", first["mime_type"])
	assert.Equal(t, "text", input[1].(map[string]any)["type"])
	assert.Equal(t, "image_to_video", body["generation_config"].(map[string]any)["video_config"].(map[string]any)["task"])

	_, err = c.Submit(context.Background(), Request{Prompt: "in this style", References: []Image{png}})
	require.NoError(t, err)
	assert.Equal(t, "reference_to_video", bodyJSON(t, f.created[1])["generation_config"].(map[string]any)["video_config"].(map[string]any)["task"])
}

func TestOmniSubmit_NegativePromptAndSilentAreFoldedIntoThePrompt(t *testing.T) {
	f := &fakeInteractions{createRes: &gaos.Interaction{ID: strp("v1_x")}}
	silent := false
	_, err := newOmniWithFakes(omniConfig(), f, nil).Submit(context.Background(), Request{Prompt: "a cat", NegativePrompt: "text overlays", Audio: &silent})
	require.NoError(t, err)
	prompt := bodyJSON(t, f.created[0])["input"].(string)
	assert.Contains(t, prompt, "Do not: text overlays")
	assert.Contains(t, prompt, "No audio")
}

func TestOmniSubmit_RetriesBeforeAcceptanceOnly(t *testing.T) {
	f := &fakeInteractions{
		createErr: []error{&apierrors.APIError{StatusCode: 503, Message: "busy"}},
		createRes: &gaos.Interaction{ID: strp("v1_ok")},
	}
	job, err := newOmniWithFakes(Config{ModelID: "m", APIModel: "m", RetryBaseDelay: 1}, f, nil).Submit(context.Background(), Request{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "v1_ok", job.ID)
	assert.Len(t, f.created, 2)

	q := &fakeInteractions{createErr: []error{&apierrors.APIError{StatusCode: 429, Message: "quota"}, &apierrors.APIError{StatusCode: 429, Message: "quota"}, &apierrors.APIError{StatusCode: 429, Message: "quota"}}}
	_, err = newOmniWithFakes(Config{ModelID: "m", APIModel: "m", RetryBaseDelay: 1}, q, nil).Submit(context.Background(), Request{Prompt: "x"})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindQuota, e.Kind)
	assert.Len(t, q.created, 3)
}

func completed(content gaos.Content) *gaos.Interaction {
	return &gaos.Interaction{ID: strp("v1_abc"), Status: gaos.InteractionStatusCompleted,
		Steps: []gaos.Step{{ModelOutputStep: &gaos.ModelOutputStep{Content: []gaos.Content{content}}}}}
}

func TestOmniPoll_InProgressThenInlineData(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("mp4-bytes"))
	mime := gaos.VideoContentMimeTypeVideoMp4
	f := &fakeInteractions{getRes: []*gaos.Interaction{
		{Status: gaos.InteractionStatusInProgress},
		completed(gaos.NewContent(gaos.VideoContent{Data: &data, MimeType: &mime})),
	}}
	c := newOmniWithFakes(omniConfig(), f, nil)
	resp, done, err := c.Poll(context.Background(), Job{ID: "v1_abc"})
	require.NoError(t, err)
	assert.False(t, done)
	assert.Nil(t, resp)
	resp, done, err = c.Poll(context.Background(), Job{ID: "v1_abc"})
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, []byte("mp4-bytes"), resp.Bytes)
	assert.Equal(t, "video/mp4", resp.MIMEType)
	assert.Equal(t, "v1_abc", resp.Job.ID, "the interaction id is what a later edit refers to")
}

func TestOmniPoll_URIWaitsForActiveThenDownloads(t *testing.T) {
	uri := "https://generativelanguage.googleapis.com/v1beta/files/abc-123:download?alt=media"
	f := &fakeInteractions{getRes: []*gaos.Interaction{completed(gaos.NewContent(gaos.VideoContent{URI: &uri}))}}
	files := &fakeOmniFiles{state: genai.FileStateProcessing, data: []byte("clip")}
	c := newOmniWithFakes(omniConfig(), f, files)

	_, done, err := c.Poll(context.Background(), Job{ID: "v1"})
	require.NoError(t, err)
	assert.False(t, done, "a file still PROCESSING is not ready")

	files.state = genai.FileStateActive
	resp, done, err := c.Poll(context.Background(), Job{ID: "v1"})
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, []byte("clip"), resp.Bytes)

	files.state = genai.FileStateFailed
	_, _, err = c.Poll(context.Background(), Job{ID: "v1"})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindFailed, e.Kind)
}

func TestOmniPoll_FailureFilteredAndExpired(t *testing.T) {
	failed := &fakeInteractions{getRes: []*gaos.Interaction{{Status: gaos.InteractionStatusFailed, Errors: []gaos.Error{{Message: strp("internal")}}}}}
	_, _, err := newOmniWithFakes(omniConfig(), failed, nil).Poll(context.Background(), Job{ID: "v1"})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindFailed, e.Kind)
	assert.Contains(t, e.Message, "internal")

	// A completed interaction with no video is how a filter shows up.
	text := completed(gaos.NewContent(gaos.TextContent{Text: "I can't create that."}))
	text.Steps[0].ModelOutputStep.Error = &gaos.Status{Message: strp("blocked by safety policy")}
	filtered := &fakeInteractions{getRes: []*gaos.Interaction{text}}
	_, _, err = newOmniWithFakes(omniConfig(), filtered, nil).Poll(context.Background(), Job{ID: "v1"})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindFiltered, e.Kind)
	assert.Contains(t, e.Message, "blocked by safety policy", "a filter must say why")

	gone := &fakeInteractions{getErr: &apierrors.APIError{StatusCode: http.StatusNotFound, Message: "nope"}}
	_, _, err = newOmniWithFakes(omniConfig(), gone, nil).Poll(context.Background(), Job{ID: "v1"})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindExpired, e.Kind)
}

func TestOmniCancelAndVertexRefused(t *testing.T) {
	f := &fakeInteractions{}
	require.NoError(t, newOmniWithFakes(omniConfig(), f, nil).Cancel(context.Background(), Job{ID: "v1_abc"}))
	assert.Equal(t, []string{"v1_abc"}, f.cancelled)

	_, err := NewOmni(Config{Vertex: &VertexConfig{Project: "p", Location: "l"}}, nil)
	require.Error(t, err)
}
