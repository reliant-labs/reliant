package main

import "strings"

// mergeTier never promotes an indeterminate affected selection into the full
// suite. The release gate is the authoritative full check on main.
func mergeTier(pl *Plan) {
	pl.Merge = true
	if pl.Go.Full {
		pl.ReleaseDeferred = append(pl.ReleaseDeferred, "full Go fallback: "+strings.Join(pl.Go.Reasons, "; "))
	}
	if pl.Web.Full {
		pl.ReleaseDeferred = append(pl.ReleaseDeferred, "full web fallback: "+strings.Join(pl.Web.Reasons, "; "))
	}
	if pl.Go.Full && len(pl.Go.Build) == 0 {
		// A dependency/config change still compiles the module; only its
		// full TEST fallback is release-only.
		pl.Go.Build = []string{"./..."}
	}
	pl.Go.Full, pl.Web.Full = false, false
	pl.Electron.Full, pl.DocGen.Full = false, false
	web := false
	inputs := false
	for _, f := range pl.Files {
		web = web || strings.HasPrefix(f.Path, "frontends/web/")
		for _, pat := range []string{"forge.yaml", "go.mod", "go.sum", "Taskfile.yml", "buf*.yaml", "proto/**", "proto-vendor/**", "db/migrations/**", "**/contract.go", "**/doc.go", "internal/handlers/**/deps.go", "internal/handlers/**/service.go", "tools/protoc-gen-es/**", "scripts/proto-generate-atomic.sh"} {
			inputs = inputs || matchGlob(pat, f.Path)
		}
	}
	if !web {
		pl.Web.Tests = nil
	}
	// Generated output and ordinary handler bodies are not generation inputs.
	// Full output validation still runs unconditionally before release.
	if !inputs && !pl.ForgeGen.HooksOnly {
		pl.ForgeGen.Full = false
	}
	pl.ReleaseDeferred = append(pl.ReleaseDeferred, "docs, Electron, production renderer build, full lint/tests, integration and e2e: scripts/release-gate.sh")
}
