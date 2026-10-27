// Package githubqueue registers the server-only github/pr.enqueue action.
// It requests GitHub's normal native queue, never merges or bypasses rules.
// GitHub permissions and required checks remain authoritative; this generic
// action does not attest reviewer identity or the coordinator's source binding.
// A private coordinator must enforce its server-owned review policy before use.
//
// forge:outbound-io
//
//forge:not-a-component: registered Go executor, not a composition component
package githubqueue

// Executor is the manifest's fixed Go executor name.
const Executor = "github.pr_enqueue"
