// Package genai gives a forge app's model calls, agent turns, tool calls and
// workflow runs the span shape of the OpenTelemetry GenAI semantic
// conventions, so the built-in AI dashboards (the console's AI hub, built by
// pkg/obsbuiltin over otel_traces) draw them with nothing else to configure.
//
// The contract is the attribute names in attrs.go: gen_ai.* from the
// conventions, and forge.ai.* for the workflow, run, node, tool location and a
// gateway-reported cost, which the conventions do not name yet. Any library
// that emits the conventions feeds the same dashboards; these helpers exist
// because neither the official anthropic-go nor openai-go client emits spans.
//
// # The span tree
//
//	invoke_workflow <workflow>        StartWorkflow   one segment of a run
//	  workflow.node <node>            StartNode
//	    invoke_agent <agent>          StartAgent      one agent turn
//	      chat <model>                StartChat       one model call
//	      execute_tool <tool>         StartTool       one tool call
//
// Every level is optional: an app that only calls a model needs StartChat
// alone. Each Start* puts what it names (the workflow, the run, the node, the
// agent) into the returned context, and every span started from that context
// carries it, so a chat span three levels down still answers "which workflow,
// which run, which node" without a join. WithScope adds the caller's own
// (AttrUserID, AttrOrgID).
//
// # Calling Anthropic (github.com/anthropics/anthropic-sdk-go)
//
//	ctx, span := genai.StartChat(ctx, genai.Chat{Provider: "anthropic", Model: string(params.Model)})
//	msg, err := client.Messages.New(ctx, params)
//	if err == nil {
//		u := msg.Usage
//		genai.SetResponse(span, genai.Response{
//			Model: string(msg.Model), ID: msg.ID, FinishReasons: []string{string(msg.StopReason)},
//			// Anthropic's input_tokens leaves out cache reads and writes;
//			// the conventions count them in.
//			Usage: genai.Usage{
//				InputTokens:              u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
//				OutputTokens:             u.OutputTokens,
//				CacheReadInputTokens:     u.CacheReadInputTokens,
//				CacheCreationInputTokens: u.CacheCreationInputTokens,
//			},
//		})
//	}
//	genai.End(span, err)
//
// # Calling OpenAI (github.com/openai/openai-go)
//
//	ctx, span := genai.StartChat(ctx, genai.Chat{Provider: "openai", Model: params.Model})
//	resp, err := client.Chat.Completions.New(ctx, params)
//	if err == nil {
//		r := genai.Response{Model: resp.Model, ID: resp.ID, Usage: genai.Usage{
//			InputTokens:           resp.Usage.PromptTokens,
//			OutputTokens:          resp.Usage.CompletionTokens,
//			CacheReadInputTokens:  resp.Usage.PromptTokensDetails.CachedTokens,
//			ReasoningOutputTokens: resp.Usage.CompletionTokensDetails.ReasoningTokens,
//		}}
//		for _, c := range resp.Choices {
//			r.FinishReasons = append(r.FinishReasons, c.FinishReason)
//		}
//		genai.SetResponse(span, r)
//	}
//	genai.End(span, err)
//
// Pass the span's ctx to the client call: the client's HTTP spans (an
// observe.NewHTTPClient transport, or option.WithHTTPClient) then nest under
// the chat span, retries included. For a streamed response, call FirstToken
// when the first chunk arrives and SetResponse from the accumulated message.
// A gateway that reports the call's cost (LiteLLM's usage.cost) goes in
// Response.CostUSD; forge never estimates one.
//
// # Sampling
//
// AI spans are never sampled away: they are few (one per model call) and
// every token total is a sum over them. observe.Setup wraps the trace sampler
// in KeepSampler, which keeps any span that starts with gen_ai.operation.name
// or a forge.ai.* attribute, so pass those at start (every Start* helper
// does). An app that builds its own tracer provider wraps its sampler itself:
//
//	sdktrace.NewTracerProvider(sdktrace.WithSampler(genai.KeepSampler(mySampler)), ...)
//
// # Content
//
// No helper records a prompt or a completion. Content capture is a later,
// opt-in phase with its own storage and redaction; until then a span carries
// metadata only (models, tokens, timings, tool names, errors).
package genai
