// Package config is reliant's typed process configuration.
//
// It is empty until P4 (research/forge-migration/PLAN.md) declares the
// configuration in proto/config/v1/config.proto, at which point forge
// generates this package and this file is deleted. It exists now only
// because every forge handler package (internal/handlers/<svc>) carries a
// Deps.Config *config.Config and its generated test harness constructs one.
package config

// Config is the typed configuration handed to every forge handler package.
// It has no fields yet: reliant still reads its environment directly.
type Config struct{}
