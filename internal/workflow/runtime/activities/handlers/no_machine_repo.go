// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// A run with no machine has no checkout, but a project whose code is on
// GitHub can still be read through the GitHub integration. noMachineRepoNote
// says where the code is and which tools read it, so the model reads the
// repository instead of asking the user for files.

// githubReadTools are the read-only GitHub actions that stand in for a
// checkout, in the order the note introduces them.
var githubReadTools = []struct{ name, use string }{
	{"github__repo_get", "metadata and the default branch"},
	{"github__repo_get_tree", "list files"},
	{"github__repo_get_content", "read a file or a directory"},
	{"github__code_search", "search code"},
}

// githubRepo is a project repository hosted on github.com. Dir is where it sits
// in the project ("" for the project root).
type githubRepo struct {
	Owner, Name, Dir string
}

func (r githubRepo) slug() string { return r.Owner + "/" + r.Name }

// The same shapes the github manifest accepts for owner and repo, so a remote
// this parses names a repository its tools can address.
var (
	githubOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	githubRepoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]*[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
)

// githubRepoFromRemote parses a git remote URL that points at github.com:
// https://github.com/o/r(.git), ssh://git@github.com/o/r, git@github.com:o/r,
// git://github.com/o/r, with or without credentials, a trailing slash or .git.
// Any other host — GitHub Enterprise included, which the integration does not
// reach — is not a match.
func githubRepoFromRemote(remote string) (owner, name string, ok bool) {
	remote = strings.TrimSpace(remote)
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if at := strings.Index(remote, "@"); at >= 0 && !strings.Contains(remote, "://") {
		// scp-like: [user@]host:owner/repo
		hostPart, rest, found := strings.Cut(remote[at+1:], ":")
		if !found {
			return "", "", false
		}
		host, path = hostPart, rest
	} else {
		return "", "", false
	}
	if host = strings.ToLower(host); host != "github.com" && host != "www.github.com" {
		return "", "", false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	owner, name, found := strings.Cut(path, "/")
	if !found || strings.Contains(name, "/") || !githubOwnerPattern.MatchString(owner) || !githubRepoPattern.MatchString(name) {
		return "", "", false
	}
	return owner, name, true
}

// projectGitHubRepos lists the distinct github.com repositories of a project:
// its own remote first, then each nested repository's.
func projectGitHubRepos(project *db.Project, repos []*core.Repo) []githubRepo {
	var out []githubRepo
	seen := map[string]bool{}
	add := func(remote *string, dir string) {
		if remote == nil {
			return
		}
		owner, name, ok := githubRepoFromRemote(*remote)
		if !ok {
			return
		}
		r := githubRepo{Owner: owner, Name: name, Dir: dir}
		if key := strings.ToLower(r.slug()); !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	if project != nil {
		add(project.RemoteURL, "")
	}
	for _, r := range repos {
		if r != nil {
			add(r.RemoteURL, r.RelativePath)
		}
	}
	return out
}

// githubReadable reports whether this turn can read GitHub: the reading tool
// is in hand, or load_tool is and may reach it. The scope's reach was recorded
// for this turn just before (getAvailableToolsWithSpawn), already narrowed to
// the integrations the owner can use.
func githubReadable(offered []tools.Tool, scope string) bool {
	const reader = "github__repo_get_content"
	canLoad := false
	for _, t := range offered {
		switch t.Name() {
		case reader:
			return true
		case tools.ToolLoadTool:
			canLoad = true
		}
	}
	return canLoad && tools.GetLoadedToolsStore().CanLoadTool(scope, reader)
}

// noMachineRepoNote is the no-machine note's addendum for a project on GitHub,
// or "" when the project has no GitHub remote.
func noMachineRepoNote(repos []githubRepo, readable bool) string {
	if len(repos) == 0 {
		return ""
	}
	var where string
	if len(repos) == 1 && repos[0].Dir == "" {
		where = "This project's code is on GitHub at github.com/" + repos[0].slug()
	} else {
		parts := make([]string, len(repos))
		for i, r := range repos {
			parts[i] = "github.com/" + r.slug()
			if r.Dir != "" {
				parts[i] += " (" + r.Dir + "/)"
			}
		}
		where = "This project's code is on GitHub: " + strings.Join(parts, ", ")
	}
	if !readable {
		return where + ". No GitHub tools are available to this run (GitHub may not be connected), so you " +
			"cannot read that code from here; if the task needs it, say so."
	}
	uses := make([]string, len(githubReadTools))
	for i, t := range githubReadTools {
		uses[i] = fmt.Sprintf("%s (%s)", t.name, t.use)
	}
	return fmt.Sprintf("%s. Read it there instead of asking for files — no checkout is needed: %s. "+
		"Pass the owner and repo to each; scope a search with repo:%s in q. Load these with load_tool if "+
		"they are not already among your tools.", where, strings.Join(uses, ", "), repos[0].slug())
}
