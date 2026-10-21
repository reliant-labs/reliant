// Copyright (c) 2025 Reliant Labs

package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
)

// The actions behind GitHub's value types, driven through the real runner
// against the fake (see github_test.go), and the projections that turn their
// output into picker options.

func installation(id int, login string) map[string]any {
	return map[string]any{"id": id, "account": map[string]any{"login": login, "type": "Organization", "id": id * 10},
		"target_type": "Organization", "repository_selection": "selected", "html_url": "https://github.com/organizations/" + login}
}

func installationRepo(id int, full string, push bool) map[string]any {
	owner, name, _ := strings.Cut(full, "/")
	return map[string]any{"id": id, "name": name, "full_name": full, "private": id%2 == 0,
		"owner": map[string]any{"login": owner}, "html_url": "https://github.com/" + full,
		"permissions": map[string]any{"push": push, "pull": true}}
}

func reply(v any) http.HandlerFunc {
	b, _ := json.Marshal(v)
	return jsonReply(200, string(b))
}

func TestAppInstallationsList(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/user/installations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "installations": []any{installation(2, "globex")}})
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+`/user/installations?per_page=100&page=2>; rel="next"`)
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "installations": []any{installation(1, "acme")}})
	})
	out := data(t, f.run("app.installations.list", nil))

	reqs := f.requests()
	require.Len(t, reqs, 2, "follows the Link header")
	for _, r := range reqs {
		assertGitHubHeaders(t, r)
	}
	items := out["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, map[string]any{
		"id": 1.0, "account": "acme", "account_type": "Organization", "target_type": "Organization",
		"repository_selection": "selected", "html_url": "https://github.com/organizations/acme",
	}, items[0])
	assert.Equal(t, "globex", items[1].(map[string]any)["account"])
}

// The walk: every installation, then every page of each one's repositories,
// annotated with the installation each came through and whether the user can
// push.
func TestAppInstallationRepositoriesWalksTheInstallations(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/user/installations", reply(map[string]any{"total_count": 2, "installations": []any{installation(11, "acme"), installation(22, "globex")}}))
	f.on("GET", "/user/installations/11/repositories", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 3, "repositories": []any{installationRepo(3, "acme/docs", false)}})
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+`/user/installations/11/repositories?per_page=100&page=2>; rel="next"`)
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 3, "repositories": []any{installationRepo(1, "acme/app", true), installationRepo(2, "acme/api", true)}})
	})
	f.on("GET", "/user/installations/22/repositories", reply(map[string]any{"total_count": 1, "repositories": []any{installationRepo(4, "globex/site", true)}}))

	out := data(t, f.run("app.installation_repositories.list", nil))

	var paths []string
	for _, r := range f.requests() {
		assertGitHubHeaders(t, r)
		paths = append(paths, r.Path)
	}
	assert.Equal(t, []string{
		"/user/installations",
		"/user/installations/11/repositories", "/user/installations/11/repositories",
		"/user/installations/22/repositories",
	}, paths)

	items := out["items"].([]any)
	require.Len(t, items, 4, "every repository of every installation, in order")
	var got []string
	for _, it := range items {
		m := it.(map[string]any)
		got = append(got, fmt.Sprintf("%v|%v|%v|%v", m["full_name"], m["installation_id"], m["installation_account"], m["can_push"]))
	}
	assert.Equal(t, []string{
		"acme/app|11|acme|true", "acme/api|11|acme|true", "acme/docs|11|acme|false", "globex/site|22|globex|true",
	}, got)
	assert.Equal(t, map[string]any{
		"id": 1.0, "full_name": "acme/app", "name": "app", "owner": "acme", "private": false,
		"html_url": "https://github.com/acme/app", "description": nil, "default_branch": nil,
		"archived": false, "fork": false, "installation_id": 11.0, "installation_account": "acme", "can_push": true,
	}, items[0])
}

func TestAppInstallationRepositoriesNarrowedToOneInstallation(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/user/installations", reply(map[string]any{"installations": []any{installation(11, "acme"), installation(22, "globex")}}))
	f.on("GET", "/user/installations/22/repositories", reply(map[string]any{"repositories": []any{installationRepo(4, "globex/site", true)}}))

	out := data(t, f.run("app.installation_repositories.list", map[string]any{"installation_id": 22}))
	for _, r := range f.requests() {
		assert.NotContains(t, r.Path, "/11/", "the other installation is not walked")
	}
	require.Len(t, out["items"], 1)
}

// An installation the user lost access to between the two listings is
// dropped, not a failure; any other failure fails the whole listing, so a
// partial list is never mistaken for a complete one.
func TestAppInstallationRepositoriesSkipsAGoneInstallationAndFailsOtherwise(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/user/installations", reply(map[string]any{"installations": []any{installation(11, "acme"), installation(22, "globex")}}))
	f.on("GET", "/user/installations/22/repositories", reply(map[string]any{"repositories": []any{installationRepo(4, "globex/site", true)}}))
	// 11 has no route: GitHub's 404.
	out := data(t, f.run("app.installation_repositories.list", nil))
	items := out["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "globex/site", items[0].(map[string]any)["full_name"])

	f = newFake(t)
	f.on("GET", "/user/installations", reply(map[string]any{"installations": []any{installation(11, "acme"), installation(22, "globex")}}))
	f.on("GET", "/user/installations/11/repositories", jsonReply(502, `<html>bad gateway</html>`))
	res := f.run("app.installation_repositories.list", nil)
	assert.True(t, res.IsError)
	assert.True(t, res.Retryable)
	assert.Equal(t, 502, res.StatusCode)
}

func TestAppInstallationRepositoriesRefusesToWalkTooMany(t *testing.T) {
	f := newFake(t)
	var in []any
	for i := 1; i <= 51; i++ {
		in = append(in, installation(i, fmt.Sprintf("org%d", i)))
	}
	f.on("GET", "/user/installations", reply(map[string]any{"installations": in}))
	_, err := f.try("app.installation_repositories.list", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most 50")
	assert.Len(t, f.requests(), 1, "no repositories were requested")
}

func TestUsersSearch(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/search/users", reply(map[string]any{"total_count": 1, "incomplete_results": false, "items": []any{
		map[string]any{"id": 583231, "login": "octocat", "type": "User", "html_url": "https://github.com/octocat", "avatar_url": "https://avatars/1"},
	}}))
	out := data(t, f.run("users.search", map[string]any{"q": "octo"}))
	req := f.only()
	assert.Equal(t, "/search/users", req.Path)
	assert.Equal(t, "page=1&per_page=20&q=octo", sortedQuery(req.Query))
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{
		"total_count": 1.0, "incomplete_results": false,
		"items": []any{map[string]any{"id": 583231.0, "login": "octocat", "type": "User", "html_url": "https://github.com/octocat", "avatar_url": "https://avatars/1"}},
	}, out)
}

func TestUserGetByID(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/user/583231", reply(map[string]any{"id": 583231, "login": "octocat", "type": "User", "name": "The Octocat"}))
	out := data(t, f.run("user.get_by_id", map[string]any{"account_id": 583231}))
	assert.Equal(t, "/user/583231", f.only().Path)
	assert.Equal(t, map[string]any{"id": 583231.0, "login": "octocat", "type": "User", "name": "The Octocat", "html_url": nil}, out)

	f = newFake(t)
	res := f.run("user.get_by_id", map[string]any{"account_id": 1})
	assert.True(t, res.IsError, "an id nobody has is a 404")
}

// --- the types ---------------------------------------------------------------

func TestGitHubDeclaresItsTypes(t *testing.T) {
	m, err := catalog.MustBuiltin().Manifest("github", 1)
	require.NoError(t, err)
	var ids []string
	for _, ty := range m.GetTypes() {
		ids = append(ids, ty.GetId())
	}
	assert.Equal(t, []string{"repository", "label", "user", "installation"}, ids)

	// Every option source is a read-only action, and every attribute type the
	// triggers name resolves: load-time rules, restated against the shipped
	// manifest so a weakened rule shows up here.
	for _, ty := range m.GetTypes() {
		a, ok := manifest.Action(m, ty.GetOptions().GetAction())
		require.True(t, ok, ty.GetId())
		assert.False(t, a.GetMutates(), "%s options come from %s", ty.GetId(), a.GetId())
	}
	typed := map[string]string{}
	for _, tr := range m.GetTriggers() {
		for _, a := range tr.GetAttributes() {
			if a.GetType() != "" {
				typed[a.GetName()] = a.GetType()
			}
		}
	}
	assert.Equal(t, map[string]string{"repository": "repository", "installation_id": "installation", "sender_id": "user", "label": "label"}, typed)
}

func TestGitHubValueSchemas(t *testing.T) {
	m, err := catalog.MustBuiltin().Manifest("github", 1)
	require.NoError(t, err)
	valid := map[string][]any{
		"repository":   {"acme/app", "octo-cat/hello.world_2"},
		"user":         {"583231"},
		"installation": {"98765"},
		"label":        {"needs-triage", "agent:re-review"},
	}
	invalid := map[string][]any{
		"repository":   {"acme", "acme/", "/app", "acme/app/extra", "-acme/app", "acme/a b", 7, ""},
		"user":         {"octocat", "", "12ab", 583231},
		"installation": {"abc", ""},
		"label":        {"", strings.Repeat("x", 51)},
	}
	for id, values := range valid {
		ty, _ := manifest.Type(m, id)
		for _, v := range values {
			assert.NoError(t, validateValue(t, manifest.ValueSchema(ty), v), "%s %v", id, v)
		}
	}
	for id, values := range invalid {
		ty, _ := manifest.Type(m, id)
		for _, v := range values {
			assert.Error(t, validateValue(t, manifest.ValueSchema(ty), v), "%s %v", id, v)
		}
	}
}

// The repository projection, run over what app.installation_repositories.list
// returns: value, label, group, annotations, and the bot warning.
func TestRepositoryOptionProjection(t *testing.T) {
	m, err := catalog.MustBuiltin().Manifest("github", 1)
	require.NoError(t, err)
	ty, _ := manifest.Type(m, "repository")
	o := ty.GetOptions()

	item := map[string]any{"id": 1.0, "full_name": "acme/app", "name": "app", "owner": "acme", "private": true,
		"installation_id": 11.0, "installation_account": "acme", "can_push": false, "archived": false}
	eval := func(expr string, vars map[string]any) any {
		v, err := tmpl.EvalWith(expr, vars)
		require.NoError(t, err, expr)
		return v
	}
	vars := map[string]any{"item": item}
	assert.Equal(t, "acme/app", eval(o.GetValue(), vars))
	assert.Equal(t, "acme/app", eval(o.GetLabel(), vars))
	assert.Equal(t, "private", eval(o.GetDescription(), vars))
	assert.Equal(t, "acme", eval(o.GetGroup(), vars))
	assert.Equal(t, false, eval(o.GetAnnotations()["can_push"], vars))
	assert.Equal(t, 11.0, eval(o.GetAnnotations()["installation_id"], vars))

	require.Len(t, o.GetWarnings(), 1)
	warn := func(identity string, canPush bool) any {
		return eval(o.GetWarnings()[0].GetWhen(), map[string]any{
			"item": item, "context": map[string]any{"acting_identity": identity},
			"option": map[string]any{"annotations": map[string]any{"can_push": canPush}},
		})
	}
	assert.Equal(t, true, warn("app", false), "the bot cannot write where the user cannot push")
	assert.Equal(t, false, warn("app", true))
	assert.Equal(t, false, warn("owner", false), "the owner's own access is the user's own: no warning")

	// The optional installation scope: a param only when chosen.
	p := o.GetParams()["installation_id"]
	assert.Nil(t, renderParam(t, p, map[string]any{}))
	assert.Equal(t, 22.0, renderParam(t, p, map[string]any{"installation": "22"}))
}

func renderParam(t *testing.T, template string, scope map[string]any) any {
	t.Helper()
	v, err := tmpl.RenderWith(template, map[string]any{"scope": scope, "query": "", "page_token": ""})
	require.NoError(t, err)
	return v
}

func TestLabelOptionsAreScopedByRepository(t *testing.T) {
	m, err := catalog.MustBuiltin().Manifest("github", 1)
	require.NoError(t, err)
	ty, _ := manifest.Type(m, "label")
	o := ty.GetOptions()
	assert.True(t, ty.GetFreeText(), "a label that does not exist yet is allowed")
	assert.True(t, o.GetScope()["repository"].GetRequired())
	scope := map[string]any{"repository": "acme/app"}
	assert.Equal(t, "acme", renderParam(t, o.GetParams()["owner"], scope))
	assert.Equal(t, "app", renderParam(t, o.GetParams()["repo"], scope))
}

func TestUserOptionsSearchServerSideAndResolveByID(t *testing.T) {
	m, err := catalog.MustBuiltin().Manifest("github", 1)
	require.NoError(t, err)
	ty, _ := manifest.Type(m, "user")
	o := ty.GetOptions()
	assert.Equal(t, "param:q", o.GetSearch())
	v, err := tmpl.EvalWith(o.GetValue(), map[string]any{"item": map[string]any{"id": 583231.0, "login": "octocat"}})
	require.NoError(t, err)
	assert.Equal(t, "583231", v, "the stored value is the id, never the login")
	assert.Equal(t, "user.get_by_id", o.GetResolve().GetAction())
	assert.Equal(t, 583231.0, renderResolveParam(t, o.GetResolve().GetParams()["account_id"], "583231"))
}

func renderResolveParam(t *testing.T, template, value string) any {
	t.Helper()
	v, err := tmpl.RenderWith(template, map[string]any{"value": value})
	require.NoError(t, err)
	return v
}

func validateValue(t *testing.T, schema map[string]any, v any) error {
	t.Helper()
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	var sch gojsonschema.Schema
	require.NoError(t, json.Unmarshal(raw, &sch))
	r, err := sch.Resolve(nil)
	require.NoError(t, err)
	return r.Validate(v)
}
