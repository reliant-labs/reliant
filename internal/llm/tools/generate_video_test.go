// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/llm/drivers/videogen"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/videojobs"
)

// mp4Bytes starts with a valid ftyp box and has non-UTF-8 bytes, so a path that
// carries it through a JSON string corrupts it.
var mp4Bytes = append([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0xff, 0xfe}, make([]byte, 64)...)

type fakeVideoJobs struct {
	mu   sync.Mutex
	jobs map[string]*videojobs.Job
}

func newFakeVideoJobs() *fakeVideoJobs { return &fakeVideoJobs{jobs: map[string]*videojobs.Job{}} }

func (f *fakeVideoJobs) GetByToolCall(_ context.Context, id string) (*videojobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j := f.jobs[id]; j != nil {
		c := *j
		return &c, nil
	}
	return nil, nil
}

func (f *fakeVideoJobs) GetByAttachment(_ context.Context, id string) (*videojobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.AttachmentID == id {
			c := *j
			return &c, nil
		}
	}
	return nil, nil
}

func (f *fakeVideoJobs) Create(_ context.Context, j *videojobs.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *j
	f.jobs[j.ToolCallID] = &c
	return nil
}

func (f *fakeVideoJobs) Complete(_ context.Context, id, attachmentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[id].State, f.jobs[id].AttachmentID = videojobs.StateCompleted, attachmentID
	return nil
}

func (f *fakeVideoJobs) Finish(_ context.Context, id string, state videojobs.State, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[id].State, f.jobs[id].ErrorMessage = state, msg
	return nil
}

// fakeVideoGenerator counts Submit and Poll and can watch the job store at the
// moment of the first Poll.
type fakeVideoGenerator struct {
	caps        *models.VideoCapabilities
	submits     int
	polls       int
	cancels     int
	lastReq     videogen.Request
	pollsUntil  int // Poll reports not-done until this many calls
	pollErr     error
	onFirstPoll func()
	modelID     string // overrides the default veo-3.1-generate
}

func (g *fakeVideoGenerator) Info() videogen.ModelInfo {
	id := "veo-3.1-generate"
	if g.modelID != "" {
		id = g.modelID
	}
	return videogen.ModelInfo{ModelID: id, APIModel: "veo-3.1-generate-preview", Driver: "gemini", Capabilities: g.caps}
}

func (g *fakeVideoGenerator) Submit(_ context.Context, r videogen.Request) (videogen.Job, error) {
	g.submits++
	g.lastReq = r
	return videogen.Job{Provider: "veo", ID: "operations/new-1"}, nil
}

func (g *fakeVideoGenerator) Poll(_ context.Context, job videogen.Job) (*videogen.Response, bool, error) {
	g.polls++
	if g.polls == 1 && g.onFirstPoll != nil {
		g.onFirstPoll()
	}
	if g.pollErr != nil {
		return nil, false, g.pollErr
	}
	if g.polls < g.pollsUntil {
		return nil, false, nil
	}
	return &videogen.Response{Bytes: mp4Bytes, MIMEType: "video/mp4", ModelID: "veo-3.1-generate", Driver: "gemini", Job: job}, true, nil
}

func (g *fakeVideoGenerator) Cancel(context.Context, videogen.Job) error { g.cancels++; return nil }

func veoCaps() *models.VideoCapabilities {
	return &models.VideoCapabilities{
		Durations: []int{4, 6, 8}, Resolutions: []string{"720p", "1080p"}, Aspects: []string{"16:9", "9:16"},
		MaxReferenceImages: 3, SupportsNegativePrompt: true, FullResolutionDuration: 8, HighResolutions: []string{"1080p"},
	}
}

func videoResolver(g VideoGenerator) VideoGeneratorResolver {
	return func(context.Context, string, models.ModelSelector) (VideoGenerator, error) { return g, nil }
}

func newVideoTool(repo *fakeAttachmentRepo, jobs VideoJobStore, g VideoGenerator) *generateVideoTool {
	return &generateVideoTool{repo: repo, jobs: jobs, resolve: videoResolver(g), wait: videogen.WaitOptions{Interval: time.Millisecond, Deadline: 5 * time.Second}}
}

func videoCtx(t *testing.T, toolCallID string) *rctx.ToolContext {
	t.Helper()
	tc := createTestContext(t, "chat-1")
	tc.Context = context.WithValue(tc.Context, ctxkeys.ToolCallContextKey, &ctxkeys.ToolCallContext{CurrentToolCallID: toolCallID})
	return tc
}

// TestGenerateVideo_ResumesPersistedJobInsteadOfSubmitting is the contract that
// stops a worker restart from billing a second clip. The re-dispatched activity
// carries the same tool call id; a job record already exists; Execute must
// Poll that job and never Submit.
func TestGenerateVideo_ResumesPersistedJobInsteadOfSubmitting(t *testing.T) {
	repo := newFakeAttachmentRepo()
	jobs := newFakeVideoJobs()
	require.NoError(t, jobs.Create(context.Background(), &videojobs.Job{
		ToolCallID: "tc-1", UserID: "test-user", Driver: "gemini", ModelID: "veo-3.1-generate",
		APIModel: "veo-3.1-generate-preview", ProviderJob: "operations/already-running", State: videojobs.StateSubmitted,
	}))
	generator := &fakeVideoGenerator{caps: veoCaps()}
	tool := newVideoTool(repo, jobs, generator)

	resp, err := tool.Execute(videoCtx(t, "tc-1"), GenerateVideoParams{Prompt: "a red ball bouncing, single continuous shot"})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error: %s", resp.Content)

	assert.Equal(t, 0, generator.submits, "a re-dispatched call must NOT submit a second, billed render")
	assert.GreaterOrEqual(t, generator.polls, 1, "it must poll the job that is already running")

	var output GenerateVideoOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	assert.Equal(t, "operations/already-running", output.ProviderJob)
	stored := jobs.jobs["tc-1"]
	assert.Equal(t, videojobs.StateCompleted, stored.State)
	assert.Equal(t, output.AttachmentID, stored.AttachmentID)
}

// A finished job re-dispatched (the worker died after storing but before the
// result was recorded) returns the stored clip without any provider call.
func TestGenerateVideo_ReturnsAlreadyCompletedJobWithoutProviderCalls(t *testing.T) {
	repo := newFakeAttachmentRepo()
	jobs := newFakeVideoJobs()
	generator := &fakeVideoGenerator{caps: veoCaps()}
	tool := newVideoTool(repo, jobs, generator)

	first, err := tool.Execute(videoCtx(t, "tc-2"), GenerateVideoParams{Prompt: "a red ball"})
	require.NoError(t, err)
	require.False(t, first.IsError, first.Content)
	submits, polls := generator.submits, generator.polls

	second, err := tool.Execute(videoCtx(t, "tc-2"), GenerateVideoParams{Prompt: "a red ball"})
	require.NoError(t, err)
	require.False(t, second.IsError, second.Content)
	assert.Equal(t, submits, generator.submits)
	assert.Equal(t, polls, generator.polls, "a completed job needs no provider call")

	var a, b GenerateVideoOutput
	require.NoError(t, json.Unmarshal([]byte(first.Metadata), &a))
	require.NoError(t, json.Unmarshal([]byte(second.Metadata), &b))
	assert.Equal(t, a.AttachmentID, b.AttachmentID, "the same clip, not a second attachment")
	assert.Len(t, repo.stored, 1)
}

// The job record must exist BEFORE the first poll, or a crash between Submit
// and the first poll loses the provider job id.
func TestGenerateVideo_WritesJobRecordBeforePolling(t *testing.T) {
	repo := newFakeAttachmentRepo()
	jobs := newFakeVideoJobs()
	generator := &fakeVideoGenerator{caps: veoCaps()}
	var recordAtFirstPoll *videojobs.Job
	generator.onFirstPoll = func() { recordAtFirstPoll, _ = jobs.GetByToolCall(context.Background(), "tc-3") }

	_, err := newVideoTool(repo, jobs, generator).Execute(videoCtx(t, "tc-3"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	require.NotNil(t, recordAtFirstPoll, "no job record existed when polling began")
	assert.Equal(t, "operations/new-1", recordAtFirstPoll.ProviderJob)
	assert.Equal(t, videojobs.StateSubmitted, recordAtFirstPoll.State)
}

// Bytes belong in the attachments table only: never in the tool result, its
// metadata (Temporal history) or logs.
func TestGenerateVideo_BytesNeverLeaveTheAttachmentRow(t *testing.T) {
	repo := newFakeAttachmentRepo()
	jobs := newFakeVideoJobs()
	generator := &fakeVideoGenerator{caps: veoCaps()}

	resp, err := newVideoTool(repo, jobs, generator).Execute(videoCtx(t, "tc-4"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)

	assert.Empty(t, resp.BinaryParts, "the model gets no video bytes")
	assert.Equal(t, ToolResponseTypeText, resp.Type)
	marker := string(mp4Bytes[:12])
	assert.NotContains(t, resp.Content, marker)
	assert.NotContains(t, resp.Metadata, marker)
	raw, _ := json.Marshal(resp)
	assert.NotContains(t, string(raw), "ftyp", "the serialized result (what Temporal records) must carry no clip bytes")
	assert.Less(t, len(raw), 4096, "result stays small regardless of clip size")

	var output GenerateVideoOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	att := repo.stored[output.AttachmentID]
	require.NotNil(t, att)
	assert.Equal(t, mp4Bytes, att.Content)
	assert.Equal(t, string(attachment.TypeVideo), att.AttachmentType)
	assert.Equal(t, "video/mp4", att.MimeType)
	assert.Equal(t, attachment.TypeVideo, attachment.GetAttachmentType(att.Filename))
	assert.Contains(t, resp.Content, "You cannot see this video")
	assert.Contains(t, resp.Content, output.AttachmentID)
}

func TestGenerateVideo_ValidatesAgainstDeclaredCapabilities(t *testing.T) {
	repo := newFakeAttachmentRepo()
	generator := &fakeVideoGenerator{caps: veoCaps()}
	tool := newVideoTool(repo, newFakeVideoJobs(), generator)

	resp, err := tool.Execute(videoCtx(t, "tc-5"), GenerateVideoParams{Prompt: "x", DurationSeconds: 4, Resolution: "1080p"})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "requires duration_seconds=8")
	assert.Equal(t, 0, generator.submits, "an invalid request must never reach the provider")

	resp, _ = tool.Execute(videoCtx(t, "tc-5"), GenerateVideoParams{Prompt: "x", EditFrom: "nope"})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "Edit handle unavailable")
	assert.Equal(t, 0, generator.submits)

	resp, _ = tool.Execute(videoCtx(t, "tc-5"), GenerateVideoParams{Prompt: "  "})
	assert.Contains(t, resp.Content, "prompt is required")
}

func TestGenerateVideo_DefaultsTo720p(t *testing.T) {
	generator := &fakeVideoGenerator{caps: veoCaps()}
	tool := newVideoTool(newFakeAttachmentRepo(), newFakeVideoJobs(), generator)
	_, err := tool.Execute(videoCtx(t, "tc-6"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "720p", generator.lastReq.Resolution, "cost rises with resolution; the default stays low")
}

func TestGenerateVideo_FailureMarksJobAndExplains(t *testing.T) {
	jobs := newFakeVideoJobs()
	generator := &fakeVideoGenerator{caps: veoCaps(), pollErr: &videogen.Error{Kind: videogen.KindFiltered, Message: "the video was withheld by a content filter"}}
	resp, err := newVideoTool(newFakeAttachmentRepo(), jobs, generator).Execute(videoCtx(t, "tc-7"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "content filter")
	assert.Equal(t, videojobs.StateFailed, jobs.jobs["tc-7"].State)
}

func TestGenerateVideo_CancelStopsPollingAndCancelsProvider(t *testing.T) {
	jobs := newFakeVideoJobs()
	generator := &fakeVideoGenerator{caps: veoCaps(), pollsUntil: 1 << 30}
	tool := newVideoTool(newFakeAttachmentRepo(), jobs, generator)

	tc := videoCtx(t, "tc-8")
	ctx, cancel := context.WithCancel(tc.Context)
	tc.Context = ctx
	generator.onFirstPoll = cancel

	resp, err := tool.Execute(tc, GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, strings.ToLower(resp.Content), "cancelled")
	assert.Equal(t, 1, generator.cancels)
	assert.Equal(t, videojobs.StateCancelled, jobs.jobs["tc-8"].State, "a cancelled job must not be resumed by the next turn")
}

func TestGenerateVideo_UnavailableWithoutResolver(t *testing.T) {
	tool := &generateVideoTool{repo: newFakeAttachmentRepo(), jobs: newFakeVideoJobs()}
	resp, err := tool.Execute(videoCtx(t, "tc-9"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "not available in this context")
}

func omniCaps() *models.VideoCapabilities {
	return &models.VideoCapabilities{
		Durations: []int{3, 4, 5, 6, 7, 8, 9, 10}, Resolutions: []string{"360p", "720p", "1080p", "4k"},
		Aspects: []string{"16:9", "9:16"}, MaxReferenceImages: 3, SupportsEdit: true,
	}
}

// editHandleFixture runs a first generation on an edit-capable model and returns
// the edit handle (the clip's attachment id).
func editHandleFixture(t *testing.T) (*fakeAttachmentRepo, *fakeVideoJobs, string) {
	t.Helper()
	repo := newFakeAttachmentRepo()
	jobs := newFakeVideoJobs()
	first := &fakeVideoGenerator{caps: omniCaps()}
	resp, err := newVideoTool(repo, jobs, first).Execute(videoCtx(t, "tc-first"), GenerateVideoParams{Prompt: "a marble rolling"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	var out GenerateVideoOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &out))
	require.Equal(t, out.AttachmentID, out.EditHandle, "an edit-capable model hands back the attachment id as the edit handle")
	assert.Contains(t, resp.Content, "Edit handle: "+out.AttachmentID)
	return repo, jobs, out.EditHandle
}

func TestGenerateVideo_EditFromMapsToThePriorProviderJobAndPinsTheModel(t *testing.T) {
	repo, jobs, handle := editHandleFixture(t)

	var gotSelector models.ModelSelector
	second := &fakeVideoGenerator{caps: omniCaps()}
	tool := &generateVideoTool{repo: repo, jobs: jobs, wait: videogen.WaitOptions{Interval: time.Millisecond, Deadline: 5 * time.Second},
		resolve: func(_ context.Context, _ string, sel models.ModelSelector) (VideoGenerator, error) {
			gotSelector = sel
			return second, nil
		}}

	resp, err := tool.Execute(videoCtx(t, "tc-edit"), GenerateVideoParams{Prompt: "make it slower", EditFrom: handle})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)

	assert.Equal(t, "operations/new-1", second.lastReq.EditOf, "edit_from must become the PROVIDER job of the clip it names, never the attachment id")
	assert.Equal(t, "veo-3.1-generate", gotSelector.ID, "an edit stays on the model that made the clip")
	assert.Equal(t, []string{"gemini"}, gotSelector.Providers)
}

func TestGenerateVideo_EditHandleIsOwnedAndCompletedOnly(t *testing.T) {
	repo, jobs, handle := editHandleFixture(t)
	generator := &fakeVideoGenerator{caps: omniCaps()}
	tool := newVideoTool(repo, jobs, generator)

	// A handle belonging to another user looks absent, never forbidden.
	for _, j := range jobs.jobs {
		j.UserID = "someone-else"
	}
	resp, _ := tool.Execute(videoCtx(t, "tc-foreign"), GenerateVideoParams{Prompt: "x", EditFrom: handle})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "Edit handle unavailable")
	assert.Equal(t, 0, generator.submits)
}

func TestGenerateVideo_EditOnAModelWithoutEditIsRejectedBeforeAnyProviderCall(t *testing.T) {
	repo, jobs, handle := editHandleFixture(t)
	veo := &fakeVideoGenerator{caps: veoCaps()} // no SupportsEdit
	resp, err := newVideoTool(repo, jobs, veo).Execute(videoCtx(t, "tc-veo-edit"), GenerateVideoParams{Prompt: "x", EditFrom: handle})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "cannot edit a previous video")
	assert.Equal(t, 0, veo.submits)
}

func TestGenerateVideo_NoEditHandleWhenTheModelCannotEdit(t *testing.T) {
	resp, err := newVideoTool(newFakeAttachmentRepo(), newFakeVideoJobs(), &fakeVideoGenerator{caps: veoCaps()}).
		Execute(videoCtx(t, "tc-noedit"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	assert.NotContains(t, resp.Content, "Edit handle")
	var out GenerateVideoOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &out))
	assert.Empty(t, out.EditHandle)
}

// The description is the only place the agent learns that Omni cuts between
// shots by default, so the guidance is pinned.
func TestGenerateVideo_DescriptionCarriesSingleShotAndCostGuidance(t *testing.T) {
	desc := (&generateVideoTool{}).Description()
	for _, want := range []string{"single continuous shot", "no scene cuts", "CANNOT SEE THE VIDEO", "PER SECOND", "edit_from"} {
		assert.Contains(t, desc, want)
	}
}

// A credits-only user's default tier lands on Fast because Omni is not
// servable; the Chosen line must say so rather than silently substituting.
func TestGenerateVideo_SubstitutionIsVisibleInChosen(t *testing.T) {
	generator := &fakeVideoGenerator{caps: veoCaps(), modelID: "veo-3.1-fast-generate"}
	tool := newVideoTool(newFakeAttachmentRepo(), newFakeVideoJobs(), generator)
	tool.resolve = func(_ context.Context, _ string, selector models.ModelSelector) (VideoGenerator, error) {
		if selector.ID == "gemini-omni-1.1-flash" {
			return nil, errors.New("omni is not servable with your providers")
		}
		return generator, nil
	}
	resp, err := tool.Execute(videoCtx(t, "tc-sub"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "Chosen: veo-3.1-fast-generate via default quality=standard.")
	assert.Contains(t, resp.Content, "Substituted: standard → veo-3.1-fast-generate: gemini-omni-1.1-flash needs a Gemini API key.")
}
