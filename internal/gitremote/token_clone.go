package gitremote

import "strings"

// TokenCloneURL is the URL to clone remote over when git authenticates with
// the user's GitHub token: the https form of a github.com SSH remote
// (git@github.com:o/r, ssh://git@github.com/o/r), since a token
// authenticates https only and SSH transport never consults a credential
// helper. A daemon's clone helper is bound to https://github.com alone
// (daemonruntime gitCredentialURL), so every other remote — https already,
// another host's SSH, a local path — is returned unchanged and keeps
// whatever credential the machine has for it (its own SSH key).
//
//	git@github.com:acme/shop.git        -> https://github.com/acme/shop.git
//	ssh://git@github.com:22/acme/shop   -> https://github.com/acme/shop.git
//	git@gitlab.com:acme/shop.git        -> git@gitlab.com:acme/shop.git
//	https://github.com/acme/shop        -> https://github.com/acme/shop
func TokenCloneURL(remote string) string {
	trimmed := strings.TrimSpace(remote)
	var hostAndPath string
	switch lower := strings.ToLower(trimmed); {
	case strings.HasPrefix(lower, "ssh://"), strings.HasPrefix(lower, "git+ssh://"), strings.HasPrefix(lower, "ssh+git://"):
		hostAndPath = trimmed[strings.Index(trimmed, "://")+3:]
		if at := strings.Index(hostAndPath, "@"); at >= 0 && at < strings.Index(hostAndPath+"/", "/") {
			hostAndPath = hostAndPath[at+1:]
		}
	case strings.Contains(trimmed, "://"):
		return remote
	default:
		// scp-like: [user@]github.com:owner/repo. A path with a ":" in a
		// later segment, or a Windows drive, is not one.
		colon := strings.Index(trimmed, ":")
		if colon < 0 || strings.Contains(trimmed[:colon], "/") || strings.Contains(trimmed[:colon], `\`) {
			return remote
		}
		host := trimmed[:colon]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		hostAndPath = host + "/" + strings.TrimPrefix(trimmed[colon+1:], "/")
	}
	slash := strings.Index(hostAndPath, "/")
	if slash < 0 {
		return remote
	}
	host := hostAndPath[:slash]
	if c := strings.LastIndex(host, ":"); c >= 0 {
		host = host[:c] // an SSH port means nothing over https
	}
	if !strings.EqualFold(host, "github.com") {
		return remote
	}
	repoPath := strings.Trim(strings.TrimSuffix(strings.TrimRight(hostAndPath[slash:], "/"), ".git"), "/")
	if strings.Count(repoPath, "/") != 1 {
		return remote // not owner/repo
	}
	return "https://github.com/" + repoPath + ".git"
}
