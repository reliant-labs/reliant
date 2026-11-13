package cluster

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The verbatim shape of the failing prod apply: a Deployment created as
// RollingUpdate, re-applied as Recreate, rejected because the API server's
// own defaulting left spec.strategy.rollingUpdate behind under a different
// field manager. kubectl's server-side-apply error names the GVR form
// (`Deployment.apps`), which parseInvalidResource normalizes.
const recreateStrategyStderr = `The Deployment "control-plane-workers" is invalid: spec.strategy.rollingUpdate: Forbidden: may not be specified when strategy ` + "`type`" + ` is 'Recreate'
`

const recreateManifests = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: control-plane-workers
  namespace: control-plane-prod
spec:
  replicas: 1
  strategy:
    type: Recreate
  template:
    spec:
      containers:
      - name: c
        image: ghcr.io/acme/cp:v1
`

func TestStrategyConflicts_FindsTheRecreateWedge(t *testing.T) {
	got := strategyConflicts(recreateStrategyStderr, recreateManifests)
	if len(got) != 1 {
		t.Fatalf("strategyConflicts() returned %d targets, want 1: %+v", len(got), got)
	}
	if got[0].Kind != "Deployment" || got[0].Name != "control-plane-workers" {
		t.Errorf("target = %s/%s, want Deployment/control-plane-workers", got[0].Kind, got[0].Name)
	}
	// The namespace must come from the manifest, so the patch targets the
	// object the apply failed on rather than `default`.
	if got[0].Namespace != "control-plane-prod" {
		t.Errorf("namespace = %q, want control-plane-prod", got[0].Namespace)
	}
	if got[0].Strategy["type"] != "Recreate" {
		t.Errorf("strategy type = %v, want Recreate", got[0].Strategy["type"])
	}
}

// An unrelated apply failure must NOT be classified as this case, or the
// recovery would patch a strategy in response to an error about something
// else and mask the real cause.
func TestStrategyConflicts_IgnoresUnrelatedFailures(t *testing.T) {
	for name, stderr := range map[string]string{
		"immutable job":  `The Job "cp-migrate" is invalid: spec.template: Invalid value: ...: field is immutable`,
		"quota":          `error: failed to create deployment: exceeded quota`,
		"other invalid":  `The Deployment "api" is invalid: spec.replicas: Invalid value: -1`,
		"empty":          ``,
		"no such object": `Error from server (NotFound): deployments.apps "api" not found`,
	} {
		if got := strategyConflicts(stderr, recreateManifests); len(got) != 0 {
			t.Errorf("%s: strategyConflicts() = %+v, want none", name, got)
		}
	}
}

// Without a declared strategy in the manifest there is nothing to patch TO.
// Guessing would be worse than surfacing the original error.
func TestStrategyConflicts_SkipsResourceWithNoDeclaredStrategy(t *testing.T) {
	noStrategy := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: control-plane-workers
  namespace: control-plane-prod
spec:
  replicas: 1
`
	if got := strategyConflicts(recreateStrategyStderr, noStrategy); len(got) != 0 {
		t.Errorf("strategyConflicts() = %+v, want none when the manifest declares no strategy", got)
	}
}

// forge applies the whole workload batch in ONE server-side apply, so one
// apply can report this for several Deployments. Healing only the first would
// leave the rest to fail the re-apply — the same bug the immutable recovery's
// batch-awareness closed.
func TestStrategyConflicts_IsBatchAware(t *testing.T) {
	stderr := `The Deployment "workers" is invalid: spec.strategy.rollingUpdate: Forbidden: may not be specified when strategy ` + "`type`" + ` is 'Recreate'
The Deployment "api" is invalid: spec.replicas: Invalid value: -1
The Deployment "store" is invalid: spec.strategy.rollingUpdate: Forbidden: may not be specified when strategy ` + "`type`" + ` is 'Recreate'
`
	manifests := `apiVersion: apps/v1
kind: Deployment
metadata: {name: workers, namespace: ns}
spec: {strategy: {type: Recreate}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: ns}
spec: {strategy: {type: Recreate}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: store, namespace: ns}
spec: {strategy: {type: Recreate}}
`
	got := strategyConflicts(stderr, manifests)
	if len(got) != 2 {
		t.Fatalf("strategyConflicts() returned %d targets, want 2 (workers, store): %+v", len(got), got)
	}
	// `api` failed for an unrelated reason and must not be swept in — the
	// marker check is scoped to each resource's own error body.
	for _, g := range got {
		if g.Name == "api" {
			t.Errorf("target %q was classified as a strategy conflict, but its failure was spec.replicas", g.Name)
		}
	}
}

// The patch must carry the TYPE as well as the null. Measured against a real
// API server: patching rollingUpdate alone is `patched (no change)` — while
// the live type is still RollingUpdate, defaulting immediately re-adds the
// field, so the removal and the type change have to be one write.
func TestStrategyMergePatch_CarriesTypeAndNullsRollingUpdate(t *testing.T) {
	patch, err := strategyMergePatch(strategyTarget{
		Kind: "Deployment", Name: "workers",
		Strategy: map[string]any{"type": "Recreate"},
	})
	if err != nil {
		t.Fatalf("strategyMergePatch() error = %v", err)
	}
	var got struct {
		Spec struct {
			Strategy map[string]json.RawMessage `json:"strategy"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(patch), &got); err != nil {
		t.Fatalf("patch is not valid JSON (%v): %s", err, patch)
	}
	if string(got.Spec.Strategy["type"]) != `"Recreate"` {
		t.Errorf("patch type = %s, want \"Recreate\"", got.Spec.Strategy["type"])
	}
	ru, present := got.Spec.Strategy["rollingUpdate"]
	if !present {
		t.Fatalf("patch omits rollingUpdate; a merge patch removes a field only via an explicit null: %s", patch)
	}
	if string(ru) != "null" {
		t.Errorf("patch rollingUpdate = %s, want null (null is the deletion in a JSON merge patch)", ru)
	}
}

// A Deployment that legitimately declares its own rollingUpdate (tuned
// maxSurge under type RollingUpdate) must keep those values — the null is
// only for removing a field nobody declared.
func TestStrategyMergePatch_PreservesADeclaredRollingUpdate(t *testing.T) {
	patch, err := strategyMergePatch(strategyTarget{
		Kind: "Deployment", Name: "api",
		Strategy: map[string]any{
			"type":          "RollingUpdate",
			"rollingUpdate": map[string]any{"maxSurge": "50%"},
		},
	})
	if err != nil {
		t.Fatalf("strategyMergePatch() error = %v", err)
	}
	if strings.Contains(patch, `"rollingUpdate":null`) {
		t.Errorf("patch nulls a DECLARED rollingUpdate: %s", patch)
	}
	if !strings.Contains(patch, "50%") {
		t.Errorf("patch dropped the declared maxSurge: %s", patch)
	}
}

// The happy path must cost nothing: an apply that succeeds never patches.
func TestApplyWithStrategyRecovery_NoPatchWhenApplySucceeds(t *testing.T) {
	patched := 0
	stdout, _, err := applyWithStrategyRecovery(recreateManifests,
		func() (string, string, error) { return "deployment.apps/workers serverside-applied\n", "", nil },
		func(strategyTarget) error { patched++; return nil },
	)
	if err != nil {
		t.Fatalf("applyWithStrategyRecovery() error = %v, want nil", err)
	}
	if patched != 0 {
		t.Errorf("patched %d times on a successful apply, want 0", patched)
	}
	if !strings.Contains(stdout, "serverside-applied") {
		t.Errorf("stdout = %q, want the apply's own output", stdout)
	}
}

// The recovery: patch the offending resource, then re-apply. The returned
// stdout must be the WINNING re-apply's — the failed attempt's object list
// under-reports what landed and would trip verifyApplyComplete on a deploy
// that in fact healed.
func TestApplyWithStrategyRecovery_PatchesThenReappliesAndReturnsWinningStdout(t *testing.T) {
	var patchedTargets []strategyTarget
	calls := 0
	stdout, _, err := applyWithStrategyRecovery(recreateManifests,
		func() (string, string, error) {
			calls++
			if calls == 1 {
				return "", recreateStrategyStderr, errors.New("exit status 1")
			}
			return "deployment.apps/control-plane-workers serverside-applied\n", "", nil
		},
		func(t strategyTarget) error { patchedTargets = append(patchedTargets, t); return nil },
	)
	if err != nil {
		t.Fatalf("applyWithStrategyRecovery() error = %v, want nil (the wedge is recoverable)", err)
	}
	if len(patchedTargets) != 1 || patchedTargets[0].Name != "control-plane-workers" {
		t.Fatalf("patched %+v, want exactly control-plane-workers", patchedTargets)
	}
	if calls != 2 {
		t.Errorf("apply called %d times, want 2 (fail, patch, re-apply)", calls)
	}
	if !strings.Contains(stdout, "serverside-applied") {
		t.Errorf("stdout = %q, want the winning re-apply's output", stdout)
	}
}

// An unrelated apply failure must pass through untouched, with no patch —
// never masked by a recovery that does not apply to it.
func TestApplyWithStrategyRecovery_SurfacesUnrelatedErrorUnchanged(t *testing.T) {
	orig := errors.New("exit status 1")
	patched := 0
	_, _, err := applyWithStrategyRecovery(recreateManifests,
		func() (string, string, error) {
			return "", `The Job "cp-migrate" is invalid: spec.template: field is immutable`, orig
		},
		func(strategyTarget) error { patched++; return nil },
	)
	if !errors.Is(err, orig) {
		t.Errorf("error = %v, want the original apply error unchanged", err)
	}
	if patched != 0 {
		t.Errorf("patched %d times on an unrelated failure, want 0", patched)
	}
}

// A patch failure is its own problem and must be surfaced, not swallowed in
// favor of the original validation error — otherwise the cause is invisible.
func TestApplyWithStrategyRecovery_SurfacesPatchFailure(t *testing.T) {
	patchErr := errors.New("forbidden: no permission to patch")
	_, _, err := applyWithStrategyRecovery(recreateManifests,
		func() (string, string, error) { return "", recreateStrategyStderr, errors.New("exit status 1") },
		func(strategyTarget) error { return patchErr },
	)
	if !errors.Is(err, patchErr) {
		t.Errorf("error = %v, want the patch failure", err)
	}
}
