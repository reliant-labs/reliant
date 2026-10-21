// Copyright (c) 2025 Reliant Labs

// Package app is reliant's composition root: the composition graph (forge's
// docs/design/composition-graph.md) every server role is a root of.
//
//   - roots_<role>.go: the //forge:root structs — what a process builds. Each
//     is a command (cmd/reliant/cmd/roots_gen.go, generated), run under the
//     process policy in cmd/reliant/cmd/serve.go.
//   - providers.go: the infrastructure every root shares (the database, NATS,
//     the token authority, TLS, pprof).
//   - nodes_<role>.go: a role's own nodes, mostly adapters that give an
//     existing component the graph's lifecycle signatures.
//   - compose.go: one constructor method per node, holding every edge.
//   - graph_gen.go, routes_gen.go: generated.
//
// The gateway is a root today; `reliant server api` and `reliant server
// worker` still construct themselves (internal/serverapi, internal/
// serverworker) and become roots next (research/forge-migration/
// 09-roots-plan.md, R4 and R5).
package app
