// Copyright (c) 2025 Reliant Labs
package models

import "strings"

// SupportsTemperature reports whether a temperature set on model def and
// served through driverID actually reaches the provider. It is the single
// source of truth for UI gating ("show a temperature control") and mirrors
// what the resolver and drivers do:
//
//   - a model with temperature_mode: omit never gets temperature
//     (drivers/resolver.go drops it);
//   - the anthropic driver (also reached as "claude" / "claude-code" for
//     Claude OAuth) never sends temperature for any model;
//   - copilot wraps the anthropic client for claude-* api models, so it
//     ignores temperature there too, while its OpenAI path honors it.
func SupportsTemperature(def *ModelDefinition, driverID string) bool {
	if def == nil {
		return false
	}
	if def.DriverSettings != nil && def.DriverSettings.TemperatureMode == string(TemperatureModeOmit) {
		return false
	}
	switch driverID {
	case "anthropic", "claude", "claude-code":
		return false
	case "copilot":
		for _, p := range def.Providers {
			if p.Driver == "copilot" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.APIModel)), "claude") {
				return false
			}
		}
	}
	return true
}
