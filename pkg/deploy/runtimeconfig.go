package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// RuntimeConfigGlobal is the browser global a StaticSite's runtime config
// document assigns, and the one forge's generated config module reads.
const RuntimeConfigGlobal = "__FORGE_CONFIG__"

// RuntimeConfigFile is the document's name. It is served at
// <basePath>/RuntimeConfigFile, beside the site's index.html.
const RuntimeConfigFile = "config.js"

// WorkloadURLResolver answers "what is the public URL of the workload named
// name, in this environment?". It returns an error when the name is unknown
// or the workload has no URL; a resolver never guesses.
type WorkloadURLResolver func(name string) (string, error)

// ResolveRuntimeConfig turns a StaticSite's declared runtime config into the
// flat key -> string document the browser receives, resolving every
// workloadURL reference through resolve.
//
// Every failing entry is reported at once, so a spec with two dangling
// references is fixed in one pass. The input is validated first: a spec that
// sets both or neither channel has no correct reading, and resolving it would
// hide that.
func ResolveRuntimeConfig(rc map[string]v1alpha1.RuntimeConfigValue, resolve WorkloadURLResolver) (map[string]string, error) {
	if err := v1alpha1.ValidateRuntimeConfig(rc); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rc))
	var errs []error
	for _, k := range sortedRuntimeConfigKeys(rc) {
		v := rc[k]
		if v.Value != nil {
			out[k] = *v.Value
			continue
		}
		url, err := resolve(v.WorkloadURL.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("runtimeConfig.%s: workloadURL %q: %w", k, v.WorkloadURL.Name, err))
			continue
		}
		out[k] = url
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// ResolveEnvWorkloadURLs returns a copy of env with every workloadURL
// reference replaced by its resolved literal value. Render refuses an
// unresolved reference, so this is the step that must run before it.
func ResolveEnvWorkloadURLs(env []v1alpha1.EnvVar, resolve WorkloadURLResolver) ([]v1alpha1.EnvVar, error) {
	if env == nil {
		return nil, nil
	}
	out := make([]v1alpha1.EnvVar, len(env))
	var errs []error
	for i, e := range env {
		out[i] = e
		if e.WorkloadURL == nil {
			continue
		}
		url, err := resolve(e.WorkloadURL.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("env var %s: workloadURL %q: %w", e.Name, e.WorkloadURL.Name, err))
			continue
		}
		out[i].WorkloadURL = nil
		out[i].Value = url
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// RuntimeConfigJS renders the runtime config document a StaticSite serves at
// <basePath>/config.js:
//
//	window.__FORGE_CONFIG__ = {"API_URL":"https://..."};
//
// It is the ONE renderer of that document for resolved specs, so the control
// plane's operator and forge produce identical bytes for identical input.
// Keys are emitted sorted (encoding/json sorts map keys), which keeps the
// document byte-stable across syncs and lets a no-op sync be detected by
// comparing bytes.
func RuntimeConfigJS(values map[string]string) (string, error) {
	if values == nil {
		values = map[string]string{}
	}
	body, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode runtime config: %w", err)
	}
	return "window." + RuntimeConfigGlobal + " = " + string(body) + ";\n", nil
}

func sortedRuntimeConfigKeys(rc map[string]v1alpha1.RuntimeConfigValue) []string {
	keys := make([]string, 0, len(rc))
	for k := range rc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
