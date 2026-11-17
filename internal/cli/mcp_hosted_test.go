package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeHostedCP is a control plane that knows one project, shop in org-1,
// with one env, and records who called it how.
type fakeHostedCP struct {
	mu      sync.Mutex
	auth    []string
	via     []string
	calls   []string
	creates []string // projects a ResolveHostedProject call asked to create
}

func (f *fakeHostedCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.via = append(f.via, r.Header.Get("Forge-Via"))
	f.calls = append(f.calls, strings.TrimPrefix(r.URL.Path, "/"))
	if body["create"] == true {
		f.creates = append(f.creates, fmt.Sprint(body["name"]))
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/controlplane.v1.DeployService/ResolveHostedProject":
		if body["name"] != "shop" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"code":"not_found","message":"no such project"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"projectId":"p-1","orgId":"org-1"}`)
	case "/controlplane.v1.DeployService/ListEnvironments":
		_, _ = fmt.Fprint(w, `{"environments":[{"id":"env-1","name":"prod","project":"shop"},{"id":"env-x","name":"other","project":"elsewhere"}]}`)
	default:
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = fmt.Fprint(w, `{"code":"unimplemented","message":"not in this fake"}`)
	}
}

// connectHosted serves the hosted tools on an in-memory pipe.
func connectHosted(t *testing.T, o AccountToolsOptions) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	srv := mcp.NewServer(&mcp.Implementation{Name: "forge-hosted", Version: "0"}, &mcp.ServerOptions{Instructions: HostedInstructions()})
	if err := AddHostedTools(srv, o); err != nil {
		t.Fatal(err)
	}
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func staticToken(tok string) func(context.Context, *mcp.CallToolRequest) (string, error) {
	return func(context.Context, *mcp.CallToolRequest) (string, error) { return tok, nil }
}

func TestHostedToolsListAndAnnotations(t *testing.T) {
	cs := connectHosted(t, AccountToolsOptions{ControlPlaneURL: "http://cp.invalid", Token: staticToken("t")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, info := range HostedTools() {
		tool, ok := got[info.Name]
		if !ok {
			t.Errorf("tool %s is not listed", info.Name)
			continue
		}
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations", info.Name)
			continue
		}
		// A mutating tool must not claim to be read-only: that is what
		// makes a client ask before calling it.
		if tool.Annotations.ReadOnlyHint == info.Mutating {
			t.Errorf("%s: readOnlyHint=%v, mutating=%v", info.Name, tool.Annotations.ReadOnlyHint, info.Mutating)
		}
		if _, hasDir := schemaProps(t, tool.InputSchema)["dir"]; hasDir {
			t.Errorf("%s: a hosted tool has no repo, so no dir input", info.Name)
		}
	}
	for _, name := range []string{"obs_init", "release_record"} {
		if a := got[name].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s annotations = %+v, want mutating and non-destructive", name, a)
		}
	}
	if len(res.Tools) != len(HostedTools()) {
		t.Errorf("listed %d tools, HostedTools names %d", len(res.Tools), len(HostedTools()))
	}
}

func TestHostedToolsLimitToGranted(t *testing.T) {
	cs := connectHosted(t, AccountToolsOptions{ControlPlaneURL: "http://cp.invalid", Token: staticToken("t"), Tools: []string{"obs_status", "skill_load"}})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "obs_status,skill_load" {
		t.Fatalf("tools = %v, want only the granted two", names)
	}
}

// The tools both servers serve have one input schema; the local one adds only
// its repo-only dir.
func TestHostedSharedToolSchemasMatchLocal(t *testing.T) {
	local := connectMCP(t, t.TempDir())
	hosted := connectHosted(t, AccountToolsOptions{ControlPlaneURL: "http://cp.invalid", Token: staticToken("t")})
	schemas := func(cs *mcp.ClientSession) map[string]map[string]any {
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, tool := range res.Tools {
			props := schemaProps(t, tool.InputSchema)
			delete(props, "dir")
			out[tool.Name] = props
		}
		return out
	}
	l, h := schemas(local), schemas(hosted)
	for _, name := range []string{"skill_list", "skill_search", "skill_load", "benefits", "explain_error"} {
		if !reflect.DeepEqual(l[name], h[name]) {
			t.Errorf("%s: hosted input schema %v differs from local %v", name, h[name], l[name])
		}
	}
}

// skill_load serves the body `forge skill load` prints outside a project:
// the renderer the local skill_load (and `skill load`) runs, with commands
// written for `forge`.
func TestHostedSkillLoadMatchesLocal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hosted := connectHosted(t, AccountToolsOptions{ControlPlaneURL: "http://cp.invalid", Token: staticToken("t")})
	for _, name := range []string{"adopt", "forge", "observability", "connect"} {
		want, err := RenderSkillContentAt("", name, SkillAudienceAll, "forge")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, herr := callMCP(t, hosted, "skill_load", map[string]any{"name": name})
		if herr {
			t.Fatalf("%s: hosted error: %s", name, got)
		}
		if strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
			t.Errorf("%s: hosted skill_load is not `forge skill load %s` (%d vs %d bytes)", name, name, len(got), len(want))
		}
	}
	if _, isErr := callMCP(t, hosted, "skill_load", map[string]any{"name": "no-such-skill"}); !isErr {
		t.Error("an unknown skill must be a tool error")
	}
}

func TestHostedEnvsListReadsTheControlPlaneAsTheCaller(t *testing.T) {
	cp := &fakeHostedCP{}
	ts := httptest.NewServer(cp)
	t.Cleanup(ts.Close)
	var seen []AccountTarget
	cs := connectHosted(t, AccountToolsOptions{
		ControlPlaneURL: ts.URL,
		ConsoleURL:      "https://console.example",
		Token:           staticToken("user-jwt"),
		Authorize: func(_ context.Context, tool AccountToolInfo, target AccountTarget) error {
			if tool.Name != "envs_list" {
				t.Errorf("authorize saw tool %q", tool.Name)
			}
			seen = append(seen, target)
			return nil
		},
	})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "envs_list", Arguments: map[string]any{"project": "shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("envs_list failed: %+v", res.Content[0])
	}
	doc, _ := res.StructuredContent.(map[string]any)
	if doc["org_id"] != "org-1" || doc["console_url"] != "https://console.example/forge/p/shop" {
		t.Errorf("doc = %v", doc)
	}
	envs, _ := doc["envs"].([]any)
	if len(envs) != 1 || envs[0].(map[string]any)["console_url"] != "https://console.example/forge/p/shop/environments/prod" {
		t.Errorf("envs = %v, want only shop's prod with its console link", envs)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "https://console.example/forge/p/shop") {
		t.Errorf("the text carries no console link:\n%s", text)
	}
	if len(seen) != 1 || seen[0] != (AccountTarget{OrgID: "org-1", Project: "shop"}) {
		t.Errorf("authorize saw %+v", seen)
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	for i := range cp.auth {
		if cp.auth[i] != "Bearer user-jwt" || cp.via[i] != "mcp" {
			t.Errorf("call %s: Authorization=%q Forge-Via=%q", cp.calls[i], cp.auth[i], cp.via[i])
		}
	}
}

func TestHostedRefusesBeforeReading(t *testing.T) {
	cp := &fakeHostedCP{}
	ts := httptest.NewServer(cp)
	t.Cleanup(ts.Close)
	for name, o := range map[string]AccountToolsOptions{
		"no authorize": {ControlPlaneURL: ts.URL, Token: staticToken("t")},
		"refused": {ControlPlaneURL: ts.URL, Token: staticToken("t"),
			Authorize: func(context.Context, AccountToolInfo, AccountTarget) error {
				return errors.New("this connector may not read shop")
			}},
		"no token": {ControlPlaneURL: ts.URL, Token: staticToken(""),
			Authorize: func(context.Context, AccountToolInfo, AccountTarget) error { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			cs := connectHosted(t, o)
			text, isErr := callMCP(t, cs, "obs_status", map[string]any{"project": "shop", "env": "prod"})
			if !isErr {
				t.Fatalf("obs_status answered: %s", text)
			}
		})
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	for _, c := range cp.calls {
		if c != "controlplane.v1.DeployService/ResolveHostedProject" {
			t.Errorf("a refused call reached %s", c)
		}
	}
}

// obs_init registers envs and mints an ingest token for each, so every env
// it names reaches Authorize before anything is written: a grant that covers
// the project but not one of its envs refuses the whole call, and no env,
// token or project is created.
func TestHostedObsInitAuthorizesEveryEnvBeforeWriting(t *testing.T) {
	for name, tc := range map[string]struct {
		project     string
		envs        []any
		wantTargets []AccountTarget
	}{
		"existing project": {project: "shop", envs: []any{"prod", "staging", "prod"}, wantTargets: []AccountTarget{
			{OrgID: "org-1", Project: "shop"}, {OrgID: "org-1", Project: "shop", Env: "prod"}, {OrgID: "org-1", Project: "shop", Env: "staging"}}},
		"new project": {project: "newshop", envs: []any{"prod", "staging"}, wantTargets: []AccountTarget{
			{OrgID: "org-2", Project: "newshop"}, {OrgID: "org-2", Project: "newshop", Env: "prod"}, {OrgID: "org-2", Project: "newshop", Env: "staging"}}},
		"not an env name": {project: "shop", envs: []any{"prod", "Staging!"}},
	} {
		t.Run(name, func(t *testing.T) {
			cp := &fakeHostedCP{}
			ts := httptest.NewServer(cp)
			t.Cleanup(ts.Close)
			var seen []AccountTarget
			cs := connectHosted(t, AccountToolsOptions{
				ControlPlaneURL: ts.URL,
				Token:           staticToken("t"),
				NewProjectOrg:   func(context.Context, *mcp.CallToolRequest) (string, error) { return "org-2", nil },
				// The grant covers the project and its prod env only.
				Authorize: func(_ context.Context, _ AccountToolInfo, target AccountTarget) error {
					seen = append(seen, target)
					if target.Env != "" && target.Env != "prod" {
						return fmt.Errorf("this connector may not act on %s", target.Env)
					}
					return nil
				},
			})
			text, isErr := callMCP(t, cs, "obs_init", map[string]any{"project": tc.project, "envs": tc.envs})
			if !isErr {
				t.Fatalf("obs_init answered: %s", text)
			}
			if !reflect.DeepEqual(seen, tc.wantTargets) {
				t.Errorf("authorize saw %+v, want %+v", seen, tc.wantTargets)
			}
			cp.mu.Lock()
			defer cp.mu.Unlock()
			if len(cp.creates) != 0 {
				t.Errorf("a refused obs_init created project(s) %v", cp.creates)
			}
			for _, c := range cp.calls {
				if c != "controlplane.v1.DeployService/ResolveHostedProject" {
					t.Errorf("a refused obs_init reached %s", c)
				}
			}
		})
	}
}

// Calls from many sessions at once share nothing: each is its own person.
func TestHostedConcurrentCallsKeepTheirCaller(t *testing.T) {
	cp := &fakeHostedCP{}
	ts := httptest.NewServer(cp)
	t.Cleanup(ts.Close)
	var wg sync.WaitGroup
	for i := range 8 {
		cs := connectHosted(t, AccountToolsOptions{ControlPlaneURL: ts.URL, Token: staticToken(fmt.Sprintf("tok-%d", i)),
			Authorize: func(context.Context, AccountToolInfo, AccountTarget) error { return nil }})
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "envs_list", Arguments: map[string]any{"project": "shop"}})
			if err != nil || res.IsError {
				t.Errorf("session %d: %v %+v", i, err, res)
			}
		}()
	}
	wg.Wait()
	cp.mu.Lock()
	defer cp.mu.Unlock()
	counts := map[string]int{}
	for _, a := range cp.auth {
		counts[a]++
	}
	for i := range 8 {
		if counts[fmt.Sprintf("Bearer tok-%d", i)] != 2 {
			t.Errorf("tok-%d made %d calls, want 2 (resolve once + list)", i, counts[fmt.Sprintf("Bearer tok-%d", i)])
		}
	}
}

func schemaProps(t *testing.T, schema any) map[string]any {
	t.Helper()
	b, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.Properties == nil {
		return map[string]any{}
	}
	return s.Properties
}
