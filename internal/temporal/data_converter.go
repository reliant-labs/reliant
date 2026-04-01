package temporal

import (
	"encoding/json"
	"errors"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
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

// NewFlexibleDataConverter creates a Temporal DataConverter that handles
// proto return types gracefully when decoding into non-proto targets.
// This allows activities to return proto messages while callers can
// decode into map[string]interface{} without needing per-type handling.
func NewFlexibleDataConverter() converter.DataConverter {
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
