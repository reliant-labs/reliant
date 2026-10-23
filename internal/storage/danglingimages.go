package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Dangling images: untagged images that no container, running or stopped, was
// created from.
//
// Every rebuild of a tag leaves the image it replaced behind, untagged. On a
// prod cloud workspace on 2026-10-09 that was most of the image store: 10.35 GB
// of images, 8.9 GB reclaimable. Nothing else reclaims them: BuildKit's prune
// covers its own cache, not the image store, and the kubelet only sees the
// images inside k3d's nodes.
//
// This layer runs in the automatic passes too (NonDisruptiveGC). An untagged
// image cannot be selected by name, so no `docker run`, compose file or build
// can be relying on it; the only things that can are a container (checked
// here, and again by the daemon, since removal is never forced) and a child
// image (the daemon refuses to remove a parent). Being wrong costs a rebuild
// of an image whose tag already points at something newer.

// danglingImageMinAge retains a dangling image built in the last hour.
// `docker build` without a tag leaves its result dangling until somebody tags
// or runs it by ID, so the newest dangling image may be one a person is about
// to use. An hour covers that hand-off and is short against the days a
// superseded build otherwise sits.
const danglingImageMinAge = time.Hour

// DanglingImage is one untagged image a pass may remove.
type DanglingImage struct {
	ID string
	// Repository is the repository the image was pushed to, from its digest
	// reference, or "". It is for the person reading the plan; no decision
	// reads it.
	Repository string
	Created    time.Time
	Size       uint64
}

func (img DanglingImage) describe() string {
	where := img.Repository
	if where == "" {
		where = "<none>"
	}
	return fmt.Sprintf("%s (%s, %s, built %s)", img.ID, where, formatBytes(img.Size), img.Created.UTC().Format(time.RFC3339))
}

// danglingPlan is a pass's dangling images: the removable ones, oldest first,
// and how many were retained for each reason.
type danglingPlan struct {
	targets      []DanglingImage
	inUse, young int
}

// imageFacts is the part of `docker image inspect` the plan reads.
type imageFacts struct {
	ID          string   `json:"Id"`
	Created     string   `json:"Created"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"`
	Size        uint64   `json:"Size"`
}

const imageFactsFormat = `{"Id":{{json .Id}},"Created":{{json .Created}},"RepoTags":{{json .RepoTags}},"RepoDigests":{{json .RepoDigests}},"Size":{{.Size}}}`

// planDanglingImages lists every dangling image and keeps as targets only
// those still untagged, used by no container and at least
// danglingImageMinAge old. A failed docker call fails the plan: nothing is
// removed on a guess.
func (r Runner) planDanglingImages(ctx context.Context, now time.Time) (danglingPlan, error) {
	var plan danglingPlan
	b, err := r.docker(ctx, "image", "ls", "--filter", "dangling=true", "--no-trunc", "--quiet")
	if err != nil {
		return plan, err
	}
	ids := uniqueFields(b)
	if len(ids) == 0 {
		return plan, nil
	}
	used, err := r.containerImageUse(ctx)
	if err != nil {
		return plan, err
	}
	facts, err := r.inspectImages(ctx, ids)
	if err != nil {
		return plan, err
	}
	for _, img := range facts {
		created, parseErr := time.Parse(time.RFC3339Nano, img.Created)
		switch {
		case len(img.RepoTags) > 0:
			// Tagged since the listing: not dangling, not counted.
		case used[img.ID]:
			plan.inUse++
		case parseErr != nil || now.Sub(created) < danglingImageMinAge:
			plan.young++
		default:
			plan.targets = append(plan.targets, DanglingImage{ID: img.ID, Repository: digestRepository(img.RepoDigests), Created: created, Size: img.Size})
		}
	}
	sort.SliceStable(plan.targets, func(i, j int) bool { return plan.targets[i].Created.Before(plan.targets[j].Created) })
	return plan, nil
}

func (r Runner) inspectImages(ctx context.Context, ids []string) ([]imageFacts, error) {
	b, err := r.docker(ctx, append([]string{"image", "inspect", "--format", imageFactsFormat}, ids...)...)
	if err != nil {
		return nil, err
	}
	var facts []imageFacts
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var img imageFacts
		if err := json.Unmarshal([]byte(line), &img); err != nil {
			return nil, fmt.Errorf("parse docker image inspect: %w", err)
		}
		facts = append(facts, img)
	}
	return facts, nil
}

// DanglingImages removes every planned dangling image, or only lists them when
// apply is false. Oldest go first, so a pass stopped by its caps or deadline
// has reclaimed the longest-superseded builds.
func (r Runner) DanglingImages(ctx context.Context, apply bool) error {
	d := r.deletes()
	plan, err := r.planDanglingImages(ctx, time.Now())
	if err != nil {
		return dockerLayerErr(d, err)
	}
	var (
		removed int
		freed   uint64
		failed  []string
		stop    error
	)
	for _, img := range plan.targets {
		if !apply {
			r.print("remove dangling image %s\n", img.describe())
			continue
		}
		outcome, err := r.removeDanglingImage(ctx, d, img)
		if stopsPass(err) {
			stop = err
			break
		}
		switch outcome {
		case dockerRemoved:
			removed++
			freed += img.Size
			r.print("removed dangling image %s\n", img.describe())
		case dockerRefused:
			r.print("kept dangling image %s: %v\n", img.ID, err)
		case dockerFailed:
			failed = append(failed, fmt.Sprintf("%s: %v", img.ID, err))
		}
	}
	retained := fmt.Sprintf("retained %d in use by a container, %d built in the last %s", plan.inUse, plan.young, danglingImageMinAge)
	if apply {
		r.print("dangling images: removed %d of %d (up to %s; shared layers free less); %s\n", removed, len(plan.targets), formatBytes(freed), retained)
	} else {
		r.print("dangling images: %d removable; %s\n", len(plan.targets), retained)
	}
	if stop != nil {
		return stop
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not remove %d dangling image(s): %s", len(failed), strings.Join(failed, "; "))
	}
	return nil
}

// removeDanglingImage re-reads the image's tags right before removing it.
// `docker rmi <id>` of an image with exactly one tag untags AND deletes it, so
// an image somebody tagged after the plan was made must be caught here, not by
// the daemon.
func (r Runner) removeDanglingImage(ctx context.Context, d deleter, img DanglingImage) (dockerRemoval, error) {
	if err := d.grantObject(ctx); err != nil {
		return dockerFailed, err
	}
	what := "docker rmi " + img.ID
	b, err := r.docker(ctx, "image", "inspect", "--format", "{{json .RepoTags}}", img.ID)
	if err != nil {
		return classifyDockerRemoval(d, what, err)
	}
	var tags []string
	if err := json.Unmarshal(b, &tags); err != nil {
		return dockerFailed, fmt.Errorf("parse docker image inspect: %w", err)
	}
	if len(tags) > 0 {
		return dockerRefused, fmt.Errorf("tagged %s since the plan", strings.Join(tags, ", "))
	}
	_, err = r.docker(ctx, "rmi", img.ID)
	return classifyDockerRemoval(d, what, err)
}

// digestRepository is the repository of an image's first digest reference.
func digestRepository(digests []string) string {
	for _, ref := range digests {
		if repo, _, ok := strings.Cut(ref, "@"); ok && repo != "" {
			return repo
		}
	}
	return ""
}

// uniqueFields splits docker's one-per-line output into distinct values, in
// order.
func uniqueFields(b []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.Fields(string(b)) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
