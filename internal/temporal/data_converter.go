package temporal

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"
)

// flexibleProtoJSONConverter wraps ProtoJSONPayloadConverter to handle
// decoding proto-encoded payloads into non-proto targets (e.g. map[string]interface{}).
//
// Temporal's default ProtoJSON converter encodes proto messages as valid JSON,
// but requires the decode target to implement proto.Message. This wrapper falls
// back to plain JSON deserialization when the target isn't a proto type, since
// the payload data is valid JSON regardless.
type flexibleProtoJSONConverter struct {
	inner *converter.ProtoJSONPayloadConverter
}

func (c *flexibleProtoJSONConverter) ToPayload(value interface{}) (*commonpb.Payload, error) {
	return c.inner.ToPayload(value)
}

func (c *flexibleProtoJSONConverter) FromPayload(payload *commonpb.Payload, valuePtr interface{}) error {
	err := c.inner.FromPayload(payload, valuePtr)
	if err != nil && errors.Is(err, converter.ErrTypeNotImplementProtoMessage) {
		return json.Unmarshal(payload.GetData(), valuePtr)
	}
	return err
}

func (c *flexibleProtoJSONConverter) ToString(payload *commonpb.Payload) string {
	return c.inner.ToString(payload)
}

func (c *flexibleProtoJSONConverter) Encoding() string {
	return c.inner.Encoding()
}

// DataConverterOption configures NewFlexibleDataConverter.
type DataConverterOption func(*dataConverterOptions)

type dataConverterOptions struct {
	payloadStore claimcheck.Store
}

// WithPayloadStore enables the claim-check codec: payloads at or above
// claimcheck.DefaultThreshold are stored in store and replaced in workflow
// history by a reference. A nil store is the same as omitting the option.
func WithPayloadStore(store claimcheck.Store) DataConverterOption {
	return func(o *dataConverterOptions) { o.payloadStore = store }
}

// NewFlexibleDataConverter creates a Temporal DataConverter that handles
// proto return types gracefully when decoding into non-proto targets.
// This allows activities to return proto messages while callers can
// decode into map[string]interface{} without needing per-type handling.
//
// The claim-check codec is ALWAYS installed. Without WithPayloadStore it
// encodes nothing and fails loudly (claimcheck.ErrNoStore) on any reference
// payload it is asked to decode, so a process wired without the store cannot
// mistake a reference for data. Pre-codec histories contain no references and
// decode unchanged either way.
func NewFlexibleDataConverter(opts ...DataConverterOption) converter.DataConverter {
	var o dataConverterOptions
	for _, opt := range opts {
		opt(&o)
	}
	codec := claimcheck.NewCodec(o.payloadStore)
	return newWorkflowDeadlockPausingConverter(
		converter.NewCodecDataConverter(newBaseDataConverter(), codec),
	)
}

// workflowDeadlockPausingConverter pauses Temporal's workflow deadlock detector
// around conversions that run ON the workflow goroutine, and only those.
//
// Decoding an activity result runs on the workflow-task goroutine, and with a
// claim-check store that can mean a DB read; pausing the detector around it
// keeps a slow fetch from being reported as a workflow deadlock. That is what
// workflow.DataConverterWithoutDeadlockDetection is for, and it is what
// `paused` delegates to.
//
// It cannot be used alone. Temporal's wrapper keeps its workflow context when
// re-bound to an activity context, and the SDK does exactly that for a local
// activity: the converter is bound to the workflow context when the activity
// is scheduled, then re-bound with WithContext on the local-activity goroutine,
// which encodes the result with it. That goroutine would then pause and resume
// the WORKFLOW's deadlock detector while the workflow dispatcher may be running
// a coroutine on another goroutine. The detector's locks are not ordered for
// that (sdk v1.50.0 internal/workflow_deadlock.go: deadlockTicker.end takes the
// ticker lock then the detector lock; deadlockDetector.resume takes them in
// the other order), so the two goroutines can block each other forever,
// wedging the workflow task. A converter bound to an activity context is never
// on the workflow goroutine, so WithContext drops the pausing wrapper — the
// same rule the SDK's own transferAwareDataConverter follows when it clears its
// workflow context in WithContext.
type workflowDeadlockPausingConverter struct {
	paused converter.DataConverter // base, wrapped by DataConverterWithoutDeadlockDetection
	base   converter.DataConverter // what an activity-bound copy uses
}

var (
	_ converter.ContextAware                          = (*workflowDeadlockPausingConverter)(nil)
	_ converter.DataConverterWithSerializationContext = (*workflowDeadlockPausingConverter)(nil)
)

func newWorkflowDeadlockPausingConverter(base converter.DataConverter) *workflowDeadlockPausingConverter {
	return &workflowDeadlockPausingConverter{
		paused: workflow.DataConverterWithoutDeadlockDetection(base),
		base:   base,
	}
}

func (c *workflowDeadlockPausingConverter) ToPayload(value any) (*commonpb.Payload, error) {
	return c.paused.ToPayload(value)
}

func (c *workflowDeadlockPausingConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
	return c.paused.FromPayload(payload, valuePtr)
}

func (c *workflowDeadlockPausingConverter) ToPayloads(values ...any) (*commonpb.Payloads, error) {
	return c.paused.ToPayloads(values...)
}

func (c *workflowDeadlockPausingConverter) FromPayloads(payloads *commonpb.Payloads, valuePtrs ...any) error {
	return c.paused.FromPayloads(payloads, valuePtrs...)
}

func (c *workflowDeadlockPausingConverter) ToString(payload *commonpb.Payload) string {
	return c.paused.ToString(payload)
}

func (c *workflowDeadlockPausingConverter) ToStrings(payloads *commonpb.Payloads) []string {
	return c.paused.ToStrings(payloads)
}

func (c *workflowDeadlockPausingConverter) WithWorkflowContext(ctx converter.WorkflowContext) converter.DataConverter {
	return &workflowDeadlockPausingConverter{
		paused: withWorkflowContext(c.paused, ctx),
		base:   withWorkflowContext(c.base, ctx),
	}
}

// WithContext binds an activity context, so the result runs off the workflow
// goroutine and must not touch the workflow's deadlock detector.
func (c *workflowDeadlockPausingConverter) WithContext(ctx context.Context) converter.DataConverter {
	if ca, ok := c.base.(converter.ContextAware); ok {
		return ca.WithContext(ctx)
	}
	return c.base
}

func (c *workflowDeadlockPausingConverter) WithSerializationContext(sc converter.SerializationContext) converter.DataConverter {
	return &workflowDeadlockPausingConverter{
		paused: converter.WithDataConverterSerializationContext(c.paused, sc),
		base:   converter.WithDataConverterSerializationContext(c.base, sc),
	}
}

func withWorkflowContext(dc converter.DataConverter, ctx converter.WorkflowContext) converter.DataConverter {
	if ca, ok := dc.(converter.ContextAware); ok {
		return ca.WithWorkflowContext(ctx)
	}
	return dc
}

func newBaseDataConverter() converter.DataConverter {
	return converter.NewCompositeDataConverter(
		converter.NewNilPayloadConverter(),
		converter.NewByteSlicePayloadConverter(),
		&flexibleProtoJSONConverter{inner: converter.NewProtoJSONPayloadConverterWithOptions(
			converter.ProtoJSONPayloadConverterOptions{
				// UseProtoNames produces snake_case field names in JSON (e.g. "tool_results")
				// instead of the default camelCase (e.g. "toolResults"). This ensures proto
				// JSON output matches the field names used in YAML workflow CEL expressions.
				UseProtoNames: true,
				// A payload recorded before a proto field was retired still
				// carries that field, and workflow code that decodes an
				// activity result into a typed message re-decodes the RECORDED
				// payload on every replay. Rejecting the unknown field fails the
				// workflow task non-deterministically (TMPRL1100) and wedges the
				// in-flight run on deploy, so reserving a field number — the
				// documented way to retire one — would be unsafe. Ignore it.
				AllowUnknownFields: true,
			},
		)},
		converter.NewProtoPayloadConverter(),
		converter.NewJSONPayloadConverter(),
	)
}
