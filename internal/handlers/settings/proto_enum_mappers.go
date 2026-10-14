package settings

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// configSeverityFromString maps a validation.Severity to its proto enum.
func configSeverityFromString(s string) reliantv1.ConfigSeverity {
	switch s {
	case "error":
		return reliantv1.ConfigSeverity_CONFIG_SEVERITY_ERROR
	case "warning":
		return reliantv1.ConfigSeverity_CONFIG_SEVERITY_WARNING
	default:
		return reliantv1.ConfigSeverity_CONFIG_SEVERITY_UNSPECIFIED
	}
}
