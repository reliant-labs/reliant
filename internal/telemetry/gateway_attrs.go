// Copyright (c) 2025 Reliant Labs
package telemetry

// The names a model call routed through Reliant's LLM gateway (control-plane's
// LLM proxy, LiteLLM behind it) adds to its chat span. They live in their own
// file, beside ai_attrs.go, so the span families that are still being added do
// not all edit one const block. They are a public contract once released:
// never rename one in place, add the new name beside it.

// AttrGatewayCallID is the gateway's own id for the call: LiteLLM's
// x-litellm-call-id response header, the id control-plane's wallet ledger
// stores as the call's provider_request_id. It is set on every gateway-routed
// call that got a response, failed ones included, and never on a call to any
// other provider. It is the exact join from a span to the gateway's billed
// cost (dev-docs/design/agent-analytics.md §3.2, §4); gen_ai.response.id is
// the upstream provider's id (x-oai-request-id), a different value.
const AttrGatewayCallID = "reliant.gateway.call_id"
