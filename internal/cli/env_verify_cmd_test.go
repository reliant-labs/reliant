package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	vDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	vDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// verifyHooks answers each workload's hooks and records every hook run, so a
// test can assert verify ran nothing but status and plan.
type verifyHooks struct {
	status map[string]string // workload -> stdout ("" = exit 1)
	plan   map[string]int    // workload -> exit code
	ran    []string          // "<workload>:<hook>"
	env    map[string]map[string]string
}

func (h *verifyHooks) RunHook(_ context.Context, inv deploytarget.HookInvocation) (int, error) {
	w, hook := inv.Env["FORGE_WORKLOAD"], inv.Env["FORGE_HOOK"]
	h.ran = append(h.ran, w+":"+hook)
	if h.env == nil {
		h.env = map[string]map[string]string{}
	}
	h.env[w+":"+hook] = inv.Env
	switch hook {
	case deploytarget.HookStatus:
		out, ok := h.status[w]
		if !ok || out == "" {
			return 1, nil
		}
		_, _ = io.WriteString(inv.Stdout, out)
		return 0, nil
	case deploytarget.HookPlan:
		code := h.plan[w]
		_, _ = fmt.Fprintf(inv.Stdout, "plan of %s\n", w)
		return code, nil
	}
	return 0, nil
}

// writeCountingBindings is a FULL bindingStore — Append included — so the test
// can prove verify never reaches the write half even when handed one.
type writeCountingBindings struct {
	current release.Promotion
	bound   bool
	appends int
}

func (b *writeCountingBindings) Current(context.Context, string) (release.Promotion, bool, error) {
	return b.current, b.bound, nil
}

func (b *writeCountingBindings) Append(_ context.Context, p release.Promotion, _ appendGuard) (release.Promotion, error) {
	b.appends++
	return p, nil
}

func (b *writeCountingBindings) Location() string { return "test ledger" }

var _ bindingStore = (*writeCountingBindings)(nil)

type fixedLister struct {
	images []cluster.WorkloadImage
	err    error
}

func (l fixedLister) ListWorkloadImages(context.Context, string, string) ([]cluster.WorkloadImage, error) {
	return l.images, l.err
}

func externalWorkload(name, image string, rt deploytarget.ExternalRuntime) WorkloadEntity {
	return WorkloadEntity{
		Name:    name,
		Image:   image,
		Runtime: RuntimeEntity{Type: RuntimeExternal, External: &rt},
		Spec:    deployv1alpha1.WorkloadSpec{Image: image},
	}
}

func statusDoc(d string) string { return fmt.Sprintf(`{"digest":%q,"health":"healthy"}`, d) }

type verifyFixture struct {
	entities *KCLEntities
	bindings *writeCountingBindings
	hooks    *verifyHooks
	lister   fixedLister
	diffs    []clusterLiveDiff
	diffErr  error
	renders  int
}

func (f *verifyFixture) deps() envVerifyDeps {
	return envVerifyDeps{
		Render: func(context.Context, string, string) (*KCLEntities, error) {
			f.renders++
			return f.entities, nil
		},
		Ledger: func(context.Context, string, string, *KCLEntities) (envPromotionReader, envLedger, error) {
			return f.bindings, envLedger{Bindings: f.bindings}, nil
		},
		Hooks:  deploytarget.ExternalHooks{Runner: f.hooks, Stderr: io.Discard},
		Lister: f.lister,
		LiveDiff: func(context.Context, string, string, string, envLedger) ([]clusterLiveDiff, error) {
			return f.diffs, f.diffErr
		},
		Secrets: func(*KCLEntities) hookSecretResolver { return nil },
		Stderr:  io.Discard,
	}
}

func boundTo(digests map[string]string) *writeCountingBindings {
	return &writeCountingBindings{bound: true, current: release.Promotion{Release: "v1.4.0", Resolved: digests}}
}

func runVerify(t *testing.T, f *verifyFixture, opts envVerifyOptions) (envVerifyDocument, error) {
	t.Helper()
	opts.Env, opts.JSON = "prod", true
	var out bytes.Buffer
	err := runEnvVerify(context.Background(), &out, opts, f.deps())
	var doc envVerifyDocument
	if jerr := json.Unmarshal(out.Bytes(), &doc); jerr != nil {
		t.Fatalf("--json printed no document (%v):\n%s", jerr, out.String())
	}
	if doc.ExitCode != exitCodeForError(err) {
		t.Errorf("document exit_code %d, process would exit %d", doc.ExitCode, exitCodeForError(err))
	}
	return doc, err
}

func rowOf(t *testing.T, doc envVerifyDocument, name string) envVerifyRow {
	t.Helper()
	for _, r := range doc.Workloads {
		if r.Workload == name {
			return r
		}
	}
	t.Fatalf("no row for %s in %+v", name, doc.Workloads)
	return envVerifyRow{}
}

var fullHooks = deploytarget.ExternalRuntime{
	Status: []string{"status"}, Plan: []string{"plan"}, Deploy: []string{"deploy"}, Health: []string{"health"},
}

func TestEnvVerify_ExternalMatch(t *testing.T) {
	f := &verifyFixture{
		entities: &KCLEntities{Project: "acme", Workloads: []WorkloadEntity{externalWorkload("api", "ghcr.io/acme/api:v1", fullHooks)}},
		bindings: boundTo(map[string]string{"ghcr.io/acme/api": vDigestA}),
		hooks:    &verifyHooks{status: map[string]string{"api": statusDoc(vDigestA)}, plan: map[string]int{"api": 0}},
	}
	doc, err := runVerify(t, f, envVerifyOptions{})
	if err != nil || doc.ExitCode != exitOK || !doc.OK {
		t.Fatalf("all match must exit 0: err=%v doc=%+v", err, doc)
	}
	r := rowOf(t, doc, "api")
	if r.State != verifyMatch || r.Image.Expected != vDigestA || r.Image.Running != vDigestA || r.Config.State != verifyMatch {
		t.Errorf("row = %+v", r)
	}
	// The plan ran against the PINNED image, with the promotion's release.
	env := f.hooks.env["api:plan"]
	if env["FORGE_IMAGE"] != "ghcr.io/acme/api@"+vDigestA || env["FORGE_DIGEST"] != vDigestA || env["FORGE_RELEASE"] != "v1.4.0" {
		t.Errorf("plan env = %v", env)
	}
	if doc.Release != "v1.4.0" || doc.Summary.Match != 1 {
		t.Errorf("doc = %+v", doc)
	}
}

func TestEnvVerify_ImageDriftExits1(t *testing.T) {
	f := &verifyFixture{
		entities: &KCLEntities{Workloads: []WorkloadEntity{externalWorkload("api", "ghcr.io/acme/api", fullHooks)}},
		bindings: boundTo(map[string]string{"ghcr.io/acme/api": vDigestA}),
		hooks:    &verifyHooks{status: map[string]string{"api": statusDoc(vDigestB)}, plan: map[string]int{"api": 0}},
	}
	doc, err := runVerify(t, f, envVerifyOptions{})
	if exitCodeForError(err) != exitWrong {
		t.Fatalf("drift must exit 1, got %d (%v)", exitCodeForError(err), err)
	}
	if r := rowOf(t, doc, "api"); r.Image.State != verifyDrift || r.Image.Running != vDigestB || r.State != verifyDrift {
		t.Errorf("row = %+v", r)
	}
}

func TestEnvVerify_ConfigDriftShowsThePlan(t *testing.T) {
	f := &verifyFixture{
		entities: &KCLEntities{Workloads: []WorkloadEntity{externalWorkload("api", "ghcr.io/acme/api", fullHooks)}},
		bindings: boundTo(map[string]string{"ghcr.io/acme/api": vDigestA}),
		hooks:    &verifyHooks{status: map[string]string{"api": statusDoc(vDigestA)}, plan: map[string]int{"api": 2}},
	}
	doc, err := runVerify(t, f, envVerifyOptions{})
	if exitCodeForError(err) != exitWrong {
		t.Fatalf("config drift must exit 1, got %v", err)
	}
	r := rowOf(t, doc, "api")
	if r.Image.State != verifyMatch || r.Config.State != verifyDrift || r.Config.Output != "plan of api" {
		t.Errorf("row = %+v", r)
	}
}

// A missing hook is UNVERIFIED, never "no diff" — and a record-only binding
// (no hooks at all) is unverified on both checks.
func TestEnvVerify_MissingHooksAreUnverified(t *testing.T) {
	statusOnly := deploytarget.ExternalRuntime{Status: []string{"status"}}
	f := &verifyFixture{
		entities: &KCLEntities{Workloads: []WorkloadEntity{
			externalWorkload("api", "ghcr.io/acme/api", statusOnly),
			externalWorkload("worker", "ghcr.io/acme/worker", deploytarget.ExternalRuntime{}),
		}},
		bindings: boundTo(map[string]string{"ghcr.io/acme/api": vDigestA, "ghcr.io/acme/worker": vDigestA}),
		hooks:    &verifyHooks{status: map[string]string{"api": statusDoc(vDigestA)}},
	}
	doc, err := runVerify(t, f, envVerifyOptions{})
	if exitCodeForError(err) != exitUndetermined {
		t.Fatalf("unverified must exit 2, got %d (%v)", exitCodeForError(err), err)
	}
	api := rowOf(t, doc, "api")
	if api.Image.State != verifyMatch || api.Config.State != verifyUnverified || !strings.Contains(api.Config.Detail, "no plan hook") {
		t.Errorf("api = %+v", api)
	}
	worker := rowOf(t, doc, "worker")
	if worker.Image.State != verifyUnverified || worker.Config.State != verifyUnverified || worker.State != verifyUnverified {
		t.Errorf("worker (record-only) = %+v", worker)
	}
	if doc.Summary.Unverified != 2 || doc.Summary.Match != 0 {
		t.Errorf("summary = %+v: a partly checked row is not a match", doc.Summary)
	}
}

func TestEnvVerify_HookErrorsAreErrorsNotMatches(t *testing.T) {
	f := &verifyFixture{
		entities: &KCLEntities{Workloads: []WorkloadEntity{externalWorkload("api", "ghcr.io/acme/api", fullHooks)}},
		bindings: boundTo(map[string]string{"ghcr.io/acme/api": vDigestA}),
		// status exits 1; plan exits 7 (neither 0 nor 2).
		hooks: &verifyHooks{status: map[string]string{}, plan: map[string]int{"api": 7}},
	}
	doc, err := runVerify(t, f, envVerifyOptions{})
	if exitCodeForError(err) != exitUndetermined {
		t.Fatalf("hook errors must exit 2, got %v", err)
	}
	r := rowOf(t, doc, "api")
	if r.Image.State != verifyError || r.Config.State != verifyError || r.State != verifyError {
		t.Errorf("row = %+v", r)
	}
	if !strings.Contains(r.Config.Detail, "exited 7") {
		t.Errorf("config detail = %q", r.Config.Detail)
	}
}

// Before any forge release exists the expected digest comes from --image;
// without it, there is nothing to compare against and the row says so.
func TestEnvVerify_ImageFlagStandsInForAPromotion(t *testing.T) {
	newFixture := func() *verifyFixture {
		return &verifyFixture{
			entities: &KCLEntities{Workloads: []WorkloadEntity{externalWorkload("api", "ghcr.io/acme/api:v1", fullHooks)}},
			bindings: &writeCountingBindings{},
			hooks:    &verifyHooks{status: map[string]string{"api": statusDoc(vDigestB)}, plan: map[string]int{"api": 0}},
		}
	}
	doc, err := runVerify(t, newFixture(), envVerifyOptions{})
	if exitCodeForError(err) != exitUndetermined {
		t.Fatalf("no promotion and no --image: want 2, got %v", err)
	}
	if r := rowOf(t, doc, "api"); r.Image.State != verifyUnverified || r.Image.Running != vDigestB || !strings.Contains(r.Image.Detail, "--image api=") {
		t.Errorf("row = %+v", r)
	}

	f := newFixture()
	doc, err = runVerify(t, f, envVerifyOptions{Images: map[string]string{"api": "ghcr.io/acme/api@" + vDigestB}})
	if err != nil {
		t.Fatalf("--image matching what runs: %v (%+v)", err, doc)
	}
	if got := f.hooks.env["api:plan"]["FORGE_IMAGE"]; got != "ghcr.io/acme/api@"+vDigestB {
		t.Errorf("plan FORGE_IMAGE = %q, want the --image ref", got)
	}
}

func TestEnvVerify_ParseImageFlags(t *testing.T) {
	if _, err := parseVerifyImages([]string{"api=ghcr.io/acme/api:v1"}); err == nil {
		t.Error("a tag proves nothing; --image must require a digest")
	}
	if _, err := parseVerifyImages([]string{"api=ghcr.io/a@" + vDigestA, "api=ghcr.io/b@" + vDigestA}); err == nil {
		t.Error("a workload named twice must be refused")
	}
	got, err := parseVerifyImages([]string{"api=ghcr.io/a/api@" + vDigestA})
	if err != nil || got["api"] != "ghcr.io/a/api@"+vDigestA {
		t.Errorf("got %v %v", got, err)
	}
	f := &verifyFixture{entities: &KCLEntities{Workloads: []WorkloadEntity{externalWorkload("api", "ghcr.io/acme/api", fullHooks)}}, bindings: &writeCountingBindings{}, hooks: &verifyHooks{}}
	if _, err := runVerify(t, f, envVerifyOptions{Images: map[string]string{"nope": "x@" + vDigestA}}); exitCodeForError(err) != exitWrong || !strings.Contains(err.Error(), `no workload "nope"`) {
		t.Errorf("an --image for an undeclared workload: %v", err)
	}
}

func TestEnvVerify_ClusterWorkloads(t *testing.T) {
	web := WorkloadEntity{
		Name:    "web",
		Image:   "ghcr.io/acme/web",
		Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "gke_prod", Namespace: "acme"}},
		Spec:    deployv1alpha1.WorkloadSpec{Image: "ghcr.io/acme/web:v1"},
	}
	running := fixedLister{images: []cluster.WorkloadImage{{Kind: "Deployment", Name: "web", Container: "web", Image: "ghcr.io/acme/web@" + vDigestA}}}

	// Matching image, bundle diff clean.
	f := &verifyFixture{
		entities: &KCLEntities{Workloads: []WorkloadEntity{web}},
		bindings: boundTo(map[string]string{"ghcr.io/acme/web": vDigestA}),
		hooks:    &verifyHooks{}, lister: running,
		diffs: []clusterLiveDiff{{Cluster: "gke_prod", Changed: []string{}}},
	}
	doc, err := runVerify(t, f, envVerifyOptions{})
	if err != nil {
		t.Fatalf("cluster match: %v %+v", err, doc)
	}

	// The bundle cannot be found: config is "not verifiable without a build".
	f.diffs, f.diffErr = nil, errors.New("no bundle is recorded for release v1.4.0")
	doc, err = runVerify(t, f, envVerifyOptions{})
	if exitCodeForError(err) != exitUndetermined {
		t.Fatalf("no bundle: want 2, got %v", err)
	}
	if r := rowOf(t, doc, "web"); r.Image.State != verifyMatch || r.Config.State != verifyUnverified || !strings.Contains(r.Config.Detail, "not verifiable without a build") {
		t.Errorf("row = %+v", r)
	}

	// The live diff names changed objects: drift, with them listed.
	f.diffs, f.diffErr = []clusterLiveDiff{{Cluster: "gke_prod", Changed: []string{"apps.v1.Deployment.acme.web"}}}, nil
	doc, err = runVerify(t, f, envVerifyOptions{})
	if exitCodeForError(err) != exitWrong {
		t.Fatalf("live drift: want 1, got %v", err)
	}
	if r := rowOf(t, doc, "web"); r.Config.State != verifyDrift || r.Config.Output != "apps.v1.Deployment.acme.web" {
		t.Errorf("row = %+v", r)
	}
}

func TestEnvVerify_OtherRuntimes(t *testing.T) {
	f := &verifyFixture{
		entities: &KCLEntities{Workloads: []WorkloadEntity{
			{Name: "local", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}},
			{Name: "site", Runtime: RuntimeEntity{Type: RuntimeHosted, Hosted: &HostedRuntime{}}},
		}},
		bindings: &writeCountingBindings{}, hooks: &verifyHooks{},
	}
	doc, err := runVerify(t, f, envVerifyOptions{})
	if exitCodeForError(err) != exitUndetermined {
		t.Fatalf("a hosted workload verify cannot read is unverified (2), got %v", err)
	}
	if r := rowOf(t, doc, "local"); r.State != verifySkipped {
		t.Errorf("host = %+v", r)
	}
	if r := rowOf(t, doc, "site"); r.State != verifyUnverified || !strings.Contains(r.Image.Detail, "forge env status prod") {
		t.Errorf("hosted = %+v", r)
	}
}

// THE GUARANTEE: verify builds, pushes, records and deploys nothing. It runs
// only the read-only hooks, never appends to the ledger even when handed a
// store that could, renders once, and writes no file into the project.
func TestEnvVerify_HasNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f := &verifyFixture{
		entities: &KCLEntities{Workloads: []WorkloadEntity{
			externalWorkload("api", "ghcr.io/acme/api", fullHooks),
			externalWorkload("worker", "ghcr.io/acme/worker", fullHooks),
		}},
		bindings: boundTo(map[string]string{"ghcr.io/acme/api": vDigestA, "ghcr.io/acme/worker": vDigestA}),
		hooks: &verifyHooks{
			status: map[string]string{"api": statusDoc(vDigestA), "worker": statusDoc(vDigestB)},
			plan:   map[string]int{"api": 0, "worker": 2},
		},
	}
	_, _ = runVerify(t, f, envVerifyOptions{})
	for _, ran := range f.hooks.ran {
		if strings.HasSuffix(ran, ":deploy") || strings.HasSuffix(ran, ":health") {
			t.Errorf("verify ran %s: only status and plan are read-only", ran)
		}
	}
	if f.bindings.appends != 0 {
		t.Errorf("verify appended %d promotion(s) to the ledger", f.bindings.appends)
	}
	if f.renders != 1 {
		t.Errorf("verify rendered %d times; one plain render, never a build render", f.renders)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("verify wrote into the project directory: %v", names)
	}
}

// `forge env verify` is registered, and it is the parity command — not the
// release half `forge env status` absorbed.
func TestEnvVerify_IsTheParityCommand(t *testing.T) {
	env := newEnvCmd()
	found, _, err := env.Find([]string{"verify"})
	if err != nil || found.Name() != "verify" {
		t.Fatalf("`forge env verify` must resolve: %v", err)
	}
	if !strings.Contains(found.Long, "NO side effects") || found.Flags().Lookup("image") == nil {
		t.Errorf("`forge env verify` is not the parity command: %q", found.Short)
	}
}
