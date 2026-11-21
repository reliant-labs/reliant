package project

import (
	"strings"

	"github.com/reliant-labs/forge/pkg/release"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/controlplane"
)

// THE LINK RULE (control-plane docs/design/unified-shell-and-projects.md
// §8.1–§8.2): a project the caller may read, in org O, contains O's app N
// when one of its repos holds a forge.yaml whose `name` is N. Deploying from
// that checkout targets exactly that app (the org plus the forge name), so
// the name in the project's own org IS the link. A recorded deploy source
// from the same canonical remote and subpath makes it SOURCE; the name alone
// is NAME; a name the org has no app of is NOT_HOSTED.
//
// Nothing here takes a project id from control-plane or from a request: the
// projects are the ones the caller's authorization already listed, and an
// app's sources are compared by remote and subpath only.

// projectAppRef is one forge.yaml in a listed project's repos.
type projectAppRef struct {
	Name    string
	RepoID  string
	Subpath string
	// Remote is the repo's canonical remote (forgeRemote); "" when it
	// has no shareable one, which can never match a source.
	Remote string
}

// linkedApp is how one projectAppRef links to the org's apps.
type linkedApp struct {
	Link reliantv1.ProjectAppLink
	// Hosted is the org's app of the ref's name; nil when NOT_HOSTED.
	Hosted *controlplane.HostedApp
}

// linkProjectApps links each listed project's forge apps (refs, one slice
// per project) to the org's apps. appsKnown is false when the org's apps
// could not be read: every ref is then NOT_HOSTED and nothing is unlinked.
// links[i][j] is refs[i][j]'s link; unlinked is the org's apps no ref names,
// in the order the control plane listed them.
func linkProjectApps(refs [][]projectAppRef, apps []controlplane.HostedApp, appsKnown bool) (links [][]linkedApp, unlinked []controlplane.HostedApp) {
	byName := make(map[string]*controlplane.HostedApp, len(apps))
	if appsKnown {
		for i := range apps {
			byName[apps[i].Name] = &apps[i]
		}
	}
	contained := make(map[string]bool)
	links = make([][]linkedApp, len(refs))
	for i, project := range refs {
		links[i] = make([]linkedApp, len(project))
		for j, ref := range project {
			contained[ref.Name] = true
			app, ok := byName[ref.Name]
			switch {
			case !ok:
				links[i][j] = linkedApp{Link: reliantv1.ProjectAppLink_PROJECT_APP_LINK_NOT_HOSTED}
			case deployedFrom(app, ref):
				links[i][j] = linkedApp{Link: reliantv1.ProjectAppLink_PROJECT_APP_LINK_SOURCE, Hosted: app}
			default:
				links[i][j] = linkedApp{Link: reliantv1.ProjectAppLink_PROJECT_APP_LINK_NAME, Hosted: app}
			}
		}
	}
	if !appsKnown {
		return links, nil
	}
	for _, app := range apps {
		if !contained[app.Name] {
			unlinked = append(unlinked, app)
		}
	}
	return links, unlinked
}

// deployedFrom reports whether app records a source from ref's checkout: the
// same canonical remote (case-insensitive) at the same subpath. A ref with
// no remote matches nothing, a source with none (a local folder) neither.
func deployedFrom(app *controlplane.HostedApp, ref projectAppRef) bool {
	if ref.Remote == "" {
		return false
	}
	for _, s := range app.Sources {
		if s.Remote != "" && strings.EqualFold(s.Remote, ref.Remote) && normalizeSubpath(s.Subpath) == normalizeSubpath(ref.Subpath) {
			return true
		}
	}
	return false
}

// forgeRemote is a git remote URL in forge's canonical "host/owner/repo"
// form (release.CanonicalRepo, what forge records a deploy's source as), or
// "" when it names no shareable repository: empty, or a local path.
func forgeRemote(url string) string {
	c := release.CanonicalRepo(url)
	if c == "" || strings.HasPrefix(c, "/") || strings.HasPrefix(c, ".") || !strings.Contains(c, "/") {
		return ""
	}
	return c
}

// normalizeSubpath is a forge.yaml directory in one spelling:
// "/"-separated, no leading "./" or "/", no trailing "/", "" for the root.
func normalizeSubpath(p string) string {
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
