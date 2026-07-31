// Copyright (c) 2025 Reliant Labs
package config_test

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestComposePostgresPortIsConsistent pins every entrypoint that dials the
// repo's OWN docker-compose Postgres to the host port that compose actually
// publishes.
//
// The repo contains two DIFFERENT Postgres servers and they are not drift:
// docker-compose.yml here publishes its own postgres on one host port, while
// the control-plane stack publishes a separate server on another. Commands
// that target control-plane's server (reliant-dev workflow analyze, scripts/wf-supervise)
// are deliberately out of scope — collapsing those onto this port would point
// them at the wrong database.
//
// What makes this guard real is that it never looks for a port LITERAL. The
// expected value is computed from the compose file's own `ports:` mapping (the
// producer of the host port), and each consumer's value is computed by parsing
// its DSN / shell default. Renumber the compose mapping and this test starts
// demanding the NEW port everywhere without being edited. Every step that
// could silently derive nothing fails the test instead.
func TestComposePostgresPortIsConsistent(t *testing.T) {
	root := repoRootFromTest(t)

	want := composePostgresHostPort(t, filepath.Join(root, "docker-compose.yml"))

	// Consumers of the compose-hosted Postgres: the value each one resolves to
	// is extracted from its own syntax, not matched against a literal.
	t.Run("Makefile E2E_DATABASE_URL", func(t *testing.T) {
		body := readRepoFile(t, filepath.Join(root, "Makefile"))
		dsn := captureOne(t, body,
			regexp.MustCompile(`(?m)^E2E_DATABASE_URL\s*\?=\s*(\S+)`),
			"E2E_DATABASE_URL assignment in Makefile")
		assertPortEquals(t, portOfDSN(t, dsn), want, "Makefile E2E_DATABASE_URL")
	})

	t.Run("scripts/dev.sh per-worktree bootstrap default", func(t *testing.T) {
		body := readRepoFile(t, filepath.Join(root, "scripts", "dev.sh"))
		got := captureOne(t, body,
			regexp.MustCompile(`RELIANT_POSTGRES_PORT:-(\d+)`),
			"RELIANT_POSTGRES_PORT default in scripts/dev.sh")
		assertPortEquals(t, mustPort(t, got, "scripts/dev.sh default"), want, "scripts/dev.sh RELIANT_POSTGRES_PORT default")
	})
}

// composePostgresHostPort returns the host side of the postgres service's
// published port, read out of the compose file itself. Every failure mode
// (missing file, missing service, no ports, unparsable mapping) is fatal:
// a guard that silently derived nothing would pass against anything.
func composePostgresHostPort(t *testing.T, composePath string) int {
	t.Helper()

	var doc struct {
		Services map[string]struct {
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, composePath)), &doc); err != nil {
		t.Fatalf("parse %s: %v", composePath, err)
	}

	svc, ok := doc.Services["postgres"]
	if !ok {
		t.Fatalf("%s declares NO `postgres` service — this guard derived its expected "+
			"port from nothing and would pass against any value", composePath)
	}
	if len(svc.Ports) == 0 {
		t.Fatalf("%s: the `postgres` service publishes NO ports, so there is no host "+
			"port to hold the dev entrypoints to", composePath)
	}

	// "HOST:CONTAINER" — the host side is what a client on the machine dials.
	for _, mapping := range svc.Ports {
		host, container, found := strings.Cut(strings.Trim(mapping, `"`), ":")
		if !found {
			continue
		}
		if strings.TrimSpace(container) != "5432" {
			continue // not the Postgres wire port
		}
		return mustPort(t, host, fmt.Sprintf("%s postgres ports entry %q", composePath, mapping))
	}

	t.Fatalf("%s: no `postgres` port mapping publishes container port 5432 (saw %v)",
		composePath, svc.Ports)
	return 0
}

func portOfDSN(t *testing.T, dsn string) int {
	t.Helper()
	// Makefile values may be single-quoted in the assignment.
	parsed, err := url.Parse(strings.Trim(dsn, `'"`))
	if err != nil {
		t.Fatalf("parse DSN %q: %v", dsn, err)
	}
	port := parsed.Port()
	if port == "" {
		t.Fatalf("DSN %q carries no explicit port, so it cannot be compared against the "+
			"compose-published port", dsn)
	}
	return mustPort(t, port, fmt.Sprintf("DSN %q", dsn))
}

func captureOne(t *testing.T, body string, re *regexp.Regexp, what string) string {
	t.Helper()
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("found NO %s — the shape this guard reads has moved, so it is no longer "+
			"checking anything; update the pattern rather than deleting the check", what)
	}
	return m[1]
}

func mustPort(t *testing.T, raw, what string) int {
	t.Helper()
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("%s: %q is not a port number: %v", what, raw, err)
	}
	return port
}

func assertPortEquals(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s uses Postgres port %d, but docker-compose.yml publishes the postgres "+
			"service on host port %d. These target the SAME container, so one of them dials "+
			"a server that is not there.", what, got, want)
	}
}

// repoRootFromTest walks up to the directory holding go.mod so the files this
// guard reads are located rather than assumed.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked to the filesystem root without finding go.mod — this guard " +
				"would have read no files at all")
		}
		dir = parent
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) == 0 {
		t.Fatalf("%s is empty", path)
	}
	return string(data)
}
