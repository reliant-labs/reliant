package mountkit

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Every hint is read off protojson's real error, so a protojson that rewords
// one fails here rather than silently dropping the hint. A hinted error still
// wraps protojson's.
func TestWithJSONHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  proto.Message
		json string
		want string // "" = no hint
	}{
		{"go duration", &durationpb.Duration{}, `"1h"`, `send "3600s" for "1h"`},
		{"go duration compound", &durationpb.Duration{}, `"1h30m"`, `send "5400s" for "1h30m"`},
		{"go duration fraction", &durationpb.Duration{}, `"250ms"`, `send "0.25s" for "250ms"`},
		{"go duration negative", &durationpb.Duration{}, `"-1.5s5ms"`, `send "-1.505s" for "-1.5s5ms"`},
		{"go duration negative ok", &durationpb.Duration{}, `"-90s"`, ``},
		{"go duration negative minutes", &durationpb.Duration{}, `"-1m30s"`, `send "-90s" for "-1m30s"`},
		{"bare seconds", &durationpb.Duration{}, `"3600"`, `send "3600s"`},
		{"garbage duration", &durationpb.Duration{}, `"soon"`, `durations are decimal seconds with an "s" suffix, e.g. "3600s" for 1h`},
		{"date only", &timestamppb.Timestamp{}, `"2024-01-02"`, `send "2024-01-02T00:00:00Z" for "2024-01-02" in UTC`},
		{"zoneless", &timestamppb.Timestamp{}, `"2024-01-02T15:04:05"`, `send "2024-01-02T15:04:05Z" for "2024-01-02T15:04:05" in UTC`},
		{"space separated", &timestamppb.Timestamp{}, `"2024-01-02 15:04:05+02:00"`, `send "2024-01-02T15:04:05+02:00" for "2024-01-02 15:04:05+02:00"`},
		{"garbage timestamp", &timestamppb.Timestamp{}, `"yesterday"`, `timestamps are RFC 3339 strings with a time zone, e.g. "2024-01-02T15:04:05Z"`},
		{"snake mask", &fieldmaskpb.FieldMask{}, `"name,update_time"`, `send "updateTime" for "update_time"`},
		{"nested snake mask", &fieldmaskpb.FieldMask{}, `"owner.display_name"`, `send "owner.displayName" for "owner.display_name"`},
		{"other error", &wrapperspb.StringValue{}, `7`, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := protojson.Unmarshal([]byte(tc.json), tc.msg)
			if tc.want == "" {
				if got := withJSONHint(err); !errors.Is(got, err) || (err != nil && got.Error() != err.Error()) {
					t.Errorf("withJSONHint(%v) = %v, want it unchanged", err, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("protojson accepted %s", tc.json)
			}
			got := withJSONHint(err)
			if !errors.Is(got, err) {
				t.Errorf("hinted error does not wrap protojson's: %v", got)
			}
			if !strings.HasPrefix(got.Error(), err.Error()+" (") || !strings.Contains(got.Error(), tc.want) {
				t.Errorf("withJSONHint = %q, want %q + a hint containing %q", got, err, tc.want)
			}
		})
	}
	if withJSONHint(nil) != nil {
		t.Error("withJSONHint(nil) must be nil")
	}
}

// The spelling the duration hint suggests is one protojson reads back as the
// same duration.
func TestProtoDurationRoundTrips(t *testing.T) {
	for _, d := range []time.Duration{0, time.Nanosecond, 250 * time.Millisecond, time.Hour, -90 * time.Second, -1500 * time.Millisecond, 1<<63 - 1, -1 << 63} {
		var got durationpb.Duration
		if err := protojson.Unmarshal([]byte(`"`+protoDuration(d)+`"`), &got); err != nil {
			t.Fatalf("%v → %q: %v", d, protoDuration(d), err)
		}
		if got.AsDuration() != d {
			t.Errorf("%v → %q → %v", d, protoDuration(d), got.AsDuration())
		}
	}
}
