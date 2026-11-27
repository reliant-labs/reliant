// Copyright (c) 2025 Reliant Labs

// Package gatehost is the daemon half of the merge queue's gate
// (internal/mergequeue/gaterun is the server half and the wire). It runs on a
// machine of the repository's gate pool, never on a server role: it execs
// git and checks out pull-request code (internal/architecture's
// TestServerRolesRunNoCode).
//
// [Jobs] over a [Runner] runs the gate's three steps (pipeline redesign
// §3.3, "Gate isolation"). Only the middle one runs pull-request code:
//
//  1. fetch, outside the boundary and credentialed: `reliant-queue/<batch>`
//     at the exact candidate sha into a fresh per-gate directory
//     ([GitFetcher]). The credential is per invocation; none is written into
//     the directory.
//  2. gate, inside a [Backend]: the stage script (script.go) runs lint →
//     test → build → sbom_scan from the repository's resolved stages,
//     stopping at the first stage that does not pass, and writes
//     gate-result.json.
//  3. report, outside: the output directory is read back through
//     Backend.CopyOut as a tar stream, as data only (report.go): size-capped,
//     never extracted, links refused, nothing executed. Then Teardown.
//
//forge:exclude-contract: the daemon's gate command handler drives Jobs directly; its seams are the Backend and Fetcher interfaces it is handed, not a service of its own
package gatehost
