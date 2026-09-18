// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Package agywire holds the Antigravity (Google cloudcode) wire types.
//
// Antigravity double-wraps a standard Gemini exchange in BOTH directions:
//
//	request:  {"project":…,"requestId":…,"model":…,"request":{<GenerateContentRequest>}}
//	response: {"response":{<GenerateContentResponse>},"traceId":…,"metadata":{}}
//
// google.golang.org/genai unmarshals a body straight into
// GenerateContentResponse, so an Antigravity body decodes to an EMPTY struct
// and the call appears to succeed while producing nothing. That is why these
// structs exist at all, and why neither caller uses the SDK for transport.
//
// This is a LEAF package — it imports nothing from this repo — because two
// packages on opposite sides of an import edge both need these types. The chat
// driver (internal/llm/drivers/antigravity) imports internal/llm/tools, which
// imports internal/llm/drivers/imagegen, so imagegen can never import the chat
// driver. Putting the shared types here is what lets both speak one wire
// format instead of maintaining two drifting copies of it.
package agywire

// Wire constants captured from antigravity/cli/1.2.5. These look like
// client-identity gating, so they are sent verbatim rather than guessed.
const (
	// StreamEndpointURL is the chat surface: SSE, many frames.
	StreamEndpointURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"

	// GenerateEndpointURL is the image surface. Note the two differences from
	// the chat URL that are easy to carry over by mistake: :generateContent
	// rather than :streamGenerateContent, and NO ?alt=sse — image generation
	// answers with a single JSON body, not a stream.
	GenerateEndpointURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent"

	UserAgentHeader = "antigravity/cli/1.2.5 (aidev_client; os_type=darwin; arch=arm64; cl=982839923; auth_method=consumer)"

	// EnvelopeProject and EnvelopeUserAgent are the top-level identity fields
	// the capture sends alongside the wrapped request. They are not model
	// configuration, and they are the same for every request type.
	EnvelopeProject   = "aicode-consumers"
	EnvelopeUserAgent = "antigravity"

	// RequestTypeAgent and RequestTypeImageGen are the two request types
	// observed. The value travels in the envelope and selects the surface;
	// sending "agent" to :generateContent is not the same call.
	RequestTypeAgent    = "agent"
	RequestTypeImageGen = "image_gen"
)

// RequestEnvelope is the outer object Antigravity POSTs.
//
// Note that Model lives HERE, at the top level, carrying the effort suffix
// (e.g. "gemini-3.8-flash-high"), while the inner Request deliberately has no
// model field at all. Effort suffixes are a chat/thinking concern; the image
// surface sends a bare model id.
type RequestEnvelope struct {
	Project     string       `json:"project"`
	RequestID   string       `json:"requestId"`
	Request     *GenerateReq `json:"request"`
	Model       string       `json:"model"`
	UserAgent   string       `json:"userAgent"`
	RequestType string       `json:"requestType"`
}

// GenerateReq is a standard Gemini GenerateContentRequest, minus the model.
//
// The capture also carries a "labels" object (trajectory_id, model_enum,
// used_claude, …). Those are Antigravity's own client telemetry, describing a
// UI trajectory we do not have, so they are deliberately not sent.
type GenerateReq struct {
	Contents          []*Content        `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []*ToolDecls      `json:"tools,omitempty"`
	ToolConfig        *ToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	SessionID         string            `json:"sessionId,omitempty"`
}

type Content struct {
	Role  string  `json:"role"`
	Parts []*Part `json:"parts"`
}

// Part mirrors genai.Part, with ONE deliberate divergence: ThoughtSignature is
// a string here, not []byte. The wire format carries it base64-encoded
// already, and message.ToolCall.ThoughtSignature stores a base64 string, so
// keeping it a string round-trips verbatim and skips the decode/encode pair
// the genai-based driver needs. Reusing genai.Part instead re-base64s an
// already-base64 value, which was proven by experiment.
type Part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
	InlineData       *Blob             `json:"inlineData,omitempty"`
}

type FunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type FunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

// Blob is an inline binary part. Data is []byte, which encoding/json decodes
// from the wire's base64 string automatically — which is how a generated image
// arrives.
type Blob struct {
	MIMEType string `json:"mimeType"`
	Data     []byte `json:"data"`
}

type ToolDecls struct {
	FunctionDeclarations []*FunctionDeclaration `json:"functionDeclarations"`
}

type FunctionDeclaration struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Parameters  *Schema `json:"parameters,omitempty"`
}

type Schema struct {
	Type        string             `json:"type,omitempty"`
	Description string             `json:"description,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
}

type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// GenerationConfig carries both surfaces' knobs. Every field is omitempty, so
// the chat request and the image request each serialize only their own: chat
// sends maxOutputTokens plus thinkingConfig, image sends candidateCount plus
// imageConfig, and neither sees the other's fields on the wire.
type GenerationConfig struct {
	MaxOutputTokens int64           `json:"maxOutputTokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ThinkingConfig  *ThinkingConfig `json:"thinkingConfig,omitempty"`
	CandidateCount  int             `json:"candidateCount,omitempty"`
	ImageConfig     *ImageConfig    `json:"imageConfig,omitempty"`
}

// ThinkingConfig is the inner thinking control. It is SEPARATE from the effort
// suffix on the top-level model id: the capture sends both, with an unbounded
// budget of -1 while the effort level rides on "…-high".
type ThinkingConfig struct {
	IncludeThoughts bool  `json:"includeThoughts"`
	ThinkingBudget  int32 `json:"thinkingBudget"`
}

// ImageConfig is the image surface's only generation knob in the capture.
// AspectRatio is a ratio string ("1:1", "3:2"), not a pixel size.
type ImageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
}

// UnboundedThinkingBudget is the capture's thinkingBudget: let the server
// decide how much thinking the selected effort level warrants.
const UnboundedThinkingBudget int32 = -1

// --- response side ---

// Envelope is one response body — an SSE `data:` payload on the chat surface,
// or the whole body on the image surface. The standard Gemini response is
// nested under "response"; a parser that expects it at the top level decodes
// an empty struct rather than failing loudly.
type Envelope struct {
	Response *GenerateResp `json:"response"`
	TraceID  string        `json:"traceId,omitempty"`
}

type GenerateResp struct {
	Candidates    []*Candidate   `json:"candidates,omitempty"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
	// ModelVersion echoes the BASE model id ("gemini-3.8-flash") even when the
	// request asked for a suffixed variant ("gemini-3.8-flash-high"). That is
	// expected, not drift: the suffix selects an effort variant, it is not a
	// distinct served model. Do not "fix" this into an equality assertion.
	ModelVersion string `json:"modelVersion,omitempty"`
	ResponseID   string `json:"responseId,omitempty"`

	// PromptFeedback carries a prompt-level block, which is one of the ways a
	// generation returns no image at all.
	PromptFeedback *PromptFeedback `json:"promptFeedback,omitempty"`
}

type Candidate struct {
	Content      *Content `json:"content,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
}

type PromptFeedback struct {
	BlockReason        string `json:"blockReason,omitempty"`
	BlockReasonMessage string `json:"blockReasonMessage,omitempty"`
}

type UsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         int64 `json:"totalTokenCount,omitempty"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
}

// APIErrorEnvelope is Google's error body, which both surfaces return on a
// non-2xx. Unlike the success body it is NOT double-wrapped.
type APIErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}
