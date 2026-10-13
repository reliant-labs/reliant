// Copyright (c) 2025 Reliant Labs

// Package gitremote handles git remote URLs as server-side code sees them:
// strings that may carry a credential.
//
//forge:exclude-contract: pure string transformation of a git remote URL; no I/O, state or time
package gitremote

import "net/url"

// Redacted returns a git remote/clone URL with any userinfo removed, so
// a token embedded as https://<token>@host/owner/repo never reaches the logs.
// scp-style remotes (git@host:owner/repo) carry no credential and pass through.
func Redacted(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.User = nil
	return u.String()
}
