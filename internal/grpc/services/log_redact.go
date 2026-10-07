package services

import "net/url"

// remoteURLForLog returns a git remote/clone URL with any userinfo removed, so
// a token embedded as https://<token>@host/owner/repo never reaches the logs.
// scp-style remotes (git@host:owner/repo) carry no credential and pass through.
func remoteURLForLog(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.User = nil
	return u.String()
}
