package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeStoreProbe struct {
	crds     map[string]CRDState
	crdErr   error
	converge map[string][2]bool // namespace/name -> {succeeded, found}
	present  map[string]map[string]bool
	presErr  error
	asked    []string
}

func (f *fakeStoreProbe) CRD(_ context.Context, _, name string) (CRDState, error) {
	if f.crdErr != nil {
		return CRDState{}, f.crdErr
	}
	return f.crds[name], nil
}

func (f *fakeStoreProbe) ConvergeSucceeded(_ context.Context, _, ns, name string) (bool, bool, error) {
	r := f.converge[ns+"/"+name]
	return r[0], r[1], nil
}

func (f *fakeStoreProbe) Presence(_ context.Context, _ string, read StoreRead) (map[string]bool, error) {
	f.asked = append(f.asked, read.Label())
	if f.presErr != nil {
		return nil, f.presErr
	}
	return f.present[read.Label()], nil
}

func servedESO(owner string) map[string]CRDState {
	st := CRDState{Present: true, Owner: owner, Versions: []string{"v1"}}
	return map[string]CRDState{esoOwnerProbeCRD: st, esoSecretStoreCRD: st}
}

func oneRead(kind string, names ...string) []StoreRead {
	return []StoreRead{{Kind: kind, Names: names}}
}

func TestCheckStore(t *testing.T) {
	const ctxName = "gke_prod"
	cases := []struct {
		name   string
		probe  *fakeStoreProbe
		expect StoreExpectation
		// want substrings, one per expected blocking finding (across all lists)
		want []string
		warn bool
	}{
		{
			name:   "operator installed, store holds the names: clean",
			probe:  &fakeStoreProbe{crds: servedESO("fx.prod"), present: map[string]map[string]bool{"openbao": {"A": true}}},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", Reads: oneRead("openbao", "A")},
		},
		{
			name:   "no operator and this deploy does not install one",
			probe:  &fakeStoreProbe{},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", Reads: oneRead("hosted", "A")},
			want:   []string{"does not serve ExternalSecret and SecretStore", "forge.ExternalSecretsOperator", "--target external-secrets"},
		},
		{
			name:   "this deploy installs the operator: absent CRDs are not a gap",
			probe:  &fakeStoreProbe{present: map[string]map[string]bool{"hosted": {"A": true}}},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", InstallsESO: true, Reads: oneRead("hosted", "A")},
		},
		{
			name:   "another env owns the operator this deploy would install",
			probe:  &fakeStoreProbe{crds: servedESO("other.dev")},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", InstallsESO: true},
			want:   []string{`owned by "other.dev"`, "Remove its `external_secrets_operators` entry"},
		},
		{
			name:   "an unlabelled operator is adopted, not refused",
			probe:  &fakeStoreProbe{crds: servedESO("")},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", InstallsESO: true},
		},
		{
			name:   "another env's operator is fine to READ from when this deploy does not install",
			probe:  &fakeStoreProbe{crds: servedESO("other.dev"), present: map[string]map[string]bool{"hosted": {"A": true}}},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", Reads: oneRead("hosted", "A")},
		},
		{
			name: "operator serves only a version the render does not emit",
			probe: &fakeStoreProbe{crds: map[string]CRDState{
				esoOwnerProbeCRD:  {Present: true, Versions: []string{"v1beta1"}},
				esoSecretStoreCRD: {Present: true, Versions: []string{"v1beta1"}},
			}},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", Reads: oneRead("hosted", "A")},
			want:   []string{"serves external-secrets.io/v1beta1", "renders external-secrets.io/v1"},
		},
		{
			name:  "openbao deployed elsewhere never converged",
			probe: &fakeStoreProbe{crds: servedESO("fx.prod")},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod",
				OpenBao: &OpenBaoExpectation{Name: "openbao", Namespace: "openbao"}},
			want: []string{"has no converge Job", "--target openbao"},
		},
		{
			name:  "openbao converge still running",
			probe: &fakeStoreProbe{crds: servedESO("fx.prod"), converge: map[string][2]bool{"openbao/openbao": {false, true}}},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod",
				OpenBao: &OpenBaoExpectation{Name: "openbao", Namespace: "openbao"}},
			want: []string{"has not finished converging", "get jobs -l forge.dev/job-name=openbao-converge"},
		},
		{
			name:  "openbao converged",
			probe: &fakeStoreProbe{crds: servedESO("fx.prod"), converge: map[string][2]bool{"openbao/openbao": {true, true}}},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod",
				OpenBao: &OpenBaoExpectation{Name: "openbao", Namespace: "openbao"}},
		},
		{
			name:  "openbao this deploy installs is not asked about its Job",
			probe: &fakeStoreProbe{crds: servedESO("fx.prod")},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod",
				OpenBao: &OpenBaoExpectation{Name: "openbao", Namespace: "openbao", Installs: true}},
		},
		{
			name:   "the store lacks a name the ExternalSecrets read",
			probe:  &fakeStoreProbe{crds: servedESO("fx.prod"), present: map[string]map[string]bool{"hosted": {"A": true}}},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", Reads: oneRead("hosted", "A", "B", "C")},
			want:   []string{"hosted store holds no value for B, C", "forge secret set", "source.generate"},
		},
		{
			name:   "a store that cannot be asked is a warning, not a block",
			probe:  &fakeStoreProbe{crds: servedESO("fx.prod"), presErr: errors.New("no credential")},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod", Reads: oneRead("gcpsm", "A")},
			warn:   true,
		},
		{
			name:  "a store this deploy installs holds nothing yet: not asked",
			probe: &fakeStoreProbe{crds: servedESO("fx.prod")},
			expect: StoreExpectation{Context: ctxName, Owner: "fx.prod",
				Reads: []StoreRead{{Kind: "openbao", Names: []string{"A"}, Pending: true}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := CheckStore(context.Background(), tc.probe, []StoreExpectation{tc.expect})
			if err != nil {
				t.Fatal(err)
			}
			report := FormatStoreFindings(f)
			if len(tc.want) == 0 {
				if !f.Empty() {
					t.Fatalf("want no blocking finding, got:\n%s", report)
				}
			} else {
				if f.Empty() {
					t.Fatalf("want a blocking finding containing %q, got none", tc.want)
				}
				for _, w := range tc.want {
					if !strings.Contains(report, w) {
						t.Errorf("report lacks %q:\n%s", w, report)
					}
				}
			}
			if tc.warn && (len(f.Warnings) != 1 || !f.Empty()) {
				t.Errorf("want one non-blocking warning, got blocking=%v warnings=%v", !f.Empty(), f.Warnings)
			}
		})
	}
}

// A cluster read that FAILS is an error, never "nothing wrong".
func TestCheckStoreReadFailureAborts(t *testing.T) {
	_, err := CheckStore(context.Background(), &fakeStoreProbe{crdErr: errors.New("connection refused")},
		[]StoreExpectation{{Context: "c", Reads: oneRead("hosted", "A")}})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("a failed read must abort the preflight, got %v", err)
	}
}

// A cluster with nothing store-backed and no store is not looked at at all.
func TestCheckStoreIgnoresClustersWithNothingToDeliver(t *testing.T) {
	f, err := CheckStore(context.Background(), &fakeStoreProbe{crdErr: errors.New("must not be read")},
		[]StoreExpectation{{Context: "c"}, {Context: ""}})
	if err != nil || !f.Empty() {
		t.Fatalf("got %v, %v", f, err)
	}
}

// The values the store is asked about are names only; no probe method can
// return a value, and the finding never contains one.
func TestStoreFindingsNameNoValue(t *testing.T) {
	probe := &fakeStoreProbe{crds: servedESO("fx.prod"), present: map[string]map[string]bool{"hosted": {}}}
	f, _ := CheckStore(context.Background(), probe, []StoreExpectation{{Context: "c", Owner: "fx.prod", Reads: oneRead("hosted", "STRIPE_KEY")}})
	if !strings.Contains(FormatStoreFindings(f), "STRIPE_KEY") {
		t.Fatal("the missing NAME must be reported")
	}
}

func TestConvergeOutcomeJudgesTheNewestJob(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	newer := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	job := func(ts string, ok int) string {
		return `{"metadata":{"creationTimestamp":"` + ts + `"},"status":{"succeeded":` + string(rune('0'+ok)) + `}}`
	}
	cases := []struct {
		name, body        string
		wantDone, wantHit bool
	}{
		{"none", `{"items":[]}`, false, false},
		{"one succeeded", `{"items":[` + job(older, 1) + `]}`, true, true},
		{"newer failed is not rescued by an older success", `{"items":[` + job(older, 1) + `,` + job(newer, 0) + `]}`, false, true},
		{"newer succeeded", `{"items":[` + job(older, 0) + `,` + job(newer, 1) + `]}`, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done, found, err := convergeOutcome([]byte(tc.body))
			if err != nil || done != tc.wantDone || found != tc.wantHit {
				t.Fatalf("got done=%v found=%v err=%v", done, found, err)
			}
		})
	}
}

func TestExternalSecretsInAndJobsWithPrefix(t *testing.T) {
	m := `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: stripe, namespace: pay}
---
apiVersion: batch/v1
kind: Job
metadata: {name: openbao-converge-ab12, namespace: openbao}
---
apiVersion: batch/v1
kind: Job
metadata: {name: other, namespace: openbao}
`
	if _, ok := ExternalSecretsIn(m)["pay/stripe"]; !ok {
		t.Error("ExternalSecretsIn missed pay/stripe")
	}
	if got := JobsWithPrefix(m, "openbao-converge-"); len(got) != 1 || got[0] != "openbao/openbao-converge-ab12" {
		t.Errorf("JobsWithPrefix = %v", got)
	}
}

// The store gate takes the external-secrets.io group over from the generic
// served-kinds gate: THIS deploy may install the operator that serves it.
func TestServedKindsGateLeavesTheESOGroupToTheStoreGate(t *testing.T) {
	manifests := `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: stripe, namespace: pay}
spec: {}
`
	served := fakeServedKinds{served: servedSet()}
	base := PreflightOpts{Manifests: manifests, Context: "c", ServedKinds: served}

	res, err := runPreflightChecks(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MissingCRDs) != 1 {
		t.Fatalf("without the store gate the generic gate must still report the kind, got %v", res.MissingCRDs)
	}

	withStore := base
	withStore.StoreProbe = &fakeStoreProbe{present: map[string]map[string]bool{"hosted": {"A": true}}}
	withStore.StoreExpect = []StoreExpectation{{Context: "c", InstallsESO: true, Reads: oneRead("hosted", "A")}}
	res, err = runPreflightChecks(context.Background(), withStore)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MissingCRDs) != 0 || !res.OK() {
		t.Fatalf("a deploy that installs the operator must not be blocked on its CRDs, got %v / %s", res.MissingCRDs, FormatPreflightReport(res))
	}
}

// A store finding blocks the deploy and is in the report, with its escape.
func TestPreflightBlocksOnAStoreFinding(t *testing.T) {
	opts := PreflightOpts{
		StoreProbe:  &fakeStoreProbe{},
		StoreExpect: []StoreExpectation{{Context: "c", Reads: oneRead("hosted", "A")}},
	}
	res, err := PreflightReport(context.Background(), opts)
	if err == nil || res.OK() {
		t.Fatalf("want a blocked preflight, got %v / %+v", err, res.Store)
	}
	for _, w := range []string{"Store-backed Secrets that cannot be delivered", "CRDs not served", "--target external-secrets"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("report lacks %q:\n%v", w, err)
		}
	}
}

// The live probe against the stateful fake kubectl: the same `get` calls a real
// deploy makes, not a hand-written answer.
func TestKubectlStoreProbeReadsTheClusterItIsAskedAbout(t *testing.T) {
	c := newFakeCluster(t)
	probe := KubectlStoreProbe{}
	ctx := context.Background()

	if st, err := probe.CRD(ctx, "ctx", esoOwnerProbeCRD); err != nil || st.Present {
		t.Fatalf("an absent CRD is not present: %+v, %v", st, err)
	}

	crd := esoCRD("other.dev")
	crd["spec"] = map[string]any{"versions": []any{
		map[string]any{"name": "v1beta1", "served": false},
		map[string]any{"name": "v1", "served": true},
	}}
	c.seed(crd)
	st, err := probe.CRD(ctx, "ctx", esoOwnerProbeCRD)
	if err != nil || !st.Present || st.Owner != "other.dev" || len(st.Versions) != 1 || st.Versions[0] != "v1" {
		t.Fatalf("got %+v, %v: want present, owned by other.dev, serving only v1", st, err)
	}

	job := func(name, ts string, ok int) map[string]any {
		return map[string]any{
			"apiVersion": "batch/v1", "kind": "Job",
			"metadata": map[string]any{"name": name, "namespace": "openbao", "creationTimestamp": ts,
				"labels": map[string]any{"forge.dev/job-name": "openbao-converge"}},
			"status": map[string]any{"succeeded": ok},
		}
	}
	if _, found, err := probe.ConvergeSucceeded(ctx, "ctx", "openbao", "openbao"); err != nil || found {
		t.Fatalf("no Job yet: found=%v err=%v", found, err)
	}
	c.seed(job("openbao-converge-aaaa", "2026-01-01T00:00:00Z", 1))
	c.seed(job("openbao-converge-bbbb", "2026-02-01T00:00:00Z", 0))
	done, found, err := probe.ConvergeSucceeded(ctx, "ctx", "openbao", "openbao")
	if err != nil || !found || done {
		t.Fatalf("the newer Job has not succeeded: done=%v found=%v err=%v", done, found, err)
	}
}
