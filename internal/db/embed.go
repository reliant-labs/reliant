// Copyright (c) 2025 Reliant Labs
package db

import "embed"

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var FS embed.FS
