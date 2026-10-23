package storage

import (
	"sort"
	"strings"
)

// Resources we refuse to read rather than merely ignore. Secrets are never read
// at all; Events are high-volume churn that can only ever echo a reference some
// other resource already carries; ComponentStatus is a deprecated health probe
// with no spec, which cannot hold an image reference and makes kubectl warn on
// every listing. Everything else is scanned, because a kind allowlist is
// exactly how workspaces.reliant.dev images went unprotected.
var unscannedResources = map[string]bool{
	"secrets":              true,
	"events":               true,
	"events.events.k8s.io": true,
	"componentstatuses":    true,
}

// listableResources parses `kubectl api-resources --verbs=list -o name` into the
// set of resources to scan. Aggregated metrics resources are live gauges that
// cannot hold an image reference and are routinely unavailable, so including
// them would only let a flaky metrics-server permanently block cleanup.
func listableResources(out []byte) []string {
	seen := map[string]bool{}
	var kinds []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] || unscannedResources[name] {
			continue
		}
		if name == "metrics.k8s.io" || strings.HasSuffix(name, ".metrics.k8s.io") {
			continue
		}
		seen[name] = true
		kinds = append(kinds, name)
	}
	sort.Strings(kinds)
	return kinds
}

// resourceBatches groups resources into comma-joined `kubectl get` arguments:
// one call per batch instead of one per kind keeps a scan of ~80 resources to a
// handful of API round trips.
func resourceBatches(kinds []string, size int) [][]string {
	if size < 1 {
		size = 1
	}
	var batches [][]string
	for i := 0; i < len(kinds); i += size {
		end := i + size
		if end > len(kinds) {
			end = len(kinds)
		}
		batches = append(batches, kinds[i:end])
	}
	return batches
}

// refSeparator splits a JSON string value into reference-shaped tokens. A
// reference is often not the whole value: kubectl's last-applied-configuration
// annotation embeds a JSON document, and container args carry --image=<ref>.
func refSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', ',', '=', '{', '}', '[', ']', '(', ')', ';', '<', '>', '|', '\\':
		return true
	}
	return r < 0x20
}

// referenceCandidates walks every string in a decoded JSON document and returns
// the tokens that could name an image: bare sha256 digests and host-qualified
// repository references. It deliberately does not know which keys hold images,
// because a custom resource can nest one anywhere (spec.template.image on a
// Workspace). Host filtering against registered aliases happens in
// protectedRefs, so over-collecting here can only ever over-protect.
func referenceCandidates(value any) []string {
	seen := map[string]bool{}
	var refs []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		case string:
			for _, token := range strings.FieldsFunc(x, refSeparator) {
				token = normalizeRef(token)
				if !digestPattern.MatchString(token) && !looksLikeImageRef(token) {
					continue
				}
				if !seen[token] {
					seen[token] = true
					refs = append(refs, token)
				}
			}
		}
	}
	walk(value)
	return refs
}

// normalizeRef strips the scheme kubelet prepends to container image IDs
// (docker-pullable://, containerd://) so the remainder parses as a reference.
func normalizeRef(token string) string {
	if i := strings.Index(token, "://"); i >= 0 {
		token = token[i+3:]
	}
	return strings.Trim(token, ".:/@")
}

// looksLikeImageRef applies Docker's own rule for telling a registry host from a
// Docker Hub namespace: the first path component is a host only if it contains a
// dot or a colon, or is exactly localhost. Without that test, every relative path
// in the cluster's JSON ("docs/design.md", "usr/bin/env") reads as a reference.
// Registered aliases are always host or host:port, so nothing we could protect is
// excluded by it.
func looksLikeImageRef(token string) bool {
	host, path, ok := strings.Cut(token, "/")
	if !ok || host == "" || path == "" || strings.Contains(host, "@") {
		return false
	}
	if strings.ContainsAny(host, "*?") {
		return false
	}
	return host == "localhost" || strings.ContainsAny(host, ".:")
}

// protectedRefs reduces candidate reference strings to the protection map the
// retention plan consumes: repository -> {tag or digest}, with "*" holding bare
// digests that protect that content in every repository. References whose host
// is not a registered alias for this registry cannot describe its contents and
// are dropped.
func protectedRefs(refs []string, aliases []string) map[string]map[string]bool {
	protected := map[string]map[string]bool{}
	add := func(repo, version string) {
		if protected[repo] == nil {
			protected[repo] = map[string]bool{}
		}
		protected[repo][version] = true
	}
	for _, ref := range refs {
		if digestPattern.MatchString(ref) {
			add("*", ref)
			continue
		}
		host, path, ok := strings.Cut(ref, "/")
		if !ok || !contains(aliases, host) {
			continue
		}
		repo, version, ok := strings.Cut(path, "@")
		if ok {
			if i := strings.LastIndex(repo, ":"); i >= 0 {
				repo = repo[:i]
			}
			// A ref pinned by digest protects the content wherever it lives, and
			// the repository-qualified entry keeps the tagged alias alive too.
			add("*", version)
		} else {
			repo = path
			version = "latest"
			if i := strings.LastIndex(path, ":"); i >= 0 {
				repo, version = path[:i], path[i+1:]
			}
		}
		add(repo, version)
	}
	return protected
}
