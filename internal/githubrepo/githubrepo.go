// Copyright (c) 2026 Reliant Labs

// Package githubrepo reads which github.com repository a git remote URL
// names.
package githubrepo

import (
	"regexp"
	"strings"
)

// Of is the github.com repository a git remote URL names, as owner and repo
// lower-cased, or ok false when the remote is not one of the three spellings
// GitHub documents for it:
//
//	https://[credentials@]github.com[:443]/owner/repo[.git][/]
//	ssh://git@github.com[:22]/owner/repo[.git][/]
//	git@github.com:owner/repo[.git][/]
//
// Only github.com is recognised because it is the one host whose repository
// identity is global and proven: one namespace on every transport, owner and
// repository names unique regardless of case. Any other remote — another
// host, port, transport or ssh user, a self-hosted server whose hostname
// resolves differently on another machine, a local path — is not one.
func Of(raw string) (owner, repo string, ok bool) {
	s := strings.TrimSpace(raw)
	var host, path string
	switch {
	case strings.HasPrefix(s, "https://"):
		host, path, _ = strings.Cut(strings.TrimPrefix(s, "https://"), "/")
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:] // credentials never name the repository
		}
		host = strings.TrimSuffix(host, ":443")
	case strings.HasPrefix(s, "ssh://"):
		var user string
		var ok bool
		host, path, _ = strings.Cut(strings.TrimPrefix(s, "ssh://"), "/")
		if user, host, ok = strings.Cut(host, "@"); !ok || user != "git" {
			return "", "", false
		}
		host = strings.TrimSuffix(host, ":22")
	case strings.HasPrefix(s, "git@"):
		var ok bool
		if host, path, ok = strings.Cut(strings.TrimPrefix(s, "git@"), ":"); !ok {
			return "", "", false
		}
	default:
		return "", "", false
	}
	if !strings.EqualFold(host, "github.com") {
		return "", "", false
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	owner, repo, ok = strings.Cut(path, "/")
	if !ok || !name.MatchString(owner) || !name.MatchString(repo) || owner[0] == '.' || repo == "." || repo == ".." {
		return "", "", false
	}
	return strings.ToLower(owner), strings.ToLower(repo), true
}

// name is the character set of a GitHub owner or repository name.
var name = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
