package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// A host image tagged for a local registry that no longer exists — a k3d
// registry deleted with its cluster, or one recreated on another port — can
// never be pushed to or pulled from again, yet each copy of a base image keeps
// its full size on the host. Nothing else reclaims them: BuildKit and the
// kubelet only know their own caches.

var (
	localRegistryHost = regexp.MustCompile(`^(?:localhost|registry\.localhost|k3d-[A-Za-z0-9._-]+):([0-9]+)$`)
)

// StaleLocalImage is one host image reference to be untagged.
type StaleLocalImage struct {
	Ref string
	ID  string
}

type hostImageRow struct {
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	ID         string `json:"ID"`
}

// StaleLocalImages lists host image tags whose repository host is a local
// registry alias (`localhost:<port>`, `registry.localhost:<port>`,
// `k3d-<name>:<port>`) that no running container backs, and whose image no
// container, running or stopped, uses.
//
// An alias is dead only when NO container, running or stopped, backs it: a
// `localhost` or `registry.localhost` alias is backed by any container whose
// port bindings include that host port, a `k3d-<name>` alias by any container
// of that name, and a registry registered in the policy by its container name.
// A stopped registry (the full pass stops it for garbage-collect; Docker
// Desktop restarts leave k3d registries stopped) therefore keeps its images. Anything unparseable is kept; a failed docker
// call fails the layer rather than guessing.
func (r Runner) StaleLocalImages(ctx context.Context) ([]StaleLocalImage, error) {
	livePorts, liveNames, err := r.backedLocalRegistries(ctx)
	if err != nil {
		return nil, err
	}
	// A registry registered in the policy was not pruned as dead, so it counts
	// as live by container name whatever its state.
	liveAliases := map[string]bool{}
	for _, reg := range r.Policy.Registries {
		liveNames[reg.Container] = true
		for _, alias := range reg.Aliases {
			liveAliases[alias] = true
		}
	}

	used, err := r.containerImageUse(ctx)
	if err != nil {
		return nil, err
	}
	b, err := r.docker(ctx, "image", "ls", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var stale []StaleLocalImage
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var row hostImageRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("parse docker image ls: %w", err)
		}
		host, _, ok := strings.Cut(row.Repository, "/")
		m := localRegistryHost.FindStringSubmatch(host)
		if !ok || m == nil || row.Tag == "" || row.Tag == "<none>" {
			continue
		}
		if liveAliases[host] {
			continue
		}
		if strings.HasPrefix(host, "k3d-") {
			name, _, _ := strings.Cut(host, ":")
			if liveNames[name] {
				continue
			}
		} else if livePorts[m[1]] {
			continue
		}
		ref := row.Repository + ":" + row.Tag
		if used[row.ID] || used[ref] {
			continue
		}
		stale = append(stale, StaleLocalImage{Ref: ref, ID: row.ID})
	}
	return stale, nil
}

// containerImageUse is every image ID and image reference any container,
// running or stopped, was created from.
func (r Runner) containerImageUse(ctx context.Context) (map[string]bool, error) {
	used := map[string]bool{}
	b, err := r.docker(ctx, "ps", "-aq")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(b))
	if len(ids) == 0 {
		return used, nil
	}
	chunks, err := r.inspectContainers(ctx, ids, "--format", "{{.Image}} {{.Config.Image}}")
	if err != nil {
		return nil, err
	}
	for _, b := range chunks {
		for _, f := range strings.Fields(string(b)) {
			used[f] = true
		}
	}
	return used, nil
}

// LocalImages untags every StaleLocalImage, or only lists them when apply is
// false. `docker rmi <ref>` removes the layers once the last tag is gone, and
// fails rather than force-removing an image a container has since started
// using, which is reported and does not stop the rest.
func (r Runner) LocalImages(ctx context.Context, apply bool) error {
	if err := r.Local(ctx); err != nil {
		return err
	}
	stale, err := r.StaleLocalImages(ctx)
	if err != nil {
		return err
	}
	var failed []string
	for _, img := range stale {
		if !apply {
			r.print("untag stale local-registry image %s\n", img.Ref)
			continue
		}
		if _, err := r.docker(ctx, "rmi", img.Ref); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", img.Ref, err))
			continue
		}
		r.print("untagged stale local-registry image %s\n", img.Ref)
	}
	r.print("local-registry images: %d for a registry that no longer exists\n", len(stale))
	if len(failed) > 0 {
		return fmt.Errorf("could not untag %d image(s): %s", len(failed), strings.Join(failed, "; "))
	}
	return nil
}

// backedLocalRegistries returns the host ports and names of every container,
// running or stopped. Ports come from HostConfig.PortBindings, because a
// stopped container reports none in `docker ps`.
func (r Runner) backedLocalRegistries(ctx context.Context) (ports, names map[string]bool, err error) {
	ports, names = map[string]bool{}, map[string]bool{}
	b, err := r.docker(ctx, "ps", "-aq")
	if err != nil {
		return nil, nil, err
	}
	ids := strings.Fields(string(b))
	if len(ids) == 0 {
		return ports, names, nil
	}
	chunks, err := r.inspectContainers(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	containers, err := decodeInspected[struct {
		Name       string `json:"Name"`
		HostConfig struct {
			PortBindings map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"HostConfig"`
	}](chunks)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range containers {
		names[strings.TrimPrefix(c.Name, "/")] = true
		for _, bindings := range c.HostConfig.PortBindings {
			for _, binding := range bindings {
				if binding.HostPort != "" {
					ports[binding.HostPort] = true
				}
			}
		}
	}
	return ports, names, nil
}
