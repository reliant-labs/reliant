// Package httpaction runs a manifest action declaratively over HTTP: render the
// request from params, enforce the host rules, call out through the SSRF guard,
// and select/shape the response. It never makes a request the manifest's
// connection did not allow, and never one to a non-public address.
//
// forge:outbound-io
//
// A runner calls the integration's API at the manifest connection's base_url
// (and the hosts it allows), through netguard's dialer, authenticated by the
// Credential its source resolves.
//
//forge:not-a-component: a library, not a component — each caller builds its own *Runner with NewRunner(netguard.New()) where it is used (internal/app's NewInputOptions, the workflow action and git-publish activities, the agent's integration tools, the webhook pollers), most of them outside the composition graph; no process holds one shared instance
package httpaction

import "net/http"

// DialGuard builds the transport every request goes through. *netguard.Guard
// satisfies it: its dialer refuses any connection to a non-public address
// after DNS resolution.
type DialGuard interface {
	Transport() *http.Transport
}
