package cli

import (
	"fmt"
	"testing"
)

// `obs query --json` returns the rows as selected: an array in SELECT order,
// and an empty result is [] — never null, which an agent reads as "no field".
func TestObsQueryJSONRowsAreAlwaysAnArray(t *testing.T) {
	const procQueryPanel = "controlplane.v1.ObservabilityService/QueryPanel"
	for _, c := range []struct {
		name, reply, want string
	}{
		{"no rows", `{"draw":"table","sqlColumns":[{"name":"TraceId"},{"name":"SpanName"}]}`, "[]"},
		{"no numeric column", `{"draw":"table","sqlColumns":[{"name":"Timestamp","time":true},{"name":"TraceId"},{"name":"SpanName"}],
			"rows":[{"group":{"Timestamp":"2026-10-22T11:00:00Z","TraceId":"t1","SpanName":"GET /a"}}]}`, "[[2026-10-22T11:00:00Z t1 GET /a]]"},
	} {
		cc := &echoingCaller{fakeCloudCaller: fakeCloudCaller{replies: map[string]string{procQueryPanel: c.reply}}}
		out, err := obsRunHostedDev(t, cc, "query", "dev", "--sql", "SELECT Timestamp, TraceId, SpanName FROM otel_traces", "--json", "--all-instances")
		if err != nil {
			t.Fatalf("%s: %v\n%s", c.name, err, out)
		}
		rows, ok := decode(t, out)["rows"].([]any)
		if !ok {
			t.Fatalf("%s: rows is not an array:\n%s", c.name, out)
		}
		if got := fmt.Sprint(rows); got != c.want {
			t.Errorf("%s: rows = %s, want %s", c.name, got, c.want)
		}
	}
}
