package obsbuiltin

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/observe/genai"
)

// fixtureSpan is one otel_traces row of the AI fixture.
type fixtureSpan struct {
	at       string // "2026-03-15 10:00:00.000"
	trace    string
	name     string
	ms       uint64
	status   string // "Unset", "Error"
	message  string
	env      string // reliant.env.id; "" is e1
	attrs    map[string]string
	events   []fixtureEvent
	resource map[string]string
}

type fixtureEvent struct {
	offsetMs int
	name     string
	attrs    map[string]string
}

func chMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, lit(k)+": "+lit(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// aiSeedSQL is the INSERT of spans into db.otel_traces.
func aiSeedSQL(db string, spans []fixtureSpan) string {
	var b strings.Builder
	b.WriteString("INSERT INTO " + db + ".otel_traces (Timestamp, TraceId, SpanId, ParentSpanId, SpanName, SpanKind, ServiceName, " +
		"ResourceAttributes, SpanAttributes, Duration, StatusCode, StatusMessage, `Events.Timestamp`, `Events.Name`, `Events.Attributes`) VALUES\n")
	for i, s := range spans {
		env := s.env
		if env == "" {
			env = "e1"
		}
		res := map[string]string{"service.name": "worker", "reliant.env.id": env}
		for k, v := range s.resource {
			res[k] = v
		}
		status := s.status
		if status == "" {
			status = "Unset"
		}
		at, err := time.Parse("2006-01-02 15:04:05.000", s.at)
		if err != nil {
			panic(err)
		}
		var evTs, evNames, evAttrs []string
		for _, e := range s.events {
			evTs = append(evTs, "toDateTime64("+lit(at.Add(time.Duration(e.offsetMs)*time.Millisecond).Format("2006-01-02 15:04:05.000"))+", 9)")
			evNames = append(evNames, lit(e.name))
			evAttrs = append(evAttrs, chMap(e.attrs))
		}
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "(%s, %s, %s, '', %s, 'Internal', 'worker', %s, %s, %d, %s, %s, [%s], [%s], [%s])",
			lit(s.at), lit(s.trace), lit(fmt.Sprintf("s%03d", i)), lit(s.name), chMap(res), chMap(s.attrs), s.ms*1_000_000,
			lit(status), lit(s.message), strings.Join(evTs, ", "), strings.Join(evNames, ", "), strings.Join(evAttrs, ", "))
	}
	b.WriteString(";\n")
	return b.String()
}

// with merges attribute maps, later wins.
func attrs(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// The AI fixture. The range is the 7 days ending 2026-03-16 12:00 UTC:
//
//	r1  triage, succeeded, 03-15 10:00, 60s, user u1 org o1: a node, an agent
//	    turn, an anthropic call served by a dated model (1000 in, of which 600
//	    cache reads, 200 out, $0.01 reported, a first token 500ms in), a grep
//	    tool on the daemon, and an openai call with two 429 backoffs (500 in,
//	    of which 100 cache reads in the older spelling, 100 out).
//	r2  triage, failed, 03-15 11:00, 10s, user u2 org o1: an anthropic call
//	    rate-limited (300 in), then a bash tool that failed.
//	r3  digest, succeeded, two segments in two traces (continue-as-new),
//	    03-14 09:00 to 12:00:30, user u1: one openai call each (100 in, 50 out).
//	r4  digest, cancelled, 03-14 15:00, 5s, user u2: a cancelled openai call
//	    (status Error but error.type canceled, 50 in).
//	    a model call outside any run, 03-13, 10 in, 5 out.
//	Never counted: a plain HTTP span, a call in e2, a call on 03-01.
func aiFixture() []fixtureSpan {
	u1 := map[string]string{genai.AttrUserID: "u1", genai.AttrOrgID: "o1"}
	u2 := map[string]string{genai.AttrUserID: "u2", genai.AttrOrgID: "o1"}
	run := func(wf, id string, who map[string]string) map[string]string {
		return attrs(who, map[string]string{genai.AttrWorkflowName: wf, genai.AttrRunID: id, genai.AttrRootRunID: id})
	}
	r1, r2, r3, r4 := run("triage", "r1", u1), run("triage", "r2", u2), run("digest", "r3", u1), run("digest", "r4", u2)
	chat := func(provider, model string, in, out int) map[string]string {
		return map[string]string{genai.AttrOperationName: genai.OpChat, genai.AttrProviderName: provider, genai.AttrRequestModel: model,
			genai.AttrUsageInputTokens: fmt.Sprint(in), genai.AttrUsageOutputTokens: fmt.Sprint(out)}
	}
	wf := map[string]string{genai.AttrOperationName: genai.OpInvokeWorkflow}
	node := map[string]string{genai.AttrNodeID: "classify", genai.AttrNodeType: "agent"}
	agent := map[string]string{genai.AttrAgentName: "triager"}
	tool := func(name, loc string) map[string]string {
		return map[string]string{genai.AttrOperationName: genai.OpExecuteTool, genai.AttrToolName: name, genai.AttrToolLocation: loc}
	}
	backoff := map[string]string{genai.AttrHTTPStatusCode: "429", genai.AttrRetryReason: "rate_limit"}
	return []fixtureSpan{
		{at: "2026-03-15 10:00:00.000", trace: "t1", name: "invoke_workflow triage", ms: 60_000, attrs: attrs(r1, wf)},
		{at: "2026-03-15 10:00:01.000", trace: "t1", name: "workflow.node classify", ms: 30_000, attrs: attrs(r1, node)},
		{at: "2026-03-15 10:00:02.000", trace: "t1", name: "invoke_agent triager", ms: 20_000,
			attrs: attrs(r1, node, agent, map[string]string{genai.AttrOperationName: genai.OpInvokeAgent})},
		{at: "2026-03-15 10:00:03.000", trace: "t1", name: "chat claude-sonnet-5", ms: 2_000,
			attrs: attrs(r1, node, agent, chat("anthropic", "claude-sonnet-5", 1000, 200), map[string]string{
				genai.AttrResponseModel: "claude-sonnet-5-20261001", genai.AttrUsageCacheReadInputTokens: "600", genai.AttrCostUSD: "0.01"}),
			events: []fixtureEvent{{offsetMs: 500, name: genai.EventFirstToken}}},
		{at: "2026-03-15 10:00:06.000", trace: "t1", name: "execute_tool grep", ms: 1_000, attrs: attrs(r1, node, agent, tool("grep", "daemon"))},
		{at: "2026-03-15 10:00:08.000", trace: "t1", name: "chat gpt-5", ms: 1_000,
			attrs: attrs(r1, node, agent, chat("openai", "gpt-5", 500, 100), map[string]string{
				genai.AttrUsageCacheReadInputTokensLegacy: "100", genai.AttrRetries: "2"}),
			events: []fixtureEvent{{offsetMs: 100, name: genai.EventRetryWait, attrs: backoff}, {offsetMs: 300, name: genai.EventRetryWait, attrs: backoff}}},

		{at: "2026-03-15 11:00:00.000", trace: "t2", name: "invoke_workflow triage", ms: 10_000, status: "Error", message: "node failed", attrs: attrs(r2, wf)},
		{at: "2026-03-15 11:00:01.000", trace: "t2", name: "chat claude-sonnet-5", ms: 500, status: "Error", message: "429 too many",
			attrs: attrs(r2, chat("anthropic", "claude-sonnet-5", 300, 0), map[string]string{genai.AttrErrorType: "rate_limit"})},
		{at: "2026-03-15 11:00:05.000", trace: "t2", name: "execute_tool bash", ms: 2_000, status: "Error", message: "exit 1",
			attrs: attrs(r2, tool("bash", "daemon"), map[string]string{genai.AttrErrorType: "ExitError"})},

		{at: "2026-03-14 09:00:00.000", trace: "t3a", name: "invoke_workflow digest", ms: 60_000, attrs: attrs(r3, wf)},
		{at: "2026-03-14 09:00:10.000", trace: "t3a", name: "chat gpt-5", ms: 1_000, attrs: attrs(r3, chat("openai", "gpt-5", 100, 50))},
		{at: "2026-03-14 12:00:00.000", trace: "t3b", name: "invoke_workflow digest", ms: 30_000, attrs: attrs(r3, wf)},
		{at: "2026-03-14 12:00:10.000", trace: "t3b", name: "chat gpt-5", ms: 1_000, attrs: attrs(r3, chat("openai", "gpt-5", 100, 50))},

		{at: "2026-03-14 15:00:00.000", trace: "t4", name: "invoke_workflow digest", ms: 5_000,
			attrs: attrs(r4, wf, map[string]string{genai.AttrErrorType: genai.ErrorTypeCanceled})},
		{at: "2026-03-14 15:00:01.000", trace: "t4", name: "chat gpt-5", ms: 4_000, status: "Error", message: "context canceled",
			attrs: attrs(r4, chat("openai", "gpt-5", 50, 0), map[string]string{genai.AttrErrorType: genai.ErrorTypeCanceled})},

		{at: "2026-03-13 08:00:00.000", trace: "t5", name: "chat gpt-5", ms: 1_000, attrs: chat("openai", "gpt-5", 10, 5)},

		{at: "2026-03-15 10:00:00.000", trace: "t6", name: "GET /x", ms: 5, attrs: map[string]string{"http.route": "/x"}},
		{at: "2026-03-15 10:00:00.000", trace: "t7", name: "chat gpt-5", ms: 1_000, env: "e2", attrs: attrs(u1, chat("openai", "gpt-5", 9999, 9999))},
		{at: "2026-03-01 10:00:00.000", trace: "t8", name: "chat gpt-5", ms: 1_000, attrs: attrs(u1, chat("openai", "gpt-5", 9999, 9999))},
	}
}

// TestAIPanelsOnClickHouse runs every AI panel over the fixture and pins
// what the headline and breakdown panels answer.
func TestAIPanelsOnClickHouse(t *testing.T) {
	db := "forge_obsbuiltin_ai"
	ch := newClickHouse(t, db, aiSeedSQL(db, aiFixture()))
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	start := end.Add(-7 * 24 * time.Hour)
	rows := func(r ...string) string { return strings.Join(r, "\n") }
	byID := map[string]dashspec.Dashboard{}
	for _, d := range AIDashboards(Input{Project: "shop"}) {
		byID[d.ID] = d
		// Every panel runs.
		for _, p := range d.Panels {
			for _, sql := range []string{p.SQL, p.TrendSQL} {
				if sql == "" {
					continue
				}
				if _, err := ch.try(sql, start, end, "e1"); err != nil {
					t.Errorf("%s/%s: %v", d.ID, p.ID, err)
				}
			}
		}
	}
	check := func(dash, id string, trend bool, want string) {
		t.Helper()
		p := panel(t, byID[dash], id)
		sql := p.SQL
		if trend {
			sql = p.TrendSQL
		}
		if got := ch.run(sql, start, end, "e1"); got != want {
			t.Errorf("%s/%s (trend %v) =\n%s\nwant\n%s", dash, id, trend, got, want)
		}
	}

	// Overview: four runs (r3's two segments are one), two of three finished
	// runs succeeded (r4 was cancelled), seven calls, 2465 tokens, 700 of
	// 2060 input tokens from the cache, and the one reported cost.
	check(AIOverviewID, "runs", false, "4")
	check(AIOverviewID, "runs", true, rows("2026-03-14\t2", "2026-03-15\t2"))
	check(AIOverviewID, "success-rate", false, "0.6667")
	// Run wall times 5, 10, 60 and 10830 seconds (r3 spans both segments).
	check(AIOverviewID, "run-p50", false, "35")
	check(AIOverviewID, "run-p95", false, "9214.5")
	check(AIOverviewID, "calls", false, "7")
	check(AIOverviewID, "calls", true, rows("2026-03-13\t1", "2026-03-14\t3", "2026-03-15\t3"))
	check(AIOverviewID, "tokens", false, "2465")
	check(AIOverviewID, "cache-hit", false, "0.3398")
	check(AIOverviewID, "reported-cost", false, "0.01")
	check(AIOverviewID, "runs-by-outcome", false, rows("2026-03-14\tCancelled\t1", "2026-03-14\tSucceeded\t1", "2026-03-15\tFailed\t1", "2026-03-15\tSucceeded\t1"))
	check(AIOverviewID, "tokens-by-day", false, rows("2026-03-13\t10\t0\t5", "2026-03-14\t250\t0\t100", "2026-03-15\t1100\t700\t300"))

	// By workflow: triage ran twice (one failed), digest twice (one cancelled,
	// so 100% of its finished runs succeeded; its wall times are 5s and
	// 10830s). triage called three models once each: the tie goes to the
	// last by name.
	check(AIWorkflowsID, "workflows", false, rows(
		"digest\t2\t1\t5417.5\t10288.8\t1.5\t175\t350\t\\N\tgpt-5",
		"triage\t2\t0.5\t35\t57.5\t1.5\t1050\t2100\t0.01\tgpt-5"))
	check(AIWorkflowsID, "nodes", false, "triage\tclassify\t2\t1500\t300\t0\t1950")
	check(AIWorkflowsID, "agents", false, "triager\t1\t2\t1500\t300\t20000")

	// Models: the served model is the row. gpt-5's output speed leaves out
	// the cancelled call that produced nothing (205 tokens over 4s); only the
	// streamed anthropic call has a time to first token.
	check(AIModelsID, "models", false, rows(
		"openai\tgpt-5\t5\t0\t1000\t3400\t\\N\t51.2\t760\t205\t0.1316\t\\N",
		"anthropic\tclaude-sonnet-5\t1\t1\t500\t500\t\\N\t\\N\t300\t0\t0\t\\N",
		"anthropic\tclaude-sonnet-5-20261001\t1\t0\t2000\t2000\t500\t100\t1000\t200\t0.6\t0.01"))
	check(AIToolsID, "time-split", false, rows("triage\t3\t3.5", "digest\t0\t6"))
	// The alias resolved to a dated model.
	check(AIModelsID, "fallbacks", false, "claude-sonnet-5\tclaude-sonnet-5-20261001\t1")

	// Users: u1 made four calls in two runs, u2 two in two; one org.
	check(AIUsersID, "top-users", false, rows(
		"u1\t2\t4\t1700\t400\t0.01\t2026-03-15 10:00:08.000000000",
		"u2\t2\t2\t350\t0\t\\N\t2026-03-15 11:00:01.000000000"))
	check(AIUsersID, "top-orgs", false, "o1\t4\t6\t2050\t400\t0.01\t2026-03-15 11:00:01.000000000")

	// Errors: one real failure in seven calls (the cancelled call is none),
	// two retries, two 429 backoffs, and r2's first failing span.
	check(AIErrorsID, "error-rate", false, "0.1429")
	check(AIErrorsID, "retries", false, "2")
	check(AIErrorsID, "errors-by-type", false, "2026-03-15\trate_limit\t1")
	check(AIErrorsID, "backoffs", false, "2026-03-15\t429\t2")
	check(AIErrorsID, "failed-runs", false, "2026-03-15 11:00:00.000000000\ttriage\tr2\t10\tchat claude-sonnet-5\t429 too many\tt2")

	// Tools: grep ran on the daemon, bash failed, and bash is the tool of the
	// failed run.
	check(AIToolsID, "tools", false, rows("bash\t1\t1\t1\t2000\t2000\tdaemon", "grep\t1\t0\t0\t1000\t1000\tdaemon"))
	check(AIToolsID, "tools-in-failed-runs", false, "bash\t1\t1\t1")
}
