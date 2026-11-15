package varsembed

import "embed"

// MigrationsFS is a //go:embed target — the embed package requires the
// var to be at file scope, so the analyzer must NOT flag it.
//
//go:embed migrations
var MigrationsFS embed.FS

// Multi-line directive with explicit pattern is also fine.
//
//go:embed migrations/*
var MigrationGlob embed.FS
