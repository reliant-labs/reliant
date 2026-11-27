package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/internal/testcollect"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/testreport"
)

func targetsByName(t *testing.T, e *KCLEntities, projectDir string) map[string]testcollect.Target {
	t.Helper()
	targets, err := collectTargets(e, projectDir, e.Env, true)
	if err != nil {
		t.Fatalf("collectTargets: %v", err)
	}
	out := map[string]testcollect.Target{}
	for _, tg := range targets {
		out[tg.Workload] = tg
	}
	return out
}

// Each workload resolves to where its coverage is, by runtime, with the
// same instrumented answer the build used (ResolveCoverBinaries).
func TestCollectTargets(t *testing.T) {
	root := t.TempDir()
	tool := hostWL("seed")
	tool.Kind = string(deployv1alpha1.KindTool)
	hosted := hostWL("edge")
	hosted.Runtime = RuntimeEntity{Type: RuntimeHosted, Hosted: &HostedRuntime{}}
	prebuilt := clusterWL("web", "k3d-dev", "", func(w *WorkloadEntity) { w.Build = BuildConfigEntity{} })
	compose := hostWL("billing")
	compose.Runtime = RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: "billing-svc"}}
	e := &KCLEntities{Env: "dev", Lifecycle: "local", Project: "shop", ClusterTarget: &ClusterTargetEntity{Cluster: "k3d-dev", Namespace: "shop-dev"},
		Workloads: []WorkloadEntity{
			hostWL("api"),
			hostWL("worker", withEnv("PPROF_ADDR", "0.0.0.0:6061")),
			hostWL("migrate", coverGo("./cmd/migrate", boolp(false))),
			clusterWL("orders", "", "", withEnv("PPROF_ADDR", "127.0.0.1:6070")),
			prebuilt, hosted, tool, compose,
		}}
	got := targetsByName(t, e, root)

	if _, ok := got["seed"]; ok {
		t.Error("a tool never runs, so it has nothing to collect")
	}
	api := got["api"]
	if api.Host == nil || api.Host.PullAddr != "127.0.0.1:6060" || api.Host.CoverDir != filepath.Join(root, ".forge", "tests", "covdata", "dev", "api") {
		t.Errorf("api %+v host %+v, want a pull on the default debug listener and the env's GOCOVERDIR", api, api.Host)
	}
	if w := got["worker"]; w.Host == nil || w.Host.PullAddr != "" || !strings.Contains(w.Host.Withheld, "not loopback") {
		t.Errorf("worker host %+v, want the pull withheld: pprof binds 0.0.0.0", w.Host)
	}
	if m := got["migrate"]; m.Coverage != testcollect.CoverageNotInstrumented {
		t.Errorf("migrate %+v, want not instrumented", m)
	}
	if w := got["web"]; w.Coverage != testcollect.CoverageUnavailable {
		t.Errorf("web %+v, want unavailable (not a forge Go build)", w)
	}
	o := got["orders"]
	if o.Cluster == nil || *o.Cluster != (testcollect.ClusterTarget{Context: "k3d-dev", Namespace: "shop-dev", Selector: "app.kubernetes.io/name=orders", Port: 6070}) {
		t.Errorf("orders cluster %+v", o.Cluster)
	}
	if h := got["edge"]; h.Unreachable == "" {
		t.Errorf("edge %+v, want a hosted workload unreachable", h)
	}
	b := got["billing"]
	if b.Compose == nil || b.Compose.Service != "billing-svc" || b.Compose.CoverDir != "/tmp" || b.Compose.WorkingDir != root || !strings.HasPrefix(b.Compose.Project, "forge-") {
		t.Errorf("billing compose %+v", b.Compose)
	}

	// A persistent env instruments nothing by default.
	e.Lifecycle = ""
	for name, tg := range targetsByName(t, e, root) {
		if tg.Coverage == "" && tg.Unreachable == "" {
			t.Errorf("persistent env: %s is collectable (%+v)", name, tg)
		}
	}
}

// Two host workloads on one debug listener: only one bound it, so a pull
// could read the wrong process. Both fall back to their exit flush.
func TestCollectTargetsSharedDebugListener(t *testing.T) {
	e := &KCLEntities{Env: "dev", Lifecycle: "local", Workloads: []WorkloadEntity{hostWL("api"), hostWL("worker")}}
	for name, tg := range targetsByName(t, e, t.TempDir()) {
		if tg.Host.PullAddr != "" || !strings.Contains(tg.Host.Withheld, "api, worker share the debug listener 127.0.0.1:6060") {
			t.Errorf("%s: %+v", name, tg.Host)
		}
	}
}

func coverageTar(t *testing.T, files ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range files {
		body := []byte("data " + name)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	return buf.Bytes()
}

// newCollectRun makes a run directory the way `forge test begin` leaves it,
// in a project whose forge.yaml opts in to coverage.
func newCollectRun(t *testing.T, root string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: shop\ntests:\n  coverage: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(root, ".forge", "tests", "runs", "01RUN")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(testreport.RunManifest{Schema: testreport.RunManifestSchema, ID: "01RUN", Lanes: []string{}})
	if err := os.WriteFile(filepath.Join(run, testreport.RunManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return run
}

// The verb, from a render to a recorded lane: the host workload's debug
// listener is pulled, its build written under the run, and the document
// validates against the published forge.test.collect/v1 schema.
func TestRunTestCollectHost(t *testing.T) {
	root := t.TempDir()
	run := newCollectRun(t, root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testcollect.CoveragePath {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(coverageTar(t, "covmeta.ab", "covcounters.ab.7.100"))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	e := &KCLEntities{Env: "dev", Lifecycle: "local", Workloads: []WorkloadEntity{
		hostWL("api", withEnv("PPROF_ADDR", addr)),
		clusterWL("web", "k3d-dev", "ns", func(w *WorkloadEntity) { w.Build = BuildConfigEntity{} }),
	}}
	req := testCollectRequest{projectDir: root, env: "dev", run: run, lane: "e2e", command: []string{"forge", "test", "collect", "dev"}}
	prev := testCollectConvert
	testCollectConvert = func(context.Context, string, string) ([]testreport.CovdataBuild, error) {
		return []testreport.CovdataBuild{{MetaHash: "ab", Profile: []byte("mode: atomic\nexample.com/shop/cmd/api/main.go:1.1,2.2 1 1\n")}}, nil
	}
	t.Cleanup(func() { testCollectConvert = prev })

	doc, err := runTestCollect(context.Background(), e, req)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if doc.Run != run || doc.Collected != 1 || doc.Workloads[0].State != testcollect.StateCollected {
		t.Fatalf("doc %+v", doc)
	}
	if c := doc.Workloads[0].Build.Coverage; c == nil || c.Statements != 1 || c.Covered != 1 {
		t.Errorf("converted coverage %+v", c)
	}
	m, err := testreport.ReadLaneManifest(run, "e2e")
	if err != nil || len(m.Coverage) != 1 || m.Coverage[0].Path != "covdata/api/" {
		t.Fatalf("lane %+v %v", m, err)
	}

	var schema bytes.Buffer
	if err := runProjectSchema(&schema, "test-collect"); err != nil {
		t.Fatal(err)
	}
	compiled, err := schemagen.Compile(schema.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	v, err := schemagen.DecodeJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if errs := compiled.Validate(v); len(errs) > 0 {
		t.Errorf("the document does not validate against forge.test.collect/v1: %v", errs)
	}

	req.workloads = []string{"nope"}
	if _, err := runTestCollect(context.Background(), e, req); exitcode.Of(err) != exitWrong || !strings.Contains(err.Error(), "api, web") {
		t.Errorf("unknown --workload: %v, want exit 1 naming the workloads", err)
	}
}

// Only running pods are pulled, oldest first, so the newest pod's build is
// the one testcollect ranks current.
func TestRunningPods(t *testing.T) {
	t0 := metav1.NewTime(time.Unix(1_700_000_000, 0))
	t1 := metav1.NewTime(t0.Add(time.Minute))
	gone := metav1.NewTime(t0.Time)
	pods := runningPods([]corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "new"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &t1}},
		{ObjectMeta: metav1.ObjectMeta{Name: "old", CreationTimestamp: t0}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pending"}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
		{ObjectMeta: metav1.ObjectMeta{Name: "terminating", DeletionTimestamp: &gone}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	})
	if len(pods) != 2 || pods[0].Name != "old" || pods[1].Name != "new" || !pods[1].Started.Equal(t1.Time) {
		t.Errorf("running pods %+v, want old then new", pods)
	}
}

// Before a teardown, a collect runs only when a run is active or --collect
// asks, never fails, and says why it collected nothing.
func TestCollectBeforeTeardown(t *testing.T) {
	root := t.TempDir()
	run := newCollectRun(t, root)
	closed := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(closed.URL, "http://")
	closed.Close()
	e := &KCLEntities{Env: "dev", Lifecycle: "local", Workloads: []WorkloadEntity{hostWL("api", withEnv("PPROF_ADDR", addr))}}

	t.Setenv(testcapture.EnvRun, "")
	var out bytes.Buffer
	collectBeforeTeardown(context.Background(), root, "dev", false, e, &out)
	if out.Len() != 0 {
		t.Errorf("no run and no --collect, yet it printed:\n%s", out.String())
	}

	t.Setenv(testcapture.EnvRun, run)
	collectBeforeTeardown(context.Background(), root, "dev", false, e, &out)
	if s := out.String(); !strings.Contains(s, "api") || !strings.Contains(s, "warning: coverage not collected before teardown") {
		t.Errorf("an active run and an unreachable workload: want the workload reported and a warning, got:\n%s", s)
	}

	out.Reset()
	plain := &KCLEntities{Env: "dev", Lifecycle: "local", Workloads: []WorkloadEntity{hostWL("api", coverGo("./cmd/api", boolp(false)))}}
	collectBeforeTeardown(context.Background(), root, "dev", true, plain, &out)
	if out.Len() != 0 {
		t.Errorf("nothing instrumented, yet it printed:\n%s", out.String())
	}

	if !teardownCollectRequested(withTeardownCollect(context.Background(), true)) || teardownCollectRequested(context.Background()) {
		t.Error("--collect is not carried by the context")
	}
}
