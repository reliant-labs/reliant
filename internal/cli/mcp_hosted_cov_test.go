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

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// covAHostedCP is a control plane that answers by procedure with a scripted
// JSON body (404 not_found for the project "ghost", 501 for anything else
// unscripted) and records each call's request body.
type covAHostedCP struct {
	mu      sync.Mutex
	replies map[string]string
	calls   []string
	bodies  map[string][]map[string]any
}

func (f *covAHostedCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	proc := strings.TrimPrefix(r.URL.Path, "/controlplane.v1.")
	f.mu.Lock()
	f.calls = append(f.calls, proc)
	if f.bodies == nil {
		f.bodies = map[string][]map[string]any{}
	}
	f.bodies[proc] = append(f.bodies[proc], body)
	reply, ok := f.replies[proc]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if proc == "DeployService/ResolveHostedProject" {
		switch body["name"] {
		case "shop":
			_, _ = fmt.Fprint(w, `{"projectId":"p-1","orgId":"org-1"}`)
		case "halfproject":
			_, _ = fmt.Fprint(w, `{"projectId":"p-1"}`)
		case "newshop":
			if body["create"] == true {
				_, _ = fmt.Fprint(w, `{"projectId":"p-new"}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"code":"not_found","message":"no such project"}`)
		case "broken":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"code":"unavailable","message":"down"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"code":"not_found","message":"no such project"}`)
		}
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = fmt.Fprint(w, `{"code":"unimplemented","message":"not scripted: `+proc+`"}`)
		return
	}
	_, _ = fmt.Fprint(w, reply)
}

func (f *covAHostedCP) called(proc string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == proc {
			n++
		}
	}
	return n
}

func covAHosted(t *testing.T, replies map[string]string, mod func(*AccountToolsOptions)) (*mcp.ClientSession, *covAHostedCP) {
	t.Helper()
	cp := &covAHostedCP{replies: replies}
	ts := httptest.NewServer(cp)
	t.Cleanup(ts.Close)
	o := AccountToolsOptions{
		ControlPlaneURL: ts.URL, ConsoleURL: "https://console.example", Token: staticToken("jwt"),
		Authorize: func(context.Context, AccountToolInfo, AccountTarget) error { return nil },
	}
	if mod != nil {
		mod(&o)
	}
	return connectHosted(t, o), cp
}

const covAEnvsReply = `{"environments":[{"id":"env-1","name":"prod","project":"shop"}]}`

func TestHostedCatalogToolsServeForgeShippedContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cs, cp := covAHosted(t, nil, nil)

	t.Run("benefits lists the shipped benefits and refuses an unknown category", func(t *testing.T) {
		text, isErr := callMCP(t, cs, "benefits", map[string]any{"all": true})
		if isErr || !strings.Contains(text, "benefits") {
			t.Fatalf("isErr=%v text=%.200s", isErr, text)
		}
		text, isErr = callMCP(t, cs, "benefits", map[string]any{"category": "no-such-category"})
		if !isErr || !strings.Contains(text, `no benefit category "no-such-category"; use one of:`) {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})
	t.Run("why answers one reason and refuses an unknown one", func(t *testing.T) {
		if text, isErr := callMCP(t, cs, "why", nil); isErr || text == "" {
			t.Fatalf("every reason: isErr=%v text=%.100s", isErr, text)
		}
		if text, isErr := callMCP(t, cs, "why", map[string]any{"reason": "no-such-reason"}); !isErr {
			t.Errorf("an unknown reason must be refused: %.200s", text)
		}
	})
	t.Run("skill_list shows groups, one group's members, and refuses an unknown group", func(t *testing.T) {
		text, isErr := callMCP(t, cs, "skill_list", nil)
		if isErr || !strings.Contains(text, "forge") {
			t.Fatalf("isErr=%v text=%.200s", isErr, text)
		}
		if strings.Contains(text, "reliant forge") {
			t.Errorf("commands in the hosted catalog are written for `forge`:\n%.300s", text)
		}
		if text, isErr := callMCP(t, cs, "skill_list", map[string]any{"group": "db"}); isErr || !strings.Contains(text, "db/") {
			t.Errorf("group db: isErr=%v text=%.300s", isErr, text)
		}
		text, isErr = callMCP(t, cs, "skill_list", map[string]any{"group": "no-such-group"})
		if !isErr || !strings.Contains(text, `no skill or group named "no-such-group"`) {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
		if _, isErr := callMCP(t, cs, "skill_list", map[string]any{"all": true, "include_migrations": true}); isErr {
			t.Error("the exhaustive list must work")
		}
	})
	t.Run("skill_search finds skills by keyword", func(t *testing.T) {
		text, isErr := callMCP(t, cs, "skill_search", map[string]any{"query": "migration"})
		if isErr || !strings.Contains(strings.ToLower(text), "migration") {
			t.Fatalf("isErr=%v text=%.300s", isErr, text)
		}
	})
	t.Run("skill_load accepts a forge/ prefix and names what is missing", func(t *testing.T) {
		a, errA := callMCP(t, cs, "skill_load", map[string]any{"name": "forge"})
		b, errB := callMCP(t, cs, "skill_load", map[string]any{"name": " forge/forge "})
		if errA || errB || a != b || a == "" {
			t.Errorf("a prefixed name loads the same skill: %v %v", errA, errB)
		}
		text, isErr := callMCP(t, cs, "skill_load", map[string]any{"name": "nope"})
		if !isErr || !strings.Contains(text, `skill "nope" not found`) {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})
	if n := len(cp.calls); n != 0 {
		t.Errorf("catalog tools never reach the control plane: %v", cp.calls)
	}
}

func TestHostedObsToolsRunTheirVerbOverTheControlPlane(t *testing.T) {
	// Nothing scripted past the env list: every verb is refused by the
	// control plane (501), which must surface as a tool error naming it, not
	// a protocol failure — and each tool must have resolved the env first.
	cs, cp := covAHosted(t, map[string]string{"DeployService/ListEnvironments": covAEnvsReply}, nil)
	tests := []struct {
		tool string
		args map[string]any
	}{
		{"obs_status", map[string]any{"since": "1h", "limit": 5}},
		{"obs_issues", map[string]any{"status": []any{"new"}, "since_release": "v2", "sort": "events", "page_token": "t"}},
		{"obs_issue", map[string]any{"id": "ISS-1", "events": 3}},
		{"obs_logs", map[string]any{"query": "service:api", "severity": []any{"error"}, "trace": "abc", "oldest": true, "page_token": "p"}},
		{"obs_traces", map[string]any{"errors": true, "slow": true, "min_duration": "500ms", "route": "/x", "page_token": "p"}},
		{"obs_check", map[string]any{"release": "v2", "window": "10m"}},
		{"obs_check", map[string]any{"promotion": "p1"}},
		{"obs_changes", map[string]any{"since": "2h", "limit": 3}},
		{"releases_list", map[string]any{"since": "7d", "limit": 2}},
		{"release_show", map[string]any{"ref": "v2"}},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			args := map[string]any{"project": "shop", "env": "prod"}
			for k, v := range tc.args {
				args[k] = v
			}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			doc, _ := res.StructuredContent.(map[string]any)
			text := res.Content[0].(*mcp.TextContent).Text
			// The verb answered, with its own failure document: not a protocol error.
			if res.IsError || doc["ok"] != false || doc["exit_code"] != float64(2) || !strings.Contains(fmt.Sprint(doc["error"]), "not scripted") {
				t.Errorf("an unserved verb answers with a could-not-look document naming the server's refusal: isErr=%v doc=%v", res.IsError, doc)
			}
			if !strings.HasPrefix(fmt.Sprint(doc["console_url"]), "https://console.example/forge/p/shop") {
				t.Errorf("the answer carries a console link: %v", doc["console_url"])
			}
			if strings.Contains(text, "jwt") {
				t.Errorf("the caller's credential leaked into a tool result: %q", text)
			}
		})
	}
	if cp.called("DeployService/ResolveHostedProject") < len(tests) {
		t.Errorf("every call resolves its project first: %v", cp.calls)
	}
}

func TestHostedToolsRequireTheirTarget(t *testing.T) {
	cs, cp := covAHosted(t, nil, nil)
	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"a blank project", "obs_status", map[string]any{"project": " ", "env": "prod"}, "project is required"},
		{"a blank env", "obs_status", map[string]any{"project": "shop", "env": " "}, "env is required: an environment of shop (envs_list names them)"},
		{"obs_issue with a blank id", "obs_issue", map[string]any{"project": "shop", "env": "prod", "id": " "}, "id is required"},
		{"release_show with a blank ref", "release_show", map[string]any{"project": "shop", "env": "prod", "ref": " "}, "ref is required"},
		{"a project nobody can see", "obs_status", map[string]any{"project": "ghost", "env": "prod"}, "does not exist in an organization you belong to; obs_init creates it"},
		{"a control plane that is down", "obs_status", map[string]any{"project": "broken", "env": "prod"}, "HTTP 503"},
		{"an incomplete project identity", "envs_list", map[string]any{"project": "halfproject"}, "incomplete project identity"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, isErr := callMCP(t, cs, tc.tool, tc.args)
			if !isErr || !strings.Contains(text, tc.want) {
				t.Errorf("isErr=%v text=%q, want %q", isErr, text, tc.want)
			}
		})
	}
	if cp.called("DeployService/ListEnvironments") != 0 {
		t.Error("a refused call must not read environments")
	}
}

func TestHostedObsInitRegistersEnvsAndMintsTokens(t *testing.T) {
	replies := map[string]string{
		"DeployService/EnsureEnvironment": `{"environment":{"id":"env-prod","name":"prod","project":"shop","kind":"DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED"},"created":true}`,
		"TelemetryTokenService/CreateIngestToken": `{"token":{"id":"jti-1","orgId":"org-1","environmentId":"env-prod","name":"forge-obs-init","expiresAt":"2027-02-09T00:00:00Z"},` +
			`"jwt":"eyJ.a.b","otlpHeaders":"authorization=Bearer eyJ.a.b","otlpEndpoint":"https://otlp.example.test"}`,
		"TelemetryTokenService/ListIngestTokens": `{"tokens":[]}`,
	}
	t.Run("an existing project", func(t *testing.T) {
		cs, cp := covAHosted(t, replies, nil)
		text, isErr := callMCP(t, cs, "obs_init", map[string]any{"project": "shop", "envs": []any{"prod"}, "service": "api", "token_name": "ci", "ttl": "720h"})
		if isErr {
			t.Fatalf("obs_init: %s", text)
		}
		for _, want := range []string{`"project": "shop"`, `"org_id": "org-1"`, "eyJ.a.b", "shown only now"} {
			if !strings.Contains(text, want) {
				t.Errorf("result lacks %q:\n%s", want, text)
			}
		}
		if cp.called("DeployService/EnsureEnvironment") != 1 {
			t.Errorf("calls = %v", cp.calls)
		}
		b := cp.bodies["TelemetryTokenService/CreateIngestToken"][0]
		if b["name"] != "ci" || b["ttl"] != "2592000s" || b["orgId"] != "org-1" {
			t.Errorf("token request = %v", b)
		}
		for _, b := range cp.bodies["DeployService/ResolveHostedProject"] {
			if b["create"] == true {
				t.Error("an existing project must not be created again")
			}
		}
	})
	t.Run("a new project is created in the host's organization", func(t *testing.T) {
		cs, cp := covAHosted(t, replies, func(o *AccountToolsOptions) {
			o.NewProjectOrg = func(context.Context, *mcp.CallToolRequest) (string, error) { return "org-9", nil }
		})
		text, isErr := callMCP(t, cs, "obs_init", map[string]any{"project": "newshop", "envs": []any{"prod"}})
		if isErr {
			t.Fatalf("obs_init: %s", text)
		}
		creates := 0
		for _, b := range cp.bodies["DeployService/ResolveHostedProject"] {
			if b["create"] == true {
				creates++
				if b["orgId"] != "org-9" || b["name"] != "newshop" {
					t.Errorf("create request = %v", b)
				}
			}
		}
		if creates != 1 || !strings.Contains(text, `"org_id": "org-9"`) {
			t.Errorf("creates=%d text=%s", creates, text)
		}
	})
	t.Run("a new project with no host organization says how to create it", func(t *testing.T) {
		cs, _ := covAHosted(t, replies, nil)
		text, isErr := callMCP(t, cs, "obs_init", map[string]any{"project": "newshop", "envs": []any{"prod"}})
		if !isErr || !strings.Contains(text, "project newshop does not exist yet: create it with `forge obs init` from the repository, or in the console") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})
	t.Run("the host cannot choose an organization", func(t *testing.T) {
		cs, cp := covAHosted(t, replies, func(o *AccountToolsOptions) {
			o.NewProjectOrg = func(context.Context, *mcp.CallToolRequest) (string, error) {
				return "", fmt.Errorf("no org for this session")
			}
		})
		text, isErr := callMCP(t, cs, "obs_init", map[string]any{"project": "newshop", "envs": []any{"prod"}})
		if !isErr || !strings.Contains(text, "no org for this session") || cp.called("DeployService/EnsureEnvironment") != 0 {
			t.Errorf("isErr=%v text=%s calls=%v", isErr, text, cp.calls)
		}
	})
	t.Run("input is validated before any call", func(t *testing.T) {
		cs, cp := covAHosted(t, replies, nil)
		for name, tc := range map[string]struct {
			args map[string]any
			want string
		}{
			"bad project name":     {map[string]any{"project": "Not A Name", "envs": []any{"prod"}}, "is not a project name"},
			"an empty env list":    {map[string]any{"project": "shop", "envs": []any{}}, "envs is required"},
			"a bad ttl":            {map[string]any{"project": "shop", "envs": []any{"prod"}, "ttl": "forever"}, "ttl:"},
			"a down control plane": {map[string]any{"project": "broken", "envs": []any{"prod"}}, "HTTP 503"},
		} {
			text, isErr := callMCP(t, cs, "obs_init", tc.args)
			if !isErr || !strings.Contains(text, tc.want) {
				t.Errorf("%s: isErr=%v text=%q, want %q", name, isErr, text, tc.want)
			}
		}
		if cp.called("DeployService/EnsureEnvironment") != 0 {
			t.Errorf("nothing may be registered: %v", cp.calls)
		}
	})
	t.Run("a signed-out connection", func(t *testing.T) {
		cs, _ := covAHosted(t, replies, func(o *AccountToolsOptions) { o.Token = staticToken("") })
		text, isErr := callMCP(t, cs, "obs_init", map[string]any{"project": "shop", "envs": []any{"prod"}})
		if !isErr || !strings.Contains(text, "not signed in: reconnect the Reliant Forge connector") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})
}

func TestHostedReleaseRecordValidatesBeforeWriting(t *testing.T) {
	cs, cp := covAHosted(t, map[string]string{"DeployService/ListEnvironments": covAEnvsReply}, nil)
	tests := map[string]struct {
		args map[string]any
		want string
	}{
		"a blank version":    {map[string]any{"project": "shop", "env": "prod", "version": " "}, "version is required"},
		"a bad image":        {map[string]any{"project": "shop", "env": "prod", "version": "v1", "workloads": []any{map[string]any{"workload": "api", "image": "nginx:latest"}}}, "api"},
		"a bad commit":       {map[string]any{"project": "shop", "env": "prod", "version": "v1", "commit": "zzz"}, "commit"},
		"a blank env":        {map[string]any{"project": "shop", "env": " ", "version": "v1"}, "env is required"},
		"an unknown project": {map[string]any{"project": "ghost", "env": "prod", "version": "v1"}, "does not exist in an organization you belong to"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			text, isErr := callMCP(t, cs, "release_record", tc.args)
			if !isErr || !strings.Contains(text, tc.want) {
				t.Errorf("isErr=%v text=%q, want %q", isErr, text, tc.want)
			}
		})
	}
	for _, p := range cp.calls {
		if strings.Contains(p, "RecordRelease") || strings.Contains(p, "Promotion") {
			t.Errorf("a refused record reached %s", p)
		}
	}
}

func TestDocumentResult(t *testing.T) {
	t.Run("a document is the structured content with the console link added", func(t *testing.T) {
		res := documentResult([]byte(`{"schema":"x/v1","ok":true}`), "a note", nil, "https://c/x")
		if res.IsError {
			t.Fatal("a document is an answer")
		}
		m, _ := res.StructuredContent.(map[string]any)
		if m["console_url"] != "https://c/x" || m["schema"] != "x/v1" {
			t.Errorf("structured = %v", m)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, `"console_url": "https://c/x"`) || !strings.HasSuffix(text, "a note") {
			t.Errorf("text = %q", text)
		}
	})
	t.Run("the document's own console_url wins", func(t *testing.T) {
		res := documentResult([]byte(`{"console_url":"own"}`), "", nil, "https://c/x")
		if m, _ := res.StructuredContent.(map[string]any); m["console_url"] != "own" {
			t.Errorf("structured = %v", m)
		}
	})
	t.Run("a failing verb that still answered is not a failed call", func(t *testing.T) {
		if res := documentResult([]byte(`{"verdict":"FAIL"}`), "", fmt.Errorf("exit 1"), ""); res.IsError {
			t.Error("an obs_check FAIL is an answer")
		}
	})
	for name, tc := range map[string]struct {
		doc, stderr string
		err         error
		want        string
	}{
		"no document, a run error": {"", "warn", fmt.Errorf("boom"), "Error: warn\nboom"},
		"no document, stderr only": {"", " just stderr ", nil, "Error: just stderr"},
		"nothing at all":           {"", "", nil, "Error: no answer"},
		"a non-object document":    {`[1,2]`, "", nil, "Error: no answer"},
		"null":                     {`null`, "", nil, "Error: no answer"},
	} {
		t.Run(name, func(t *testing.T) {
			res := documentResult([]byte(tc.doc), tc.stderr, tc.err, "")
			if !res.IsError || res.Content[0].(*mcp.TextContent).Text != tc.want {
				t.Errorf("res = %+v, want %q", res, tc.want)
			}
		})
	}
}

func TestHostedSmallHelpers(t *testing.T) {
	if got := flagArg([]string{"a"}, "--x", "  "); len(got) != 1 {
		t.Errorf("a blank value adds no flag: %v", got)
	}
	if got := flagArg(nil, "--x", "v"); strings.Join(got, " ") != "--x v" {
		t.Errorf("got %v", got)
	}
	if got := (obsIssueArgs{}).args(); got != nil {
		t.Errorf("no events asks for none: %v", got)
	}
	if got := (obsIssueArgs{Events: 4}).args(); strings.Join(got, " ") != "--events 4" {
		t.Errorf("got %v", got)
	}
	if got := (obsCheckArgs{Release: "v2", Promotion: "p", Window: "5m"}).args(); strings.Join(got, " ") != "--release v2 --promotion p --window 5m" {
		t.Errorf("got %v", got)
	}
	if got := (obsStatusArgs{}).args(); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	c := hostedConsole{}
	if got := c.Link("/releases", "prod"); got != "" {
		t.Errorf("no console configured, no link: %q", got)
	}
	if res := textResult(nil); res.Content[0].(*mcp.TextContent).Text != "(no output)" {
		t.Errorf("empty output is named")
	}
	if res := textResult([]byte("body\n\n")); res.Content[0].(*mcp.TextContent).Text != "body" {
		t.Errorf("trailing newlines are trimmed")
	}
	if res := structuredResult(map[string]any{"bad": make(chan int)}, ""); !res.IsError {
		t.Error("an unencodable result is an error result")
	}
	if _, err := (noCheckout{}).Commits(context.Background(), "a", "b", 1); err == nil || !strings.Contains(err.Error(), "the hosted server has no checkout") {
		t.Errorf("err = %v", err)
	}
}
