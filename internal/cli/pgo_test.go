package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

var pgoTestNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// pgoHeavyRows is 12s of edge weight over two edges: above the 10s default.
const pgoHeavyRows = `{"caller":"main.main","callee":"main.hot","offset":7,"weight":"9000000000","oldest":1763049600,"newest":1763463600}
{"caller":"main.serve","callee":"main.(*T).Do","offset":3,"weight":"3000000000","oldest":1763049600,"newest":1763460000}
`

// pgoLightRows is 3.2s of edge weight: below the default min_weight.
const pgoLightRows = `{"caller":"main.main","callee":"main.hot","offset":7,"weight":"3200000000","oldest":1763049600,"newest":1763463600}
`

// pgoFakeRunner records each local statement and answers with out/err.
type pgoFakeRunner struct {
	out    string
	err    error
	params []map[string]string
	envs   []string
}

func (f *pgoFakeRunner) runner(env string) profileRunner {
	return func(_ context.Context, sql string, params map[string]string) ([]byte, error) {
		if !strings.Contains(sql, "profile_pgo_edges") {
			return nil, fmt.Errorf("unexpected statement %q", sql)
		}
		f.params = append(f.params, params)
		f.envs = append(f.envs, env)
		return []byte(f.out), f.err
	}
}

// pgoFakeCaller is a control plane serving ExportPGOProfile.
type pgoFakeCaller struct {
	pprof []byte
	err   error
	reqs  []map[string]any
}

func (f *pgoFakeCaller) Call(_ context.Context, procedure string, req, out any) error {
	if procedure != "controlplane.v1.ObservabilityService/ExportPGOProfile" {
		return fmt.Errorf("unexpected procedure %s", procedure)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	f.reqs = append(f.reqs, m)
	if f.err != nil {
		return f.err
	}
	var p profilequery.PGOProfile
	if len(f.pprof) > 0 {
		if p, err = profilequery.ReadPGO(bytes.NewReader(f.pprof)); err != nil {
			return err
		}
	}
	resp := fmt.Sprintf(`{"pprof":%q,"edgeCount":%d,"totalWeight":"%d"}`, encodeBase64(f.pprof), len(p.Edges), p.TotalWeight())
	return json.Unmarshal([]byte(resp), out)
}

func encodeBase64(b []byte) string {
	raw, _ := json.Marshal(b) // []byte marshals as a base64 JSON string
	return strings.Trim(string(raw), `"`)
}

// pgoFixture is a project with cmd/shop and cmd/proxy, and the seams to run
// `forge pgo` against it.
type pgoFixture struct {
	dir     string
	envs    map[string]*KCLEntities
	facts   map[string]obsbackend.Facts
	local   *pgoFakeRunner
	cp      *pgoFakeCaller
	stdout  bytes.Buffer
	renders []string
}

func newPGOFixture(t *testing.T) *pgoFixture {
	t.Helper()
	dir := t.TempDir()
	for _, bin := range []string{"shop", "proxy"} {
		if err := os.MkdirAll(filepath.Join(dir, "cmd", bin), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	local := pgoFacts("prod", false)
	return &pgoFixture{
		dir: dir,
		envs: map[string]*KCLEntities{
			// A modular monolith: api, worker and nightly all run ./cmd/shop.
			"prod": {Project: "shop", Env: "prod", Workloads: []WorkloadEntity{
				clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/shop", nil)),
				clusterWL("worker", "k3d-x", "ns", pgoGo("./cmd/shop", nil)),
				clusterWL("nightly", "k3d-x", "ns", pgoGo("./cmd/shop", nil)),
			}},
		},
		facts: map[string]obsbackend.Facts{"prod": local},
		local: &pgoFakeRunner{out: pgoHeavyRows},
		cp:    &pgoFakeCaller{},
	}
}

// pgoFacts are an env whose telemetry is local (the ClickStack) or hosted.
func pgoFacts(env string, hosted bool) obsbackend.Facts {
	if hosted {
		return obsbackend.Facts{Env: env, HasHosted: true, ControlPlane: "https://cp.example"}
	}
	return obsbackend.Facts{Env: env, Declared: true, Enabled: true, HasLocal: true}
}

func (f *pgoFixture) deps() pgoDeps {
	return pgoDeps{
		ProjectDir: f.dir,
		Envs: func(string) []string {
			var out []string
			for e := range f.envs {
				out = append(out, e)
			}
			slices.Sort(out)
			return out
		},
		Render: func(_ context.Context, _, env string) (*KCLEntities, error) {
			f.renders = append(f.renders, env)
			e, ok := f.envs[env]
			if !ok {
				return nil, fmt.Errorf("no env %s", env)
			}
			return e, nil
		},
		Facts: func(_ context.Context, _ io.Writer, env string) (obsbackend.Facts, error) {
			fa, ok := f.facts[env]
			if !ok {
				return obsbackend.Facts{}, obsbackend.Undetermined("cannot read env %q's declaration", env)
			}
			return fa, nil
		},
		Local: f.local.runner,
		Cloud: func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-" + env, OrgID: "org-1", Caller: f.cp}, nil
		},
		Now:    func() time.Time { return pgoTestNow },
		Stdout: &f.stdout,
		Stderr: &bytes.Buffer{},
	}
}

func (f *pgoFixture) profilePath(bin string) string {
	return filepath.Join(f.dir, "cmd", bin, "default.pgo")
}

// writeForgeProfile puts a forge-written profile at cmd/<bin>/default.pgo and
// returns its bytes.
func (f *pgoFixture) writeForgeProfile(t *testing.T, bin string) []byte {
	t.Helper()
	var buf bytes.Buffer
	p := profilequery.PGOProfile{Services: []string{"old"}, Env: "prod", From: pgoTestNow.Add(-48 * time.Hour), To: pgoTestNow.Add(-24 * time.Hour), Oldest: pgoTestNow.Add(-48 * time.Hour), Newest: pgoTestNow.Add(-25 * time.Hour),
		Edges: []profilequery.Edge{{Caller: "main.old", Callee: "main.older", CallSiteOffset: 2, Weight: 42}}}
	if err := profilequery.WritePGO(&buf, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.profilePath(bin), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (f *pgoFixture) pull(t *testing.T, o pgoPullOpts) (pgoPullDoc, error) {
	t.Helper()
	o.jsonOut = true
	f.stdout.Reset()
	err := runPGOPull(context.Background(), f.deps(), o)
	var doc pgoPullDoc
	if derr := json.Unmarshal(f.stdout.Bytes(), &doc); derr != nil {
		t.Fatalf("decode --json document: %v\n%s", derr, f.stdout.String())
	}
	return doc, err
}

func exitOf(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}

func pgoOnly(t *testing.T, doc pgoPullDoc, pkg string) pgoPullResult {
	t.Helper()
	for _, r := range doc.Binaries {
		if r.Package == pkg {
			return r
		}
	}
	t.Fatalf("no result for %s in %+v", pkg, doc.Binaries)
	return pgoPullResult{}
}

// The common case: a modular monolith's three workloads merged into one
// profile, read from the local ClickStack, written atomically.
func TestPGOPull_WritesModularMonolithFromLocal(t *testing.T) {
	f := newPGOFixture(t)
	doc, err := f.pull(t, pgoPullOpts{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	r := pgoOnly(t, doc, "./cmd/shop")
	if r.Action != pgoWritten || r.Source != obsbackend.BackendLocal || r.Edges != 2 || r.TotalWeightNS != 12e9 {
		t.Fatalf("result = %+v", r)
	}
	if want := []string{"api", "nightly", "worker"}; !slices.Equal(r.Services, want) {
		t.Errorf("services = %v, want %v", r.Services, want)
	}
	if len(f.local.params) != 1 {
		t.Fatalf("queries = %d, want 1", len(f.local.params))
	}
	p := f.local.params[0]
	if p["services"] != "['api','nightly','worker']" || p["env"] != "prod" || p["namespace"] != "shop" ||
		p["to"] != fmt.Sprint(pgoTestNow.Unix()) || p["from"] != fmt.Sprint(pgoTestNow.Add(-168*time.Hour).Unix()) {
		t.Errorf("query params = %v", p)
	}
	data, err := os.ReadFile(f.profilePath("shop"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := profilequery.ReadPGO(bytes.NewReader(data))
	if err != nil || len(got.Edges) != 2 || got.Env != "prod" {
		t.Fatalf("written profile = %+v, %v", got, err)
	}
	// No stray temp file beside it.
	entries, _ := os.ReadDir(filepath.Join(f.dir, "cmd", "shop"))
	if len(entries) != 1 {
		t.Errorf("cmd/shop holds %d entries, want only default.pgo", len(entries))
	}
}

// Every way a binary is skipped or fails leaves an existing profile exactly
// as it was, and the exit status follows the contract.
func TestPGOPull_NeverTouchesTheFileUnlessWriting(t *testing.T) {
	missingTable := errors.New("exit status 60: Code: 60. DB::Exception: Table default.profile_pgo_edges does not exist. (UNKNOWN_TABLE)")
	cases := []struct {
		name    string
		setup   func(f *pgoFixture)
		opts    pgoPullOpts
		action  string
		reason  string
		exit    int
		queried bool
	}{
		{name: "below min weight", setup: func(f *pgoFixture) { f.local.out = pgoLightRows },
			action: pgoSkipped, reason: "only 3.2s of edge weight (sampled CPU summed over call edges) in 168h from prod; needs 10s", queried: true},
		{name: "too many edges", setup: func(f *pgoFixture) {
			f.local.out = strings.Repeat(`{"caller":"main.main","callee":"main.hot","offset":7,"weight":"1"}`+"\n", profilequery.DefaultMaxPGOEdges+1)
		}, action: pgoFailed, reason: "narrow it — a shorter window (forge pgo pull --since)", exit: 1, queried: true},
		{name: "no data", setup: func(f *pgoFixture) { f.local.out = "" },
			action: pgoSkipped, reason: "no CPU profile edges from api, nightly, worker in env prod over the last 168h", queried: true},
		{name: "disabled", setup: func(f *pgoFixture) {
			for i := range f.envs["prod"].Workloads {
				f.envs["prod"].Workloads[i].Build.Go.PGO = pgoOff()
			}
		}, action: pgoSkipped, reason: "PGO is off for ./cmd/shop in env prod"},
		{name: "clickstack not running", setup: func(f *pgoFixture) {
			f.local.err = errors.New("service \"clickstack\" is not running")
		}, action: pgoFailed, reason: "local ClickStack is not running — start it with `forge env up prod`", exit: 2, queried: true},
		{name: "missing local table", setup: func(f *pgoFixture) { f.local.err = missingTable },
			action: pgoFailed, reason: "run `forge project upgrade` (see `forge skill load migrations/v1.8.0/profile-guided-builds`", exit: 2, queried: true},
		{name: "unimplemented RPC", setup: func(f *pgoFixture) {
			f.facts["prod"] = pgoFacts("prod", true)
			f.cp.err = &cloud.Error{HTTPStatus: 404, Code: cloud.CodeUnimplemented}
		}, action: pgoFailed, reason: "does not serve PGO profiles yet", exit: 2},
		{name: "control plane unreachable", setup: func(f *pgoFixture) {
			f.facts["prod"] = pgoFacts("prod", true)
			f.cp.err = errors.New("dial tcp: connection refused")
		}, action: pgoFailed, reason: "could not be reached", exit: 2},
		{name: "dry run", opts: pgoPullOpts{dryRun: true}, action: pgoWouldWrite, queried: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPGOFixture(t)
			before := f.writeForgeProfile(t, "shop")
			if tc.setup != nil {
				tc.setup(f)
			}
			doc, err := f.pull(t, tc.opts)
			r := pgoOnly(t, doc, "./cmd/shop")
			if r.Action != tc.action || !strings.Contains(r.Reason, tc.reason) {
				t.Errorf("result = %s %q, want %s containing %q", r.Action, r.Reason, tc.action, tc.reason)
			}
			if got := exitOf(err); got != tc.exit || doc.ExitCode != tc.exit {
				t.Errorf("exit = %d (document %d), want %d (err %v)", got, doc.ExitCode, tc.exit, err)
			}
			if (len(f.local.params) > 0) != tc.queried {
				t.Errorf("queried the local ClickStack = %v, want %v", len(f.local.params) > 0, tc.queried)
			}
			after, rerr := os.ReadFile(f.profilePath("shop"))
			if rerr != nil || !bytes.Equal(before, after) {
				t.Errorf("the existing default.pgo changed (%v)", rerr)
			}
		})
	}
}

// A hosted env is read through ExportPGOProfile: the scope carries the org,
// the env and the window; the control plane's bytes are written as they are.
func TestPGOPull_WritesFromControlPlane(t *testing.T) {
	f := newPGOFixture(t)
	f.facts["prod"] = pgoFacts("prod", true)
	var served bytes.Buffer
	if err := profilequery.WritePGO(&served, profilequery.PGOProfile{Services: []string{"api", "nightly", "worker"}, Env: "prod",
		From: pgoTestNow.Add(-168 * time.Hour), To: pgoTestNow, Oldest: pgoTestNow.Add(-100 * time.Hour), Newest: pgoTestNow.Add(-time.Hour),
		Edges: []profilequery.Edge{{Caller: "main.main", Callee: "main.hot", CallSiteOffset: 4, Weight: 20e9}}}); err != nil {
		t.Fatal(err)
	}
	f.cp.pprof = served.Bytes()
	doc, err := f.pull(t, pgoPullOpts{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	r := pgoOnly(t, doc, "./cmd/shop")
	if r.Action != pgoWritten || r.Source != obsbackend.BackendCloud || r.Edges != 1 || r.TotalWeightNS != 20e9 || r.Newest == nil {
		t.Fatalf("result = %+v", r)
	}
	got, _ := os.ReadFile(f.profilePath("shop"))
	if !bytes.Equal(got, served.Bytes()) {
		t.Error("the written file is not the control plane's bytes")
	}
	if len(f.local.params) != 0 {
		t.Error("a hosted env was queried on the local ClickStack")
	}
	req := f.cp.reqs[0]
	scope := req["scope"].(map[string]any)
	tr := scope["timeRange"].(map[string]any)
	if scope["orgId"] != "org-1" || fmt.Sprint(scope["envIds"]) != "[env-prod]" ||
		tr["start"] != pgoTestNow.Add(-168*time.Hour).Format(time.RFC3339) || tr["end"] != pgoTestNow.Format(time.RFC3339) {
		t.Errorf("scope = %v", scope)
	}
	if fmt.Sprint(req["services"]) != "[api nightly worker]" || req["env"] != "prod" || req["serviceNamespace"] != "shop" {
		t.Errorf("request = %v", req)
	}
}

// An edge whose function name go tool preprofile cannot carry is left out
// and reported; the rest of the profile is written.
func TestPGOPull_ReportsDroppedEdges(t *testing.T) {
	f := newPGOFixture(t)
	f.local.out = pgoHeavyRows + `{"caller":"main.main","callee":"main.a\nb","offset":1,"weight":"5"}` + "\n"
	doc, err := f.pull(t, pgoPullOpts{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	r := pgoOnly(t, doc, "./cmd/shop")
	if r.Action != pgoWritten || r.Edges != 2 || r.Dropped != 1 {
		t.Fatalf("result = %+v", r)
	}
	got, err := profilequery.ReadPGO(strings.NewReader(mustRead(t, f.profilePath("shop"))))
	if err != nil || got.Dropped != 1 || got.Namespace != "shop" {
		t.Errorf("written profile = %+v, %v; want the drop and the namespace recorded", got, err)
	}
	f.stdout.Reset()
	printPGOPull(&f.stdout, doc)
	if !strings.Contains(f.stdout.String(), "1 edge left out: a function name with a line break or over 64 KiB") {
		t.Errorf("human report lacks the drop:\n%s", f.stdout.String())
	}
}

// Config read from staging, profiles from prod: the services are prod's
// workloads that run the binary, and the query is bound to env prod.
func TestPGOPull_FromAnotherEnv(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pgo     *PGOConfig
		flag    string
		renders []string
	}{
		{name: "pgo.from_env", pgo: &PGOConfig{FromEnv: "prod"}, renders: []string{"staging", "prod"}},
		{name: "--from-env", flag: "prod", renders: []string{"staging", "prod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPGOFixture(t)
			f.envs["prod"] = &KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{
				clusterWL("shop-api", "k3d-x", "ns", pgoGo("./cmd/shop", nil)),
				clusterWL("shop-cron", "k3d-x", "ns", pgoGo("./cmd/shop", nil)),
			}}
			f.envs["staging"] = &KCLEntities{Project: "shop", Env: "staging", Workloads: []WorkloadEntity{
				clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/shop", tc.pgo)),
			}}
			doc, err := f.pull(t, pgoPullOpts{env: "staging", fromEnv: tc.flag})
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			r := pgoOnly(t, doc, "./cmd/shop")
			if r.Action != pgoWritten || r.FromEnv != "prod" || !slices.Equal(r.Services, []string{"shop-api", "shop-cron"}) {
				t.Fatalf("result = %+v", r)
			}
			if p := f.local.params[0]; p["env"] != "prod" || p["services"] != "['shop-api','shop-cron']" || f.local.envs[0] != "prod" {
				t.Errorf("query = %v on %v", p, f.local.envs)
			}
			if !slices.Equal(f.renders, tc.renders) {
				t.Errorf("renders = %v, want %v", f.renders, tc.renders)
			}
		})
	}
}

// pgo.services overrides the workloads, and --since the window.
func TestPGOPull_DeclaredServicesAndSince(t *testing.T) {
	f := newPGOFixture(t)
	for i := range f.envs["prod"].Workloads {
		f.envs["prod"].Workloads[i].Build.Go.PGO = &PGOConfig{Services: []string{"api"}}
	}
	if _, err := f.pull(t, pgoPullOpts{since: 72 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	p := f.local.params[0]
	if p["services"] != "['api']" || p["from"] != fmt.Sprint(pgoTestNow.Add(-72*time.Hour).Unix()) {
		t.Errorf("query params = %v", p)
	}
}

// A default.pgo forge did not write (a hand-captured profile) is never
// replaced.
func TestPGOPull_LeavesAForeignProfileAlone(t *testing.T) {
	f := newPGOFixture(t)
	if err := os.WriteFile(f.profilePath("shop"), []byte("not a pprof"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := f.pull(t, pgoPullOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if r := pgoOnly(t, doc, "./cmd/shop"); r.Action != pgoSkipped || !strings.Contains(r.Reason, "pull only replaces a profile it wrote") {
		t.Errorf("result = %+v", r)
	}
	if got, _ := os.ReadFile(f.profilePath("shop")); string(got) != "not a pprof" {
		t.Error("a foreign profile was overwritten")
	}
}

// --binary narrows the run; one the env does not build is a usage error. So
// is an absent default env.
func TestPGOPull_UsageErrors(t *testing.T) {
	f := newPGOFixture(t)
	f.envs["prod"].Workloads = append(f.envs["prod"].Workloads, clusterWL("proxy", "k3d-x", "ns"))
	doc, err := f.pull(t, pgoPullOpts{binaries: []string{"cmd/proxy/"}})
	if err != nil || len(doc.Binaries) != 1 || doc.Binaries[0].Package != "./cmd/proxy" {
		t.Fatalf("--binary cmd/proxy/ = %+v, %v", doc.Binaries, err)
	}
	if err := runPGOPull(context.Background(), f.deps(), pgoPullOpts{binaries: []string{"./cmd/nope"}}); err == nil ||
		!strings.Contains(err.Error(), "builds no such Go binary (it builds: ./cmd/proxy, ./cmd/shop)") {
		t.Errorf("unknown --binary: %v", err)
	}
	if err := runPGOPull(context.Background(), f.deps(), pgoPullOpts{since: 31 * 24 * time.Hour}); err == nil || !strings.Contains(err.Error(), "longer than 720h") {
		t.Errorf("--since 31d: %v", err)
	}
	f.envs = map[string]*KCLEntities{"dev": f.envs["prod"], "staging": f.envs["prod"]}
	if err := runPGOPull(context.Background(), f.deps(), pgoPullOpts{}); err == nil || !strings.Contains(err.Error(), "declares no prod env") ||
		!strings.Contains(err.Error(), "dev, staging") {
		t.Errorf("no prod: %v", err)
	}
}

// The --json document's keys are a contract.
func TestPGOPull_JSONShape(t *testing.T) {
	f := newPGOFixture(t)
	if _, err := f.pull(t, pgoPullOpts{}); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(f.stdout.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["schema"] != pgoPullSchema || doc["env"] != "prod" || doc["dry_run"] != false || doc["exit_code"] != float64(0) {
		t.Errorf("document = %v", doc)
	}
	bin := doc["binaries"].([]any)[0].(map[string]any)
	for _, k := range []string{"package", "path", "action", "from_env", "source", "services", "from", "to", "edges", "total_weight_ns", "min_weight_ns", "oldest", "newest"} {
		if _, ok := bin[k]; !ok {
			t.Errorf("binary entry has no %q: %v", k, bin)
		}
	}
	if bin["path"] != "cmd/shop/default.pgo" || bin["action"] != "written" {
		t.Errorf("binary entry = %v", bin)
	}
}

// The human report names what was written and reminds once to commit it.
func TestPGOPull_TextReport(t *testing.T) {
	f := newPGOFixture(t)
	f.envs["prod"].Workloads = append(f.envs["prod"].Workloads, clusterWL("proxy", "k3d-x", "ns", pgoGo("./cmd/proxy", pgoOff())))
	if err := runPGOPull(context.Background(), f.deps(), pgoPullOpts{}); err != nil {
		t.Fatal(err)
	}
	out := f.stdout.String()
	for _, want := range []string{
		"wrote cmd/shop/default.pgo: 2 edges, 12s edge weight",
		"from env prod (local), services api, nightly, worker",
		"skipped ./cmd/proxy: PGO is off",
		"git add cmd/shop/default.pgo && git commit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

// Through the real command tree: registered, flags parsed, exit 2 surfaced.
func TestPGOPull_CommandExitCode(t *testing.T) {
	f := newPGOFixture(t)
	f.local.err = errors.New("service \"clickstack\" is not running")
	pgoDepsOverride = func(*cobra.Command) (pgoDeps, error) { return f.deps(), nil }
	t.Cleanup(func() { pgoDepsOverride = nil })
	root := newTestRootCmd(t)
	root.SetArgs([]string{"pgo", "pull", "--binary", "./cmd/shop", "--since", "2d"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.Execute()
	if exitOf(err) != 2 {
		t.Fatalf("exit = %d (%v), want 2", exitOf(err), err)
	}
	if p := f.local.params[0]; p["from"] != fmt.Sprint(pgoTestNow.Add(-48*time.Hour).Unix()) {
		t.Errorf("--since 2d not applied: %v", p)
	}
}

func TestPGOStatus(t *testing.T) {
	f := newPGOFixture(t)
	f.envs["prod"].Workloads = append(f.envs["prod"].Workloads, clusterWL("proxy", "k3d-x", "ns", pgoGo("./cmd/proxy", pgoOff())))
	f.writeForgeProfile(t, "shop")
	d := f.deps()
	if err := runPGOStatus(context.Background(), d, pgoStatusOpts{jsonOut: true}); err != nil {
		t.Fatal(err)
	}
	var doc pgoStatusDoc
	if err := json.Unmarshal(f.stdout.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != pgoStatusSchema || len(doc.Binaries) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	proxy, shop := doc.Binaries[0], doc.Binaries[1]
	if proxy.Config.Enabled || proxy.Exists {
		t.Errorf("proxy = %+v", proxy)
	}
	if !shop.Exists || !shop.ForgeWritten || shop.Provenance == nil || shop.Provenance.Env != "prod" || shop.Provenance.Edges != 1 ||
		shop.Description == "" || shop.Config.Since != "168h" || shop.Config.MinWeight != "10s" {
		t.Errorf("shop = %+v (provenance %+v)", shop, shop.Provenance)
	}

	f.stdout.Reset()
	if err := runPGOStatus(context.Background(), d, pgoStatusOpts{}); err != nil {
		t.Fatal(err)
	}
	out := f.stdout.String()
	for _, want := range []string{"cmd/shop/default.pgo (from prod, data up to", "profile: off (pgo.enabled = False", "since=168h min_weight=10s"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestPGODuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		168 * time.Hour: "168h", 90 * time.Minute: "1h30m", 3200 * time.Millisecond: "3.2s", 10 * time.Second: "10s", 2 * time.Minute: "2m",
	} {
		if got := pgoDuration(d); got != want {
			t.Errorf("pgoDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
