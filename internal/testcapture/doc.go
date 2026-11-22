// Package testcapture records a project's own test lanes: `forge test begin`
// allocates a run directory and `forge test capture` runs one lane's command
// unchanged, storing what it produced beside it. The layout and the two
// manifests are pkg/testreport's (manifest.go); this package writes them.
//
// It never decides how tests run, and it never changes a verdict. The command
// is the project's own (its Taskfile's flags), its exit status is capture's,
// and a record that cannot be written is a warning: a lane must never fail, or
// pass, because of the thing recording it.
//
//forge:exclude-contract: plumbing behind two CLI verbs (Begin, Capture) plus the value types they use (Renderer, Scrubber); its one consumer is internal/cli and nothing substitutes it, so a Service interface would be indirection
package testcapture
