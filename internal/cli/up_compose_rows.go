package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"

	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/envutil"
)

// composePublisher is one published port of a running compose service, as
// `docker compose ps --format json` reports it.
type composePublisher struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}

// composePublishersFunc returns what one compose service publishes to this
// machine. A seam so the row assembly is testable without docker.
type composePublishersFunc func(ctx context.Context, projectDir string, spec *deploytarget.ComposeSpec, service string) ([]composePublisher, error)

// composeRows lists, for the env-up summary, every port this env's compose
// workloads publish to the host — above all the HyperDX UI and OTLP ports of
// the local ClickStack. Published ports can be dynamic, so nothing on screen
// otherwise says where a compose service is.
//
// Read from docker at print time, because a dynamic port exists only once
// the container does. Best-effort like the rest of the box: a service docker
// cannot describe contributes no row rather than failing the summary.
func composeRows(ctx context.Context, e *KCLEntities, projectDir, env string, targets []string, publishers composePublishersFunc) []upServiceRow {
	if e == nil || publishers == nil {
		return nil
	}
	var rows []upServiceRow
	for _, w := range e.WorkloadsOn(RuntimeCompose) {
		if !inTargetSet(targets, w.Name) || w.Runtime.Compose == nil {
			continue
		}
		service := w.Runtime.Compose.Service
		if service == "" {
			service = w.Name
		}
		pubs, err := publishers(ctx, projectDir, composeSpecFor(projectDir, env, w.Runtime.Compose), service)
		if err != nil {
			continue
		}
		sort.Slice(pubs, func(i, j int) bool { return pubs[i].TargetPort < pubs[j].TargetPort })
		seen := map[int]bool{}
		for _, p := range pubs {
			// IPv4 and IPv6 bindings of one mapping are reported separately.
			if p.PublishedPort <= 0 || seen[p.PublishedPort] || (p.Protocol != "" && p.Protocol != "tcp") {
				continue
			}
			seen[p.PublishedPort] = true
			host := "localhost"
			if strings.HasPrefix(p.URL, "127.0.0.1") {
				host = "127.0.0.1"
			}
			label := fmt.Sprintf("%s :%d", w.Name, p.TargetPort)
			if l, ok := clickstackRowLabel(service, p.TargetPort); ok {
				label = l
			}
			rows = append(rows, upServiceRow{
				Name: label,
				Kind: "compose",
				Port: p.PublishedPort,
				URL:  fmt.Sprintf("http://%s:%d", host, p.PublishedPort),
				Log:  "docker compose logs " + service,
			})
		}
	}
	return rows
}

// composePublishersFn is the publishers source the env-up summary and
// `forge env status` read. A seam so the package's tests never ask the host's
// docker; TestMain installs the answer a host with no daemon gives.
var composePublishersFn composePublishersFunc = dockerComposePublishers

// dockerComposePublishers asks docker what a running compose service
// publishes. It never starts anything.
func dockerComposePublishers(ctx context.Context, projectDir string, spec *deploytarget.ComposeSpec, service string) ([]composePublisher, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	file := spec.ComposeFile
	if file == "" {
		file = "docker-compose.yml"
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(spec.ProjectDirectory, file)
	}
	args := []string{"compose", "--project-directory", spec.ProjectDirectory, "-f", file}
	args = append(args, "ps", "--format", "json", service)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = projectDir
	cmd.Env = envutil.MergeExtraWins(cmd.Environ(), spec.Env)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var pubs []composePublisher
	// One JSON object per line (compose v2.21+), or one JSON array (older).
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		type psEntry struct {
			Service    string             `json:"Service"`
			Publishers []composePublisher `json:"Publishers"`
		}
		var entries []psEntry
		if strings.HasPrefix(line, "[") {
			if err := json.Unmarshal([]byte(line), &entries); err != nil {
				return nil, err
			}
		} else {
			var one psEntry
			if err := json.Unmarshal([]byte(line), &one); err != nil {
				return nil, err
			}
			entries = append(entries, one)
		}
		for _, e := range entries {
			if e.Service == service {
				pubs = append(pubs, e.Publishers...)
			}
		}
	}
	return pubs, nil
}
