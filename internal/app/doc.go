// Copyright (c) 2025 Reliant Labs

// Package app is reliant's composition root: the composition graph (forge's
// docs/design/composition-graph.md) every server role is a root of.
//
//   - roots_<role>.go: the //forge:root structs — what a process builds. Each
//     is a command (cmd/reliant/cmd/roots_gen.go, generated), run under the
//     process policy in cmd/reliant/cmd/serve.go.
//   - providers.go: the infrastructure the roots share (the database, NATS,
//     Temporal, the hubs, the vault, the token authority, TLS, pprof, the
//     process-wide settings).
//   - nodes_<role>.go / providers_<role>.go: a role's own nodes — adapters
//     that give an existing component the graph's lifecycle signatures, and
//     the role's variants of a component (their own types, so no other root
//     can be handed them).
//   - compose.go: one constructor method per node, holding every edge.
//   - graph_gen.go, routes_gen.go: generated.
//
// Every server role is a root: `reliant server api` (roots_api.go),
// `reliant server worker` (roots_worker.go) and `reliant server gateway`
// (roots_gateway.go).
package app
