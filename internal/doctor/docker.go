package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// composeService represents one service from `docker compose ps --format json`.
type composeService struct {
	Name       string             `json:"Name"`
	Service    string             `json:"Service"`
	State      string             `json:"State"`
	Health     string             `json:"Health"`
	Status     string             `json:"Status"`
	Publishers []composePublisher `json:"Publishers"`
}

type composePublisher struct {
	PublishedPort int    `json:"PublishedPort"`
	TargetPort    int    `json:"TargetPort"`
	URL           string `json:"URL"`
}

// portQuery defines a service/port pair we want to discover.
type portQuery struct {
	service string
	port    int
}

var defaultPortQueries = []portQuery{
	{"app", 8080},
	{"app", 6060},
	{"postgres", 5432},
	{"app-debug", 2345},
	{"app-debug", 8080},
}

// composeFileNames are the filenames `docker compose` picks up by default.
// Their ABSENCE is the discriminator between "this project declares no
// compose stack" (not applicable) and "the compose stack is broken".
var composeFileNames = []string{
	"compose.yaml", "compose.yml",
	"docker-compose.yaml", "docker-compose.yml",
}

// hasComposeFile reports whether projectDir declares a compose stack.
func hasComposeFile(projectDir string) bool {
	for _, name := range composeFileNames {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err == nil {
			return true
		}
	}
	return false
}

// CheckDocker verifies that the project's docker-compose infra (postgres,
// nats, the local ClickStack container, …) is running, and discovers
// published ports for use by downstream checks.
//
// It covers ONLY the compose half of a dev stack. The host-service half —
// what `forge env up` launches — is `forge env status <env>`'s own table, which
// reports holder pid, forge-ownership and build freshness; a compose probe
// can see none of that, and doctor's old attempt to answer both was how
// `forge doctor` came to FAIL on a completely healthy host-mode stack.
func CheckDocker(ctx context.Context, env *Environment) CheckResult {
	// A project that declares no compose file has no compose stack by
	// design (--kind cli, or a host-only dev loop). That is NOT APPLICABLE,
	// and it is the state doctor used to report as a hard failure.
	if !hasComposeFile(env.ProjectDir) {
		return CheckResult{
			Status:   StatusSkip,
			Message:  "no compose file — this project declares no compose infra",
			Evidence: fmt.Sprintf("looked for %s in %s", strings.Join(composeFileNames, ", "), env.ProjectDir),
		}
	}

	// Run docker compose ps --format json in the project directory.
	out, err := env.compose(ctx, "ps", "--format", "json")
	if err != nil {
		return CheckResult{
			Status:  StatusFail,
			Message: "Docker Compose is not running",
			Evidence: fmt.Sprintf(
				"Failed to run 'docker compose ps' in %s: %v\nHint: is Docker running? %s",
				env.ProjectDir, err, composeStartHint(env.Env),
			),
		}
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		// The file declares services and none are up. Nothing is broken —
		// the user has not started them — so this is a state, not a fault,
		// and the message says how to change it.
		return CheckResult{
			Status:   StatusSkip,
			Message:  "compose infra declared but not running — " + composeStartHint(env.Env),
			Evidence: fmt.Sprintf("'docker compose ps' returned empty output in %s", env.ProjectDir),
		}
	}

	// Parse one JSON object per line.
	var services []composeService
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var svc composeService
		if err := json.Unmarshal([]byte(line), &svc); err != nil {
			continue
		}
		services = append(services, svc)
	}

	if len(services) == 0 {
		// `docker compose ps` answered, but nothing in the answer parsed.
		// forge cannot tell whether the infra is healthy — that is
		// UNDETERMINED, not a failure of the project.
		return CheckResult{
			Status:   StatusUnknown,
			Message:  "could not parse any service from `docker compose ps`",
			Evidence: output,
		}
	}

	// Tally service states.
	var healthy, unhealthy, running, total int
	var evidence strings.Builder
	for _, svc := range services {
		total++
		state := strings.ToLower(svc.State)
		health := strings.ToLower(svc.Health)

		switch {
		case health == "healthy":
			healthy++
		case health == "unhealthy":
			unhealthy++
		case state == "running":
			running++
		}

		fmt.Fprintf(&evidence, "  %-20s state=%-10s health=%-10s %s\n",
			svc.Service, svc.State, svc.Health, svc.Status)
	}

	// Discover ports.
	for _, pq := range defaultPortQueries {
		addr := discoverPort(ctx, env, pq.service, pq.port)
		if addr != "" {
			env.SetPort(pq.service, pq.port, addr)
		}
	}

	// Determine overall status.
	status := StatusPass
	var msg string

	switch {
	case unhealthy > 0:
		status = StatusWarn
		msg = fmt.Sprintf("%d/%d services running (%d unhealthy)", healthy+running, total, unhealthy)
	default:
		msg = fmt.Sprintf("%d/%d services healthy/running", healthy+running, total)
	}

	return CheckResult{
		Status:   status,
		Message:  msg,
		Evidence: strings.TrimRight(evidence.String(), "\n"),
	}
}

// discoverPort runs `docker compose port <service> <port>` and returns the
// host address (e.g. "0.0.0.0:55010") or "" if the service/port is not available.
func discoverPort(ctx context.Context, env *Environment, service string, port int) string {
	out, err := env.compose(ctx, "port", service, fmt.Sprintf("%d", port))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// composeStartHint says how compose services come up in a forge project:
// through the environment that binds them, never a bare `docker compose up`.
// That bypass started EVERY service in the file — including ones the env
// deliberately leaves off, like the ~1 GB observability stack — with none of
// the env-declared values (`forge.OnCompose.env`: ports, the OTLP port) the
// file interpolates, so what it brought up was not what the env declares.
func composeStartHint(env string) string {
	if env == "" {
		env = "<env>"
	}
	return fmt.Sprintf("`forge env up %s` starts the compose services that environment binds (forge.OnCompose)", env)
}
