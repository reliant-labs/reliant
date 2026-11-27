package mountkit

// json_codec.go — the protobuf JSON codec forge serves with.
//
// It is connect-go's own JSON codec, byte for byte on the wire: protojson,
// unknown fields discarded. What it changes is the error a client reads when
// a well-known type is spelled the way a Go or JavaScript programmer would
// write it. protojson's answer to {"interval": "1h"} is
//
//	invalid google.protobuf.Duration value "1h"
//
// which names the problem but not the fix, so every API user pays a
// round-trip to the protobuf JSON spec. The codec appends the spelling to
// send instead:
//
//	invalid google.protobuf.Duration value "1h" (protobuf JSON durations are
//	decimal seconds with an "s" suffix: send "3600s" for "1h")
//
// It never ACCEPTS the other spelling: a server that took "1h" would diverge
// from the protobuf JSON mapping every other client and server implements, and
// a request one forge service accepted would be refused by the next hop. The
// status stays invalid_argument; only the message grows.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/runtime/protoiface"
)

// JSONCodecOption is the connect-go v1 handler option that serves the
// "json" and "json; charset=utf-8" codecs with forge's protobuf JSON codec:
// connect-go's own, whose unmarshal errors also say how to spell a Duration,
// Timestamp or FieldMask (see the file comment). serverkit.Chain puts it
// first in HandlerOptions, so a later connect.WithCodec of either name — a
// caller's own codec — still wins.
func JSONCodecOption() connect.HandlerOption {
	return connect.WithHandlerOptions(
		connect.WithCodec(&jsonCodecV1{name: "json"}),
		connect.WithCodec(&jsonCodecV1{name: "json; charset=utf-8"}),
	)
}

// JSONCodecOptionV2 is JSONCodecOption for connect-go v2: connecthttp's
// default codecs (binary protobuf first, then JSON) with forge's JSON codec
// in place of connectproto's. MountV2 puts it ahead of the caller's options;
// connecthttp.WithCodecs replaces the whole list, so a caller's own
// WithCodecs still wins.
func JSONCodecOptionV2() connecthttp.Option {
	return connecthttp.WithCodecs(connectproto.NewBinaryCodec(), &jsonCodecV2{JSONCodec: connectproto.NewJSONCodec()})
}

// jsonCodecV1 is connect-go v1's unexported protoJSONCodec, reproduced (v1
// exports no constructor for it to wrap) with hinted unmarshal errors.
type jsonCodecV1 struct{ name string }

func (c *jsonCodecV1) Name() string { return c.name }

func (c *jsonCodecV1) Marshal(message any) ([]byte, error) {
	m, err := asProto(message)
	if err != nil {
		return nil, err
	}
	return protojson.MarshalOptions{}.Marshal(m)
}

func (c *jsonCodecV1) MarshalAppend(dst []byte, message any) ([]byte, error) {
	m, err := asProto(message)
	if err != nil {
		return nil, err
	}
	return protojson.MarshalOptions{}.MarshalAppend(dst, m)
}

func (c *jsonCodecV1) Unmarshal(binary []byte, message any) error {
	m, err := asProto(message)
	if err != nil {
		return err
	}
	if len(binary) == 0 {
		return errors.New("zero-length payload is not a valid JSON object")
	}
	// Discard unknown fields so clients and servers aren't forced to always
	// use exactly the same version of the schema (connect-go's default).
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(binary, m); err != nil {
		return fmt.Errorf("unmarshal into %T: %w", message, withJSONHint(err))
	}
	return nil
}

// MarshalStable compacts protojson's output, whose whitespace is not stable,
// as connect-go's codec does for Connect GET requests.
func (c *jsonCodecV1) MarshalStable(message any) ([]byte, error) {
	raw, err := c.Marshal(message)
	if err != nil {
		return nil, err
	}
	compacted := bytes.NewBuffer(raw[:0])
	if err := json.Compact(compacted, raw); err != nil {
		return nil, err
	}
	return compacted.Bytes(), nil
}

func (c *jsonCodecV1) IsBinary() bool { return false }

func asProto(message any) (proto.Message, error) {
	if m, ok := message.(proto.Message); ok {
		return m, nil
	}
	if _, ok := message.(protoiface.MessageV1); ok {
		return nil, fmt.Errorf("%T uses github.com/golang/protobuf, but connect-go only supports google.golang.org/protobuf: see https://go.dev/blog/protobuf-apiv2", message)
	}
	return nil, fmt.Errorf("%T doesn't implement proto.Message", message)
}

// jsonCodecV2 is connectproto's JSON codec with hinted unmarshal errors; its
// marshalling (MarshalWrite, MarshalWriteStable, IsBinary) is connectproto's.
type jsonCodecV2 struct{ *connectproto.JSONCodec }

var _ connectv2.StableCodec = (*jsonCodecV2)(nil)

func (c *jsonCodecV2) UnmarshalRead(ctx context.Context, src io.Reader, msg any) error {
	return withJSONHint(c.JSONCodec.UnmarshalRead(ctx, src, msg))
}

// protojson's errors carry no structure, so the hint reads their text: the
// value rejected is the JSON string protojson echoes (Duration, Timestamp)
// or the Go-quoted path it names (FieldMask).
var (
	invalidWKTValue  = regexp.MustCompile(`invalid google\.protobuf\.(Duration|Timestamp) value ("(?:[^"\\]|\\.)*")`)
	invalidMaskPath  = regexp.MustCompile(`google\.protobuf\.FieldMask\.paths contains invalid path: ("(?:[^"\\]|\\.)*")`)
	decimalSeconds   = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	timestampLayouts = []struct {
		layout string
		zoned  bool
	}{
		{"2006-01-02 15:04:05.999999999Z07:00", true},
		{"2006-01-02T15:04:05.999999999", false},
		{"2006-01-02 15:04:05.999999999", false},
		{time.DateOnly, false},
	}
)

// withJSONHint appends to a protojson unmarshal error the spelling of the
// well-known-type value it rejected that protojson would accept. Any other
// error, and nil, is returned as it is; a hinted one still wraps err.
func withJSONHint(err error) error {
	if err == nil {
		return nil
	}
	if hint := jsonHint(err.Error()); hint != "" {
		return fmt.Errorf("%w (%s)", err, hint)
	}
	return err
}

func jsonHint(msg string) string {
	if m := invalidWKTValue.FindStringSubmatch(msg); m != nil {
		value, err := strconv.Unquote(m[2])
		if m[1] == "Duration" {
			return durationHint(value, err == nil)
		}
		return timestampHint(value, err == nil)
	}
	if m := invalidMaskPath.FindStringSubmatch(msg); m != nil {
		path, err := strconv.Unquote(m[1])
		return fieldMaskHint(path, err == nil)
	}
	return ""
}

func durationHint(value string, ok bool) string {
	const rule = `protobuf JSON durations are decimal seconds with an "s" suffix`
	if ok {
		if d, err := time.ParseDuration(value); err == nil {
			return fmt.Sprintf("%s: send %q for %q", rule, protoDuration(d), value)
		}
		if decimalSeconds.MatchString(value) {
			return fmt.Sprintf("%s: send %q", rule, value+"s")
		}
	}
	return rule + `, e.g. "3600s" for 1h`
}

// protoDuration spells d the way protojson reads it: whole seconds, then any
// fraction with its trailing zeros dropped ("5400s", "0.25s", "-1.5s").
func protoDuration(d time.Duration) string {
	sign := ""
	u := uint64(d)
	if d < 0 {
		sign, u = "-", uint64(-(d+1))+1 // -(math.MinInt64) overflows int64
	}
	secs, nanos := u/uint64(time.Second), u%uint64(time.Second)
	if nanos == 0 {
		return fmt.Sprintf("%s%ds", sign, secs)
	}
	return fmt.Sprintf("%s%d.%ss", sign, secs, strings.TrimRight(fmt.Sprintf("%09d", nanos), "0"))
}

func timestampHint(value string, ok bool) string {
	const rule = `protobuf JSON timestamps are RFC 3339 strings with a time zone`
	if ok {
		for _, l := range timestampLayouts {
			t, err := time.Parse(l.layout, value)
			if err != nil {
				continue
			}
			if l.zoned {
				return fmt.Sprintf("%s: send %q for %q", rule, t.Format(time.RFC3339Nano), value)
			}
			return fmt.Sprintf("%s: send %q for %q in UTC", rule, t.Format(time.RFC3339Nano), value)
		}
	}
	return rule + `, e.g. "2024-01-02T15:04:05Z"`
}

func fieldMaskHint(path string, ok bool) string {
	const rule = `protobuf JSON field masks are one string of comma-separated lowerCamelCase paths`
	if ok && strings.Contains(path, "_") {
		if camel := lowerCamel(path); camel != path {
			return fmt.Sprintf("%s: send %q for %q", rule, camel, path)
		}
	}
	return rule + `, e.g. "displayName,updateTime"`
}

// lowerCamel is protobuf's JSON name of a snake_case field path: each "_x"
// becomes "X", dots (nested fields) kept ("owner.display_name" is
// "owner.displayName").
func lowerCamel(path string) string {
	var b strings.Builder
	upper := false
	for _, r := range path {
		switch {
		case r == '_':
			upper = true
		case upper && 'a' <= r && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
			upper = false
		default:
			b.WriteRune(r)
			upper = false
		}
	}
	return b.String()
}
