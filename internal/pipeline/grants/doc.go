// Copyright (c) 2025 Reliant Labs

// Package grants is the pipeline's permission model: the owner grants named
// capabilities to one workflow activation (a triggers row), on one machine
// pool, and a run holds a capability only while all of this is true
// (Authorize):
//
//   - the run is a run of that activation, as its chat's launch event says;
//   - it executes on a machine that is a member of the granted pool, judged
//     by the labels the machine's owner set on it (daemons.labels), never by
//     the labels it reports when it connects;
//   - the grant is live (not revoked), and its owner is the owner of the
//     activation and of the pool, read again on every use.
//
// The capabilities are approve (a model review verdict the run writes
// counts: pr.verdict@1) and land (the merge queue may squash-merge, with the
// server-only Queue App key, a head such a verdict admitted). No daemon ever
// holds merge power: land is checked by the server, before it merges.
//
// A grant is made only by the owner approving a card in chat (RequestGrant,
// the reliant/pipeline.request_grant@1 server action, raises it; the
// approval service calls GrantApproved), so it works from a phone and is
// never automatic. Revoking needs no card: it only takes power away.
//
// Nothing here runs unless the server's pipeline.enabled switch is on: the
// server-action dispatcher and PipelineService refuse every call before
// this package is reached.
//
//forge:exclude-contract: the Authorizer seam is declared here by hand for its consumers (pr.verdict, the merge queue's lander); every collaborator is a consumer-declared interface in deps.go
package grants
