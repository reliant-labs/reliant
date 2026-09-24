package temporal

import (
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
	// Decoding an activity result runs on the workflow-task goroutine, and with
	// a store that can mean a DB read; pause the deadlock detector around it so
	// a slow fetch is not reported as a workflow deadlock. Nil-safe outside
	// workflow context.
	return workflow.DataConverterWithoutDeadlockDetection(
		converter.NewCodecDataConverter(newBaseDataConverter(), codec),
	)
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
			},
		)},
		converter.NewProtoPayloadConverter(),
		converter.NewJSONPayloadConverter(),
	)
}
