// Tests for `forge env render` — the read-only manifest printer.
//
// The load-bearing property is CLUSTER ATTRIBUTION, so most of what follows
// drives a two-cluster environment through the real routing path
// (buildDeployGroups → clusterScopeForGroups → cluster.ScopeManifestsToGroup)
// with a rendered entity contract, and asserts each document lands where a
// deploy would put it. The fixture is control-plane's dev shape reduced to
// its essentials, because that is the shape the command exists for: one
// cluster holding most of the stack, one service overridden onto another,
// and env-shared resources belonging to neither.

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// twoClusterContract is a rendered `output` contract with two cluster-shaped
// services on DIFFERENT clusters plus an operator, which carries no cluster
// of its own and therefore rides the env's main cluster.
const twoClusterContract = `{
  "output": {
    "workloads": [
      {
        "name": "daemon-gateway",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-control-plane",
          "namespace": "cp-dev"
        },
        "spec": {
          "kind": "service"
        }
      },
      {
        "name": "workspace-proxy",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-cp-daemon",
          "namespace": "cp-dev"
        },
        "spec": {
          "kind": "service"
        }
      },
      {
        "name": "admin-server",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service"
        }
      },
      {
        "name": "workspace-controller",
        "kind": "operator",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-control-plane",
          "namespace": "cp-dev"
        },
        "spec": {
          "kind": "operator"
        }
      }
    ]
  }
}`

// twoClusterManifests is the stream that contract renders: one document per
// routing case the scoper distinguishes.
const twoClusterManifests = `apiVersion: v1
kind: Namespace
metadata:
  name: cp-dev
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: daemon-gateway
  namespace: cp-dev
  labels:
    app.kubernetes.io/name: daemon-gateway
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-proxy
  namespace: cp-dev
  labels:
    app.kubernetes.io/name: workspace-proxy
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-controller
  namespace: cp-dev
  labels:
    app.kubernetes.io/name: workspace-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: public
  namespace: cp-dev
  labels:
    forge.dev/cluster: k3d-cp-daemon
`

// renderFixture renders the contract into typed entities and returns the
// pieces attributeRenderedObjects consumes, through the same deploy-path
// helpers the command uses.
func renderFixture(t *testing.T, contract string) *KCLEntities {
	t.Helper()
	entities, err := parseKCLEntities([]byte(contract))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	return entities
}

func attributeFixture(t *testing.T, contract, manifests string) ([]renderedObject, []string) {
	t.Helper()
	entities := renderFixture(t, contract)
	groups, err := buildDeployGroups("dev", entities, "cp-dev")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	return attributeRenderedObjects(manifests, groups, entities)
}

func objectByName(t *testing.T, objects []renderedObject, name string) renderedObject {
	t.Helper()
	for _, o := range objects {
		if o.Name == name {
			return o
		}
	}
	t.Fatalf("no rendered object named %q in %d object(s)", name, len(objects))
	return renderedObject{}
}

// TestEnvRenderAttributesEachObjectToItsCluster is the whole reason the
// command exists: a two-cluster environment renders ONE stream, and reading
// it without knowing which cluster each document lands on is how an audit
// deletes the wrong object out of the wrong cluster.
//
// The four cases below are ScopeManifestsToGroup's four rules, and the
// assertion is that the printer agrees with the router on every one.
func TestEnvRenderAttributesEachObjectToItsCluster(t *testing.T) {
	objects, clusters := attributeFixture(t, twoClusterContract, twoClusterManifests)

	if got, want := strings.Join(clusters, ","), "k3d-control-plane,k3d-cp-daemon"; got != want {
		t.Fatalf("env clusters: got %q, want %q", got, want)
	}
	if len(objects) != 5 {
		t.Fatalf("objects: got %d, want 5", len(objects))
	}

	for name, want := range map[string]string{
		// Owned by a service that declares k3d-control-plane.
		"daemon-gateway": "k3d-control-plane",
		// Owned by the one service overridden onto the other cluster.
		"workspace-proxy": "k3d-cp-daemon",
		// An operator carries no cluster of its own; it rides the env's
		// main cluster (the first cluster-shaped service's) and is dropped
		// from every other — replicating it would leave a pod stuck
		// ContainerCreating on a cluster with none of its RBAC.
		"workspace-controller": "k3d-control-plane",
		// The first-class routing label wins outright, with no app-label
		// indirection: this Gateway goes to the named cluster only.
		"public": "k3d-cp-daemon",
		// Unlabelled and env-shared: the deploy layer never picks a cluster
		// for it, it applies to every one.
		"cp-dev": "k3d-control-plane, k3d-cp-daemon",
	} {
		if got := describeClusters(objectByName(t, objects, name).Clusters); got != want {
			t.Errorf("%s lands on %q, want %q", name, got, want)
		}
	}
}

// TestEnvRenderSingleClusterAttributesEverything covers the common shape.
// A single-cluster env is never partitioned at deploy time (ClusterScope
// stays nil and the whole stream applies), so every object — including the
// ones carrying another env's routing label — must be attributed to the one
// cluster rather than filtered by a scope that does not run.
func TestEnvRenderSingleClusterAttributesEverything(t *testing.T) {
	const contract = `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "gke-prod",
          "namespace": "prod"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	objects, clusters := attributeFixture(t, contract, twoClusterManifests)

	if len(clusters) != 1 || clusters[0] != "gke-prod" {
		t.Fatalf("env clusters: got %v, want [gke-prod]", clusters)
	}
	for _, o := range objects {
		if describeClusters(o.Clusters) != "gke-prod" {
			t.Errorf("%s %s lands on %q, want gke-prod", o.Kind, o.Name, describeClusters(o.Clusters))
		}
	}
}

// TestEnvRenderNoClusterIsReportedNotGuessed: a host-only or compose-only
// environment declares no cluster at all. An empty cluster column reads as
// "nothing here"; the truth is "this environment targets no cluster", and
// the report has to say the second thing.
func TestEnvRenderNoClusterIsReportedNotGuessed(t *testing.T) {
	const contract = `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	objects, clusters := attributeFixture(t, contract, twoClusterManifests)

	if len(clusters) != 0 {
		t.Fatalf("env clusters: got %v, want none", clusters)
	}
	for _, o := range objects {
		if got := describeClusters(o.Clusters); got != "(none declared)" {
			t.Errorf("%s %s: got %q, want (none declared)", o.Kind, o.Name, got)
		}
	}
}

// TestEnvRenderStreamIsApplyable pins the stdout contract: a `---`-separated
// document stream a caller can pipe straight into kubectl, with the cluster
// carried as a YAML comment so it survives that pipe instead of breaking it.
func TestEnvRenderStreamIsApplyable(t *testing.T) {
	objects, _ := attributeFixture(t, twoClusterContract, twoClusterManifests)

	var out bytes.Buffer
	if err := writeRenderedStream(&out, objects); err != nil {
		t.Fatalf("writeRenderedStream: %v", err)
	}
	got := out.String()

	// One annotation per object, one separator between each pair.
	if n := strings.Count(got, "# cluster:"); n != len(objects) {
		t.Errorf("annotations: got %d, want %d", n, len(objects))
	}
	if n := strings.Count(got, "\n---\n"); n != len(objects)-1 {
		t.Errorf("document separators: got %d, want %d", n, len(objects)-1)
	}
	if strings.HasPrefix(got, "---") {
		t.Error("stream starts with a separator; the first document needs none")
	}
	if !strings.Contains(got, "# cluster: k3d-cp-daemon\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: workspace-proxy") {
		t.Errorf("workspace-proxy is not annotated with its cluster:\n%s", got)
	}
	// Round-trip: dropping the comments must give back exactly the render.
	var body []string
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasPrefix(line, "# cluster:") {
			body = append(body, line)
		}
	}
	if rejoined, want := strings.Join(body, "\n"), strings.TrimSpace(twoClusterManifests); rejoined != want {
		t.Errorf("stream is not the render verbatim:\n got:\n%s\nwant:\n%s", rejoined, want)
	}
}

// TestEnvRenderListNamesEveryObject covers the inventory view — the one an
// audit actually reads before deciding anything.
func TestEnvRenderListNamesEveryObject(t *testing.T) {
	objects, _ := attributeFixture(t, twoClusterContract, twoClusterManifests)

	var out bytes.Buffer
	if err := writeRenderedTable(&out, objects); err != nil {
		t.Fatalf("writeRenderedTable: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(objects)+1 { // + header
		t.Fatalf("rows: got %d, want %d", len(lines)-1, len(objects))
	}
	if !strings.HasPrefix(lines[0], "CLUSTER") || !strings.HasSuffix(lines[0], "APP") {
		t.Errorf("header row should run CLUSTER..APP: %q", lines[0])
	}
	joined := out.String()
	for _, want := range []string{"k3d-cp-daemon", "workspace-proxy", "daemon-gateway", "cp-dev"} {
		if !strings.Contains(joined, want) {
			t.Errorf("table omits %q:\n%s", want, joined)
		}
	}
	// A cluster-scoped, env-shared object has neither a namespace nor an
	// owning app; both columns say so rather than leaving blanks that read
	// as "the default one" and "unknown".
	if !regexp.MustCompile(`Namespace\s+-\s+cp-dev\s+-`).MatchString(joined) {
		t.Errorf("env-shared object should show dashes for namespace and app:\n%s", joined)
	}
	// An owned object names its group — the ownership fact routing is
	// decided by, and the one an audit otherwise infers by reading KCL.
	if !regexp.MustCompile(`Deployment\s+cp-dev\s+workspace-proxy\s+workspace-proxy`).MatchString(joined) {
		t.Errorf("owned object should name its app group:\n%s", joined)
	}
}

// TestEnvRenderFiltersCompose checks each filter narrows and that they AND
// together. --cluster in particular must yield exactly the stream that one
// cluster receives, replicated shared resources included.
func TestEnvRenderFiltersCompose(t *testing.T) {
	objects, _ := attributeFixture(t, twoClusterContract, twoClusterManifests)

	daemon := filterRenderedObjects(objects, envRenderOptions{cluster: "k3d-cp-daemon"})
	var names []string
	for _, o := range daemon {
		names = append(names, o.Name)
	}
	if got, want := strings.Join(names, ","), "cp-dev,workspace-proxy,public"; got != want {
		t.Errorf("--cluster k3d-cp-daemon: got %q, want %q", got, want)
	}

	deployments := filterRenderedObjects(objects, envRenderOptions{kinds: []string{"deployment"}})
	if len(deployments) != 3 {
		t.Errorf("--kind deployment: got %d, want 3", len(deployments))
	}

	both := filterRenderedObjects(objects, envRenderOptions{cluster: "k3d-control-plane", kinds: []string{"Deployment"}})
	if len(both) != 2 {
		t.Errorf("--cluster + --kind: got %d, want 2", len(both))
	}

	named := filterRenderedObjects(objects, envRenderOptions{name: "workspace-proxy"})
	if len(named) != 1 || named[0].Kind != "Deployment" {
		t.Errorf("--name workspace-proxy: got %v", named)
	}
	// Exact, not substring: an audit that widens silently is the bug.
	if got := filterRenderedObjects(objects, envRenderOptions{name: "workspace"}); len(got) != 0 {
		t.Errorf("--name is a prefix match: got %d object(s), want 0", len(got))
	}
}

// TestEnvRenderSummaryCountsPerCluster documents the one arithmetic surprise
// in the report: per-cluster counts can sum to more than the object count,
// because a shared resource is applied to every cluster. The summary has to
// say that rather than leave a reader to find the discrepancy.
func TestEnvRenderSummaryCountsPerCluster(t *testing.T) {
	objects, clusters := attributeFixture(t, twoClusterContract, twoClusterManifests)

	var out bytes.Buffer
	writeRenderSummary(&out, renderProvenance{
		envName:   "dev",
		mainK:     "deploy/kcl/dev/main.k",
		tagSource: "git describe",
		imageTag:  "v1",
		namespace: "cp-dev",
	}, clusters, objects, len(objects))
	got := out.String()

	for _, want := range []string{
		"dev — 5 object(s)",
		"k3d-control-plane        3 object(s)",
		"k3d-cp-daemon            3 object(s)",
		"1 object(s) land on more than one cluster",
		"image tag: v1  (source: git describe)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary omits %q:\n%s", want, got)
		}
	}
}

// TestEnvRenderSummaryReportsNoClusterEnv keeps the host-only environment
// honest in the summary too.
func TestEnvRenderSummaryReportsNoClusterEnv(t *testing.T) {
	var out bytes.Buffer
	writeRenderSummary(&out, renderProvenance{
		envName:   "dev",
		mainK:     "main.k",
		tagSource: "unresolved (the KCL default applies)",
		namespace: "cp-dev",
	}, nil, nil, 0)
	if got := out.String(); !strings.Contains(got, "none declared (host-only / compose environment)") {
		t.Errorf("summary should name the no-cluster case:\n%s", got)
	}
}

// ── write detection ─────────────────────────────────────────────────────

// TestRenderWriteScanSeesEveryKindOfChange is the honesty mechanism under
// test. forge cannot stop a project's KCL calling file.write, so the promise
// it makes instead is that it will SAY what changed — and a detector that
// missed the byte-identical rewrite (the exact shape a deterministic
// generator like control-plane's nats.conf produces) would report a render
// as clean when it was not.
func TestRenderWriteScanSeesEveryKindOfChange(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		return p
	}
	same := write("deploy/nats/nats.conf", "generated")
	write("deploy/other.conf", "small")
	doomed := write("deploy/gone.conf", "bye")
	write("untouched.txt", "still here")

	scan := newRenderWriteScan(root, false)

	// A byte-identical rewrite: same size, new mtime — invisible to a
	// content check, and exactly what the reference project does.
	if err := os.Chtimes(same, time.Now().Add(time.Second), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	write("deploy/other.conf", "considerably larger")
	if err := os.Remove(doomed); err != nil {
		t.Fatalf("remove: %v", err)
	}
	write("deploy/new.conf", "brand new")

	changes := scan.changes()
	got := map[string]string{}
	for _, c := range changes {
		got[c.path] = c.how
	}
	want := map[string]string{
		"deploy/nats/nats.conf": "rewritten",
		"deploy/other.conf":     "modified",
		"deploy/gone.conf":      "removed",
		"deploy/new.conf":       "created",
	}
	for path, how := range want {
		if got[path] != how {
			t.Errorf("%s: got %q, want %q (all: %v)", path, got[path], how, got)
		}
	}
	if len(changes) != len(want) {
		t.Errorf("changes: got %d (%v), want %d — an untouched file must not appear", len(changes), got, len(want))
	}

	var out bytes.Buffer
	scan.report(&out, "dev")
	report := out.String()
	if !strings.Contains(report, "NOT side-effect-free") {
		t.Errorf("report should refuse to claim purity:\n%s", report)
	}
	if !strings.Contains(report, "deploy/nats/nats.conf  rewritten") {
		t.Errorf("report should name the rewritten file:\n%s", report)
	}
	if !strings.Contains(report, "size and mtime are compared, not content") {
		t.Errorf("report should state its own limits:\n%s", report)
	}
}

// TestRenderWriteScanReportsTheCleanCase: "nothing changed" is the claim a
// read-only auditor came for, so it is stated explicitly. A check that only
// speaks when it fails cannot be told apart from one that never ran.
func TestRenderWriteScanReportsTheCleanCase(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	scan := newRenderWriteScan(root, false)
	if changes := scan.changes(); len(changes) != 0 {
		t.Fatalf("changes on an untouched tree: %v", changes)
	}
	var out bytes.Buffer
	scan.report(&out, "dev")
	if got := out.String(); !strings.Contains(got, "wrote nothing") {
		t.Errorf("clean report should be explicit:\n%s", got)
	}
}

// TestRenderWriteScanDisabledSaysSo — --no-write-check must not read as a
// clean bill of health.
func TestRenderWriteScanDisabledSaysSo(t *testing.T) {
	scan := newRenderWriteScan(t.TempDir(), true)
	var out bytes.Buffer
	scan.report(&out, "dev")
	got := out.String()
	if !strings.Contains(got, "write check skipped") || !strings.Contains(got, "cannot say") {
		t.Errorf("skipped check must not imply purity:\n%s", got)
	}
}

// TestRenderWriteScanSkipsForgesOwnChurn keeps the report readable. .git and
// forge's own process logs change under any command on a machine with a dev
// stack up; listing them buries the one line that matters.
func TestRenderWriteScanSkipsForgesOwnChurn(t *testing.T) {
	root := t.TempDir()
	// .forge/hostinfra is forge's own postgres/IdP runtime and data dir,
	// written by a live server (and a first-run binary extraction) while
	// `forge env up` renders. It is never a render's write.
	churn := []string{".git/objects/ab/cdef", ".forge/logs/dev/api.log", ".forge/hostinfra/postgres/data/pg_wal/000000010000000000000004", "deploy/real.conf"}
	for _, rel := range churn {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("before"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	scan := newRenderWriteScan(root, false)
	for _, rel := range churn {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("after the render"), 0o644); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
	}
	changes := scan.changes()
	if len(changes) != 1 || changes[0].path != "deploy/real.conf" {
		t.Errorf("scan should report only the real write: %v", changes)
	}
}

// TestPinFileRevertsOnlyWhatMoved: forge suppresses its OWN render-state
// write (the resolve_port store) rather than leaving it to drift — and does
// it without leaving a trace the write report would then have to explain.
func TestPinFileRevertsOnlyWhatMoved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ports-dev.json")
	if err := os.WriteFile(path, []byte(`{"api":8080}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Untouched: the revert must not even move the mtime, or the write
	// report would list forge's own no-op.
	unpin := pinFile(path)
	unpin()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("revert touched a file the render never changed")
	}

	// Drifted: the revert restores content AND mtime.
	unpin = pinFile(path)
	if err := os.WriteFile(path, []byte(`{"api":9999}`), 0o644); err != nil {
		t.Fatalf("drift: %v", err)
	}
	unpin()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != `{"api":8080}` {
		t.Errorf("content not reverted: %s", got)
	}
	if restored, _ := os.Stat(path); !restored.ModTime().Equal(before.ModTime()) {
		t.Error("revert left a new mtime; the write report would report forge's own restore")
	}
	// Idempotent — the command both defers it and may call it early.
	unpin()
	if got, _ = os.ReadFile(path); string(got) != `{"api":8080}` {
		t.Errorf("second revert changed the file: %s", got)
	}

	// A file the render CREATED is removed again.
	fresh := filepath.Join(root, "ports-new.json")
	unpin = pinFile(fresh)
	if err := os.WriteFile(fresh, []byte("{}"), 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}
	unpin()
	if _, serr := os.Stat(fresh); !os.IsNotExist(serr) {
		t.Error("a file the render created should not survive the revert")
	}
}

// TestEnvRenderRejectsAnUndeclaredCluster: a --cluster nobody deploys to
// would otherwise print an empty stream, which reads as "this cluster owns
// nothing" — the most dangerous possible wrong answer for the audit this
// command serves.
func TestEnvRenderRejectsAnUndeclaredCluster(t *testing.T) {
	_, clusters := attributeFixture(t, twoClusterContract, twoClusterManifests)
	if containsString(clusters, "k3d-typo") {
		t.Fatal("fixture unexpectedly declares k3d-typo")
	}
	if got := describeClusters(clusters); !strings.Contains(got, "k3d-cp-daemon") {
		t.Errorf("the error message must list the real clusters, got %q", got)
	}
}

// TestEnvRenderCommandSurface pins the flag set. A new flag has to be a
// deliberate addition to a command whose whole value is that it is safe and
// predictable to point at a production environment.
func TestEnvRenderCommandSurface(t *testing.T) {
	cmd := newEnvRenderCmd()
	if got, want := cmd.Name(), "render"; got != want {
		t.Errorf("name: got %q, want %q", got, want)
	}
	assertStringSlicesEqual(t, "env render visible flags", visibleFlagNames(cmd), []string{
		"cluster",
		// Per-cluster (and per-kind) object counts on stdout.
		"count",
		"fail-on-write",
		"kind",
		"list",
		"name",
		"namespace",
		// Offline escape hatch: skips `helm template` (the render's only
		// network step) and the summary names what it left out.
		"no-charts",
		"no-digest",
		"no-write-check",
		"tag",
		"target",
	})
	if err := cmd.Args(cmd, []string{"dev", "extra"}); err == nil {
		t.Error("render takes exactly one environment")
	}
}

// --count answers "how many objects does each cluster get" on stdout, where
// a script can read it, instead of `forge env shape --json | jq`.
func TestEnvRenderCountPerCluster(t *testing.T) {
	objects, clusters := attributeFixture(t, twoClusterContract, twoClusterManifests)
	var out bytes.Buffer
	if err := writeRenderedCounts(&out, clusters, objects); err != nil {
		t.Fatalf("writeRenderedCounts: %v", err)
	}
	perCluster := map[string]int{}
	for _, o := range objects {
		for _, c := range o.Clusters {
			perCluster[c]++
		}
	}
	got := out.String()
	for _, c := range clusters {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(c) + `\s+` + strconv.Itoa(perCluster[c]) + `\s`).MatchString(got) {
			t.Errorf("no row %q with %d object(s):\n%s", c, perCluster[c], got)
		}
	}
	if !regexp.MustCompile(`(?m)^TOTAL\s+` + strconv.Itoa(len(objects)) + `\s`).MatchString(got) {
		t.Errorf("no TOTAL row of %d:\n%s", len(objects), got)
	}
	if !strings.Contains(got, "Deployment=") {
		t.Errorf("no per-kind breakdown:\n%s", got)
	}
}
