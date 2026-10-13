// Copyright (c) 2025 Reliant Labs
//
// Package localprobe probes an OpenAI-compatible model server: it lists the
// server's models, detects the server family (Ollama, LM Studio, llama.cpp,
// vLLM) and asks the family's own metadata endpoints what /v1/models cannot
// say (context window, vision, tools, thinking).
//
// forge:outbound-io
//
// Service.Probe is the adapter's one outbound operation, and it owns the wire
// format of every family's endpoints. The *http.Client in Deps decides where
// the bytes go: a plain client for a server the daemon dials directly, an
// SSRF-guarded one for a user-supplied URL in a hosted deployment. The
// parsers (ParseModelIDs, ParseOllamaShow, …), NormalizeRoot and EndpointID
// stay free functions: they do no I/O.
//
//forge:not-a-component: built by reliant's hand-wired model-endpoint manager (internal/modelendpoints) and the daemon's local model prober (internal/toolexec/daemonruntime), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package localprobe

import (
	"context"
	"net/http"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Service probes model servers.
type Service interface {
	// Probe asks the server at t what it serves. It never returns nil: a
	// server that does not answer yields an endpoint with Error set and no
	// models.
	Probe(ctx context.Context, id string, t Target, opt Options) *reliantv1.LocalModelEndpoint
}

// Deps configures the adapter.
type Deps struct {
	// HTTPClient carries every request, and so decides which destinations a
	// probe may reach. Nil means a plain &http.Client{}.
	HTTPClient *http.Client
}
