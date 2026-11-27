package doctor

import (
	"context"
	"strings"
	"testing"
)

// The deploy checks descend into a workload's pod template to read probes,
// resources, env and serviceAccountName. Job and CronJob were missing from the
// set of kinds they descend into, and that omission cut BOTH ways:
//
//   - FALSE POSITIVE. CheckDeployServiceAccount collects every rendered
//     ServiceAccount and marks one bound when some pod spec names it. A Job's
//     pod spec was never read, so a migrate Job that correctly sets
//     serviceAccountName was reported as "rendered but no pod spec sets
//     serviceAccountName — the workload runs as the namespace `default` SA".
//     That is the opposite of the truth, and it is the worst kind of finding:
//     it sends someone to fix a ServiceAccount binding that is already right.
//
//   - FALSE NEGATIVE, and the more dangerous half. CheckDeploySecrets
//     documents that it scans init containers because "the migration step runs
//     with the same env as the app, so a leak there is the same leak" — but a
//     migration that runs as a JOB rather than an initContainer was invisible.
//     A plaintext DATABASE_URL on a migrate Job is exactly the credential this
//     check exists to catch, and it passed silently.
//
// A Job's pod template lives at spec.template.spec, the same place a
// Deployment's does. A CronJob nests one level deeper, under
// spec.jobTemplate.spec.template.spec.

const jobSA = `{"apiVersion":"v1","kind":"ServiceAccount","metadata":{"name":"migrate","namespace":"proj-prod"}}`

// jobWith renders a Job whose pod spec carries the supplied fields.
func jobWith(podExtra, container string) string {
	return `{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"migrate","namespace":"proj-prod"},` +
		`"spec":{"template":{"spec":{` + podExtra + `"containers":[` + container + `]}}}}`
}

// cronJobWith renders a CronJob, whose pod template sits under jobTemplate.
func cronJobWith(podExtra, container string) string {
	return `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":"migrate","namespace":"proj-prod"},` +
		`"spec":{"schedule":"0 * * * *","jobTemplate":{"spec":{"template":{"spec":{` +
		podExtra + `"containers":[` + container + `]}}}}}}`
}

func TestCheckDeployServiceAccount_ReadsJobPodSpecs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Status
	}{
		{
			name: "job that binds the SA passes",
			body: `{"output":{"manifests":[` + jobSA + `,` + jobWith(`"serviceAccountName":"migrate",`, `{"name":"migrate"}`) + `]}}`,
			want: StatusPass,
		},
		{
			name: "cronjob that binds the SA passes",
			body: `{"output":{"manifests":[` + jobSA + `,` + cronJobWith(`"serviceAccountName":"migrate",`, `{"name":"migrate"}`) + `]}}`,
			want: StatusPass,
		},
		{
			name: "job that does not bind the SA still fails",
			body: `{"output":{"manifests":[` + jobSA + `,` + jobWith("", `{"name":"migrate"}`) + `]}}`,
			want: StatusFail,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := envWithRender([]envRender{renderFromJSON(t, "prod", tt.body)})
			got := CheckDeployServiceAccount(context.Background(), env)
			if got.Status != tt.want {
				t.Fatalf("status = %q, want %q\nmessage: %s\nevidence: %s",
					got.Status, tt.want, got.Message, got.Evidence)
			}
		})
	}
}

// The false-negative half: a credential on a Job must be caught, because a
// migration Job carries the same database URL the app does.
func TestCheckDeploySecrets_ReadsJobPodSpecs(t *testing.T) {
	leaky := `{"name":"migrate","env":[{"name":"DATABASE_URL","value":"postgres://u:hunter2@db/app"}]}`

	for _, tt := range []struct {
		name string
		body string
	}{
		{"job", `{"output":{"manifests":[` + jobWith("", leaky) + `]}}`},
		{"cronjob", `{"output":{"manifests":[` + cronJobWith("", leaky) + `]}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := envWithRender([]envRender{renderFromJSON(t, "prod", tt.body)})
			got := CheckDeploySecrets(context.Background(), env)
			if got.Status != StatusFail {
				t.Fatalf("a plaintext DATABASE_URL on a %s must be reported — a migration "+
					"step carries the same credential the app does. status = %q (%s)",
					tt.name, got.Status, got.Message)
			}
			if !strings.Contains(got.Evidence, "DATABASE_URL") {
				t.Errorf("evidence should name the leaked variable, got: %s", got.Evidence)
			}
		})
	}
}

// IMAGE_PULL_SECRET_NAME holds the NAME of a Kubernetes Secret, and
// IMAGE_PULL_SECRET_PATH the PATH to one. Neither is a credential — the name
// of a secret is not secret, and it is exactly what a manifest is supposed to
// carry in the clear so a pod can reference it.
//
// The "SECRET" fragment matched both, and on a real project they were 68 of
// 89 findings — enough noise to bury the ~21 genuine plaintext credentials in
// the same report. A check whose output is mostly false is one people learn
// to skip, which costs more than the check gains.
func TestCheckDeploySecrets_NameAndPathAreNotCredentials(t *testing.T) {
	refs := `{"name":"api","env":[` +
		`{"name":"IMAGE_PULL_SECRET_NAME","value":"ghcr-pull"},` +
		`{"name":"IMAGE_PULL_SECRET_PATH","value":"/var/run/secrets/pull.json"}]}`
	body := `{"output":{"manifests":[{"apiVersion":"apps/v1","kind":"Deployment",` +
		`"metadata":{"name":"api","namespace":"proj-prod"},` +
		`"spec":{"template":{"spec":{"containers":[` + refs + `]}}}}]}}`

	env := envWithRender([]envRender{renderFromJSON(t, "prod", body)})
	got := CheckDeploySecrets(context.Background(), env)
	if got.Status == StatusFail {
		t.Fatalf("the NAME of / PATH to a pull secret is not a credential and must not "+
			"be reported as plaintext:\n%s", got.Evidence)
	}
}

// The narrowing must not blunt the check: a variable that really does hold a
// secret VALUE still fails, including one whose name also contains "NAME".
func TestCheckDeploySecrets_StillCatchesRealCredentials(t *testing.T) {
	for _, envVar := range []string{
		`{"name":"DATABASE_URL","value":"postgres://u:pw@db/app"}`,
		`{"name":"SUPABASE_JWT_SECRET","value":"s3cret"}`,
		`{"name":"NATS_PASSWORD","value":"pw"}`,
		// "…SECRET_NAME" as a suffix is the exemption; a secret whose name
		// merely mentions a name is not.
		`{"name":"SECRET_NAME_ENCRYPTION_KEY","value":"k"}`,
	} {
		body := `{"output":{"manifests":[{"apiVersion":"apps/v1","kind":"Deployment",` +
			`"metadata":{"name":"api","namespace":"proj-prod"},` +
			`"spec":{"template":{"spec":{"containers":[{"name":"api","env":[` + envVar + `]}]}}}}]}}`
		env := envWithRender([]envRender{renderFromJSON(t, "prod", body)})
		got := CheckDeploySecrets(context.Background(), env)
		if got.Status != StatusFail {
			t.Errorf("%s is a plaintext credential and must still be reported (status %q)",
				envVar, got.Status)
		}
	}
}
