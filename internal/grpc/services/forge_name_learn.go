// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/reliant-labs/reliant/internal/logging"
)

// LEARN forge's NAME WHEN THE CLONE LANDS, NOT WHEN SOMEBODY OPENS A TAB.
//
// projects.forge_project_name is the key the web joins a project to its
// control-plane deploy environments on, and it has to be readable with the
// daemon OFFLINE. Until this existed the only writer was
// ForgeService.rememberForgeProjectName, which runs when a forge TOPOLOGY RPC
// succeeds — so a freshly cloned forge project reported "this project's forge
// name has not been read from its forge.yaml by a daemon yet" until someone
// opened the Forge tab while a daemon happened to be online. The name was
// sitting in a file the daemon had just written.
//
// The clone is the right moment precisely because the daemon is already there,
// already holding the checkout, and has just finished writing it. One narrow
// file read costs nothing next to the clone it follows.
//
// A non-forge repo is the common case and is NOT marked: SetProjectForgeName
// sets is_forge, so calling it for a repo with no forge.yaml would label every
// plain project a forge project. An empty read is therefore a no-op, not a
// write of "".
//
// Best effort throughout. The clone succeeded and the project is usable
// whether or not this lands, and the topology path still backfills later — so
// a daemon that drops between the clone and this read costs a deferred label,
// never the install.

// ── WHY THIS CANNOT RUN ON THE RECEIVE LOOP ───────────────────────────
//
// ToolsDaemonService reads every DaemonMessage from one connection in a single
// loop, and a DaemonCommandResponse is delivered to its waiter BY THAT LOOP.
// So a handler which blocks waiting for a command response can never receive
// it: the goroutine that would hand it over is the one parked waiting. It is a
// guaranteed deadlock, held until the command's own timeout expires, and it
// stalls every other message from that daemon meanwhile.
//
// That is why learnForgeNameForProject dispatches and returns. The settle this
// follows is already committed, so nothing downstream depends on the read.

// forgeNameReader is the one daemon capability this needs: ask a SPECIFIC
// daemon for forge.yaml's name at a path.
//
// Declared at the consumer and limited to one method. The daemon id matters —
// the project was cloned onto one particular machine, and asking the user's
// default daemon could read a different disk that does not have the checkout.
type forgeNameReader interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// forgeNameWriter is the one database capability this needs.
type forgeNameWriter interface {
	SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error)
}

// forgeNameReadTimeoutMs bounds the read. It is a single stat-and-parse of a
// file the daemon just wrote, so this is generous; it exists so a wedged
// daemon defers the label instead of holding the caller open.
const forgeNameReadTimeoutMs = 10_000

// learnForgeProjectName asks the daemon holding a checkout for forge.yaml's
// `name` and persists it, so the project knows its forge name with no daemon
// online and without anyone opening the Forge tab.
//
// Returns the name it persisted, or "" when the project is not a forge project
// or the read did not succeed. Callers log; nothing here is fatal.
func learnForgeProjectName(
	ctx context.Context,
	daemons forgeNameReader,
	projects forgeNameWriter,
	userID, daemonID, projectID, path string,
) string {
	if daemons == nil || projects == nil {
		return ""
	}
	if userID == "" || daemonID == "" || projectID == "" || strings.TrimSpace(path) == "" {
		return ""
	}

	payload, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return ""
	}
	respBytes, err := daemons.SendDaemonCommandToDaemon(
		ctx, userID, daemonID, "forge.project_name", payload, forgeNameReadTimeoutMs)
	if err != nil {
		logging.Info("could not read forge.yaml's name from the daemon; the Forge tab will backfill it",
			"error", err, "project_id", projectID, "daemon_id", daemonID)
		return ""
	}

	var resp struct {
		HasForge         bool   `json:"has_forge"`
		ForgeProjectName string `json:"forge_project_name"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return ""
	}
	name := strings.TrimSpace(resp.ForgeProjectName)
	if !resp.HasForge || name == "" {
		// Not a forge project, or a forge.yaml that names nothing. Marking
		// it would set is_forge on a plain repo.
		return ""
	}

	if _, err := projects.SetProjectForgeName(ctx, projectID, userID, name); err != nil {
		logging.Warn("could not persist forge's name for a freshly cloned project",
			"error", err, "project_id", projectID, "forge_project_name", name)
		return ""
	}
	logging.Info("learned forge's name for a project from its forge.yaml",
		"project_id", projectID, "daemon_id", daemonID, "forge_project_name", name)
	return name
}
