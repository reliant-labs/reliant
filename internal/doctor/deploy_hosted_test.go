package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// shapedRender is renderFromJSON plus the deploy path's classification,
// applied the way renderDeployEnvs applies a shaper's answer.
func shapedRender(t *testing.T, env, body string, shape DeployShape) envRender {
	t.Helper()
	r := renderFromJSON(t, env, body)
	r.applyShape(shape)
	return r
}

// The houndersclub Deployability failure, at the unit level: an env whose
// every workload is hosted renders an empty `output.manifests` by design, and
// the check used to call that "render produced no k8s objects". The shape
// says it is hosted and admitted.
func TestCheckDeployManifests_HostedEnvIsJudgedByItsDeployPath(t *testing.T) {
	empty := `{"output":{"manifests":[],"workloads":[]}}`
	cases := []struct {
		name     string
		shape    DeployShape
		body     string
		want     Status
		evidence string
	}{
		{
			name:  "admitted hosted env with an empty stream passes",
			shape: DeployShape{Destinations: []string{"hosted"}, Hosted: true, Workloads: 3},
			body:  empty,
			want:  StatusPass,
		},
		{
			name: "the deploy path's refusal is the finding",
			shape: DeployShape{Destinations: []string{"hosted"}, Hosted: true,
				Refusal: errors.New(`hosted env "prod": refusing to publish anything — 1 workload(s) are not admissible:\n  api: off the shape band`)},
			body:     empty,
			want:     StatusFail,
			evidence: "off the shape band",
		},
		{
			name:     "an all-hosted env rendering k8s objects fails: nothing applies them",
			shape:    DeployShape{Destinations: []string{"hosted"}, Hosted: true, Workloads: 1},
			body:     `{"output":{"manifests":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"stray"}}]}}`,
			want:     StatusFail,
			evidence: "this env applies nothing to a cluster",
		},
		{
			// Hosting is per workload: a MIXED env's cluster part is judged
			// like any other cluster env, and its hosted part by admission.
			name:  "a mixed env's cluster stream is applyable",
			shape: DeployShape{Destinations: []string{"cluster", "hosted"}, Hosted: true, Workloads: 1},
			body:  `{"output":{"manifests":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cfg"}}]}}`,
			want:  StatusPass,
		},
		{
			name:     "a mixed env with an empty cluster stream still fails",
			shape:    DeployShape{Destinations: []string{"cluster", "hosted"}, Hosted: true, Workloads: 1},
			body:     `{"output":{"manifests":[]}}`,
			want:     StatusFail,
			evidence: "no k8s objects",
		},
		{
			// An env the deploy path runs entirely on this machine applies
			// nothing to Kubernetes; its empty stream is correct.
			name:  "host-only env with an empty stream passes",
			shape: DeployShape{Destinations: []string{"host"}},
			body:  empty,
			want:  StatusPass,
		},
		{
			// A cluster env that renders nothing is still the defect.
			name:     "cluster env with an empty stream still fails",
			shape:    DeployShape{Destinations: []string{"cluster"}},
			body:     empty,
			want:     StatusFail,
			evidence: "no k8s objects",
		},
		{
			// Unclassified (no contract, or no shaper) keeps the strict
			// reading: nothing has shown it exempt.
			name:     "unclassified env with an empty stream still fails",
			shape:    DeployShape{},
			body:     `{"output":{"manifests":[]}}`,
			want:     StatusFail,
			evidence: "no k8s objects",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envWithRender([]envRender{shapedRender(t, "prod", tc.body, tc.shape)})
			got := CheckDeployManifests(context.Background(), env)
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s (%s)\n%s", got.Status, tc.want, got.Message, got.Evidence)
			}
			if tc.evidence != "" && !strings.Contains(got.Evidence, tc.evidence) {
				t.Errorf("evidence does not mention %q:\n%s", tc.evidence, got.Evidence)
			}
		})
	}
}

// A hosted env's platform objects are what RUNS, so the content checks read
// them — but they land in a namespace the platform allocates, so the checks
// about forge's own applies (object collisions) must never see them.
func TestHostedPlatformObjectsFeedContentChecksOnly(t *testing.T) {
	platform := `[{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"hosted-platform"},` +
		`"spec":{"template":{"spec":{"containers":[{"name":"api","ports":[{"containerPort":8080}],` +
		`"resources":{"requests":{"cpu":"250m","memory":"1Gi"},"limits":{"cpu":"250m","memory":"1Gi"}}}]}}}}]`
	shape := DeployShape{Destinations: []string{"hosted"}, Hosted: true, Workloads: 1, PlatformObjects: []byte(platform)}
	// Two hosted envs of one project: their platform objects share a name
	// and a placeholder namespace, which is NOT a collision — the platform
	// gives each env its own namespace.
	env := envWithRender([]envRender{
		shapedRender(t, "prod", `{"output":{"manifests":[]}}`, shape),
		shapedRender(t, "staging", `{"output":{"manifests":[]}}`, shape),
	})

	probes := CheckDeployProbes(context.Background(), env)
	if probes.Status != StatusFail || !strings.Contains(probes.Evidence, "hosted") ||
		!strings.Contains(probes.Evidence, "probes = tiers.Probes") {
		t.Errorf("Deploy Probes did not judge the hosted backend the platform runs: %s\n%s", probes.Status, probes.Evidence)
	}
	if res := CheckDeployResources(context.Background(), env); res.Status != StatusPass {
		t.Errorf("Deploy Resources = %s: %s", res.Status, res.Message)
	}
	if res := CheckObjectCollision(context.Background(), env); res.Status == StatusFail {
		t.Errorf("Object Collision reported two hosted envs' platform objects as a collision: %s\n%s", res.Message, res.Evidence)
	}
}

// An undecodable platform object list is a forge defect; the env is reported
// unreadable (UNDETERMINED for content checks), never judged on half its
// workloads.
func TestHostedUndecodablePlatformObjectsAreUnread(t *testing.T) {
	r := shapedRender(t, "prod", `{"output":{"manifests":[]}}`,
		DeployShape{Destinations: []string{"hosted"}, Hosted: true, PlatformObjects: []byte("{not json")})
	if r.err == nil {
		t.Fatal("an undecodable platform object list was accepted")
	}
	env := envWithRender([]envRender{r})
	if res := CheckDeployProbes(context.Background(), env); res.Status != StatusUnknown {
		t.Errorf("Deploy Probes = %s over an env it could not read, want unknown", res.Status)
	}
}

// The applied stream is judged EXPANDED. A Cluster workload reaches the
// render as a forge.dev Workload record, and the probes, resources and
// ServiceAccount a check reads exist only after pkg/deploy.RenderWorkloads
// expands it — which is exactly what forge applies. Judging the record
// itself would report "no workload containers" for every forge-built env.
func TestParseRender_JudgesTheExpandedStream(t *testing.T) {
	body := `{"output":{"manifests":[
	  {"apiVersion":"forge.dev/v1alpha1","kind":"Workload",
	   "metadata":{"name":"api","namespace":"acme-prod","labels":{"forge.dev/cluster":"gke_prod"}},
	   "spec":{"kind":"service","image":"ghcr.io/acme/api:v1","args":["api"],
	           "ports":[{"name":"http","port":8080}],"probes":{}}}]}}`
	r := renderFromJSON(t, "prod", body)
	var deploy *k8sObject
	for i, o := range r.objects {
		if o.Kind == "Workload" {
			t.Fatalf("a Workload record reached the judged stream unexpanded")
		}
		if o.Kind == "Deployment" && o.Metadata.Name == "api" {
			deploy = &r.objects[i]
		}
	}
	if deploy == nil {
		t.Fatalf("no expanded Deployment; kinds judged: %v", kindsOf(r.objects))
	}
	env := envWithRender([]envRender{r})
	if got := CheckDeployProbes(context.Background(), env); got.Status != StatusPass {
		t.Errorf("Deploy Probes = %s over a workload RenderWorkloads gives /readyz + /healthz: %s\n%s", got.Status, got.Message, got.Evidence)
	}
}

func kindsOf(objs []k8sObject) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.Kind
	}
	return out
}
