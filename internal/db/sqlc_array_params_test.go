// Copyright (c) 2025 Reliant Labs
package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoSqlcSliceInPostgresQueries rejects sqlc.slice() in the Postgres query
// files.
//
// sqlc.slice() is a MySQL/SQLite feature. For the postgresql engine sqlc
// emits `IN ($n)` but the generated Go still rewrites a `/*SLICE:x*/?` marker
// that is not in the string, so the query binds len(ids) arguments against a
// single placeholder: one id works, zero or several fail with "expected 1
// arguments, got N". Every query that used it had grown a hand-built IN clause
// in its store to route around the broken generated function. The Postgres
// form is an array parameter — `= ANY(sqlc.arg('ids')::text[])` — which sqlc
// binds with pq.Array and which handles an empty list without special-casing.
func TestNoSqlcSliceInPostgresQueries(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("postgres", "queries", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "query files not found — did the directory move?")

	sliceCall := regexp.MustCompile(`sqlc\.slice\s*\(`)
	for _, f := range files {
		body, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			if sliceCall.MatchString(line) {
				t.Errorf("%s:%d uses sqlc.slice(), which sqlc does not support for postgresql; "+
					"use `= ANY(sqlc.arg('name')::text[])` instead", f, i+1)
			}
		}
	}
}
