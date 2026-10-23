// Package cli — `forge project audit` external-builds category (Phase 3 of the
// build_cmd / external-build escape-hatch refactor).
//
// Surfaces every KCL service whose effective build is a ShellBuild. For
// each such service the category emits:
//
//   - the resolved build_cwd state (exists / missing on disk),
//   - the last build state from buildtarget.ReadState (image, tag,
//     pushed_at) for every env that recorded one,
//   - any build_env keys that conflict with a built-in substitution
//     token (IMAGE / TAG / SERVICE / PROJECT_DIR / REGISTRY /
//     TARGETARCH / BUILD_CWD).
//
// Sub-agents and CI dashboards can branch on
// `.external_builds.status == "warn"` (or "error") directly. The
// category honours the additive-extension contract from the
// audit-json skill — a new field on the per-service detail map is
// additive; consumers that don't read the new key are unaffected.
//
// We render the dev env to enumerate services (matching auditIngress's
// approach) because that's the only env every project is guaranteed
// to have. The per-service ReadState loop walks every env declared
// under deploy/kcl/<env>/main.k so the snapshot reflects what each
// env last built, not just dev.
//
// Status semantics:
//
//   - ok    — no services declare a ShellBuild, OR every service has a
//     present build_cwd, no env-key conflicts, and at least
//     one env has recorded state (or no envs at all).
//   - warn  — at least one service has a missing build_cwd on disk, OR
//     at least one service declares a build_env key that
//     collides with a built-in token. Neither blocks a build —
//     skip-with-warn for cwd is the documented contract, and
//     the built-in wins on conflict — but both are surface-up
//     worthy.
//   - error — KCL render itself failed AND we know features.build is
//     on (project intends to build something; we can't see
//     whether external builds are configured). Degrades to
//     warn when the failure is environmental (kcl not on
//     PATH, no deploy/kcl/dev dir).
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/config"
)

// externalBuildEntry is the per-service detail row surfaced under
// `.external_builds.details.services`. JSON field names follow the
// snake_case convention the rest of audit uses.
type externalBuildEntry struct {
	Service      string                `json:"service"`
	Image        string                `json:"image,omitempty"`
	BuildCwd     string                `json:"build_cwd,omitempty"`
	ResolvedCwd  string                `json:"resolved_cwd,omitempty"`
	CwdExists    bool                  `json:"cwd_exists"`
	BuildEnvKeys []string              `json:"build_env_keys,omitempty"`
	LastBuilds   []externalBuildStateE `json:"last_builds,omitempty"`
}

// externalBuildStateE is the per-(env, service) state snapshot. We
// drop the inner buildtarget.State Service field — it's already the
// outer key — and flatten env in so the JSON reads naturally when a
// service has built across multiple envs.
type externalBuildStateE struct {
	Env      string `json:"env"`
	Image    string `json:"image,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Registry string `json:"registry,omitempty"`
	PushedAt string `json:"pushed_at,omitempty"`
}

// auditExternalBuilds is the audit collector for the external-builds
// category. Mirrors auditIngress's shape — render the dev env to
// enumerate services, then cross-check on disk. KCL render failures
// degrade to warn rather than failing the whole audit, same as the
// rest of the categories.
//
// The category appears in every project's audit report regardless of
// whether any service declares build_cmd — when none do, it surfaces
// as an ok with a "0 services" summary so CI consumers always have
// the key to read (additive-extension contract).
func auditExternalBuilds(cfg *config.ProjectConfig, projectDir string) audittype.Category {
	if cfg == nil {
		return audittype.Category{
			Status:  audittype.StatusError,
			Summary: "no forge.yaml",
		}
	}

	// There is no external_builds gate any more. The flag had already
	// been reduced to an inert key nothing consulted: `Service.build_cmd`
	// is the build-side mirror of `External.deploy_cmd`, and since
	// `forge env deploy` of an External target never required an opt-in,
	// gating `forge build` of the same target left the pair with
	// mismatched maturity gates (fr-da9a6614fb). build_cmd just builds,
	// so the audit reports what is declared, unconditionally.
	entities, err := RenderKCL(context.Background(), projectDir, "dev")
	if err != nil {
		// Environmental: kcl not on PATH or deploy/kcl/dev missing.
		// Same disposition as auditIngress — degrade to warn rather
		// than failing the whole audit. CI without the kcl toolchain
		// still gets every other category.
		return audittype.Category{
			Status:  audittype.StatusWarn,
			Summary: fmt.Sprintf("could not evaluate dev KCL: %v", err),
		}
	}

	envs, _ := ListEnvs(projectDir)
	return collectExternalBuildEntries(entities, envs, projectDir)
}

// collectExternalBuildEntries is the pure decision core. Given the
// dev-rendered KCL entity set and the list of envs to read state for,
// it returns the audittype.Category. Split out from auditExternalBuilds so
// unit tests can exercise the cross-check (cwd-stat, env-conflict,
// state-aggregation) without shelling kcl or ListEnvs.
//
// envs may be nil/empty when ListEnvs fails — the per-service state
// lookup degrades gracefully (zero LastBuilds entries).
func collectExternalBuildEntries(entities *KCLEntities, envs []string, projectDir string) audittype.Category {
	services := externalBuildServices(entities)
	if len(services) == 0 {
		return audittype.Category{
			Status:  audittype.StatusOK,
			Summary: "0 service(s) declare a ShellBuild",
			Details: map[string]any{
				"services": []externalBuildEntry{},
			},
		}
	}

	entries := make([]externalBuildEntry, 0, len(services))
	missingCwdCount := 0
	stateCount := 0

	for _, svc := range services {
		entry := buildExternalBuildEntry(svc, projectDir, envs)
		if entry.BuildCwd != "" && !entry.CwdExists {
			missingCwdCount++
		}
		stateCount += len(entry.LastBuilds)
		entries = append(entries, entry)
	}
	// Stable order so JSON diffs across audit runs are clean.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Service < entries[j].Service
	})

	status := audittype.StatusOK
	if missingCwdCount > 0 {
		status = audittype.StatusWarn
	}

	details := map[string]any{
		"services":          entries,
		"service_count":     len(services),
		"missing_cwd_count": missingCwdCount,
		"state_count":       stateCount,
	}
	if missingCwdCount > 0 {
		details["hint"] = []string{"missing build_cwd on disk is a hard failure at build time; check out the sibling repo or set build_cwd to a path that exists"}
	}

	summary := fmt.Sprintf("%d service(s) declare a ShellBuild; %d missing cwd, %d recorded build state(s)",
		len(services), missingCwdCount, stateCount)
	return audittype.Category{
		Status:  status,
		Summary: summary,
		Details: details,
	}
}

// buildExternalBuildEntry composes the per-service detail row. Pure
// over the (svc, projectDir, envs) tuple — stats the resolved cwd and
// reads state for each env.
func buildExternalBuildEntry(svc WorkloadEntity, projectDir string, envs []string) externalBuildEntry {
	buildCwd := svc.EffectiveBuildCwd()
	buildEnv := svc.EffectiveBuildEnv()
	entry := externalBuildEntry{
		Service:  svc.Name,
		Image:    svc.Image,
		BuildCwd: buildCwd,
	}

	// Resolve the build cwd against the project root (relative paths
	// resolve against projectDir; absolute pass through) and stat it.
	// Empty cwd means "use projectDir directly" — projectDir always
	// exists by construction (audit runs from it), so CwdExists=true.
	resolved := buildCwd
	if resolved == "" {
		resolved = projectDir
	} else if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(projectDir, resolved)
	}
	entry.ResolvedCwd = resolved
	if _, err := os.Stat(resolved); err == nil {
		entry.CwdExists = true
	} else {
		entry.CwdExists = false
	}

	// Sorted build_env keys. Sorted keeps the JSON stable across audit
	// runs (map iteration order is non-deterministic).
	if len(buildEnv) > 0 {
		keys := make([]string, 0, len(buildEnv))
		for k := range buildEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		entry.BuildEnvKeys = keys
	}

	// Per-env state aggregation. Missing state files (ReadState
	// returns (nil, nil)) skip — that's the "haven't built yet" case
	// and audit shouldn't conflate it with a real error. Read errors
	// (malformed JSON, permission denied) are logged into the entry
	// implicitly as "no state recorded" since audit-json is
	// observational.
	for _, env := range envs {
		st, err := buildtarget.ReadState(projectDir, env, svc.Name)
		if err != nil || st == nil {
			continue
		}
		entry.LastBuilds = append(entry.LastBuilds, externalBuildStateE{
			Env:      env,
			Image:    st.Image,
			Tag:      st.Tag,
			PushedAt: st.PushedAt,
		})
	}
	return entry
}
