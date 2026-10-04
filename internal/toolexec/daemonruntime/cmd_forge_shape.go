// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
)

func init() {
	RegisterCommand("forge.env_shape", handleForgeEnvShape)
}

// --- forge.env_shape ---

// forgeEnvShapeArgs builds the argv for `forge env shape <env> --json`.
//
// A named type with a method rather than an inline slice, matching
// forgeDeployArgs / forgePromoteArgs, because cmd_forge_argv_test.go parses
// the argv every daemon path builds against the forge command tree this
// binary EMBEDS. A builder it can call without driving the whole handler is
// what lets that test cover this command.
type forgeEnvShapeArgs struct {
	ProjectPath string `json:"project_path"`
	Env         string `json:"env"`
}

// shapeArgs pins the env as a POSITIONAL, which is what `forge env shape`
// takes (the same position `forge env build <env>` and `forge env render
// <env>` use). Contrast `forge secret list`, whose env moved to a required
// --env flag and silently broke this daemon for a release: that is the
// regression cmd_forge_argv_test.go exists to catch, and it covers this argv
// too.
func (a forgeEnvShapeArgs) shapeArgs() []string {
	return []string{"env", "shape", a.Env, "--json"}
}

// handleForgeEnvShape projects ONE environment's render into the shape the
// control plane records for it.
//
// ── WHY THE UI NEEDS THIS, GIVEN THAT LIVE NEVER CALLS THE DAEMON ───────────
//
// Live reads the control plane directly and never comes here (design §8.0,
// O-14). But an environment that exists only in the user's KCL has no
// control-plane row to read: nothing has ever recorded it, so Live cannot show
// it at all, and the chicken-and-egg that follows is the one the owner hit —
// you cannot set a secret on an environment before its first deploy, and you
// cannot deploy without the secret.
//
// This command is the bootstrap that breaks it (briefing §6). PREVIEW, which
// is daemon-dependent by definition because it renders the user's checkout,
// calls this to learn the env's kind and shape; the browser then calls
// EnsureEnvironment ITSELF, with the user's session, to create the row. From
// that moment the env is in Live and needs no daemon again — not for secrets,
// not for its provenance — except for Preview and further builds.
//
// So the daemon's involvement is confined to the one thing only it can do:
// read the files. It is not in the path of anything the UI SHOWS about a
// registered environment.
//
// ── THE PROJECTION IS FORGE'S, NOT OURS ─────────────────────────────────────
//
// `forge env shape` is a read-only, write-watched projection of the env's
// render into {project, env, kind, shape, provenance} — the SAME projection
// `forge env build` records. That identity is the whole point: if this handler
// derived a shape of its own, or the UI assembled one from a topology report,
// a Register could write a declaration that disagreed with what the next
// build records, and the env's immutable kind would be whichever of the two
// got there first. Registering must be indistinguishable from building, minus
// the bytes.
//
// Hence the usual rule here, exactly as the other forge handlers follow it:
// THE DAEMON IS A TRANSPORT. forge's JSON document goes upstream verbatim and
// nothing on this path parses, re-derives or validates it. The server does
// the validating, strictly, against forge's own release.Shape type.
func handleForgeEnvShape(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeEnvShapeArgs
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Env == "" {
		return nil, fmt.Errorf("env is required")
	}

	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        req.shapeArgs(),
	})
}
