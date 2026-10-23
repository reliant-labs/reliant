package release

import (
	"fmt"
	"strings"
	"time"
)

// EnvKind is how an environment runs, and therefore who applies it and
// whether its secrets can be read back. Closed. It is a PREDICATE over an
// env's rendered declaration — forge derives it, nobody selects it — and a
// backend treats it as immutable once recorded: an env whose kind changed is
// a different env.
type EnvKind string

const (
	// EnvPersistent: the platform runs it (something is bound OnHosted).
	// A converger applies its promotions; its secrets are write-only.
	EnvPersistent EnvKind = "persistent"
	// EnvPreview: a persistent env with a TTL.
	EnvPreview EnvKind = "preview"
	// EnvSelfManaged: a control plane keeps its ledger, nothing is hosted,
	// and something runs on a cluster the author operates. forge applies
	// it; the platform places nothing. Its secrets are write-only, which is
	// the point of separating it from EnvLocal: a cluster env's secrets must
	// never be pullable.
	EnvSelfManaged EnvKind = "self_managed"
	// EnvLocal: every workload runs on a developer machine (`forge env
	// up`). Never a deploy target; its secrets are readable back.
	EnvLocal EnvKind = "local"
)

// EnvKinds is the closed set, in a stable order.
var EnvKinds = []EnvKind{EnvPersistent, EnvPreview, EnvSelfManaged, EnvLocal}

// Valid reports whether k is one of [EnvKinds].
func (k EnvKind) Valid() bool {
	for _, known := range EnvKinds {
		if k == known {
			return true
		}
	}
	return false
}

// Placed reports whether the platform holds placement (a namespace on its
// fleet) for an env of this kind, and so converges and observes it.
func (k EnvKind) Placed() bool { return k == EnvPersistent || k == EnvPreview }

// Promotable reports whether an env of this kind can be bound to a release.
// Everything but LOCAL: a self-managed env IS promoted; it is just applied by
// forge rather than converged by the platform.
func (k EnvKind) Promotable() bool { return k.Valid() && k != EnvLocal }

// ParseEnvKind reads a stored or wire kind, refusing anything outside the set.
func ParseEnvKind(s string) (EnvKind, error) {
	if k := EnvKind(s); k.Valid() {
		return k, nil
	}
	return "", fmt.Errorf("%w: unknown environment kind %q (expected one of %s)", ErrInvalid, s, strings.Join(envKindNames(), ", "))
}

// UnmarshalJSON refuses an empty or unknown kind.
func (k *EnvKind) UnmarshalJSON(data []byte) error {
	return decodeClosed(data, "environment kind", func(s string) bool { return EnvKind(s).Valid() }, envKindNames(), (*string)(k))
}

func envKindNames() []string {
	out := make([]string, len(EnvKinds))
	for i, k := range EnvKinds {
		out[i] = string(k)
	}
	return out
}

// EnvRecord is an environment as a ledger holds it: its identity, its kind,
// and the most recently DECLARED shape — what its KCL said the last time
// someone built it. DeclaredShape is what lets a reader answer "what kind,
// which secrets, which provider" without rendering anything, which is what
// makes the Live view work with no daemon.
type EnvRecord struct {
	Name          string      `json:"name"`
	Kind          EnvKind     `json:"kind"`
	DeclaredShape *Shape      `json:"declared_shape,omitempty"`
	DeclaredBy    *Provenance `json:"declared_by,omitempty"`
	DeclaredAt    *time.Time  `json:"declared_at,omitempty"`
}

// Validate checks the record and, when present, that its declared shape
// agrees with its kind: a declaration is a projection of one render, and a
// render cannot disagree with itself about how the env runs.
func (r EnvRecord) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w: environment name is required", ErrInvalid)
	}
	if !r.Kind.Valid() {
		return fmt.Errorf("%w: environment %q: unknown kind %q", ErrInvalid, r.Name, r.Kind)
	}
	if (r.DeclaredShape == nil) != (r.DeclaredAt == nil) {
		return fmt.Errorf("%w: environment %q: a declared shape and its time come together", ErrInvalid, r.Name)
	}
	if r.DeclaredShape != nil {
		if err := r.DeclaredShape.Validate(); err != nil {
			return fmt.Errorf("environment %q: %w", r.Name, err)
		}
		if r.DeclaredShape.Kind != r.Kind {
			return fmt.Errorf("%w: environment %q is %s, but its declared shape says %s", ErrInvalid, r.Name, r.Kind, r.DeclaredShape.Kind)
		}
	}
	if r.DeclaredBy != nil {
		if err := r.DeclaredBy.Validate(); err != nil {
			return fmt.Errorf("environment %q: %w", r.Name, err)
		}
	}
	return nil
}
