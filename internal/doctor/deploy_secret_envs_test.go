package doctor

import (
	"context"
	"strings"
	"testing"
)

// CheckDeploySecrets failed a project for a plaintext credential in ANY
// environment, including dev and e2e. forge's own KCL schema does not agree
// with that: RenderedSecretKey's check permits `from = "literal"` when
// option("env") is dev or e2e, and refuses it everywhere else —
//
//	"RenderedSecretKey(from='literal') is only allowed in dev/e2e —
//	 other envs must not inline secret values"
//
// The two halves of forge therefore disagreed about the same question, and
// the doctor's half is the one that is wrong. A dev credential is
// `postgres:postgres` against a database bound to the developer's own
// machine; an e2e credential is a throwaway that the test's own assertions
// hold the other half of, and that says `do-not-use-in-prod` in the value.
// Neither is a secret in any sense that a Secret would protect: routing them
// through one adds a provisioning step to `task dev-up` and hides a value
// that has to stay greppable when a per-worktree database name is wrong.
//
// The cost of the disagreement is not pedantic. On a real project it left 16
// permanently-red findings that could never be fixed, which is how a check
// stops being read at all — and the findings that matter are the ones in
// prod, sitting in the same list.
func TestCheckDeploySecrets_LiteralsAllowedInDevAndE2E(t *testing.T) {
	leaky := `{"name":"api","env":[{"name":"DATABASE_URL","value":"postgres://postgres:postgres@localhost:5434/app"}]}`
	body := `{"output":{"manifests":[{"apiVersion":"apps/v1","kind":"Deployment",` +
		`"metadata":{"name":"api","namespace":"dev"},` +
		`"spec":{"template":{"spec":{"containers":[` + leaky + `]}}}}]}}`

	for _, envName := range []string{"dev", "e2e"} {
		t.Run(envName, func(t *testing.T) {
			env := envWithRender([]envRender{renderFromJSON(t, envName, body)})
			got := CheckDeploySecrets(context.Background(), env)
			if got.Status == StatusFail {
				t.Fatalf("forge's own schema permits an inlined literal in %q; the doctor "+
					"must not fail a project for the thing the schema allows.\nevidence: %s",
					envName, got.Evidence)
			}
		})
	}
}

// The exemption is scoped to the two throwaway environments BY NAME. Every
// deployed environment still fails on a plaintext credential — that is the
// finding this check exists for, and it is the one that was being buried.
func TestCheckDeploySecrets_LiteralsStillFailInDeployedEnvs(t *testing.T) {
	leaky := `{"name":"api","env":[{"name":"SENTRY_DSN","value":"https://k@o.ingest.sentry.io/1"}]}`

	for _, envName := range []string{"prod", "preprod", "staging", "dev-k8s", "production"} {
		t.Run(envName, func(t *testing.T) {
			body := `{"output":{"manifests":[{"apiVersion":"apps/v1","kind":"Deployment",` +
				`"metadata":{"name":"api","namespace":"` + envName + `"},` +
				`"spec":{"template":{"spec":{"containers":[` + leaky + `]}}}}]}}`
			env := envWithRender([]envRender{renderFromJSON(t, envName, body)})
			got := CheckDeploySecrets(context.Background(), env)
			if got.Status != StatusFail {
				t.Fatalf("%q is a DEPLOYED environment — a plaintext credential there is "+
					"the real finding and must still fail. status = %q (%s)",
					envName, got.Status, got.Message)
			}
			if !strings.Contains(got.Evidence, "SENTRY_DSN") {
				t.Errorf("evidence should name the leaked variable, got: %s", got.Evidence)
			}
		})
	}
}
