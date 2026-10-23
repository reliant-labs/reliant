package codegen

import (
	"sort"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// IntrospectComponents enumerates a project's Connect SERVICES from the
// proto descriptor — the single authoritative, non-brittle source (the
// same one all of codegen reads via ParseServicesFromProtos). It is the
// shared seam every read-only diagnostic consumer (audit, graph, api, dev
// info, architecture docs, run, doctor parity, debug) reads a project's
// services through.
//
// It enumerates SERVICES only. The supervised kinds (worker, operator) and
// secondary binaries have no proto contract, so they are discovered from
// their package directories instead — see DiscoverProjectComponents, which
// unions the two into an [Inventory] and is what every caller that needs the
// project's FULL component set goes through.
//
// Each entry carries only Name, Kind=server, and the conventional handler
// Path. Ports are a DEPLOY fact (they live in KCL), left unset. Best-effort:
// a missing or unparsable descriptor yields no services (not an error), so
// the surface it decorates degrades gracefully rather than failing.
func IntrospectComponents(projectDir string) []config.ComponentConfig {
	defs, err := ParseServicesFromProtos("", projectDir)
	if err != nil || len(defs) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]config.ComponentConfig, 0, len(defs))
	for _, d := range defs {
		// Canonicalize "BillingService" → "billing" via the SAME transform
		// the cmd-subcommand / mounts codegen uses, so the introspected name
		// matches the handler dir and the pkg/app/services.go registration key.
		name := naming.ServicePackage(d.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, config.ComponentConfig{
			Name: name,
			Kind: config.ComponentKindServer,
			Path: "internal/handlers/" + name,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// IntrospectComponentNames is the flat service-name list (e.g. a "did you
// mean" hint). Order matches IntrospectComponents.
func IntrospectComponentNames(projectDir string) []string {
	comps := IntrospectComponents(projectDir)
	out := make([]string, 0, len(comps))
	for _, c := range comps {
		out = append(out, c.Name)
	}
	return out
}
