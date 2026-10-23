package deploytarget

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Observe reads each compose service's container state back out of the
// local docker daemon.
//
// # Why `compose ps --format json` and not the state file
//
// The deploy path writes a DeployState after a successful `up`, and
// reading it would be cheap, offline and wrong: it records what forge
// last DID, which stops being true the moment a container exits, is
// stopped by hand, or is recreated by another tool. That is precisely the
// gap this verb exists to close, so the observation asks the daemon.
//
// `--format json` rather than the table `compose ps` the deploy-time
// health check parses. The table's columns shift between compose
// versions and `composeHasRunningLine` only needs a yes/no, but an
// observation needs the running image, the state and the health
// separately — and recovering three fields by splitting a table whose
// column order is not a contract is how a parser silently starts
// reporting the wrong column. The JSON keys are stable.
//
// # Why the digest is usually empty, and why that is correct
//
// Compose reports the image REFERENCE the container was created from,
// which for the overwhelmingly common dev case is a mutable tag. A tag
// cannot answer "which bytes are running", so Digest stays empty unless
// the reference is digest-pinned — the same rule, and the same reasoning,
// as the cluster tier's digestFromImageRef.
//
// # Why no replica counts
//
// A compose service CAN be scaled, and compose reports one row per
// container, so the count is readable. It is deliberately not reported as
// ReplicaCounts: that struct's four fields are a rollout's vocabulary
// (updated-vs-ready is what distinguishes "rolled out" from "rolling
// out"), and compose has no rollout — there is no pod template generation
// to be current or stale with respect to. Filling Desired and Ready while
// leaving Updated and Available at zero would render as a permanently
// half-finished rollout. Container count goes in Detail, where it is
// information rather than a claim about a concept compose does not have.
func (p ComposeProvider) Observe(ctx context.Context, group ServiceGroup) (Observed, error) {
	out := Observed{
		ProviderID: p.Name(),
		ObservedAt: time.Now().UTC(),
		Items:      make([]ObservedItem, 0, len(group.Services)),
	}
	runner := p.runner()
	for _, svc := range group.Services {
		out.Items = append(out.Items, p.observeOne(ctx, runner, svc))
	}
	return out, nil
}

// observeOne reads one compose service. Like the cluster tier, every
// failure path yields an ITEM rather than an error, so one unreadable
// service does not erase its siblings' observations.
func (p ComposeProvider) observeOne(ctx context.Context, runner commandRunner, svc ResolvedService) ObservedItem {
	if svc.Compose == nil {
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthUnknown,
			Detail: "Compose spec is nil (group misrouted?)",
		}
	}
	spec := svc.Compose
	file := composeFile(spec)
	target := composeServiceName(spec, svc.Name)

	// The SAME env overlay the deploy path assembles, for the same
	// reason it uses one: `compose ps` re-reads and re-interpolates the
	// compose file exactly like `compose up` does, so without the
	// overlay a project whose ports or image come from `${VAR}` resolves
	// a DIFFERENT config than the one that was deployed — and then
	// reports on containers belonging to a project name it just made up.
	//
	// Secrets are not in the overlay here: this is a read, and the
	// deploy path's secret layer exists to inject values INTO containers
	// rather than to resolve the file. A missing `${VAR}` yields a
	// compose warning and an empty substitution, which surfaces as an
	// honest unknown rather than a wrong answer.
	envOverlay, ferr := loadEnvFile(composeEnvFile(spec))
	if ferr != nil {
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthUnknown,
			Detail: fmt.Sprintf("env_file: %v", ferr),
		}
	}
	envOverlay = mergeComposeEnv(envOverlay, spec.Env)

	// --all, so a container that EXITED is still reported. Without it
	// compose lists only running containers and a crashed service is
	// indistinguishable from one that was never created — "absent" and
	// "it died" being the two states a caller most needs to tell apart.
	//
	// composeArgs, not a bare `-f file`: a SHARED stack is selected by its
	// pinned project directory, and observing it from a worktree without
	// that would read the worktree's file and resolve a different config.
	args := append(composeArgs(spec), "ps", "--all", "--format", "json", target)
	raw, err := outputWithEnv(ctx, runner, envOverlay, "docker", args...)
	if err != nil {
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthUnknown,
			Detail: fmt.Sprintf("docker compose ps failed: %v", err),
		}
	}

	containers, perr := parseComposePS(raw)
	if perr != nil {
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthUnknown,
			Detail: fmt.Sprintf("could not parse `docker compose ps --format json` output: %v", perr),
		}
	}
	if len(containers) == 0 {
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthAbsent,
			Detail: fmt.Sprintf("no container for compose service %q in %s", target, file),
		}
	}
	return composeItem(svc.Name, target, containers)
}

// composeItem turns the container rows for one service into a verdict.
func composeItem(name, target string, containers []composeContainer) ObservedItem {
	running := 0
	var notes []string
	for _, c := range containers {
		if c.isRunning() {
			running++
		}
		if note := c.note(); note != "" {
			notes = append(notes, note)
		}
	}

	item := ObservedItem{
		Name:   name,
		Digest: digestFromImageRef(containers[0].Image),
	}
	switch {
	case running == 0:
		item.Health = HealthAbsent
		item.Detail = fmt.Sprintf("%d container(s) for %q, none running", len(containers), target)
	case running < len(containers):
		item.Health = HealthDegraded
		item.Detail = fmt.Sprintf("%d/%d container(s) running for %q", running, len(containers), target)
	default:
		item.Health = HealthHealthy
	}

	// A declared healthcheck OVERRIDES a running verdict, and only ever
	// downwards. "The process is up" and "the process is serving" are
	// different claims, and a service that declares a healthcheck has
	// stated which one it wants to be judged by — reporting it healthy
	// while its own probe says unhealthy would be forge overruling the
	// service's own definition of ready. A service with NO healthcheck
	// reports an empty Health field, which is left alone: absent
	// evidence is not evidence of ill health.
	if len(notes) > 0 {
		item.Health = worseHealth(item.Health, HealthDegraded)
		item.Detail = appendDetail(item.Detail, strings.Join(notes, "; "))
	}
	return item
}

// composeContainer is the slice of `docker compose ps --format json` this
// observation needs. Hand-declared, like k8sDeployment, rather than
// taking a dependency on compose's Go types for four fields.
type composeContainer struct {
	Name string `json:"Name"`
	// State is the container's docker state: "running", "exited",
	// "created", "restarting", …
	State string `json:"State"`
	// Health is the declared healthcheck's verdict — "healthy",
	// "unhealthy", "starting" — and EMPTY for a service that declares no
	// healthcheck, which is the common case and not a problem.
	Health string `json:"Health"`
	// Image is the reference the container was created from.
	Image string `json:"Image"`
	// ExitCode is meaningful for a stopped container and is what turns
	// "it is not running" into "it is not running because it exited 1".
	ExitCode int `json:"ExitCode"`
}

func (c composeContainer) isRunning() bool {
	return strings.EqualFold(c.State, "running")
}

// note returns the one thing worth saying about this container, or "".
//
// Order matters: an unhealthy container is reported as unhealthy even
// though its State is "running", because the healthcheck is the more
// specific verdict. A stopped container reports its exit code, which is
// the difference between "it was stopped" and "it crashed".
func (c composeContainer) note() string {
	if strings.EqualFold(c.Health, "unhealthy") {
		return fmt.Sprintf("%s is running but its healthcheck reports unhealthy", c.Name)
	}
	if strings.EqualFold(c.Health, "starting") {
		return fmt.Sprintf("%s healthcheck is still starting", c.Name)
	}
	if !c.isRunning() {
		return fmt.Sprintf("%s is %s (exit code %d)", c.Name, strings.ToLower(c.State), c.ExitCode)
	}
	return ""
}

// parseComposePS decodes `docker compose ps --format json`, which emits
// EITHER a JSON array or newline-delimited JSON objects depending on the
// compose version — v2.21 changed it to NDJSON and back-ported neither
// direction. Both shapes are accepted rather than pinning a minimum
// compose version, because an observation that refuses to read on an
// older docker is a gap the user cannot close from their side.
//
// Empty output is zero containers, not a parse error: `compose ps` for a
// service with nothing created prints nothing at all.
func parseComposePS(raw []byte) ([]composeContainer, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []composeContainer
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}
	var out []composeContainer
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c composeContainer
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
