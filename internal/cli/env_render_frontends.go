package cli

import (
	"fmt"
	"io"

	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The runtime config keys Frontend.fixtures renders to (kcl/render.k
// _fixtures_runtime_config). The browser reads them from config.js as
// window.__FORGE_CONFIG__.FIXTURES_SCENARIO / .FIXTURES_READ_ONLY.
const (
	fixturesScenarioKey = "FIXTURES_SCENARIO"
	fixturesReadOnlyKey = "FIXTURES_READ_ONLY"
)

// envRenderDocFrontend is one frontend in `forge env render --json`: where it
// runs, and the runtime config document this env serves it. A frontend
// renders no Kubernetes object on most runtimes (a dev server, platform
// hosting, your bucket), so without this the per-env facts that reach the
// browser — Frontend.fixtures above all — had no readback in the render.
type envRenderDocFrontend struct {
	Name    string `json:"name"`
	Runtime string `json:"runtime"`
	// RuntimeConfig is the document as the env DECLARES it: literals as
	// {"value": …}, forge.WorkloadURL references kept as {"workloadURL":
	// {"name": …}}, the same wire shape an OnHosted frontend publishes. {}
	// when the frontend has none.
	RuntimeConfig map[string]deployv1alpha1.RuntimeConfigValue `json:"runtime_config"`
}

// renderDocFrontends projects the env's frontends for the --json document.
// [] when there are none, so a consumer iterates it unconditionally.
func renderDocFrontends(e *KCLEntities) []envRenderDocFrontend {
	out := []envRenderDocFrontend{}
	if e == nil {
		return out
	}
	for _, f := range e.Frontends {
		rc := f.RuntimeConfigSpec
		if rc == nil {
			rc = map[string]deployv1alpha1.RuntimeConfigValue{}
		}
		out = append(out, envRenderDocFrontend{Name: f.Name, Runtime: f.Runtime.Type, RuntimeConfig: rc})
	}
	return out
}

// frontendFixtures reports the fixture scenario a frontend's runtime config
// names, and whether writes stay refused. ok is false when it names none.
// Read-only unless the document says exactly "false", as the browser reads it.
func frontendFixtures(f FrontendEntity) (scenario string, readOnly, ok bool) {
	v, found := f.RuntimeConfigSpec[fixturesScenarioKey]
	if !found || v.Value == nil || *v.Value == "" {
		return "", false, false
	}
	ro := f.RuntimeConfigSpec[fixturesReadOnlyKey]
	return *v.Value, ro.Value == nil || *ro.Value != "false", true
}

// writeFixturesSummary names, on stderr beside the render summary, every
// frontend this env serves a fixture dataset instead of its backend. It is the
// one per-env fact that changes what a promoted release DOES, so it is worth
// a line even when the frontend renders no object.
func writeFixturesSummary(w io.Writer, e *KCLEntities) {
	if e == nil {
		return
	}
	header := false
	for _, f := range e.Frontends {
		scenario, readOnly, ok := frontendFixtures(f)
		if !ok {
			continue
		}
		if !header {
			fmt.Fprintf(w, "\nfixtures (served from runtime config instead of the backend; the release is unchanged):\n")
			header = true
		}
		writes := "read-only: only NO_SIDE_EFFECTS rpcs answer"
		if !readOnly {
			writes = "writes allowed, in the browser's fixture store"
		}
		fmt.Fprintf(w, "  %s: scenario %q, %s\n", f.Name, scenario, writes)
	}
}
