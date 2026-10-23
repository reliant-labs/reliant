package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploystate"
)

// stubPolicyStore is the whole reason reconcilePolicyStore is one method
// wide: a double for it is four lines, not a mock of five methods this
// package never calls.
type stubPolicyStore struct {
	policy deploystate.Policy
	err    error
}

func (s stubPolicyStore) Policy(context.Context, string) (deploystate.Policy, error) {
	return s.policy, s.err
}

func TestGateDeployOnPolicy_ObserveAllowsDeploy(t *testing.T) {
	err := gateDeployOnPolicy(context.Background(),
		stubPolicyStore{policy: deploystate.PolicyObserve}, "prod", "/tmp/policy.json")
	if err != nil {
		t.Fatalf("observe blocked a deploy: %v", err)
	}
}

func TestGateDeployOnPolicy_ConvergeAllowsDeploy(t *testing.T) {
	err := gateDeployOnPolicy(context.Background(),
		stubPolicyStore{policy: deploystate.PolicyConverge}, "prod", "/tmp/policy.json")
	if err != nil {
		t.Fatalf("converge blocked a deploy: %v", err)
	}
}

// TestGateDeployOnPolicy_PinnedBlocksDeploy is the gate's reason to
// exist. It also pins the error CONTENT: a refusal that does not say
// where the switch is sends the reader to grep during an incident.
func TestGateDeployOnPolicy_PinnedBlocksDeploy(t *testing.T) {
	path := "/proj/.forge/state/policy-prod.json"
	err := gateDeployOnPolicy(context.Background(),
		stubPolicyStore{policy: deploystate.PolicyPinned}, "prod", path)
	if err == nil {
		t.Fatal("a pinned environment accepted a deploy")
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Errorf("refusal does not name the file to edit:\n%s", msg)
	}
	if !strings.Contains(msg, "prod") {
		t.Errorf("refusal does not name the environment:\n%s", msg)
	}
}

// TestGateDeployOnPolicy_UnreadableProceedsWithWarning pins the
// deliberate asymmetry with deploystate.DecideEnv, which ABORTS on the
// same failure. A human typed this command and is standing here; a
// corrupt state file is not grounds for refusing them their own deploy.
func TestGateDeployOnPolicy_UnreadableProceedsWithWarning(t *testing.T) {
	err := gateDeployOnPolicy(context.Background(),
		stubPolicyStore{err: errors.New("disk on fire")}, "prod", "/tmp/policy.json")
	if err != nil {
		t.Fatalf("an unreadable policy blocked a human-initiated deploy: %v", err)
	}
}

func TestGateDeployOnPolicy_NilStoreIsNoop(t *testing.T) {
	if err := gateDeployOnPolicy(context.Background(), nil, "prod", ""); err != nil {
		t.Fatalf("nil store: %v", err)
	}
}

// TestGateDeployOnPolicy_AgainstRealLocalStore drives the gate through
// the actual file-backed store rather than a double, because the double
// cannot catch a wrong path or a filename that disagrees with what
// PolicyPath advertises.
func TestGateDeployOnPolicy_AgainstRealLocalStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := deploystate.NewLocal(dir)

	// A project with no policy file at all must deploy.
	if err := gateDeployOnPolicy(ctx, store, "prod", store.PolicyPath("prod")); err != nil {
		t.Fatalf("a project with no policy file was blocked: %v", err)
	}

	// The path the error advertises must be the path that works. Written
	// by hand — that is what an engineer does mid-incident.
	path := store.PolicyPath("prod")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"policy":"pinned"}`), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	err := gateDeployOnPolicy(ctx, store, "prod", path)
	if err == nil {
		t.Fatal("hand-writing the advertised policy file did not pin the environment; " +
			"the path in the error message does not match the path the store reads")
	}

	// And lifting it takes effect immediately, on the same store.
	if err := os.WriteFile(path, []byte(`{"policy":"observe"}`), 0o644); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if err := gateDeployOnPolicy(ctx, store, "prod", path); err != nil {
		t.Fatalf("unpinning did not take effect on the next call: %v", err)
	}
}

// TestGateDeployOnPolicy_OneEnvironmentDoesNotPinAnother: pinning prod
// must not stop a staging deploy.
func TestGateDeployOnPolicy_OneEnvironmentDoesNotPinAnother(t *testing.T) {
	ctx := context.Background()
	store := deploystate.NewLocal(t.TempDir())

	if err := store.SetPolicy(ctx, "prod", deploystate.PolicyPinned); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	if err := gateDeployOnPolicy(ctx, store, "prod", store.PolicyPath("prod")); err == nil {
		t.Fatal("prod was not pinned")
	}
	if err := gateDeployOnPolicy(ctx, store, "staging", store.PolicyPath("staging")); err != nil {
		t.Fatalf("pinning prod blocked staging: %v", err)
	}
}
