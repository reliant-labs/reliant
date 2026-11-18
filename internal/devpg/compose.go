package devpg

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ComposeFileName is the compose file the scaffold writes at the project root.
const ComposeFileName = "docker-compose.yml"

// composeShape is the sliver of docker-compose.yml this package reads: each
// service's published port strings. Everything else is ignored, so an
// unrelated compose edit cannot break the reconcile.
type composeShape struct {
	Services map[string]struct {
		Ports []string `yaml:"ports"`
	} `yaml:"services"`
}

// DotEnvFileName is the file `docker compose` auto-loads from the project
// directory for its OWN interpolation, in addition to the shell environment.
// Forgetting this file is what made the guard refuse correct projects: a
// developer who writes POSTGRES_PORT=5460 here (rather than exporting it)
// really does get postgres published on 5460.
const DotEnvFileName = ".env"

// ComposeEnvFilesVar is compose's own override for which dotenv files it
// loads. When set it REPLACES the default `.env` (comma-separated, later
// files winning), exactly as `--env-file` does.
const ComposeEnvFilesVar = "COMPOSE_ENV_FILES"

// ComposePublishedPort returns the HOST port the project's compose file
// publishes for its postgres service, with compose's own `${VAR:-default}`
// expansion applied against the same inputs compose reads — i.e. the port
// the container will actually bind when `docker compose up` runs here.
//
// Returns "" when there is no compose file, no postgres service, or no
// parseable published port; every caller treats that as "nothing to
// reconcile against" rather than an error, because a project may legitimately
// run its dev database out of band.
func ComposePublishedPort(projectDir string) string {
	port, _ := ResolveComposePort(projectDir, nil)
	return port
}

// ResolveComposePort resolves the published host port of the project's
// postgres service the way `docker compose` itself would, and reports the
// case where forge CANNOT know.
//
// envFiles are the `--env-file` paths forge will pass to compose for this
// service (see internal/deploytarget/compose.go, which forwards a service's
// declared env_file). Passing --env-file REPLACES the default `.env`, so
// when forge knows it will pass one, that file — not `.env` — is what
// compose interpolates from. Pass nil for the ordinary path.
//
// The second return is a REASON the guard should stand down: non-empty means
// forge could not faithfully reproduce compose's interpolation (an env file
// it cannot read), so the port it computed may be wrong. Refusing on a value
// forge is not sure of is the failure mode this guard must avoid — a guard
// that fires on a correct configuration teaches people to route around it.
// Callers skip the check and say why.
func ResolveComposePort(projectDir string, envFiles []string) (port, unknownReason string) {
	return ResolveComposeServicePort(projectDir, "postgres", envFiles)
}

// ResolveComposeServicePort is ResolveComposePort for any service in the
// project's compose file, not just postgres.
//
// The interpolation rules are the service-independent half of this package —
// compose expands `${VAR:-default}` the same way whichever service the
// mapping belongs to — so a second caller that needs a published host port
// (the dev IdP's, to reach the instance the browser reaches) asks here rather
// than reimplementing the precedence and getting it subtly wrong.
func ResolveComposeServicePort(projectDir, service string, envFiles []string) (port, unknownReason string) {
	data, err := os.ReadFile(filepath.Join(projectDir, ComposeFileName))
	if err != nil {
		return "", ""
	}
	var c composeShape
	if err := yaml.Unmarshal(data, &c); err != nil {
		return "", ""
	}
	svc, ok := c.Services[service]
	if !ok {
		return "", ""
	}

	lookup, unknownReason := composeLookup(projectDir, envFiles)
	if unknownReason != "" {
		return "", unknownReason
	}
	for _, p := range svc.Ports {
		if host := hostPortOf(expandComposeVars(p, lookup)); host != "" {
			return host, ""
		}
	}
	return "", ""
}

// composeLookup builds the variable resolver with docker compose's real
// precedence, verified against Compose v2.34.0:
//
//  1. The SHELL environment wins. `POSTGRES_PORT=5480 docker compose config`
//     publishes 5480 even when `.env` says 5460.
//  2. Then the dotenv files: the `--env-file` list when one is given
//     (later files winning), else $COMPOSE_ENV_FILES, else the project
//     directory's `.env`. An `--env-file` REPLACES `.env` rather than
//     layering over it — with `--env-file` naming a file that does not
//     define POSTGRES_PORT, compose falls to the `${VAR:-5432}` default
//     even though `.env` defines it.
//  3. Then the `${VAR:-default}` written in the compose file.
//
// A value that is present but EMPTY counts as unset at every layer, which is
// how compose's `:-` operator reads it: `POSTGRES_PORT= docker compose
// config` publishes the 5432 default.
//
// A missing DEFAULT `.env` is normal and not an error — compose tolerates
// it. A missing EXPLICIT env file is a case forge cannot resolve, and yields
// an unknownReason rather than a wrong port.
func composeLookup(projectDir string, envFiles []string) (func(string) string, string) {
	if len(envFiles) == 0 {
		if v := strings.TrimSpace(os.Getenv(ComposeEnvFilesVar)); v != "" {
			envFiles = splitEnvFiles(v)
		}
	}
	explicit := len(envFiles) > 0
	if !explicit {
		envFiles = []string{DotEnvFileName}
	}

	// Later files win, so merge in order.
	merged := map[string]string{}
	for _, f := range envFiles {
		path := f
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectDir, path)
		}
		vals, err := parseDotEnv(path)
		if err != nil {
			if !explicit && os.IsNotExist(err) {
				continue // no .env is the common, correct case
			}
			return nil, fmt.Sprintf("its env file %s could not be read (%v)", f, err)
		}
		for k, v := range vals {
			merged[k] = v
		}
	}

	return func(name string) string {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
		return strings.TrimSpace(merged[name])
	}, ""
}

// splitEnvFiles splits a $COMPOSE_ENV_FILES value on commas, dropping empties.
func splitEnvFiles(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseDotEnv reads the KEY=VALUE subset of dotenv syntax compose's own
// interpolation needs: comments, blank lines, an optional `export ` prefix,
// and one layer of matching quotes (compose publishes 5461 for
// `POSTGRES_PORT="5461"`).
func parseDotEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		out[k] = v
	}
	return out, nil
}

// composeVarRE matches compose's variable syntax in the two forms the
// scaffold uses: ${VAR} and ${VAR:-default}.
var composeVarRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandComposeVars applies compose's `${VAR:-default}` interpolation using
// lookup — the same inputs, in the same order, `docker compose` itself
// expands, so the result is the mapping the container will really get.
func expandComposeVars(s string, lookup func(string) string) string {
	return composeVarRE.ReplaceAllStringFunc(s, func(m string) string {
		parts := composeVarRE.FindStringSubmatch(m)
		if v := lookup(parts[1]); v != "" {
			return v
		}
		return parts[2]
	})
}

// hostPortOf pulls the host-side port out of one compose port mapping.
// Handles the short-syntax forms the scaffold emits and their common
// siblings: "5432", "5433:5432", "127.0.0.1:5433:5432", and any of these
// with a "/tcp" protocol suffix.
//
// A mapping with no host side ("5432" alone) publishes an EPHEMERAL host
// port, which is not a coordinate anything can be reconciled against, so it
// yields "".
func hostPortOf(mapping string) string {
	mapping = strings.TrimSpace(mapping)
	if mapping == "" {
		return ""
	}
	if i := strings.Index(mapping, "/"); i >= 0 { // strip /tcp, /udp
		mapping = mapping[:i]
	}
	parts := strings.Split(mapping, ":")
	switch len(parts) {
	case 2: // host:container
		return sanePort(parts[0])
	case 3: // ip:host:container
		return sanePort(parts[1])
	default: // bare container port — ephemeral host side
		return ""
	}
}

// sanePort returns p when it is a plain numeric port that is not compose's
// "0" (ephemeral) sentinel, else "". Port RANGES ("8000-8010") are rejected:
// they name no single coordinate.
func sanePort(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "0" {
		return ""
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return p
}
