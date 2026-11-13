package release

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

// The declared-environment catalog: every env a project declares under
// deploy/kcl/ at the bundle's commit, sealed into the bundle document
// ([BundleDoc.DeclaredEnvs]).
//
// WHY IT IS IN THE BUNDLE. A control plane can derive almost the whole
// release/environment topology from its own records — environment rows,
// promotions, releases, bundle shapes. What no record holds is the envs the
// SOURCE declares that never reached it: an env added to KCL and never built,
// and every env with a file ledger (no control_plane), which by design never
// records anything there. The catalog is that one fact, and the bundle
// document is the one place a control plane reads from verified bytes rather
// than from a request field. It is computed from the same commit the bundle
// is, so it adds nothing a reproducible build does not already pin.
//
// NAMES AND CLOSED VOCABULARIES ONLY. A catalog is stored forever and shown
// to every member of the org; [ValidateDeclaredEnvs] refuses a path, free
// text, or anything outside the vocabularies below, so a render failure is a
// code, never the error text (which carries machine paths).

// MaxDeclaredEnvs bounds the catalog's length; [MaxDeclaredEnvsBytes] its
// encoded size.
const (
	MaxDeclaredEnvs      = 100
	MaxDeclaredEnvsBytes = 32 << 10
)

// The declared-env lifecycle vocabulary ([DeclaredEnv.Lifecycle]): the env's
// Bundle.lifecycle, or "" when it declares none.
const (
	DeclaredLifecycleLocal     = "local"
	DeclaredLifecycleEphemeral = "ephemeral"
)

// The declared-env destination vocabulary ([DeclaredEnv.Destination]): where
// the env runs, as one word — each deployable part votes by its runtime
// binding, one kind is that kind, several are "mixed". The same words as
// `forge env status --json`'s destination (forge/pkg/topology).
const (
	DeclaredDestinationHosted  = "hosted"
	DeclaredDestinationCluster = "cluster"
	DeclaredDestinationCompose = "compose"
	DeclaredDestinationHost    = "host"
	DeclaredDestinationStatic  = "static"
	DeclaredDestinationMixed   = "mixed"
)

// The render-error vocabulary ([DeclaredEnv.RenderError]). Codes, never text.
const (
	// DeclaredEnvRenderFailed: the env's KCL did not render.
	DeclaredEnvRenderFailed = "render_failed"
	// DeclaredEnvRenderTimeout: the render did not finish within the
	// build's budget for the catalog.
	DeclaredEnvRenderTimeout = "render_timeout"
)

// DeclaredEnv is one env the source declares.
type DeclaredEnv struct {
	// Name is the env's directory under deploy/kcl/.
	Name string `json:"name"`
	// Lifecycle is DeclaredLifecycle*, or "" when unset.
	Lifecycle string `json:"lifecycle,omitempty"`
	// Destination is DeclaredDestination*, or "" when the env did not
	// render (RenderError says why).
	Destination string `json:"destination,omitempty"`
	// ControlPlaneKind is the env's control-plane binding: EnvLocal,
	// EnvPersistent or EnvSelfManaged for an env that declares
	// control_plane, "" for one with a file ledger.
	ControlPlaneKind string `json:"control_plane_kind,omitempty"`
	// RenderError is DeclaredEnvRender*, or "" when the env rendered.
	RenderError string `json:"render_error,omitempty"`
}

// declaredEnvName is an env directory name: what a KCL env can be called and
// nothing that can hold a path or prose.
var declaredEnvName = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

// ValidDeclaredEnvName reports whether name may appear in a catalog: lower
// case letters, digits, '.', '_' and '-', at most 63 characters, starting and
// ending with a letter or digit.
func ValidDeclaredEnvName(name string) bool { return declaredEnvName.MatchString(name) }

// ValidateDeclaredEnvs checks a catalog: bounded, sorted by name with no
// repeats, every name a bare env name and every other field in its closed
// vocabulary. A nil catalog is valid (absent).
func ValidateDeclaredEnvs(envs []DeclaredEnv) error {
	if envs == nil {
		return nil
	}
	if len(envs) > MaxDeclaredEnvs {
		return fmt.Errorf("%w: declared_envs lists %d envs (at most %d)", ErrInvalid, len(envs), MaxDeclaredEnvs)
	}
	raw, err := json.Marshal(envs)
	if err != nil {
		return fmt.Errorf("%w: declared_envs: %v", ErrInvalid, err)
	}
	if len(raw) > MaxDeclaredEnvsBytes {
		return fmt.Errorf("%w: declared_envs is %d bytes (at most %d)", ErrInvalid, len(raw), MaxDeclaredEnvsBytes)
	}
	for i, e := range envs {
		if !ValidDeclaredEnvName(e.Name) {
			return fmt.Errorf("%w: declared_envs[%d] name %q is not an env name", ErrInvalid, i, e.Name)
		}
		if i > 0 && envs[i-1].Name >= e.Name {
			return fmt.Errorf("%w: declared_envs must be sorted by name with no repeats (%q after %q)", ErrInvalid, e.Name, envs[i-1].Name)
		}
		if !oneOf(e.Lifecycle, "", DeclaredLifecycleLocal, DeclaredLifecycleEphemeral) {
			return fmt.Errorf("%w: declared env %s lifecycle %q", ErrInvalid, e.Name, e.Lifecycle)
		}
		if !oneOf(e.Destination, "", DeclaredDestinationHosted, DeclaredDestinationCluster, DeclaredDestinationCompose,
			DeclaredDestinationHost, DeclaredDestinationStatic, DeclaredDestinationMixed) {
			return fmt.Errorf("%w: declared env %s destination %q", ErrInvalid, e.Name, e.Destination)
		}
		if !oneOf(e.ControlPlaneKind, "", string(EnvLocal), string(EnvPersistent), string(EnvSelfManaged)) {
			return fmt.Errorf("%w: declared env %s control_plane_kind %q", ErrInvalid, e.Name, e.ControlPlaneKind)
		}
		if !oneOf(e.RenderError, "", DeclaredEnvRenderFailed, DeclaredEnvRenderTimeout) {
			return fmt.Errorf("%w: declared env %s render_error %q (a code, never text)", ErrInvalid, e.Name, e.RenderError)
		}
		if e.RenderError != "" && (e.Destination != "" || e.Lifecycle != "" || e.ControlPlaneKind != "") {
			return fmt.Errorf("%w: declared env %s did not render, so it can state nothing but its name", ErrInvalid, e.Name)
		}
	}
	return nil
}

func oneOf(v string, allowed ...string) bool { return slices.Contains(allowed, v) }
