package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

var updateCodeSourceGolden = flag.Bool("update-code-source-golden", false, "rewrite internal/cli/testdata/code_source_warning/*.golden from the current output")

// twoReposWarning is the control plane's D5 warning, as control-plane#2933
// words it.
const twoReposWarning = `"shop" is deployed from 2 repositories (github.com/acme/shop, github.com/other/shop). ` +
	"An app name is one app in its org, so each of them deploys to the same environments; " +
	"if that is not intended, give one of them a different `name` in its forge.yaml."

// codeSourcesReply is a cut or open response body's code_sources: with the
// warning, or (warning "") the sources of an app with one repository.
func codeSourcesReply(warning string) map[string]any {
	cs := map[string]any{
		"project": "shop",
		"sources": []any{map[string]any{"source": map[string]any{"remote": "github.com/acme/shop"}}},
	}
	if warning != "" {
		cs["warning"] = warning
	}
	return map[string]any{"codeSources": cs}
}

// codeSourceCaller answers CutRelease and OpenDeployAttempt with replies[proc]
// (nil: an older server's answer, which carries no code_sources) and every
// other procedure with an empty body.
type codeSourceCaller struct {
	mu      sync.Mutex
	replies map[string]map[string]any
}

func (c *codeSourceCaller) Call(_ context.Context, proc string, _, out any) error {
	c.mu.Lock()
	reply := c.replies[proc]
	c.mu.Unlock()
	if reply == nil {
		reply = map[string]any{}
	}
	if proc == procCutRelease {
		reply = withCreated(reply)
	}
	data, _ := json.Marshal(reply)
	return json.Unmarshal(data, out)
}

func withCreated(reply map[string]any) map[string]any {
	out := map[string]any{"created": true}
	for k, v := range reply {
		out[k] = v
	}
	return out
}

func codeSourceReplies(warning string, procs ...string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, p := range procs {
		if warning == "older server" {
			continue
		}
		out[p] = codeSourcesReply(warning)
	}
	return out
}

func recordOnlyRelease(v string) release.Release {
	return release.Release{Version: v, Project: "shop", RecordOnly: true, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// codeSourceGoldenDir is absolute, read before any test changes directory.
var codeSourceGoldenDir = func() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "testdata", "code_source_warning")
}()

// assertCodeSourceGolden compares got with testdata/code_source_warning/<name>.golden.
func assertCodeSourceGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(codeSourceGoldenDir, name+".golden")
	if *updateCodeSourceGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update-code-source-golden to create it): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s; review it and run `go test ./internal/cli -run TestCodeSourceWarning -update-code-source-golden`\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// codeSourceWarningCases: the warning present; an app with one repository
// (sources, no warning); and a control plane that predates code sources.
var codeSourceWarningCases = []struct{ golden, warning string }{
	{"present", twoReposWarning},
	{"absent", ""},
	{"older_server", "older server"},
}

// A cut outside a deploy prints the warning right after it, once per text,
// however many releases the store cuts.
func TestCodeSourceWarningAfterCut(t *testing.T) {
	t.Parallel()
	for _, tc := range codeSourceWarningCases {
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			store := &hostedStore{
				client:  &codeSourceCaller{replies: codeSourceReplies(tc.warning, procCutRelease)},
				project: "shop", warn: &stderr,
			}
			for _, v := range []string{"v1", "v2"} {
				if _, err := store.Cut(context.Background(), recordOnlyRelease(v)); err != nil {
					t.Fatalf("cut %s: %v", v, err)
				}
			}
			assertCodeSourceGolden(t, "cut_"+tc.golden, stderr.String())
		})
	}
}

// A deploy prints the warning once, after its own output: the attempt's open
// and a cut inside the deploy both return it, and neither prints it then.
func TestCodeSourceWarningAfterDeploy(t *testing.T) {
	t.Parallel()
	for _, tc := range codeSourceWarningCases {
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			c := &codeSourceCaller{replies: codeSourceReplies(tc.warning, procCutRelease, procOpenDeployAttempt)}
			ctx, a := newTestAttempt("prod", "apply", &stderr)
			store := &hostedStore{client: c, project: "shop", warn: &stderr}
			attachDeployAttempt(ctx, "prod", envLedger{Bindings: store, Hosted: true})
			if _, err := store.Cut(ctx, recordOnlyRelease("v1")); err != nil {
				t.Fatalf("cut: %v", err)
			}
			if got := stderr.String(); got != "" {
				t.Fatalf("printed before the deploy finished: %q", got)
			}
			a.finish(ctx, nil)
			assertCodeSourceGolden(t, "deploy_"+tc.golden, stderr.String())
		})
	}
}

// `forge env status` of a hosted env prints the warning in its text report
// and carries it as code_sources.warning in --json; without one, neither.
func TestCodeSourceWarningInEnvStatus(t *testing.T) {
	pin := sha("1")
	_, store := hostedPromoteFixture(t, "v1")
	read := func(context.Context, string, string) (wireRollout, error) {
		return wireRollout{Workloads: []wireWorkloadRollout{{
			Name: "api", Artifact: "api", PinnedDigest: pin, ObservedDigest: pin,
			ObservedState: "DEPLOY_OBSERVED_STATE_READY", Phase: wireRolloutPhaseSucceeded,
		}}}, nil
	}
	for _, tc := range codeSourceWarningCases[:2] {
		t.Run(tc.golden, func(t *testing.T) {
			t.Chdir(t.TempDir())
			opts := envStatusOptions{
				Bindings: store, HostedRollout: read, Resolver: stubResolver{},
				HostedCodeSourceWarning: func(context.Context, string) string { return tc.warning },
			}
			var err error
			text := captureStdout(t, func() { err = runEnvStatusRelease(context.Background(), "prod", opts) })
			if err != nil {
				t.Fatalf("the warning must not change the verdict: %v", err)
			}
			assertCodeSourceGolden(t, "status_text_"+tc.golden, statusVerdictBlock(t, text))

			opts.JSON = true
			raw := captureStdout(t, func() { err = runEnvStatusRelease(context.Background(), "prod", opts) })
			if err != nil {
				t.Fatalf("--json: the warning must not change the verdict: %v", err)
			}
			var doc map[string]json.RawMessage
			if jerr := json.Unmarshal([]byte(raw), &doc); jerr != nil {
				t.Fatalf("--json is not one document: %v\n%s", jerr, raw)
			}
			field := map[string]json.RawMessage{}
			if cs, ok := doc["code_sources"]; ok {
				field["code_sources"] = cs
			}
			out, _ := json.MarshalIndent(field, "", "  ")
			assertCodeSourceGolden(t, "status_json_"+tc.golden, string(out)+"\n")
		})
	}
}

// statusVerdictBlock is the text report from its tally line to the first
// blank line after it: the verdict block the warning joins. Everything above
// it (timestamps, ids) is another test's to pin.
func statusVerdictBlock(t *testing.T, text string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.Contains(l, " match, ") && strings.Contains(l, " unreachable") {
			var out []string
			for _, rest := range lines[i:] {
				if strings.TrimSpace(rest) == "" {
					break
				}
				out = append(out, rest)
			}
			return strings.Join(out, "\n") + "\n"
		}
	}
	t.Fatalf("no tally line in the report:\n%s", text)
	return ""
}

// A snapshot binding is observed per consumer slot, not through the hosted
// observer — and the two-repositories warning is about the app, not that
// read: `forge env status` of a hosted env bound to a snapshot carries it in
// its text report and as code_sources.warning in --json, as the observer
// path does; without one, neither.
func TestCodeSourceWarningInSnapshotEnvStatus(t *testing.T) {
	digest := sha("1")
	snap := release.Snapshot{
		Artifacts: map[release.ArtifactID]release.SnapshotArtifact{"api": {Location: "r.example/api", Content: digest}},
		Consumers: []release.Consumer{{Key: release.ConsumerKey{Cluster: "prod", Workload: "api"}, Slots: []string{"container:api"}}},
		Bindings:  []release.ConsumerBinding{{Consumer: release.ConsumerKey{Cluster: "prod", Workload: "api"}, Slot: "container:api", Artifact: "api"}},
		Resources: []release.SnapshotResource{{Key: release.ResourceKey{Cluster: "prod", Kind: "Deployment", Namespace: "shop", Name: "api"}}},
	}
	lister := &stubLister{images: []cluster.WorkloadImage{{Name: "api", Container: "api", Image: "r.example/api@" + digest}}}
	for _, tc := range codeSourceWarningCases[:2] {
		t.Run(tc.golden, func(t *testing.T) {
			t.Chdir(t.TempDir())
			opts := envStatusOptions{
				Bindings: newMemBindingStore(map[string]release.Promotion{"prod": {ID: "prom-1", Release: "v1", Snapshot: &snap}}),
				Lister:   lister, Resolver: stubResolver{}, Timeout: time.Minute,
				HostedCodeSourceWarning: func(context.Context, string) string { return tc.warning },
			}
			var err error
			text := captureStdout(t, func() { err = runEnvStatusRelease(context.Background(), "prod", opts) })
			if err != nil {
				t.Fatalf("the warning must not change the verdict: %v\n%s", err, text)
			}
			assertCodeSourceGolden(t, "status_snapshot_text_"+tc.golden, statusVerdictBlock(t, text))

			opts.JSON = true
			raw := captureStdout(t, func() { err = runEnvStatusRelease(context.Background(), "prod", opts) })
			if err != nil {
				t.Fatalf("--json: the warning must not change the verdict: %v", err)
			}
			var doc map[string]json.RawMessage
			if jerr := json.Unmarshal([]byte(raw), &doc); jerr != nil {
				t.Fatalf("--json is not one document: %v\n%s", jerr, raw)
			}
			if string(doc["source"]) != `"snapshot consumer-slot observation"` {
				t.Fatalf("not the snapshot path: source = %s", doc["source"])
			}
			field := map[string]json.RawMessage{}
			if cs, ok := doc["code_sources"]; ok {
				field["code_sources"] = cs
			}
			out, _ := json.MarshalIndent(field, "", "  ")
			assertCodeSourceGolden(t, "status_snapshot_json_"+tc.golden, string(out)+"\n")
		})
	}
}
