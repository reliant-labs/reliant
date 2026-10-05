// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

type fakeVeoModels struct {
	calls  int32
	source *genai.GenerateVideosSource
	config *genai.GenerateVideosConfig
	errs   []error
	op     *genai.GenerateVideosOperation
}

func (f *fakeVeoModels) GenerateVideosFromSource(_ context.Context, _ string, s *genai.GenerateVideosSource, c *genai.GenerateVideosConfig) (*genai.GenerateVideosOperation, error) {
	n := int(atomic.AddInt32(&f.calls, 1))
	f.source, f.config = s, c
	if n <= len(f.errs) && f.errs[n-1] != nil {
		return nil, f.errs[n-1]
	}
	return f.op, nil
}

type fakeVeoOps struct {
	results []*genai.GenerateVideosOperation
	err     error
	i       int
}

func (f *fakeVeoOps) GetVideosOperation(_ context.Context, op *genai.GenerateVideosOperation, _ *genai.GetOperationConfig) (*genai.GenerateVideosOperation, error) {
	if f.err != nil {
		return nil, f.err
	}
	r := f.results[min(f.i, len(f.results)-1)]
	f.i++
	return r, nil
}

type fakeVeoFiles struct {
	data []byte
	err  error
	n    int
}

func (f *fakeVeoFiles) Download(context.Context, genai.DownloadURI, *genai.DownloadFileConfig) ([]byte, error) {
	f.n++
	return f.data, f.err
}

func testConfig() Config {
	return Config{ModelID: "veo-3.1-generate", APIModel: "veo-3.1-generate-preview", Driver: "gemini", RetryBaseDelay: time.Millisecond}
}

func TestVeoSubmit_MapsRequestAndReturnsOperationName(t *testing.T) {
	m := &fakeVeoModels{op: &genai.GenerateVideosOperation{Name: "models/veo/operations/abc"}}
	c := newVeoWithFakes(testConfig(), m, nil, nil)
	audio := false
	job, err := c.Submit(context.Background(), Request{
		Prompt: "a ball", DurationSeconds: 8, Resolution: "1080p", AspectRatio: "9:16", NegativePrompt: "blur", Audio: &audio,
		StartFrame: &Image{Bytes: []byte{1}, MIMEType: "image/png"}, EndFrame: &Image{Bytes: []byte{2}, MIMEType: "image/png"},
		References: []Image{{Bytes: []byte{3}, MIMEType: "image/png"}},
	})
	require.NoError(t, err)
	assert.Equal(t, Job{Provider: "veo", ID: "models/veo/operations/abc"}, job)
	assert.Equal(t, "a ball", m.source.Prompt)
	assert.Equal(t, []byte{1}, m.source.Image.ImageBytes)
	assert.EqualValues(t, 8, *m.config.DurationSeconds)
	assert.Equal(t, "1080p", m.config.Resolution)
	assert.Equal(t, "9:16", m.config.AspectRatio)
	assert.Equal(t, []byte{2}, m.config.LastFrame.ImageBytes)
	require.Len(t, m.config.ReferenceImages, 1)
	assert.Nil(t, m.config.GenerateAudio, "the Gemini API backend rejects generateAudio, so it is only sent on Vertex")
}

func TestVeoSubmit_VertexForwardsAudioFlag(t *testing.T) {
	cfg := testConfig()
	cfg.Vertex = &VertexConfig{Project: "p", Location: "us-central1"}
	m := &fakeVeoModels{op: &genai.GenerateVideosOperation{Name: "x/operations/1"}}
	c := newVeoWithFakes(cfg, m, nil, nil)
	audio := false
	_, err := c.Submit(context.Background(), Request{Prompt: "x", Audio: &audio})
	require.NoError(t, err)
	require.NotNil(t, m.config.GenerateAudio)
	assert.False(t, *m.config.GenerateAudio)
}

func TestVeoSubmit_RetriesOnlyTransientAndOnlyBeforeAcceptance(t *testing.T) {
	m := &fakeVeoModels{
		errs: []error{genai.APIError{Code: 503, Message: "busy"}, genai.APIError{Code: 503, Message: "busy"}},
		op:   &genai.GenerateVideosOperation{Name: "op/1"},
	}
	job, err := newVeoWithFakes(testConfig(), m, nil, nil).Submit(context.Background(), Request{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "op/1", job.ID)
	assert.EqualValues(t, 3, m.calls)

	bad := &fakeVeoModels{errs: []error{genai.APIError{Code: 400, Message: "bad aspect"}}}
	_, err = newVeoWithFakes(testConfig(), bad, nil, nil).Submit(context.Background(), Request{Prompt: "x"})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindInvalid, e.Kind)
	assert.EqualValues(t, 1, bad.calls, "a 400 is never retried")
}

func TestVeoSubmit_QuotaIsClassifiedWithGuidance(t *testing.T) {
	m := &fakeVeoModels{errs: []error{genai.APIError{Code: 429, Status: "RESOURCE_EXHAUSTED", Message: "quota"}, genai.APIError{Code: 429, Status: "RESOURCE_EXHAUSTED", Message: "quota"}, genai.APIError{Code: 429, Status: "RESOURCE_EXHAUSTED", Message: "quota"}}}
	_, err := newVeoWithFakes(testConfig(), m, nil, nil).Submit(context.Background(), Request{Prompt: "x"})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindQuota, e.Kind)
	assert.Contains(t, e.Message, "try again later or switch model")
	assert.EqualValues(t, 3, m.calls, "bounded: at most 3 submit attempts")
}

func TestVeoPoll_NotDoneThenInlineBytes(t *testing.T) {
	ops := &fakeVeoOps{results: []*genai.GenerateVideosOperation{
		{Name: "op/1"},
		{Name: "op/1", Done: true, Response: &genai.GenerateVideosResponse{GeneratedVideos: []*genai.GeneratedVideo{{Video: &genai.Video{VideoBytes: []byte("mp4"), MIMEType: "video/mp4"}}}}},
	}}
	c := newVeoWithFakes(testConfig(), nil, ops, &fakeVeoFiles{})
	resp, done, err := c.Poll(context.Background(), Job{ID: "op/1"})
	require.NoError(t, err)
	assert.False(t, done)
	assert.Nil(t, resp)
	resp, done, err = c.Poll(context.Background(), Job{ID: "op/1"})
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, []byte("mp4"), resp.Bytes)
	assert.Equal(t, "video/mp4", resp.MIMEType)
}

func TestVeoPoll_AIStudioURIIsDownloaded(t *testing.T) {
	ops := &fakeVeoOps{results: []*genai.GenerateVideosOperation{{Done: true, Response: &genai.GenerateVideosResponse{
		GeneratedVideos: []*genai.GeneratedVideo{{Video: &genai.Video{URI: "https://generativelanguage.googleapis.com/v1beta/files/abc:download?alt=media"}}}}}}}
	files := &fakeVeoFiles{data: []byte("clip")}
	resp, done, err := newVeoWithFakes(testConfig(), nil, ops, files).Poll(context.Background(), Job{ID: "op/1"})
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, []byte("clip"), resp.Bytes)
	assert.Equal(t, "video/mp4", resp.MIMEType, "defaults to mp4 when the provider omits it")
	assert.Equal(t, 1, files.n)
}

func TestVeoPoll_FilteredFailedAndExpired(t *testing.T) {
	filtered := &fakeVeoOps{results: []*genai.GenerateVideosOperation{{Done: true, Response: &genai.GenerateVideosResponse{RAIMediaFilteredCount: 1, RAIMediaFilteredReasons: []string{"celebrity likeness"}}}}}
	_, _, err := newVeoWithFakes(testConfig(), nil, filtered, nil).Poll(context.Background(), Job{ID: "op"})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindFiltered, e.Kind)
	assert.Contains(t, e.Message, "celebrity likeness", "a filter must say why, never just 'no video'")

	failed := &fakeVeoOps{results: []*genai.GenerateVideosOperation{{Done: true, Error: map[string]any{"message": "internal error"}}}}
	_, _, err = newVeoWithFakes(testConfig(), nil, failed, nil).Poll(context.Background(), Job{ID: "op"})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindFailed, e.Kind)
	assert.Contains(t, e.Message, "internal error")

	gone := &fakeVeoOps{err: genai.APIError{Code: http.StatusNotFound, Message: "not found"}}
	_, _, err = newVeoWithFakes(testConfig(), nil, gone, nil).Poll(context.Background(), Job{ID: "op"})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindExpired, e.Kind)

	empty := &fakeVeoOps{results: []*genai.GenerateVideosOperation{{Done: true, Response: &genai.GenerateVideosResponse{}}}}
	_, _, err = newVeoWithFakes(testConfig(), nil, empty, nil).Poll(context.Background(), Job{ID: "op"})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindFailed, e.Kind)
}

func TestWait_PollsUntilDoneAndToleratesTransientErrors(t *testing.T) {
	c := &scriptedClient{script: []pollStep{{err: errors.New("connection reset")}, {}, {done: true}}}
	resp, err := Wait(context.Background(), c, Job{ID: "j"}, WaitOptions{Interval: time.Millisecond})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 3, c.polls)
}

func TestWait_GivesUpAfterConsecutiveErrorsDeadlineAndCancel(t *testing.T) {
	c := &scriptedClient{script: []pollStep{{err: errors.New("down")}}}
	_, err := Wait(context.Background(), c, Job{ID: "j"}, WaitOptions{Interval: time.Millisecond, MaxConsecutiveErrors: 3})
	require.Error(t, err)
	assert.Equal(t, 3, c.polls)

	c = &scriptedClient{script: []pollStep{{}}}
	_, err = Wait(context.Background(), c, Job{ID: "j"}, WaitOptions{Interval: time.Millisecond, Deadline: 20 * time.Millisecond})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, KindTimeout, e.Kind)
	assert.Equal(t, "j", e.Job.ID, "a timeout keeps the job so a re-ask can resume it")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Wait(ctx, &scriptedClient{script: []pollStep{{}}}, Job{ID: "j"}, WaitOptions{Interval: time.Hour})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWait_ClassifiedErrorIsFinalAndNeverSubmits(t *testing.T) {
	c := &scriptedClient{script: []pollStep{{err: &Error{Kind: KindFiltered, Message: "withheld"}}}}
	_, err := Wait(context.Background(), c, Job{ID: "j"}, WaitOptions{Interval: time.Millisecond})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 1, c.polls)
	assert.Equal(t, 0, c.submits)
	assert.True(t, strings.Contains(e.Message, "withheld"))
}

type pollStep struct {
	done bool
	err  error
}

type scriptedClient struct {
	script  []pollStep
	polls   int
	submits int
}

func (s *scriptedClient) Submit(context.Context, Request) (Job, error) {
	s.submits++
	return Job{}, nil
}
func (s *scriptedClient) Cancel(context.Context, Job) error { return nil }
func (s *scriptedClient) Poll(_ context.Context, j Job) (*Response, bool, error) {
	step := s.script[min(s.polls, len(s.script)-1)]
	s.polls++
	if step.err != nil {
		return nil, false, step.err
	}
	if step.done {
		return &Response{Bytes: []byte("x"), Job: j}, true, nil
	}
	return nil, false, nil
}
