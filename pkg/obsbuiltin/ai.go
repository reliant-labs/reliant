package obsbuiltin

import (
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/observe/genai"
)

// The AI hub: the built-in dashboards of a project's model calls, agent
// turns, tool calls and workflow runs, listed under dashspec.GroupAI. Every
// panel is one read-only SELECT over the tenant's otel_traces, reading the
// OpenTelemetry GenAI semantic conventions and forge's forge.ai.* names
// (pkg/observe/genai, which also emits them). There is no rollup: a panel
// reads raw spans, so the longest range it answers is the trace retention.
// A token total is a plain sum over the spans: AI spans are never sampled
// (genai.KeepSampler), so a stored one stands for itself.
//
// No figure is a price: tokens are shown as counted, and a cost only where a
// span carries the one its provider or gateway reported (forge.ai.cost.usd,
// or Reliant's earlier reliant.cost_usd), labelled as such. A range with no
// reported cost shows none, never $0.

// The ids of the AI hub's dashboards.
const (
	AIOverviewID  = "ai"
	AIWorkflowsID = "ai-workflows"
	AIModelsID    = "ai-models"
	AIUsersID     = "ai-users"
	AIErrorsID    = "ai-errors"
	AIToolsID     = "ai-tools"
)

// IsAIDashboard reports whether id is one of the AI hub's dashboards. Their
// sql panels read otel_traces, which a local ClickStack has too, but HyperDX
// cannot draw a sql panel, so they ship in the spec and the release bundle
// only, like the product dashboards.
func IsAIDashboard(id string) bool {
	switch id {
	case AIOverviewID, AIWorkflowsID, AIModelsID, AIUsersID, AIErrorsID, AIToolsID:
		return true
	}
	return false
}

// AIDashboards are the AI hub's dashboards for in: Overview, By workflow,
// Models & providers, Users, Errors & retries and Tools.
func AIDashboards(in Input) []dashspec.Dashboard {
	return []dashspec.Dashboard{AIOverview(in), AIWorkflows(in), AIModels(in), AIUsers(in), AIErrors(in), AITools(in)}
}

// The scope of an AI read: the dashboard's range and environments. The
// environment is the resource's reliant.env.id, as the Jobs and Releases
// dashboards read it, so the statement runs on every tenant schema version.
const (
	aiEnvScope = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id']))"
	aiScope    = inRange + " AND " + aiEnvScope
)

// attr is the SpanAttributes value of key.
func attr(key string) string { return "SpanAttributes['" + key + "']" }

// The span classes, each a predicate over one otel_traces row.
var (
	// aiOp is the span's gen_ai.operation.name.
	aiOp = attr(genai.AttrOperationName)
	// aiSpan is any AI span: an operation, or a workflow node (forge.ai.*
	// without one).
	aiSpan = "(" + aiOp + " != '' OR " + attr(genai.AttrNodeID) + " != '' OR " + attr(genai.AttrRunID) + " != '')"
	// aiCall is a model call.
	aiCall = aiOp + " IN " + lits([]string{genai.OpChat, genai.OpTextCompletion, genai.OpGenerateContent, genai.OpEmbeddings})
	// aiTool is a tool call; aiAgent an agent turn; aiWorkflow a workflow
	// segment.
	aiTool     = aiOp + " = " + lit(genai.OpExecuteTool)
	aiAgent    = aiOp + " = " + lit(genai.OpInvokeAgent)
	aiWorkflow = aiOp + " = " + lit(genai.OpInvokeWorkflow)
	// aiError is a failed span. A cancellation is error.type canceled and
	// never a failure, whatever its status says.
	aiError = "(StatusCode IN ('Error', 'STATUS_CODE_ERROR') AND " + attr(genai.AttrErrorType) + " != " + lit(genai.ErrorTypeCanceled) + ")"
)

// The values an AI read groups and sums on.
var (
	// aiModel is the model that served the call, else the one asked for.
	aiModel = "if(" + attr(genai.AttrResponseModel) + " != '', " + attr(genai.AttrResponseModel) + ", " + attr(genai.AttrRequestModel) + ")"
	// aiProvider is gen_ai.provider.name, else the older gen_ai.system.
	aiProvider = "if(" + attr(genai.AttrProviderName) + " != '', " + attr(genai.AttrProviderName) + ", " + attr(genai.AttrSystem) + ")"
	// The token counts. The cache counts read both draft spellings.
	aiIn         = "toUInt64OrZero(" + attr(genai.AttrUsageInputTokens) + ")"
	aiOut        = "toUInt64OrZero(" + attr(genai.AttrUsageOutputTokens) + ")"
	aiCacheRead  = "toUInt64OrZero(" + either(genai.AttrUsageCacheReadInputTokens, genai.AttrUsageCacheReadInputTokensLegacy) + ")"
	aiCacheWrite = "toUInt64OrZero(" + either(genai.AttrUsageCacheCreationInputTokens, genai.AttrUsageCacheCreationInputTokensLegacy) + ")"
	aiReasoning  = "toUInt64OrZero(" + attr(genai.AttrUsageReasoningOutputTokens) + ")"
	// aiCost is the reported cost, NULL where none was reported: a sum over
	// spans with none is NULL, drawn as no figure rather than 0.
	aiCost = "toFloat64OrNull(" + either(genai.AttrCostUSD, genai.AttrCostUSDReliant) + ")"
	// aiWorkflowName, aiRun are the span's workflow and run.
	aiWorkflowName = attr(genai.AttrWorkflowName)
	aiRun          = attr(genai.AttrRunID)
	// aiMs is the span's duration in milliseconds.
	aiMs = "Duration / 1e6"
	// aiTTFTMs is the time to the first token in milliseconds, NULL when the
	// span has no gen_ai.first_token event (not streamed, or not reported).
	aiTTFTMs = "if(indexOf(Events.Name, " + lit(genai.EventFirstToken) + ") = 0, NULL, " +
		"(toUnixTimestamp64Nano(Events.Timestamp[indexOf(Events.Name, " + lit(genai.EventFirstToken) + ")]) - toUnixTimestamp64Nano(Timestamp)) / 1e6)"
)

// either is the first of two attribute spellings that is set.
func either(key, fallback string) string {
	return "if(" + attr(key) + " != '', " + attr(key) + ", " + attr(fallback) + ")"
}

// aiSpans is the AI spans in scope with the columns a read needs, as a CTE.
var aiSpansCTE = `ai AS (
  SELECT Timestamp AS ts, TraceId, SpanName, StatusCode, StatusMessage, Duration,
         ` + aiOp + ` AS op, ` + aiCall + ` AS is_call, ` + aiTool + ` AS is_tool, ` + aiError + ` AS failed,
         ` + aiModel + ` AS model, ` + aiProvider + ` AS provider,
         ` + aiIn + ` AS in_tok, ` + aiOut + ` AS out_tok, ` + aiCacheRead + ` AS cache_read_tok,
         ` + aiCost + ` AS cost, ` + aiWorkflowName + ` AS workflow, ` + aiRun + ` AS run_id,
         ` + attr(genai.AttrErrorType) + ` AS error_type, ` + attr(genai.AttrToolName) + ` AS tool,
         ` + attr(genai.AttrUserID) + ` AS user_id, ` + attr(genai.AttrOrgID) + ` AS org_id
  FROM otel_traces
  WHERE ` + aiScope + ` AND ` + aiSpan + `)`

// aiRunsCTE is one row per run seen in the range (it needs aiSpansCTE): when
// it started and ended across every segment, its outcome (its last workflow
// segment's), and what it spent. A run whose workflow span is not reported
// yet (still running, or an emitter that opens none) is "In progress".
const aiRunsCTE = `runs AS (
  SELECT run_id, anyIf(workflow, workflow != '') AS wf,
         min(ts) AS started, max(addNanoseconds(ts, Duration)) AS ended,
         (toUnixTimestamp64Nano(ended) - toUnixTimestamp64Nano(started)) / 1e9 AS seconds,
         countIf(op = 'invoke_workflow') AS segments,
         multiIf(segments = 0, 'In progress',
                 argMaxIf(error_type, ts, op = 'invoke_workflow') = 'canceled', 'Cancelled',
                 argMaxIf(failed, ts, op = 'invoke_workflow'), 'Failed', 'Succeeded') AS outcome,
         countIf(is_call) AS calls, countIf(is_tool) AS tools,
         sumIf(in_tok, is_call) AS run_in, sumIf(out_tok, is_call) AS run_out, sumIf(cost, is_call) AS run_cost
  FROM ai WHERE run_id != '' GROUP BY run_id)`

// successRate is the share of finished runs (succeeded or failed; cancelled
// and in-progress runs are neither) that succeeded, over runs.
const successRate = "round(countIf(outcome = 'Succeeded') / nullIf(countIf(outcome IN ('Succeeded', 'Failed')), 0), 4)"

// aiFilters is the hub's one dropdown: a run spans services (an API that
// starts it, a worker that runs it), so a service filter would split it.
func aiFilters() []dashspec.Filter { return envFilter() }

// aiDashboard is a hub dashboard's frame.
func aiDashboard(in Input, id, title, desc string, sections []dashspec.Section) dashspec.Dashboard {
	return dashspec.Dashboard{
		ID: id, Title: in.Project + ": " + title, Description: desc + " " + aiAbout,
		Group: dashspec.GroupAI, Tags: []string{"ai"}, Filters: aiFilters(), Sections: sections, DefaultRange: "7d",
	}
}

// aiAbout ends every hub dashboard's description.
const aiAbout = "From the GenAI spans in the environment's traces, so the range is bounded by their retention."

// The colours the hub keeps: calls and runs blue, input tokens cyan, cache
// violet, output amber, cost fuchsia, failures danger.
const (
	colorCalls  = dashspec.ColorBlue
	colorInput  = dashspec.ColorCyan
	colorCache  = dashspec.ColorViolet
	colorOutput = dashspec.ColorAmber
	colorCost   = dashspec.ColorFuchsia
)

// daily marks a sql panel that buckets by UTC day.
func daily(p dashspec.Panel) dashspec.Panel {
	p.MinStep = "1d"
	return p
}

// ---- Overview ---------------------------------------------------------------

// AIOverview is the hub's landing dashboard: runs, their success and
// duration, model calls, tokens and the reported cost, and each by day.
func AIOverview(in Input) dashspec.Dashboard {
	d := aiDashboard(in, AIOverviewID, "AI overview",
		"Workflow runs, how many succeeded and how long they took, model calls, tokens and the gateway-reported cost, and each by day.",
		[]dashspec.Section{
			{ID: "glance", Title: "At a glance", Description: "The range's runs and model calls. Each figure opens beside its change against the previous period."},
			{ID: "daily", Title: "By day", Description: "Runs by outcome, model calls and tokens per UTC day."},
		})
	d.DefaultCompare = dashspec.ComparePeriod
	d.Panels = []dashspec.Panel{
		exact(figure("glance", "runs", "Runs",
			"Distinct workflow runs (forge.ai.run.id) with any span in the range. A run that continues in a new trace stays one run. The trend is each UTC day's.",
			with(aiSpansCTE, aiRunsCTE)+"\nSELECT count() AS \"Runs\" FROM runs",
			with(aiSpansCTE, aiRunsCTE)+"\nSELECT toDate(started) AS day, count() AS \"Runs\" FROM runs GROUP BY day ORDER BY day",
			dashspec.UnitCount, colorCalls)),
		figure("glance", "success-rate", "Run success rate",
			"Succeeded runs over finished ones (succeeded or failed), by the status of each run's last workflow segment. Cancelled runs, and runs whose workflow span is not reported yet, are neither.",
			with(aiSpansCTE, aiRunsCTE)+"\nSELECT "+successRate+" AS \"Success rate\" FROM runs", "", dashspec.UnitPercent, dashspec.ColorBlue),
		figure("glance", "run-p50", "Run duration (p50)",
			"The median run's wall time, from its first span to the end of its last, across every segment. Time a run spent paused (an approval, a wait) is included.",
			with(aiSpansCTE, aiRunsCTE)+"\nSELECT round(quantile(0.5)(seconds), 1) AS \"p50\" FROM runs WHERE outcome != 'In progress'", "", dashspec.UnitSeconds, colorNeutral),
		figure("glance", "run-p95", "Run duration (p95)",
			"The 95th percentile run's wall time, measured as the p50.",
			with(aiSpansCTE, aiRunsCTE)+"\nSELECT round(quantile(0.95)(seconds), 1) AS \"p95\" FROM runs WHERE outcome != 'In progress'", "", dashspec.UnitSeconds, colorNeutral),
		exact(figure("glance", "calls", "Model calls",
			"Model calls (gen_ai.operation.name chat, text_completion, generate_content or embeddings) in the range, in a run or not. The trend is each UTC day's.",
			"SELECT count() AS \"Model calls\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall,
			"SELECT toDate(Timestamp) AS day, count() AS \"Model calls\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall+" GROUP BY day ORDER BY day",
			dashspec.UnitCount, colorCalls)),
		figure("glance", "tokens", "Tokens",
			"Input and output tokens of the range's model calls (gen_ai.usage.input_tokens + output_tokens). The trend is each UTC day's.",
			"SELECT sum("+aiIn+" + "+aiOut+") AS \"Tokens\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall,
			"SELECT toDate(Timestamp) AS day, sum("+aiIn+" + "+aiOut+") AS \"Tokens\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall+" GROUP BY day ORDER BY day",
			dashspec.UnitCount, colorInput),
		figure("glance", "cache-hit", "Prompt cache hit ratio",
			"Input tokens read from the provider's prompt cache over all input tokens. Providers that report no cache reads count as 0.",
			"SELECT round(sum("+aiCacheRead+") / nullIf(sum("+aiIn+"), 0), 4) AS \"Cache hit\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall,
			"", dashspec.UnitPercent, colorCache),
		figure("glance", "reported-cost", "Gateway-reported cost (USD)",
			"The sum of the cost the provider or gateway reported on each model call (forge.ai.cost.usd), in US dollars. Calls with no reported cost "+
				"(a provider called directly with your own key) add nothing, and a range with none shows no figure. Never an estimate, and not an invoice.",
			"SELECT round(sum("+aiCost+"), 4) AS \"Reported cost\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall, "", dashspec.UnitNone, colorCost),

		daily(chart("daily", "runs-by-outcome", "Runs by outcome",
			"Runs by the UTC day they started and how they ended: succeeded, failed, cancelled, or in progress (the workflow span is not reported yet).",
			with(aiSpansCTE, aiRunsCTE)+`
SELECT toDate(started) AS day, outcome, count() AS "Runs"
FROM runs GROUP BY day, outcome ORDER BY day, outcome`,
			bars(true, dashspec.SeriesColors{"Succeeded": colorCalls, "Failed": colorDanger, "Cancelled": colorNeutral, "In progress": colorCache}), 12, 8)),
		daily(chart("daily", "calls-by-day", "Model calls by provider",
			"Model calls each UTC day by provider (gen_ai.provider.name).",
			`SELECT toDate(Timestamp) AS day, `+aiProvider+` AS provider, count() AS "Calls"
FROM otel_traces WHERE `+aiScope+` AND `+aiCall+`
GROUP BY day, provider ORDER BY day, provider`, bars(true, nil), 12, 8)),
		daily(chart("daily", "tokens-by-day", "Tokens by day",
			"Each UTC day's input tokens (less cache reads), cache reads and output tokens.",
			`SELECT toDate(Timestamp) AS day,
       sum(`+aiIn+`) - least(sum(`+aiCacheRead+`), sum(`+aiIn+`)) AS "Input",
       sum(`+aiCacheRead+`) AS "Cache read", sum(`+aiOut+`) AS "Output"
FROM otel_traces WHERE `+aiScope+` AND `+aiCall+`
GROUP BY day ORDER BY day`,
			bars(true, dashspec.SeriesColors{"Input": colorInput, "Cache read": colorCache, "Output": colorOutput}), 24, 8)),
	}
	return d
}

// ---- By workflow ------------------------------------------------------------

// AIWorkflows breaks the range down by workflow, then by node and agent.
func AIWorkflows(in Input) dashspec.Dashboard {
	d := aiDashboard(in, AIWorkflowsID, "AI by workflow",
		"Each workflow's runs, success, duration, model calls and tokens per run, and within a workflow, each node and agent.",
		[]dashspec.Section{
			{ID: "workflows", Title: "Workflows", Description: "One row per workflow (forge.ai.workflow.name). Model calls outside any workflow are the (none) row."},
			{ID: "inside", Title: "Inside a workflow", Description: "The model calls of each node (forge.ai.node.id) and agent (gen_ai.agent.name)."},
		})
	d.Panels = []dashspec.Panel{
		table("workflows", "workflows", "Workflows",
			"Runs, the success rate of finished runs, run duration, and model calls, tokens and reported cost per run, with the model the workflow called most (ties go to the last by name).",
			with(aiSpansCTE, aiRunsCTE, `top AS (
  SELECT workflow, argMax(model, (n, model)) AS top_model
  FROM (SELECT workflow, model, count() AS n FROM ai WHERE is_call GROUP BY workflow, model)
  GROUP BY workflow)`)+`
SELECT if(r.wf = '', '(none)', r.wf) AS "Workflow", count() AS "Runs", `+successRate+` AS "Success rate",
       round(quantileIf(0.5)(seconds, outcome != 'In progress'), 1) AS "p50 s",
       round(quantileIf(0.95)(seconds, outcome != 'In progress'), 1) AS "p95 s",
       round(avg(calls), 1) AS "Calls / run", round(avg(run_in + run_out)) AS "Tokens / run",
       sum(run_in + run_out) AS "Tokens", round(sum(run_cost), 4) AS "Reported cost (USD)",
       any(t.top_model) AS "Top model"
FROM runs AS r LEFT JOIN top AS t ON t.workflow = r.wf
GROUP BY r.wf ORDER BY "Runs" DESC, "Workflow" LIMIT 50`, 24, 9),
		daily(chart("workflows", "runs-by-workflow", "Runs by workflow",
			"Runs each UTC day by workflow: the ten workflows with the most runs in the range, the rest as Other.",
			with(aiSpansCTE, aiRunsCTE, `top AS (SELECT wf FROM runs GROUP BY wf ORDER BY count() DESC, wf LIMIT 10)`)+`
SELECT toDate(started) AS day, if(wf IN (SELECT wf FROM top), if(wf = '', '(none)', wf), 'Other') AS workflow, count() AS "Runs"
FROM runs GROUP BY day, workflow ORDER BY day, workflow`, bars(true, nil), 24, 8)),
		table("inside", "nodes", "Nodes",
			"Each node's model calls, tokens and failed calls, by workflow. A node a loop revisits counts every visit.",
			`SELECT if(`+aiWorkflowName+` = '', '(none)', `+aiWorkflowName+`) AS "Workflow", `+attr(genai.AttrNodeID)+` AS "Node",
       count() AS "Calls", sum(`+aiIn+`) AS "Input tokens", sum(`+aiOut+`) AS "Output tokens",
       countIf(`+aiError+`) AS "Failed calls", round(quantile(0.95)(`+aiMs+`)) AS "p95 ms"
FROM otel_traces WHERE `+aiScope+` AND `+aiCall+` AND `+attr(genai.AttrNodeID)+` != ''
GROUP BY "Workflow", "Node" ORDER BY "Calls" DESC LIMIT 100`, 12, 9),
		table("inside", "agents", "Agents",
			"Each agent's turns (invoke_agent spans), and the model calls and tokens under them, by agent name (gen_ai.agent.name).",
			`SELECT `+attr(genai.AttrAgentName)+` AS "Agent",
       countIf(`+aiAgent+`) AS "Turns", countIf(`+aiCall+`) AS "Calls",
       sumIf(`+aiIn+`, `+aiCall+`) AS "Input tokens", sumIf(`+aiOut+`, `+aiCall+`) AS "Output tokens",
       round(quantileIf(0.95)(`+aiMs+`, `+aiAgent+`)) AS "Turn p95 ms"
FROM otel_traces WHERE `+aiScope+` AND `+attr(genai.AttrAgentName)+` != '' AND (`+aiAgent+` OR `+aiCall+`)
GROUP BY "Agent" ORDER BY "Calls" DESC LIMIT 50`, 12, 9),
	}
	return d
}

// ---- Models & providers -----------------------------------------------------

// AIModels breaks model calls down by provider and model: tokens, calls,
// latency, time to first token, errors, and fallbacks.
func AIModels(in Input) dashspec.Dashboard {
	d := aiDashboard(in, AIModelsID, "AI models & providers",
		"Every model call by provider and model: tokens and calls by day, latency, time to first token, output speed, errors, the cache, and calls a gateway served with another model.",
		[]dashspec.Section{
			{ID: "usage", Title: "Usage", Description: "Tokens and calls by model, per UTC day."},
			{ID: "models", Title: "Models", Description: "One row per provider and the model that served the call (gen_ai.response.model, else the one asked for)."},
		})
	d.Panels = []dashspec.Panel{
		daily(chart("usage", "tokens-by-model", "Tokens by model",
			"Input and output tokens each UTC day by model: the eight models with the most tokens in the range, the rest as Other.",
			with(`calls AS (SELECT Timestamp AS ts, `+aiModel+` AS model, `+aiIn+` + `+aiOut+` AS tok FROM otel_traces WHERE `+aiScope+` AND `+aiCall+`)`,
				`top AS (SELECT model FROM calls GROUP BY model ORDER BY sum(tok) DESC, model LIMIT 8)`)+`
SELECT toDate(ts) AS day, if(model IN (SELECT model FROM top), model, 'Other') AS model, sum(tok) AS "Tokens"
FROM calls GROUP BY day, model ORDER BY day, model`, bars(true, nil), 12, 8)),
		daily(chart("usage", "calls-by-model", "Calls by model",
			"Model calls each UTC day by model: the eight models called most in the range, the rest as Other.",
			with(`calls AS (SELECT Timestamp AS ts, `+aiModel+` AS model FROM otel_traces WHERE `+aiScope+` AND `+aiCall+`)`,
				`top AS (SELECT model FROM calls GROUP BY model ORDER BY count() DESC, model LIMIT 8)`)+`
SELECT toDate(ts) AS day, if(model IN (SELECT model FROM top), model, 'Other') AS model, count() AS "Calls"
FROM calls GROUP BY day, model ORDER BY day, model`, bars(true, nil), 12, 8)),
		table("models", "models", "Providers and models",
			"Calls, failed calls, latency (p50, p95), time to first token of streamed calls (p50; a gen_ai.first_token event), output tokens per second of call time, "+
				"tokens, the prompt cache hit ratio, and the gateway-reported cost.",
			`SELECT `+aiProvider+` AS "Provider", `+aiModel+` AS "Model", count() AS "Calls",
       round(countIf(`+aiError+`) / count(), 4) AS "Error rate",
       round(quantile(0.5)(`+aiMs+`)) AS "p50 ms", round(quantile(0.95)(`+aiMs+`)) AS "p95 ms",
       round(quantile(0.5)(`+aiTTFTMs+`)) AS "TTFT p50 ms",
       round(sum(`+aiOut+`) / nullIf(sumIf(Duration, `+aiOut+` > 0) / 1e9, 0), 1) AS "Output tok/s",
       sum(`+aiIn+`) AS "Input tokens", sum(`+aiOut+`) AS "Output tokens",
       round(sum(`+aiCacheRead+`) / nullIf(sum(`+aiIn+`), 0), 4) AS "Cache hit",
       round(sum(`+aiCost+`), 4) AS "Reported cost (USD)"
FROM otel_traces WHERE `+aiScope+` AND `+aiCall+`
GROUP BY "Provider", "Model" ORDER BY "Calls" DESC LIMIT 50`, 24, 9),
		table("models", "fallbacks", "Served by another model",
			"Calls a provider or gateway answered with a model other than the one asked for (gen_ai.request.model against gen_ai.response.model): a fallback, or an alias resolved to a dated model.",
			`SELECT `+attr(genai.AttrRequestModel)+` AS "Asked for", `+attr(genai.AttrResponseModel)+` AS "Served by", count() AS "Calls"
FROM otel_traces WHERE `+aiScope+` AND `+aiCall+`
  AND `+attr(genai.AttrRequestModel)+` != '' AND `+attr(genai.AttrResponseModel)+` != '' AND `+attr(genai.AttrRequestModel)+` != `+attr(genai.AttrResponseModel)+`
GROUP BY "Asked for", "Served by" ORDER BY "Calls" DESC LIMIT 50`, 24, 7),
	}
	return d
}

// ---- Users ------------------------------------------------------------------

// AIUsers breaks the range down by the user and the org the calls were made
// for (user.id, org.id: the ids product analytics uses).
func AIUsers(in Input) dashspec.Dashboard {
	d := aiDashboard(in, AIUsersID, "AI by user",
		"Who the model calls were made for: the users and orgs with the most calls and tokens, by their opaque ids (user.id, org.id; genai.WithScope stamps them).",
		[]dashspec.Section{
			{ID: "users", Title: "Users", Description: "Calls without a user.id are left out."},
			{ID: "orgs", Title: "Orgs", Description: "Calls without an org.id are left out."},
		})
	d.Panels = []dashspec.Panel{
		daily(chart("users", "users-by-day", "Users with model calls",
			"Distinct users with a model call each UTC day.",
			`SELECT toDate(Timestamp) AS day, uniqExact(`+attr(genai.AttrUserID)+`) AS "Users"
FROM otel_traces WHERE `+aiScope+` AND `+aiCall+` AND `+attr(genai.AttrUserID)+` != ''
GROUP BY day ORDER BY day`, bars(false, dashspec.SeriesColors{"Users": colorUsers}), 24, 7)),
		table("users", "top-users", "Top users",
			"Users by tokens in the range: their runs, model calls, tokens, reported cost and when they last made a call.",
			aiByWho(genai.AttrUserID, "User"), 24, 9),
		table("orgs", "top-orgs", "Top orgs",
			"Orgs by tokens in the range, measured as the users.",
			aiByWho(genai.AttrOrgID, "Org"), 24, 9),
	}
	return d
}

// aiByWho is the top-N table of model calls by the id in key, titled title.
func aiByWho(key, title string) string {
	return `SELECT ` + attr(key) + ` AS "` + title + `", uniqExactIf(` + aiRun + `, ` + aiRun + ` != '') AS "Runs", count() AS "Calls",
       sum(` + aiIn + `) AS "Input tokens", sum(` + aiOut + `) AS "Output tokens",
       round(sum(` + aiCost + `), 4) AS "Reported cost (USD)", max(Timestamp) AS "Last call"
FROM otel_traces WHERE ` + aiScope + ` AND ` + aiCall + ` AND ` + attr(key) + ` != ''
GROUP BY "` + title + `" ORDER BY "Input tokens" + "Output tokens" DESC, "` + title + `" LIMIT 50`
}

// ---- Errors & retries -------------------------------------------------------

// AIErrors is the failures: model calls by error class, provider and model,
// retries, and the failed runs with their first failing span.
func AIErrors(in Input) dashspec.Dashboard {
	d := aiDashboard(in, AIErrorsID, "AI errors & retries",
		"Failed model calls by class (error.type), provider and model; retries and backoffs; and failed runs with the span that failed first. "+
			"A cancellation is never a failure.",
		[]dashspec.Section{
			{ID: "calls", Title: "Model calls", Description: "Failed calls and retries."},
			{ID: "runs", Title: "Failed runs", Description: "Runs whose last workflow segment failed. A trace id opens the trace."},
		})
	d.DefaultCompare = dashspec.ComparePeriod
	d.Panels = []dashspec.Panel{
		figure("calls", "error-rate", "Call error rate",
			"Failed model calls over all model calls in the range. The trend is each UTC day's.",
			"SELECT round(countIf("+aiError+") / nullIf(count(), 0), 4) AS \"Error rate\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall,
			"SELECT toDate(Timestamp) AS day, round(countIf("+aiError+") / count(), 4) AS \"Error rate\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall+" GROUP BY day ORDER BY day",
			dashspec.UnitPercent, colorDanger),
		exact(figure("calls", "retries", "Retries",
			"Retries before the range's model calls returned (gen_ai.retries), summed. The trend is each UTC day's.",
			"SELECT sum(toUInt64OrZero("+attr(genai.AttrRetries)+")) AS \"Retries\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall,
			"SELECT toDate(Timestamp) AS day, sum(toUInt64OrZero("+attr(genai.AttrRetries)+")) AS \"Retries\" FROM otel_traces WHERE "+aiScope+" AND "+aiCall+" GROUP BY day ORDER BY day",
			dashspec.UnitCount, colorNeutral)),
		daily(chart("calls", "errors-by-type", "Failed calls by class",
			"Failed model calls each UTC day by error.type; a span that names none is unclassified.",
			`SELECT toDate(Timestamp) AS day, if(`+attr(genai.AttrErrorType)+` = '', 'unclassified', `+attr(genai.AttrErrorType)+`) AS class, count() AS "Failed calls"
FROM otel_traces WHERE `+aiScope+` AND `+aiCall+` AND `+aiError+`
GROUP BY day, class ORDER BY day, class`, bars(true, nil), 12, 8)),
		daily(chart("calls", "backoffs", "Backoffs by status",
			"Backoff waits before a retry (gen_ai.retry_wait events on model calls) each UTC day, by the status that caused them.",
			`SELECT toDate(Timestamp) AS day, multiIf(attrs['`+genai.AttrHTTPStatusCode+`'] NOT IN ('', '0'), attrs['`+genai.AttrHTTPStatusCode+`'],
              attrs['`+genai.AttrRetryReason+`'] != '', attrs['`+genai.AttrRetryReason+`'], 'unknown') AS status,
       count() AS "Backoffs"
FROM otel_traces ARRAY JOIN Events.Name AS ev, Events.Attributes AS attrs
WHERE `+aiScope+` AND `+aiCall+` AND ev = `+lit(genai.EventRetryWait)+`
GROUP BY day, status ORDER BY day, status`, bars(true, nil), 24, 7)),
		table("calls", "errors-by-model", "Failures by provider and model",
			"Calls, failed calls, the error rate, the most frequent error class and the retries per call, by provider and model.",
			`SELECT `+aiProvider+` AS "Provider", `+aiModel+` AS "Model", count() AS "Calls", countIf(`+aiError+`) AS "Failed",
       round("Failed" / count(), 4) AS "Error rate",
       topKIf(1)(`+attr(genai.AttrErrorType)+`, `+aiError+`)[1] AS "Top class",
       round(avg(toUInt64OrZero(`+attr(genai.AttrRetries)+`)), 2) AS "Retries / call"
FROM otel_traces WHERE `+aiScope+` AND `+aiCall+`
GROUP BY "Provider", "Model" HAVING "Failed" > 0 OR "Retries / call" > 0
ORDER BY "Failed" DESC, "Calls" DESC LIMIT 50`, 24, 8),
		table("runs", "failed-runs", "Failed runs",
			"The range's failed runs, newest first: the workflow, when it started, how long it ran, and the first model or tool call in it that failed (its span name and status message) with its trace.",
			with(aiSpansCTE, aiRunsCTE, `first_failure AS (
  SELECT run_id, argMin(SpanName, ts) AS failed_span, argMin(StatusMessage, ts) AS failed_message, argMin(TraceId, ts) AS failed_trace
  FROM ai WHERE failed AND (is_call OR is_tool) AND run_id != '' GROUP BY run_id)`)+`
SELECT r.started AS "Started", if(r.wf = '', '(none)', r.wf) AS "Workflow", r.run_id AS "Run",
       round(r.seconds, 1) AS "Seconds", f.failed_span AS "First failure", f.failed_message AS "Message", f.failed_trace AS "Trace"
FROM runs AS r LEFT JOIN first_failure AS f ON f.run_id = r.run_id
WHERE r.outcome = 'Failed'
ORDER BY r.started DESC LIMIT 50`, 24, 9),
	}
	return d
}

// ---- Tools ------------------------------------------------------------------

// AITools breaks tool calls (execute_tool spans) down by tool.
func AITools(in Input) dashspec.Dashboard {
	d := aiDashboard(in, AIToolsID, "AI tools",
		"Every tool call (an execute_tool span) by tool: calls, failures, latency and where it ran; how a run's time divides between tools and model calls; and the tools of failed runs.",
		[]dashspec.Section{
			{ID: "tools", Title: "Tools", Description: "One row per tool (gen_ai.tool.name)."},
			{ID: "runs", Title: "In runs", Description: "Tool calls inside workflow runs."},
		})
	d.Panels = []dashspec.Panel{
		table("tools", "tools", "Tools",
			"Calls, failed calls, the error rate, latency (p50, p95) and where each tool ran (forge.ai.tool.location: local, daemon, remote).",
			`SELECT `+attr(genai.AttrToolName)+` AS "Tool", count() AS "Calls", countIf(`+aiError+`) AS "Failed",
       round("Failed" / count(), 4) AS "Error rate",
       round(quantile(0.5)(`+aiMs+`)) AS "p50 ms", round(quantile(0.95)(`+aiMs+`)) AS "p95 ms",
       arrayStringConcat(arraySort(groupUniqArrayIf(`+attr(genai.AttrToolLocation)+`, `+attr(genai.AttrToolLocation)+` != '')), ', ') AS "Ran in"
FROM otel_traces WHERE `+aiScope+` AND `+aiTool+`
GROUP BY "Tool" ORDER BY "Calls" DESC, "Tool" LIMIT 100`, 24, 9),
		daily(chart("tools", "tool-calls-by-day", "Tool calls by tool",
			"Tool calls each UTC day: the ten tools called most in the range, the rest as Other.",
			with(`calls AS (SELECT Timestamp AS ts, `+attr(genai.AttrToolName)+` AS tool FROM otel_traces WHERE `+aiScope+` AND `+aiTool+`)`,
				`top AS (SELECT tool FROM calls GROUP BY tool ORDER BY count() DESC, tool LIMIT 10)`)+`
SELECT toDate(ts) AS day, if(tool IN (SELECT tool FROM top), tool, 'Other') AS tool, count() AS "Calls"
FROM calls GROUP BY day, tool ORDER BY day, tool`, bars(true, nil), 24, 8)),
		chart("runs", "time-split", "Where run time goes",
			"Time inside runs spent in tool calls against model calls, by workflow: the sum of each span's duration, so calls that overlap count twice.",
			with(aiSpansCTE)+`
SELECT if(workflow = '', '(none)', workflow) AS workflow,
       round(sumIf(Duration, is_tool) / 1e9, 1) AS "Tools (s)", round(sumIf(Duration, is_call) / 1e9, 1) AS "Model calls (s)"
FROM ai WHERE run_id != '' AND (is_tool OR is_call)
GROUP BY workflow ORDER BY "Tools (s)" + "Model calls (s)" DESC LIMIT 15`,
			bars(true, dashspec.SeriesColors{"Tools (s)": colorOutput, "Model calls (s)": colorCalls}), 12, 8),
		table("runs", "tools-in-failed-runs", "Tools in failed runs",
			"The tools called most in the range's failed runs, with how many of those calls failed themselves.",
			with(aiSpansCTE, aiRunsCTE)+`
SELECT tool AS "Tool", count() AS "Calls", countIf(failed) AS "Failed", uniqExact(run_id) AS "Failed runs"
FROM ai WHERE is_tool AND run_id IN (SELECT run_id FROM runs WHERE outcome = 'Failed')
GROUP BY tool ORDER BY "Calls" DESC, tool LIMIT 25`, 12, 8),
	}
	return d
}
