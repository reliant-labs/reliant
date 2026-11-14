// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	forgecli "github.com/reliant-labs/forge/cli"
)

// ForgeTool is one tool of the Reliant Forge connector, as the consent screen
// shows it. The tools themselves are forge's (forgecli.AddHostedTools): this
// package mounts them, it does not define them.
type ForgeTool struct {
	Name        string
	Title       string
	Description string
	// Mutating tools change state (obs_init, release_record).
	Mutating bool
}

// ForgeTools is the grantable catalog of the Reliant Forge connector, in
// forge's order.
func ForgeTools() []ForgeTool {
	hosted := forgecli.HostedTools()
	out := make([]ForgeTool, 0, len(hosted))
	for _, t := range hosted {
		out = append(out, ForgeTool{Name: t.Name, Title: t.Title, Description: t.Description, Mutating: t.Mutating})
	}
	return out
}

// ForgeToolByName looks a Reliant Forge tool up by name.
func ForgeToolByName(name string) (ForgeTool, bool) {
	for _, t := range ForgeTools() {
		if t.Name == name {
			return t, true
		}
	}
	return ForgeTool{}, false
}
