// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const managedDriverID = "reliant"

// creditExhaustedMessage is what the user sees when the managed wallet is empty.
const creditExhaustedMessage = "You're out of Reliant credit. Add credit to your account to generate video."

// ManagedClient generates Veo video on Reliant credits. It speaks LiteLLM's
// OpenAI-style video API (POST /videos, GET /videos/{id}, GET /videos/{id}/content)
// to the control-plane proxy, authenticated with the user's rlat_ gateway key.
// Config.BaseURL is the proxy's /v1 root.
//
// The job id is LiteLLM's base64 video id, so a persisted job re-polls the
// same render. LiteLLM translates the OpenAI fields to Vertex Veo
// (litellm/llms/vertex_ai/videos/transformation.py): seconds -> durationSeconds,
// size -> aspectRatio (+resolution), and anything in extra_body is merged into
// the Veo "parameters" block, except a key named "instances" (merged into the
// Veo instance) and "image" (the start frame, placed in the instance).
type ManagedClient struct {
	config Config
	http   *http.Client
}

// NewManaged builds a managed client. Config.APIKey must be the rlat_ key.
func NewManaged(config Config) (*ManagedClient, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("managed video generation requires the Reliant API base URL")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("managed video generation requires a Reliant gateway key")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &ManagedClient{config: config, http: client}, nil
}

// Info reports the bound model and its declared capabilities.
func (c *ManagedClient) Info() ModelInfo {
	return ModelInfo{ModelID: c.config.ModelID, APIModel: c.config.APIModel, Driver: c.config.Driver, Capabilities: c.config.Capabilities}
}

// managedSize maps resolution + aspect to the OpenAI "size" LiteLLM understands.
func managedSize(resolution, aspect string) string {
	dims := map[string][2]int{"720p": {1280, 720}, "1080p": {1920, 1080}, "4k": {3840, 2160}}
	d, ok := dims[strings.ToLower(resolution)]
	if !ok {
		d = dims["720p"]
	}
	if aspect == "9:16" {
		return fmt.Sprintf("%dx%d", d[1], d[0])
	}
	return fmt.Sprintf("%dx%d", d[0], d[1])
}

func vertexImage(image *Image) map[string]string {
	return map[string]string{"bytesBase64Encoded": base64.StdEncoding.EncodeToString(image.Bytes), "mimeType": image.MIMEType}
}

func (c *ManagedClient) buildBody(request Request) map[string]any {
	aspect := request.AspectRatio
	if aspect == "" {
		aspect = "16:9"
	}
	resolution := request.Resolution
	if resolution == "" {
		resolution = "720p"
	}
	body := map[string]any{
		"model":  c.config.APIModel,
		"prompt": request.Prompt,
		"size":   managedSize(resolution, aspect),
	}
	if request.DurationSeconds > 0 {
		body["seconds"] = strconv.Itoa(request.DurationSeconds)
	}
	// size cannot express 4k or portrait-4k reliably, so aspect and resolution
	// are also sent as explicit Veo parameters.
	extra := map[string]any{"aspectRatio": aspect, "resolution": resolution}
	if request.NegativePrompt != "" {
		extra["negativePrompt"] = request.NegativePrompt
	}
	if request.Audio != nil {
		extra["generateAudio"] = *request.Audio
	}
	instance := map[string]any{}
	if request.StartFrame != nil {
		instance["image"] = vertexImage(request.StartFrame)
	}
	if request.EndFrame != nil {
		instance["lastFrame"] = vertexImage(request.EndFrame)
	}
	if len(request.References) > 0 {
		refs := make([]map[string]any, 0, len(request.References))
		for i := range request.References {
			refs = append(refs, map[string]any{"image": vertexImage(&request.References[i]), "referenceType": "asset"})
		}
		instance["referenceImages"] = refs
	}
	if len(instance) > 0 {
		extra["instances"] = instance
	}
	body["extra_body"] = extra
	return body
}

type managedVideoObject struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  any    `json:"error"`
}

type managedErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func (c *ManagedClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.config.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	for key, value := range c.config.ExtraHeaders {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// managedHTTPError classifies a non-2xx response.
func managedHTTPError(status int, raw []byte) *Error {
	var parsed managedErrorBody
	_ = json.Unmarshal(raw, &parsed)
	message := strings.TrimSpace(parsed.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	code := fmt.Sprint(parsed.Error.Code)
	if parsed.Error.Type == "insufficient_quota" || code == "insufficient_quota" || status == http.StatusPaymentRequired {
		return &Error{Kind: KindQuota, Message: creditExhaustedMessage}
	}
	kind := KindFailed
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = KindInvalid
	case http.StatusTooManyRequests:
		kind = KindQuota
		message = "video quota exhausted; try again later: " + message
	}
	return &Error{Kind: kind, Message: fmt.Sprintf("video generation failed (HTTP %d): %s", status, message)}
}

// Submit starts a render and returns LiteLLM's video id.
func (c *ManagedClient) Submit(ctx context.Context, request Request) (Job, error) {
	if strings.TrimSpace(request.Prompt) == "" && request.StartFrame == nil {
		return Job{}, &Error{Kind: KindInvalid, Message: "video generation requires a prompt"}
	}
	if request.EditOf != "" {
		return Job{}, &Error{Kind: KindInvalid, Message: fmt.Sprintf("model %s cannot edit a previous video", c.config.ModelID)}
	}
	payload, err := json.Marshal(c.buildBody(request))
	if err != nil {
		return Job{}, err
	}
	return submitWithRetry(ctx, c.config.RetryBaseDelay, func(ctx context.Context) (Job, int, error) {
		resp, err := c.do(ctx, http.MethodPost, "/videos", payload)
		if err != nil {
			return Job{}, 0, fmt.Errorf("video generation request failed: %w", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode/100 != 2 {
			classified := managedHTTPError(resp.StatusCode, raw)
			status := resp.StatusCode
			if classified.Message == creditExhaustedMessage {
				status = 0 // terminal: never retry an empty wallet
			}
			return Job{}, status, classified
		}
		var object managedVideoObject
		if err := json.Unmarshal(raw, &object); err != nil || object.ID == "" {
			return Job{}, 0, &Error{Kind: KindFailed, Message: "the gateway accepted the request but returned no video id"}
		}
		return Job{Provider: managedDriverID, ID: object.ID}, 0, nil
	})
}

// Poll checks one job; once completed it downloads the clip.
func (c *ManagedClient) Poll(ctx context.Context, job Job) (*Response, bool, error) {
	escaped := url.PathEscape(job.ID)
	resp, err := c.do(ctx, http.MethodGet, "/videos/"+escaped, nil)
	if err != nil {
		return nil, false, fmt.Errorf("polling video job: %w", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, &Error{Kind: KindExpired, Message: fmt.Sprintf("video job %s is no longer available at the gateway", job.ID), Job: job}
	}
	if resp.StatusCode/100 != 2 {
		// Deliberately unclassified: Wait tolerates a few transient failures.
		return nil, false, fmt.Errorf("polling video job: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var object managedVideoObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, false, fmt.Errorf("polling video job: bad response: %w", err)
	}
	switch strings.ToLower(object.Status) {
	case "failed":
		return nil, false, &Error{Kind: KindFailed, Message: "video generation failed: " + managedFailureReason(object.Error), Job: job}
	case "completed":
	default:
		return nil, false, nil
	}

	content, err := c.do(ctx, http.MethodGet, "/videos/"+escaped+"/content", nil)
	if err != nil {
		return nil, false, fmt.Errorf("downloading the generated video: %w", err)
	}
	defer content.Body.Close()
	data, err := io.ReadAll(content.Body)
	if err != nil || content.StatusCode/100 != 2 {
		return nil, false, fmt.Errorf("downloading the generated video: HTTP %d: %v", content.StatusCode, err)
	}
	if len(data) == 0 {
		return nil, false, &Error{Kind: KindFailed, Message: "the gateway returned an empty video", Job: job}
	}
	mimeType := strings.TrimSpace(strings.Split(content.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = "video/mp4"
	}
	return &Response{Bytes: data, MIMEType: mimeType, ModelID: c.config.ModelID, APIModel: c.config.APIModel, Driver: c.config.Driver, Job: job}, true, nil
}

func managedFailureReason(raw any) string {
	switch value := raw.(type) {
	case nil:
		return "the render failed"
	case string:
		return value
	case map[string]any:
		if message, ok := value["message"].(string); ok && message != "" {
			return message
		}
	}
	return fmt.Sprint(raw)
}

// Cancel is a no-op: Veo operations cannot be cancelled.
func (c *ManagedClient) Cancel(ctx context.Context, job Job) error { return nil }
