package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// `obs logs --trace` reads the range from the trace itself: a trace that ran
// two days ago, older than the 15-minute default, still finds its lines, and
// the text shows the cause the JSON carries. A named range wins; a trace with
// no span found is searched by id over the widest range.
func TestObsLogsTraceReadsTheTracesOwnRange(t *testing.T) {
	const id = "6cdcc2b8d409e05528b82b2687a58af2"
	ran := obsNow.Add(-48 * time.Hour)
	extent := func(spans int) scriptedAnswer {
		return scriptedAnswer{"count() AS spans", obsbackend.Result{Rows: [][]any{{jn(fmt.Sprint(spans)), ran.Format("2006-01-02 15:04:05.000000000"), jn(fmt.Sprint(ran.Add(30 * time.Second).UnixNano()))}}}}
	}
	failed := scriptedAnswer{"otel_logs", obsbackend.Result{Rows: [][]any{{ran.Add(30 * time.Second).Format("2006-01-02 15:04:05.000000000"), "warn", jn("13"), "api", "rpc failed", id, "29c6a57eee308d8d",
		map[string]any{"error": "deadline_exceeded: deadline exceeded", "cause": "update orders: timeout: context deadline exceeded", "code": "deadline_exceeded"}, map[string]any{}, jn("7")}}}}

	sr := &scriptedRunner{answers: []scriptedAnswer{extent(158_864), failed}}
	out, _, err := obsRun(t, devFacts(), sr, "logs", "dev", "--trace", id, "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	sc := doc["scope"].(map[string]any)
	if doc["trace_range"] != "spans" || sc["from"] != ran.Add(-time.Minute).Format(time.RFC3339Nano) || sc["to"] != ran.Add(90*time.Second).Format(time.RFC3339Nano) {
		t.Errorf("trace_range %v, scope %v", doc["trace_range"], sc)
	}
	if len(sr.ran) != 2 || !strings.Contains(sr.ran[0], "count() AS spans") {
		t.Errorf("the trace's extent is read first, then its logs: %v", sr.ran)
	}

	text, _, err := obsRun(t, devFacts(), &scriptedRunner{answers: []scriptedAnswer{extent(158_864), failed}}, "logs", "dev", "--trace", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`rpc failed  error="deadline_exceeded: deadline exceeded" cause="update orders: timeout: context deadline exceeded" code=deadline_exceeded`,
		"(1 log line of trace " + id + ", ",
		": the trace's own range, from its spans, a minute either side; --since / --from override it)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}

	// A named range is the caller's: no extent lookup.
	sr = &scriptedRunner{answers: []scriptedAnswer{extent(158_864), failed}}
	out, _, err = obsRun(t, devFacts(), sr, "logs", "dev", "--trace", id, "--since", "1h", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if doc := decode(t, out); doc["trace_range"] != nil || len(sr.ran) != 1 || strings.Contains(sr.ran[0], "count() AS spans") {
		t.Errorf("--since keeps its range: %v %v", doc["trace_range"], sr.ran)
	}

	// No span of it: the widest range the logs allow, by id.
	sr = &scriptedRunner{answers: []scriptedAnswer{extent(0), failed}}
	out, _, err = obsRun(t, devFacts(), sr, "logs", "dev", "--trace", id, "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc = decode(t, out)
	sc = doc["scope"].(map[string]any)
	if doc["trace_range"] != "wide" || sc["from"] != obsNow.Add(-obsquery.MaxRangeLogs).Format(time.RFC3339Nano) {
		t.Errorf("trace_range %v, scope %v", doc["trace_range"], sc)
	}
}
