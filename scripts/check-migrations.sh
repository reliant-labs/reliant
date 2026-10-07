#!/bin/sh
# Validate the Postgres migrations: YYYYMMDDHHMMSS_<name>.sql filenames, real
# timestamps, unique versions, and no `-- +goose Down` sections.
#
# The rules live in Go (internal/db/migrationcheck), shared with the
# internal/db tests, so this wrapper is plain POSIX sh and runs anywhere Go
# does — including macOS's bash 3.2. Extra arguments go to the checker, e.g.
#   scripts/check-migrations.sh -dir /path/to/migrations
# (a relative -dir is taken from the repository root, where this runs).
#
# GOWORK=off: the checker is part of this module alone, and a go.work in a
# parent directory that does not list this checkout would refuse to build it.

set -eu

cd "$(dirname "$0")/.."
GOWORK=off exec go run ./cmd/check-migrations "$@"
