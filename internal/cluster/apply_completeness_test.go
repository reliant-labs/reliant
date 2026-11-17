package cluster

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// v156Manifests is the shape of the deploy that caused the incident this
// check exists for, in miniature: three Deployments plus the Service and
// ServiceAccount rendered beside one of them. The extra kinds are the
// point, not padding — they are what makes a name-only comparison useless
// (see TestVerifyApplyComplete_MatchesOnKindAndName).
const v156Manifests = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: reliant-api-server
  namespace: control-plane-prod
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: daemon-gateway
  namespace: control-plane-prod
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: control-plane-prod
---
apiVersion: v1
kind: Service
metadata:
  name: reliant-api-server
  namespace: control-plane-prod
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: reliant-api-server
  namespace: control-plane-prod`

// TestVerifyApplyComplete_SilentPartialApplyIsAnError is the regression
// test for the v1.5.6 prod deploy: kubectl exits 0, prints no error, and
// two of the rendered Deployments simply have no line in its output. They
// stayed on a stale image for hours because nothing anywhere was red.
//
// The apply output below is verbatim in shape — a server-side-apply
// field-manager conflict makes kubectl skip THAT object and continue, so
// the only evidence is the absence of a line.
func TestVerifyApplyComplete_SilentPartialApplyIsAnError(t *testing.T) {
	applyOutput := strings.Join([]string{
		"deployment.apps/admin-server serverside-applied",
		"service/reliant-api-server serverside-applied",
		"serviceaccount/reliant-api-server unchanged",
	}, "\n")

	err := verifyApplyComplete(v156Manifests, applyOutput)
	if err == nil {
		t.Fatal("an apply that confirmed 3 of 5 rendered objects returned nil — " +
			"this is exactly the v1.5.6 silent partial deploy")
	}

	var incomplete *IncompleteApplyError
	if !errors.As(err, &incomplete) {
		t.Fatalf("want an *IncompleteApplyError so the rollout policy can grade it, got %T", err)
	}
	if incomplete.Rendered != 5 || incomplete.Confirmed != 3 {
		t.Errorf("counts = %d confirmed of %d rendered, want 3 of 5", incomplete.Confirmed, incomplete.Rendered)
	}

	// The error must NAME the objects. "2 objects missing" sends the
	// on-call engineer to diff a 105-document render by hand, which is the
	// state this replaces.
	for _, want := range []string{"deployment/reliant-api-server", "deployment/daemon-gateway"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name the missing object %q; got:\n%s", want, err)
		}
	}
	// ...and must not accuse an object that DID apply.
	if strings.Contains(err.Error(), "deployment/admin-server") {
		t.Errorf("error names admin-server, which applied cleanly:\n%s", err)
	}
}

// TestVerifyApplyComplete_MatchesOnKindAndName pins the comparison key.
//
// A Deployment shares its name with the Service, ServiceAccount and Role
// rendered beside it. If the comparison keyed on NAME alone, the
// reliant-api-server Deployment being rejected would still find
// "reliant-api-server" in the apply output (from its Service, which
// applied fine) and report nothing missing — a gate that is green during
// the exact incident it was written for.
func TestVerifyApplyComplete_MatchesOnKindAndName(t *testing.T) {
	// The Deployment is missing; the Service and ServiceAccount that share
	// its name applied.
	applyOutput := strings.Join([]string{
		"deployment.apps/daemon-gateway serverside-applied",
		"deployment.apps/admin-server serverside-applied",
		"service/reliant-api-server serverside-applied",
		"serviceaccount/reliant-api-server unchanged",
	}, "\n")

	err := verifyApplyComplete(v156Manifests, applyOutput)
	if err == nil {
		t.Fatal("a missing Deployment whose NAME appears on other kinds went undetected — " +
			"the comparison is keyed on name alone")
	}
	if !strings.Contains(err.Error(), "deployment/reliant-api-server") {
		t.Errorf("error should name the missing Deployment specifically; got:\n%s", err)
	}
}

// TestVerifyApplyComplete_CompleteApplyPasses is the other half: a deploy
// where every object landed must not be failed. A check that cries wolf on
// a healthy deploy gets disabled, and then the real one goes unnoticed.
//
// It also covers the verbs a real apply mixes — an unchanged object is
// just as applied as a created one — and the `<resource>.<group>` form
// kubectl uses for non-core kinds.
func TestVerifyApplyComplete_CompleteApplyPasses(t *testing.T) {
	applyOutput := strings.Join([]string{
		"deployment.apps/reliant-api-server serverside-applied",
		"deployment.apps/daemon-gateway configured",
		"deployment.apps/admin-server created",
		"service/reliant-api-server unchanged",
		"serviceaccount/reliant-api-server serverside-applied",
	}, "\n")

	if err := verifyApplyComplete(v156Manifests, applyOutput); err != nil {
		t.Fatalf("a complete apply must pass; got %v", err)
	}
}

// TestVerifyApplyComplete_ParsesRealKubectlLineShape pins the parse
// against output captured from a real kubectl, not from memory. The two
// hazards are both here: a group with DOTS in it
// (`gateway.networking.k8s.io`), which must be truncated at the FIRST dot
// to leave the bare resource, and a MULTI-WORD kind (`HTTPRoute`), which
// must survive lowercasing to the same token on both sides.
//
// Getting this wrong fails open — an unparsed line is an unconfirmed
// object — so every deploy carrying a Gateway API route would report a
// phantom incomplete apply, and the check would be turned off.
func TestVerifyApplyComplete_ParsesRealKubectlLineShape(t *testing.T) {
	manifests := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: reliant-api-server
---
apiVersion: v1
kind: Service
metadata:
  name: reliant-api-server
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: web`

	// Verbatim from `kubectl apply --dry-run=client -f` on the above.
	applyOutput := `deployment.apps/reliant-api-server created
service/reliant-api-server created
httproute.gateway.networking.k8s.io/web created`

	if err := verifyApplyComplete(manifests, applyOutput); err != nil {
		t.Fatalf("real kubectl output must parse as complete; got %v", err)
	}
}

// TestVerifyApplyComplete_IgnoresNonObjectLines guards against the
// opposite failure: counting something that is not an object confirmation
// would let a missing object be masked by an unrelated line, and counting
// too FEW would fail a healthy deploy.
//
// The lines below all appear in real apply output and none of them is a
// confirmation: a warning, a client-side deprecation note, and the
// conflict banner that accompanies the very rejection this check detects.
func TestVerifyApplyComplete_IgnoresNonObjectLines(t *testing.T) {
	applyOutput := strings.Join([]string{
		"Warning: resource deployments/reliant-api-server is missing the annotation",
		`error: Apply failed with 1 conflict: conflict with "kubectl-set" using apps/v1: .spec.template.spec.containers[name="api"].image`,
		"deployment.apps/daemon-gateway serverside-applied",
		"deployment.apps/admin-server serverside-applied",
		"service/reliant-api-server serverside-applied",
		"serviceaccount/reliant-api-server serverside-applied",
	}, "\n")

	err := verifyApplyComplete(v156Manifests, applyOutput)
	if err == nil {
		t.Fatal("the rejected Deployment was masked by a non-confirmation line mentioning it")
	}
	if !strings.Contains(err.Error(), "deployment/reliant-api-server") {
		t.Errorf("want the conflicted Deployment named as missing; got:\n%s", err)
	}
}

// TestVerifyApplyComplete_SkipsUnidentifiableDocs pins the conservative
// side of the render count. A doc with no kind or no name produces no line
// in the apply output either, so counting it would fail a deploy that is
// perfectly fine — the gate must not fire on a render whose only oddity is
// a trailing separator or a comment block.
func TestVerifyApplyComplete_SkipsUnidentifiableDocs(t *testing.T) {
	manifests := `# just a comment
---
apiVersion: v1
kind: Service
metadata:
  name: api
---
`
	if err := verifyApplyComplete(manifests, "service/api serverside-applied"); err != nil {
		t.Fatalf("empty and comment-only docs must not count as missing objects; got %v", err)
	}

	// An empty render has nothing to verify and must not fail on an empty
	// apply output.
	if err := verifyApplyComplete("", ""); err != nil {
		t.Fatalf("an empty manifest stream must pass; got %v", err)
	}
}

// TestIncompleteApplyErrorCapsTheList keeps the message readable. Eighty
// objects lost were lost for one reason; printing all eighty buries it.
// The shell gate this replaces capped at the same number.
func TestIncompleteApplyErrorCapsTheList(t *testing.T) {
	var docs []string
	for i := 0; i < maxMissingObjectsReported+5; i++ {
		docs = append(docs, fmt.Sprintf("apiVersion: v1\nkind: Service\nmetadata:\n  name: svc-%d", i))
	}
	err := verifyApplyComplete(strings.Join(docs, "\n---\n"), "")

	var incomplete *IncompleteApplyError
	if !errors.As(err, &incomplete) {
		t.Fatalf("want *IncompleteApplyError, got %v", err)
	}
	if len(incomplete.Missing) != maxMissingObjectsReported {
		t.Errorf("listed %d objects, want the cap of %d", len(incomplete.Missing), maxMissingObjectsReported)
	}
	if incomplete.Truncated != 5 {
		t.Errorf("Truncated = %d, want 5", incomplete.Truncated)
	}
	// The count of what was elided has to be visible, or the message
	// understates the damage.
	if !strings.Contains(err.Error(), "and 5 more") {
		t.Errorf("error should say how many were elided; got:\n%s", err)
	}
}

// TestApplyCompletenessFollowsRolloutPolicy pins the grading. An
// incomplete apply is the same class of failure as a rollout that never
// became ready, so it obeys the SAME policy rather than a flag of its own:
// fatal under the default, a warning under warn (the local inner loop,
// where a half-converged cluster is something you look at).
//
// RolloutSkip deliberately does NOT exempt it — skip is about not WAITING
// for convergence, and a rejected object never converges.
func TestApplyCompletenessFollowsRolloutPolicy(t *testing.T) {
	incomplete := verifyApplyComplete(v156Manifests, "deployment.apps/admin-server serverside-applied")
	if incomplete == nil {
		t.Fatal("fixture did not produce an incomplete apply")
	}

	// The zero value is RolloutWait — the default a real deploy uses.
	if err := (RolloutPolicy{}).Normalize().classifyApplyResult(incomplete); err == nil {
		t.Error("the DEFAULT policy must FAIL on an incomplete apply; a green deploy over " +
			"objects that never landed is the failure this whole check exists to remove")
	}

	if err := (RolloutPolicy{Mode: RolloutWarn}).classifyApplyResult(incomplete); err != nil {
		t.Errorf("warn mode must report and continue, got %v", err)
	}

	if err := (RolloutPolicy{Mode: RolloutSkip}).classifyApplyResult(incomplete); err == nil {
		t.Error("skip means 'do not wait for convergence', not 'ignore objects that were " +
			"rejected' — a skipped deploy still must not report success over a partial apply")
	}

	// A genuine apply error is fatal in EVERY mode. Warn was never a
	// licence to ignore a broken manifest or an unreachable API server.
	genuine := errors.New("exit status 1")
	for _, mode := range []RolloutMode{RolloutWait, RolloutWarn, RolloutSkip} {
		if err := (RolloutPolicy{Mode: mode}).classifyApplyResult(genuine); !errors.Is(err, genuine) {
			t.Errorf("mode %q swallowed a real apply error: %v", mode, err)
		}
	}
	if err := (RolloutPolicy{}).classifyApplyResult(nil); err != nil {
		t.Errorf("a clean apply must stay clean, got %v", err)
	}
}

// TestApplyWithImmutableRecovery_ReturnsWinningApplyStdout pins WHICH
// apply's output the completeness check gets to see.
//
// The immutable recovery re-applies the whole batch after deleting the
// offending Job. The FIRST attempt aborted partway, so its object list is
// short by construction — grading completeness against it would fail every
// warm redeploy that healed perfectly. The winning re-apply is the only
// account of what is actually on the cluster.
func TestApplyWithImmutableRecovery_ReturnsWinningApplyStdout(t *testing.T) {
	var applies int
	apply := func() (string, string, error) {
		applies++
		if applies == 1 {
			return "job.batch/control-plane-migrate unchanged", realImmutableStderr, errors.New("exit status 1")
		}
		return "job.batch/control-plane-migrate created\ndeployment.apps/api serverside-applied", "", nil
	}
	del := func(immutableTarget) error { return nil }

	stdout, err := applyWithImmutableRecovery(immutableJobManifests, apply, del, noopWaitGone)
	if err != nil {
		t.Fatalf("recovery should have healed the apply, got %v", err)
	}
	if !strings.Contains(stdout, "deployment.apps/api") {
		t.Fatalf("stdout must be the WINNING re-apply's, not the aborted first attempt's; got %q", stdout)
	}
	// And that stdout must actually satisfy the completeness check for the
	// bundle that was applied — i.e. a healed warm redeploy stays green.
	if err := verifyApplyComplete(immutableJobManifests, stdout); err != nil {
		t.Fatalf("a healed immutable recovery must pass the completeness check; got %v", err)
	}
}
