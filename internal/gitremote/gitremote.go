// Copyright (c) 2025 Reliant Labs

// Package gitremote handles git remote URLs as server-side code sees them:
// strings that may carry a credential.
//
//forge:exclude-contract: pure string transformation of a git remote URL; no I/O, state or time
package gitremote

import (
	"net/url"
	"strings"
)

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

// StripCredentials returns a remote URL that is safe to store, return or log:
// a secret in the userinfo is removed. Git authenticates through the
// credential helper, never through the URL, so the stored form is always the
// clean one.
//
//   - http(s), git:// and any other scheme: the whole userinfo goes. Both
//     `user:token@` and a bare `token@` are credentials there.
//   - ssh://, git+ssh://, ssh+git://: only a password goes; the user (`git@`)
//     names an account, not a secret.
//   - scp-style (git@host:o/r), file://, local paths and URLs with no
//     userinfo: unchanged.
//
// It edits the string rather than parsing and re-serialising the URL, so a
// value url.Parse rejects (a token with a space in it) is still cleaned and
// nothing else about the remote is rewritten.
func StripCredentials(raw string) string {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd <= 0 || !validScheme(raw[:schemeEnd]) {
		return raw
	}
	scheme := strings.ToLower(raw[:schemeEnd])
	authStart := schemeEnd + len("://")
	authEnd := len(raw)
	if i := strings.IndexAny(raw[authStart:], "/?#"); i >= 0 {
		authEnd = authStart + i
	}
	authority := raw[authStart:authEnd]
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return raw
	}
	userinfo, host := authority[:at], authority[at+1:]

	switch scheme {
	case "ssh", "git+ssh", "ssh+git":
		user, _, hasPassword := strings.Cut(userinfo, ":")
		if !hasPassword {
			return raw
		}
		return raw[:authStart] + user + "@" + host + raw[authEnd:]
	default:
		return raw[:authStart] + host + raw[authEnd:]
	}
}

// validScheme is RFC 3986's scheme grammar, so a local path that happens to
// contain "://" is not read as a URL.
func validScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// StripCredentialsPtr is StripCredentials for a nullable column. A nil
// pointer stays nil; a pointer to an unchanged value is returned as is.
func StripCredentialsPtr(raw *string) *string {
	if raw == nil {
		return nil
	}
	clean := StripCredentials(*raw)
	if clean == *raw {
		return raw
	}
	return &clean
}

// HasCredentials reports whether raw carries a secret StripCredentials would
// remove.
func HasCredentials(raw string) bool {
	return StripCredentials(raw) != raw
}
