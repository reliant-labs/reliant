package gitremote

import "strings"

// Canonical is a git remote URL in forge's canonical "host/owner/repo" form —
// what forge records a deploy's code source as (forge pkg/release
// CanonicalRepo) — or "" when it names no shareable repository: empty, or a
// local path. The scheme, any userinfo, a port, a trailing "/" and ".git" are
// dropped and the host is lowercased; the path keeps its case.
//
//	https://tok@GitHub.com/acme/shop.git  -> github.com/acme/shop
//	git@github.com:acme/shop.git          -> github.com/acme/shop
//	ssh://git@github.com:22/acme/shop     -> github.com/acme/shop
//	github.com/acme/shop                  -> github.com/acme/shop
//
// Compare two canonical remotes with strings.EqualFold, as forge's own app
// matching does: hosts fold paths case-insensitively.
func Canonical(url string) string {
	u := strings.TrimSpace(url)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
			u = u[at+1:]
		}
	} else if at := strings.Index(u, "@"); at >= 0 && strings.Contains(u[at:], ":") {
		// scp-like: git@github.com:owner/repo
		u = strings.Replace(u[at+1:], ":", "/", 1)
	}
	if i := strings.Index(u, "/"); i >= 0 {
		host := u[:i]
		if c := strings.LastIndex(host, ":"); c >= 0 {
			host = host[:c] // drop a port
		}
		u = strings.ToLower(host) + u[i:]
	}
	if u == "" || strings.HasPrefix(u, "/") || strings.HasPrefix(u, ".") || !strings.Contains(u, "/") {
		return ""
	}
	return u
}

// NormalizeSubpath is a forge.yaml directory in one spelling: "/"-separated,
// no leading "./" or "/", no trailing "/", "" for the repository root.
func NormalizeSubpath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	p = strings.Trim(p, "/")
	if p == "." {
		return ""
	}
	return p
}
