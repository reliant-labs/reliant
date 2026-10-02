// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/version"
)

// =============================================================================
// forge.render_capability — can THIS machine's forge render an environment?
//
// This exists because "my forge cannot render" is a CAPABILITY of the daemon's
// binary, and it was being reported as a transport failure. On a managed cloud
// daemon the Forge tab showed "Could not reach your daemon" while the daemon
// was reachable and healthy: its embedded forge was built CGO-free, so
// kcl_plugin.forge was never registered and every render was refused. Blaming
// the connection for a build-flag problem sends the reader to the network, and
// nothing they can do there will help.
//
// The distinction this command draws is the one the UI needs:
//
//	reachable + can render      -> show the environments
//	reachable + CANNOT render   -> say so, in forge's words, with the version
//	not reachable               -> the existing daemon-unreachable state
//
// WHY `forge doctor --json` AND NOT A STDERR MATCH. The capability is already
// a first-class, machine-readable fact in forge: kclplugin.Available() backs a
// `forge doctor` self-capability check (forge internal/cli/
// doctor_selfcapabilities.go) that reports `forge: kcl-plugin` pass/fail with
// the expected/found/impact/fix evidence. Reading that is a contract. Matching
// the render-time error text would be a second, weaker copy of forge's own
// judgment living in reliant, free to disagree with it the moment forge
// rewords the message — the same reasoning that keeps every other handler in
// this package a passthrough rather than a reimplementation.
//
// It is also the only probe that answers the question WITHOUT doing the thing:
// `env render` needs a renderable env and writes files, while doctor's check is
// a pure in-process capability read.
// =============================================================================

// forgeKCLPluginCheckName is the check `forge doctor` reports the KCL plugin
// capability under. Matched exactly, not by substring: `tool: kcl` is a
// DIFFERENT check in the same report (the kcl binary on PATH, which can pass
// on a binary that cannot render), and a substring match would conflate them.
const forgeKCLPluginCheckName = "forge: kcl-plugin"

// forgeDoctorCheck is one check in `forge doctor --json`'s report. Only the
// fields this decision needs are declared; forge is free to add more.
type forgeDoctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Evidence string `json:"evidence"`
}

// forgeDoctorReport is the top-level `forge doctor --json` document.
type forgeDoctorReport struct {
	Checks []forgeDoctorCheck `json:"checks"`
}

// forgeRenderCapabilityRequest asks about the project at ProjectPath.
//
// The path is required even though the capability is a property of the BINARY,
// not of the project: `forge doctor` resolves its project from the working
// directory, and the answer should be reported beside the project the UI is
// showing.
type forgeRenderCapabilityRequest struct {
	ProjectPath string `json:"project_path"`
}

// forgeRenderCapabilityResponse answers "can this machine's forge render?".
type forgeRenderCapabilityResponse struct {
	// CanRender is the whole point. False means no environment on this
	// machine can be rendered, deployed or brought up, whatever the
	// project looks like.
	CanRender bool `json:"can_render"`

	// ForgeVersion is the forge this daemon's binary embeds, read from the
	// module graph rather than a second constant so it cannot disagree
	// with the forge actually linked in.
	ForgeVersion string `json:"forge_version"`

	// Reason is forge's OWN one-line explanation when CanRender is false
	// (e.g. "kcl_plugin.forge unavailable — this forge was built without
	// CGO; no environment can render"). Forge's words, not a paraphrase,
	// so the UI and `forge doctor` cannot tell the user different stories.
	Reason string `json:"reason,omitempty"`

	// Detail is forge's expected/found/impact/fix evidence block. Carried
	// so the UI can offer the actual fix rather than only the complaint.
	Detail string `json:"detail,omitempty"`

	// Determined is false when the capability could not be established at
	// all — an old forge with no such check, or a doctor run that produced
	// nothing parseable. UNKNOWN is not "cannot render": refusing to
	// render a project over an inconclusive probe would be the same class
	// of error as blaming the transport.
	Determined bool `json:"determined"`
}

func handleForgeRenderCapability(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeRenderCapabilityRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.ProjectPath == "" {
		return nil, fmt.Errorf("project_path is required")
	}

	resp := forgeRenderCapabilityResponse{ForgeVersion: version.Forge()}

	// `doctor` exits non-zero whenever ANY check fails — an absent docker,
	// an unreachable cluster — so the exit code says nothing about the one
	// check being read here. The report is the answer; the code is noise.
	res, err := runForge(ctx, req.ProjectPath, []string{"doctor", "--json"})
	if err != nil {
		// Could not run forge at all. Undetermined, NOT incapable.
		return json.Marshal(resp)
	}

	var report forgeDoctorReport
	if err := json.Unmarshal(bytes.TrimSpace(res.Stdout), &report); err != nil {
		return json.Marshal(resp)
	}

	for _, check := range report.Checks {
		if check.Name != forgeKCLPluginCheckName {
			continue
		}
		resp.Determined = true
		// "pass" is the only status that asserts the namespace is
		// registered. "skip" means the check did not apply (a project
		// with deploy off), which is not a claim that rendering works,
		// and must not be read as one.
		switch strings.ToLower(strings.TrimSpace(check.Status)) {
		case "pass":
			resp.CanRender = true
		case "skip":
			resp.Determined = false
		default:
			resp.Reason = check.Message
			resp.Detail = check.Evidence
		}
		break
	}

	return json.Marshal(resp)
}
