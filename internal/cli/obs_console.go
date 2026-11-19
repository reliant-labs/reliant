package cli

import (
	"fmt"
	"io"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// obsConsole is the console of the project the obs verbs read: its base from
// FORGE_CONSOLE_URL, forge.yaml monitoring.console_url, else Reliant's
// (cloud.ConsoleBase), and the project from --project or forge.yaml `name`.
// Zero (no links) when there is no project to link to.
func obsConsole() cloud.Console {
	configured := ""
	if _, cfg, err := initConfig(); err == nil && cfg != nil && cfg.Monitoring != nil {
		configured = cfg.Monitoring.ConsoleURL
	}
	return cloud.NewConsole(cloud.ConsoleBase(configured), obsProjectName())
}

// printConsoleLink writes "console: <url>" under a human view when the env's
// telemetry is on a control plane: the local ClickStack has no console page.
func printConsoleLink(w io.Writer, backend, url string) {
	if backend != obsbackend.BackendCloud || url == "" {
		return
	}
	fmt.Fprintf(w, "\nconsole: %s\n", url)
}
