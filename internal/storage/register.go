package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Register is the MANUAL escape hatch (`forge storage register`): it DISCOVERS
// the registry facts from live k3d/docker metadata instead of being told them,
// then applies them through exactly the same merge rule Converge uses
// (upsertFacts) — one definition of "upsert, never remove, complete-or-nothing"
// rather than two that can drift.
//
// Prefer Converge from inside forge's own commands: they already know these
// facts declaratively, and discovery requires a reachable Docker daemon.
// Register exists for a registry forge did not create, or to repair a policy
// by hand. It never makes an arbitrary remote registry a cleanup target.
func Register(ctx context.Context, path string, contexts, repositories, pins []string) error {
	return WithLock(path, func() error {
		p, err := Load(path)
		if err != nil {
			return err
		}
		r := Runner{Policy: p}
		if err := r.bindLocalContext(ctx); err != nil {
			return err
		}
		p = r.Policy
		// Contexts and pins first, so the registry upsert below sees the
		// complete local-context set (its protected set is the union of all
		// of them — see upsertRegistry).
		p, _ = upsertFacts(p, Facts{Contexts: contexts, Pins: pins})
		discovered, err := r.discoverLocalRegistries(ctx)
		if err != nil {
			return err
		}
		for _, f := range discovered {
			f.Repositories = repositories
			p, _ = upsertFacts(p, f)
		}
		if err := Save(path, p); err != nil {
			return fmt.Errorf("register local storage ownership: %w", err)
		}
		return nil
	})
}

// discoverLocalRegistries reads every k3d-labelled registry container and
// derives its host aliases from its published port. Returns one Facts per
// registry, carrying container + aliases only — the caller supplies the
// repositories that attribute an image to it.
func (r Runner) discoverLocalRegistries(ctx context.Context) ([]Facts, error) {
	b, err := r.docker(ctx, "ps", "-aq", "--filter", "label=k3d.role=registry")
	if err != nil {
		return nil, err
	}
	var out []Facts
	for _, id := range strings.Fields(string(b)) {
		data, err := r.docker(ctx, "inspect", id)
		if err != nil {
			return nil, err
		}
		var containers []struct {
			Name            string
			NetworkSettings struct {
				Ports map[string][]struct{ HostPort string }
			}
		}
		if err := json.Unmarshal(data, &containers); err != nil {
			return nil, err
		}
		for _, c := range containers {
			name := strings.TrimPrefix(c.Name, "/")
			ports := c.NetworkSettings.Ports["5000/tcp"]
			if len(ports) == 0 {
				continue
			}
			out = append(out, Facts{Registry: name, Aliases: RegistryAliases(name, ports[0].HostPort)})
		}
	}
	return out, nil
}

// RegistryAliases is every host name one local registry answers to: the host
// ports a developer pushes to, the in-network `<container>:5000` that pods
// pull by, and the `registry.localhost` names forge's containerd mirror config
// templates. Retention attributes a repository to a registry by matching this
// set, so a name missing here means images pushed under it look unowned.
func RegistryAliases(container, hostPort string) []string {
	aliases := []string{container + ":5000", "registry.localhost:5000"}
	if hostPort != "" {
		aliases = append(aliases,
			"localhost:"+hostPort,
			"127.0.0.1:"+hostPort,
			strings.TrimPrefix(container, "k3d-")+".localhost:"+hostPort,
			"registry.localhost:"+hostPort,
		)
	}
	return aliases
}
