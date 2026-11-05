package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// SecretResolver gives a step activity the values of the secrets its workflow
// declared. It is the worker's side of the controller's private resolve call:
// the controller checks each secret's access list against the trusted run
// identity before it returns any value.
//
// Only the action activity holds one, and it puts a value into exactly one
// place: a header of the request it sends. The values live for one activity
// attempt and are never an activity input or result, never a param, and never
// part of any text the step returns: a placeholder stays a marker in workflow
// state and Temporal history.
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

// errSecretsNotConfigured is shown when a step needs secrets and this worker has
// no resolver (no control plane is configured).
var errSecretsNotConfigured = errors.New("secrets are not available: this worker has no secret resolver configured")

// resolveStepSecrets resolves the named secrets, in the environments the
// declarations name. want is the full set the step needs.
func resolveStepSecrets(ctx context.Context, resolver SecretResolver, runID, nodeID string, want []StepSecretRef) (stepsecrets.Values, error) {
	if len(want) == 0 {
		return stepsecrets.Values{}, nil
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
	for name, ref := range byName {
		v, ok := got[ref]
		if !ok {
			return nil, errors.New("secret resolution response is incomplete")
		}
		values[name] = v
	}
	return values, nil
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

// secretHeader is one credential slot: a request header whose value is a
// literal prefix (possibly empty) and one declared secret.
type secretHeader struct {
	name   string // canonical header name
	prefix string
	secret string
}

// httpIntegration and httpRequestAction name the one action that takes secret
// header slots: the generic HTTP request, whose author picks the host and so
// knows which header the API reads a key from.
const (
	httpIntegration   = "http"
	httpRequestAction = "request"
)

// refusedSecretHeaders are headers a secret may never fill: the transport owns
// them, or they describe the body rather than authenticate the call.
var refusedSecretHeaders = map[string]bool{
	"Host": true, "Content-Length": true, "Content-Type": true, "Content-Encoding": true,
	"Transfer-Encoding": true, "Connection": true, "Upgrade": true, "Te": true,
	"Trailer": true, "Keep-Alive": true,
}

// SecretHeaderAllowed reports whether a header may be a credential slot.
func SecretHeaderAllowed(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	return httpguts.ValidHeaderFieldName(name) && !refusedSecretHeaders[canonical] &&
		!strings.HasPrefix(canonical, "Proxy-")
}

// takeSecretHeaders removes the credential slots from an action's params and
// returns them. A marker anywhere else (the url, the query, the body, a header
// that is not a whole slot, any other action's input) fails the step: params
// that leave here hold no marker, so nothing downstream can be handed a value.
func takeSecretHeaders(uses, integrationID, actionID string, params map[string]any) ([]secretHeader, *reliantv1.ActionOutput) {
	fail := func(message string) ([]secretHeader, *reliantv1.ActionOutput) {
		return nil, failedAction(uses, "invalid_argument", message)
	}
	var slots []secretHeader
	if integrationID == httpIntegration && actionID == httpRequestAction {
		if headers, ok := params["headers"].(map[string]any); ok {
			kept := make(map[string]any, len(headers))
			seen := map[string]bool{}
			for name, raw := range headers {
				value, isString := raw.(string)
				if !isString || !stepsecrets.HasMarker(value) {
					kept[name] = raw
					continue
				}
				prefix, secret, ok := stepsecrets.CredentialSlot(value)
				if !ok {
					return fail(fmt.Sprintf("header %s: a secret must end the value, once, after only literal text (e.g. \"Bearer {{ secrets.NAME }}\")", name))
				}
				if !SecretHeaderAllowed(name) {
					return fail(fmt.Sprintf("header %s cannot carry a secret", name))
				}
				canonical := http.CanonicalHeaderKey(name)
				if seen[canonical] {
					return fail(fmt.Sprintf("header %s is given twice", canonical))
				}
				seen[canonical] = true
				slots = append(slots, secretHeader{name: canonical, prefix: prefix, secret: secret})
			}
			// A plain header of the same name would be overwritten by the slot,
			// or overwrite it: either way the author meant one of them.
			for name := range kept {
				if seen[http.CanonicalHeaderKey(name)] {
					return fail(fmt.Sprintf("header %s is given twice", http.CanonicalHeaderKey(name)))
				}
			}
			if len(slots) > 0 {
				params["headers"] = kept
			}
		}
	}
	if anyHasMarker(params) {
		return fail("a secret can only be the value of an HTTP request header (http/request@1 with.headers)")
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].name < slots[j].name })
	return slots, nil
}

// credentialSlot is an action's credential: {secret: NAME}, as the one header
// its integration's api_key method sends a key in. It stands in for a saved
// connection, so it is refused next to one, next to a header slot or hosted
// use, and for an automation that speaks as the app (whose identity the
// secret is not).
func credentialSlot(uses string, m *reliantv1.IntegrationManifest, secret string, params map[string]any, hasHeaderSlots, hostedUse bool, actingIdentity string) (secretHeader, *reliantv1.ActionOutput) {
	fail := func(message string) (secretHeader, *reliantv1.ActionOutput) {
		return secretHeader{}, failedAction(uses, "invalid_argument", message)
	}
	_, named := params[httpaction.ConnectionParam]
	switch {
	case !stepsecrets.NamePattern.MatchString(secret):
		return fail("credential.secret must name a declared secret")
	case named:
		return fail("credential and connection are mutually exclusive")
	case hasHeaderSlots:
		return fail("a secret header and a credential are mutually exclusive")
	case hostedUse:
		return fail("credential and hosted_use are mutually exclusive")
	case actingIdentity != "":
		return fail("an automation that acts as " + actingIdentity + " cannot authenticate with a declared secret")
	}
	header, prefix, err := httpaction.APIKeyPlacement(m)
	if err != nil {
		return fail(err.Error())
	}
	return secretHeader{name: header, prefix: prefix, secret: secret}, nil
}

func anyHasMarker(in any) bool {
	switch x := in.(type) {
	case string:
		return stepsecrets.HasMarker(x)
	case map[string]any:
		for k, item := range x {
			if stepsecrets.HasMarker(k) || anyHasMarker(item) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if anyHasMarker(item) {
				return true
			}
		}
	}
	return false
}

// secretHeaderCredential writes resolved secrets into their header slots. It
// is an httpaction.Credential, so the runner treats the call as an
// authenticated one: https only, and never sent to a second host.
type secretHeaderCredential struct {
	headers []secretHeader
	values  stepsecrets.Values
}

// newSecretHeaderCredential checks every resolved value can be a header value
// before anything is sent. The error names the secret, never its value.
func newSecretHeaderCredential(slots []secretHeader, values stepsecrets.Values) (*secretHeaderCredential, error) {
	for _, h := range slots {
		v, ok := values[h.secret]
		if !ok || v == "" {
			return nil, fmt.Errorf("secret %s resolved to no value", h.secret)
		}
		if !httpguts.ValidHeaderFieldValue(h.prefix + v) {
			return nil, fmt.Errorf("secret %s cannot be sent in header %s: it holds a line break or a control character", h.secret, h.name)
		}
	}
	return &secretHeaderCredential{headers: slots, values: values}, nil
}

func (c *secretHeaderCredential) ConnectionID() string      { return "" }
func (c *secretHeaderCredential) Params() map[string]string { return nil }

func (c *secretHeaderCredential) Apply(req *http.Request) error {
	for _, h := range c.headers {
		req.Header.Set(h.name, h.prefix+c.values[h.secret])
	}
	return nil
}

// Scrub is the Credential contract every authenticated call shares: the runner
// passes errors and results through it. The value is never put into either by
// this step; this only covers a server that echoes it back verbatim.
func (c *secretHeaderCredential) Scrub(s string) string {
	for _, h := range c.headers {
		if v := c.values[h.secret]; v != "" {
			s = strings.ReplaceAll(s, v, "[secret]")
		}
	}
	return s
}
