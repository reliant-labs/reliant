package handlers

import (
	"context"
	"errors"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// SecretResolver gives a step activity the values of the secrets its workflow
// declared. It is the worker's side of the controller's private resolve call:
// the controller checks each secret's access list against the trusted run
// identity before it returns any value.
//
// Only the step activities that run a command or a request hold one. The values
// it returns live for one activity attempt and are never an activity input or
// result: a placeholder stays literal in workflow state and Temporal history,
// and every output is masked before the activity returns.
type SecretResolver interface {
	ResolveSecrets(ctx context.Context, runID, nodeID string, refs []stepsecrets.Ref) (map[stepsecrets.Ref]string, error)
}

// StepSecretRef is one declared secret a step resolves: its name and the
// environment it lives in, already evaluated from the workflow's declaration.
// It carries no value.
type StepSecretRef struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
}

// stepSecrets are the resolved values of one activity attempt.
type stepSecrets struct {
	values stepsecrets.Values
	masker *stepsecrets.Masker
}

// errSecretsNotConfigured is shown when a step needs secrets and this worker has
// no resolver (no control plane is configured).
var errSecretsNotConfigured = errors.New("secrets are not available: this worker has no secret resolver configured")

// resolveStepSecrets resolves the named secrets, in the environments the
// declarations name. want is the full set the step needs; an empty set resolves
// nothing and returns a masker that masks nothing.
func resolveStepSecrets(ctx context.Context, resolver SecretResolver, runID, nodeID string, want []StepSecretRef) (*stepSecrets, error) {
	if len(want) == 0 {
		return &stepSecrets{values: stepsecrets.Values{}, masker: stepsecrets.NewMasker()}, nil
	}
	if resolver == nil {
		return nil, errSecretsNotConfigured
	}
	refs := make([]stepsecrets.Ref, 0, len(want))
	byName := make(map[string]stepsecrets.Ref, len(want))
	for _, w := range want {
		env := strings.TrimSpace(w.Environment)
		if env == "" || w.Name == "" {
			return nil, errors.New("secret " + w.Name + " has no environment: the declaration's environment did not resolve to a value")
		}
		ref := stepsecrets.Ref{Environment: env, Name: w.Name}
		refs = append(refs, ref)
		byName[w.Name] = ref
	}
	got, err := resolver.ResolveSecrets(ctx, runID, nodeID, refs)
	if err != nil {
		return nil, err
	}
	values := make(stepsecrets.Values, len(byName))
	raw := make([]string, 0, len(byName))
	for name, ref := range byName {
		v, ok := got[ref]
		if !ok {
			return nil, errors.New("secret resolution response is incomplete")
		}
		values[name] = v
		raw = append(raw, v)
	}
	return &stepSecrets{values: values, masker: stepsecrets.NewMasker(raw...)}, nil
}

// secretFailure is the text a step shows when its secrets could not be
// resolved. A controller refusal is one printable line written for the
// workflow's author (it names the fix and discloses nothing foreign) and is
// shown as it is; anything else is the already-curated generic message.
func secretFailure(err error) (message string, denied bool) {
	var d *stepsecrets.DeniedError
	if errors.As(err, &d) {
		return d.Reason, true
	}
	return err.Error(), false
}

// nodeSecretRefs is the declared secrets, with their (already evaluated)
// environments, for the given names. A name the node carries no declaration for
// is a workflow that was never validated: it fails rather than run with the
// placeholder in place.
func nodeSecretRefs(node *reliantv1.Node, names []string) ([]StepSecretRef, error) {
	byName := make(map[string]*reliantv1.NodeSecretRef, len(node.GetSecretRefs()))
	for _, r := range node.GetSecretRefs() {
		byName[r.GetName()] = r
	}
	sort.Strings(names)
	out := make([]StepSecretRef, 0, len(names))
	for _, name := range names {
		r, ok := byName[name]
		if !ok {
			return nil, errors.New("secret " + name + " is not declared by this workflow")
		}
		out = append(out, StepSecretRef{Name: name, Environment: model.CelStringValue(r.GetEnvironment())})
	}
	return out, nil
}

// allNodeSecretRefs is every secret the node's workflow declared, for a step
// that exports them all to a command's environment.
func allNodeSecretRefs(node *reliantv1.Node) []StepSecretRef {
	out := make([]StepSecretRef, 0, len(node.GetSecretRefs()))
	for _, r := range node.GetSecretRefs() {
		out = append(out, StepSecretRef{Name: r.GetName(), Environment: model.CelStringValue(r.GetEnvironment())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
