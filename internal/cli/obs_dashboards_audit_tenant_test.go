package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// tenantCP is a control plane serving two organizations whose projects share
// one name ("proj") but not a hosted id. Like the real one, it decides who the
// caller is from the bearer, resolves the project name to the caller's own
// hosted id, and refuses a request whose Forge-Project-ID, org or env bound is
// not the caller's. Every request it saw is kept, so a test can assert what
// the audit sent through the real cloud client.
type tenantCP struct {
	mu   sync.Mutex
	seen []tenantReq
}

type tenantReq struct {
	Proc, Bearer, ProjectHeader string
	Body                        map[string]any
}

type tenant struct{ org, hosted, env, dashboard, title string }

var tenants = map[string]tenant{
	"tok-a": {org: "org-a", hosted: "hosted-a", env: "env-a", dashboard: "da", title: "Tenant A usage"},
	"tok-b": {org: "org-b", hosted: "hosted-b", env: "env-b", dashboard: "db", title: "Tenant B usage"},
}

func (s *tenantCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	proc := strings.TrimPrefix(r.URL.Path, "/")
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.seen = append(s.seen, tenantReq{Proc: proc, Bearer: bearer, ProjectHeader: r.Header.Get("Forge-Project-ID"), Body: in})
	s.mu.Unlock()

	refuse := func(why string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `{"code":"permission_denied","message":%q}`, why)
	}
	t, ok := tenants[bearer]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":"unauthenticated","message":"unknown bearer"}`)
		return
	}
	if proc == cpResolveProject {
		if in["name"] != "proj" {
			refuse("no such project")
			return
		}
		_, _ = fmt.Fprintf(w, `{"projectId":%q,"orgId":%q}`, t.hosted, t.org)
		return
	}
	if r.Header.Get("Forge-Project-ID") != t.hosted {
		refuse("project target is not the caller's")
		return
	}
	bound := in
	if proc == cpQueryPanel {
		bound, _ = in["scope"].(map[string]any)
	}
	if bound["orgId"] != t.org {
		refuse("org is not the caller's")
		return
	}
	switch proc {
	case cpListDashboards:
		if bound["envId"] != t.env || bound["projectId"] != "proj" {
			refuse("env or project outside the caller's")
			return
		}
		_, _ = fmt.Fprintf(w, `{"dashboards":[{"ref":{"source":"DASHBOARD_SOURCE_GENERATED","projectId":"proj","key":%q},"title":%q,"source":"DASHBOARD_SOURCE_GENERATED","panelCount":1,"release":{"releaseVersion":"v7"}}]}`, t.dashboard, t.title)
	case cpGetDashboard:
		if key := in["ref"].(map[string]any)["key"]; key != t.dashboard {
			refuse("dashboard belongs to another org")
			return
		}
		_, _ = fmt.Fprintf(w, `{"summary":{"ref":{"source":"DASHBOARD_SOURCE_GENERATED","key":%q},"title":%q},"spec":{"id":%q,"title":%q,"panels":[%s]}}`,
			t.dashboard, t.title, t.dashboard, t.title, linePanel("p"))
	case cpQueryPanel:
		envs, _ := bound["envIds"].([]any)
		if bound["projectId"] != "proj" || (len(envs) > 0 && (len(envs) != 1 || envs[0] != t.env)) {
			refuse("env or project outside the caller's")
			return
		}
		_, _ = io.WriteString(w, respEmpty)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// tenantClient is the real cloud client a checkout of project "proj" builds:
// bound by name, so the hosted id comes from the control plane's answer for
// the caller.
func tenantClient(url, token string) *cloud.Client {
	return cloud.NewClient(cloud.Endpoint{URL: url, ProjectName: "proj", Env: "prod"}, cloud.Credential{Kind: "human", Token: token})
}

func tenantDeps(c *cloud.Client, org, env string) obsDeps {
	d := mxCloud(nil)
	d.Cloud = func(_ context.Context, e string) (obsbackend.Cloud, error) {
		return obsbackend.Cloud{Env: e, EnvID: env, OrgID: org, Caller: c}, nil
	}
	return d
}

// Naming the project by its ledger name in the dashboard and panel requests
// does not move what a request is authorized against: through the real cloud
// client, every read still carries the caller's own hosted project id
// (Forge-Project-ID) and bearer, and its org and env bounds, so two orgs whose
// projects share a name each audit only their own dashboards.
func TestAuditStaysInsideTheCallersTenant(t *testing.T) {
	srv := &tenantCP{}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	withAuditProject(t)

	for token, want := range tenants {
		srv.mu.Lock()
		srv.seen = nil
		srv.mu.Unlock()
		out, _, err := mxRun(t, tenantDeps(tenantClient(hs.URL, token), want.org, want.env), "dashboards", "audit", "prod", "--json")
		if exitcode.Of(err) != 2 {
			t.Fatalf("%s: exit = %d, want 2 (every panel empty)\n%s", want.org, exitcode.Of(err), out)
		}
		doc := decodeAudit(t, out)
		if len(doc.Dashboards) != 1 || doc.Dashboards[0].ID != want.dashboard {
			t.Fatalf("%s audited %+v, want only its own dashboard %s\n%s", want.org, doc.Dashboards, want.dashboard, out)
		}
		for _, other := range tenants {
			if other.org != want.org && strings.Contains(out, other.title) {
				t.Errorf("%s's audit shows %s's dashboard:\n%s", want.org, other.org, out)
			}
		}
		var reads int
		for _, r := range srv.seen {
			if r.Bearer != token {
				t.Errorf("%s: %s sent bearer %q, want the caller's", want.org, r.Proc, r.Bearer)
			}
			if r.Proc == cpResolveProject {
				continue
			}
			reads++
			if r.ProjectHeader != want.hosted {
				t.Errorf("%s: %s targeted project %q, want the caller's hosted id %s", want.org, r.Proc, r.ProjectHeader, want.hosted)
			}
		}
		if reads == 0 {
			t.Fatalf("%s: the audit sent no dashboard or panel read", want.org)
		}
	}
}

// A credential of one org that names another org's bounds is refused, and the
// audit reports that it could not look (exit 2) without showing anything of
// the other org's.
func TestAuditRefusedAcrossTenantsShowsNothing(t *testing.T) {
	hs := httptest.NewServer(&tenantCP{})
	t.Cleanup(hs.Close)
	withAuditProject(t)

	a := tenants["tok-a"]
	out, _, err := mxRun(t, tenantDeps(tenantClient(hs.URL, "tok-b"), a.org, a.env), "dashboards", "audit", "prod", "--json")
	if exitcode.Of(err) != 2 {
		t.Fatalf("exit = %d, want 2 (refused: could not look)\n%s", exitcode.Of(err), out)
	}
	if strings.Contains(out, a.title) || strings.Contains(out, `"`+a.dashboard+`"`) {
		t.Errorf("a refused cross-org audit shows org-a's dashboard:\n%s", out)
	}
}
