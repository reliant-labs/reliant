package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Anonymous volumes: the volumes Docker creates for an image's VOLUME or a
// bare `-v /path` mount, left behind when their container is removed. On a
// prod cloud workspace on 2026-10-09: 4.5 GB in 44 local volumes, 4.2 GB
// reclaimable.
//
// A volume holds data a container wrote, not a rebuildable artifact, so this is
// the most conservative layer forge has. A volume is removed only when ALL of
// these hold:
//
//   - ANONYMOUS, by two independent signals: the `com.docker.volume.anonymous`
//     label the daemon sets on every volume it creates without a name (Docker
//     23+), AND the 64-hex name it generates for one. A named volume — a
//     compose Postgres, a k3d image volume, anything a person named — is never
//     a candidate, and neither is an anonymous volume from a daemon too old to
//     label it.
//   - PLAIN LOCAL: the `local` driver with no driver options. Options make even
//     an anonymous volume a mount of something outside Docker's data root
//     (`--mount type=volume,volume-opt=o=bind,volume-opt=device=/home/…`), and
//     a plugin driver's volume may be a cloud disk. Neither is forge's to remove.
//   - UNUSED: referenced by no container, running or stopped. The daemon's
//     dangling filter says so, every container's mounts are checked again, and
//     removal is never forced, so the daemon refuses one that is in use by then.
//   - UNUSED FOR anonymous_volume_unused (default 14 days), counted from when
//     forge first SAW it unused. Docker records no last-use time, and a
//     volume's creation time says nothing about when its last container went
//     away; the observation record beside the policy (anonymous-volumes.json)
//     does. A volume seen in use again, or gone, leaves the record, so its clock
//     restarts. Every pass that applies records, automatic ones included, so
//     the clock starts at the first pass after the container went away rather
//     than at the first explicit one. A preview records nothing.
//   - EXPLICIT: only `forge storage gc --apply` run by a person or agent
//     (Runner.Explicit) removes one. The automatic passes — `forge env up`'s
//     background pass, the hourly and daily schedule, `forge storage daemon` —
//     only observe. An orphaned anonymous volume is exactly where a compose
//     service with no named volume keeps its data after `docker compose down`:
//     the postgres image declares VOLUME /var/lib/postgresql/data, so a
//     forgotten named volume leaves the database in an anonymous one,
//     recoverable only by mounting it by hash. Nothing a pass can observe tells
//     that from a scratch volume, so deleting it is a decision, and a pass
//     nobody invoked does not take decisions about data (the same rule keeps
//     worktrees out of the automatic passes). The age floor is the recovery
//     window; the explicit run is the consent.

// anonymousVolumeName is the name the daemon generates for an anonymous
// volume.
var anonymousVolumeName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// anonymousVolumeLabel is the label the daemon sets on an anonymous volume.
const anonymousVolumeLabel = "com.docker.volume.anonymous"

// volumeFacts is the part of `docker volume inspect` the plan reads.
type volumeFacts struct {
	Name      string            `json:"Name"`
	Driver    string            `json:"Driver"`
	Scope     string            `json:"Scope"`
	CreatedAt string            `json:"CreatedAt"`
	Labels    map[string]string `json:"Labels"`
	Options   map[string]string `json:"Options"`
}

func (v volumeFacts) anonymous() bool {
	_, labelled := v.Labels[anonymousVolumeLabel]
	return labelled && anonymousVolumeName.MatchString(v.Name)
}

func (v volumeFacts) plainLocal() bool {
	return v.Driver == "local" && v.Scope == "local" && len(v.Options) == 0
}

// AnonymousVolume is one anonymous volume a pass may remove.
type AnonymousVolume struct {
	Name        string
	Created     time.Time
	UnusedSince time.Time
}

func (v AnonymousVolume) describe() string {
	return fmt.Sprintf("%s (unused since %s, created %s)", v.Name, v.UnusedSince.UTC().Format(time.RFC3339), v.Created.UTC().Format(time.RFC3339))
}

// volumePlan is a pass's anonymous volumes: the removable ones, longest unused
// first, how many were retained for each reason, and the observation record
// as this pass leaves it.
type volumePlan struct {
	targets                      []AnonymousVolume
	named, inUse, special, young int
	unused                       map[string]time.Time
}

// anonymousVolumeUnused is the policy's unused window, or 0 when the layer is
// off.
func (p Policy) anonymousVolumeUnused() (time.Duration, error) {
	if p.AnonymousVolumeUnused == anonymousVolumesOff {
		return 0, nil
	}
	d, err := time.ParseDuration(p.AnonymousVolumeUnused)
	if err != nil || d < minAnonymousVolumeUnused {
		return 0, fmt.Errorf("storage: invalid anonymous_volume_unused %q (a duration of at least %s, or %q)", p.AnonymousVolumeUnused, minAnonymousVolumeUnused, anonymousVolumesOff)
	}
	return d, nil
}

// planAnonymousVolumes classifies every dangling volume against the rules
// above. seen is the observation record a previous pass left. A failed docker
// call fails the plan: nothing is removed on a guess.
func (r Runner) planAnonymousVolumes(ctx context.Context, now time.Time, floor time.Duration, seen map[string]time.Time) (volumePlan, error) {
	plan := volumePlan{unused: map[string]time.Time{}}
	b, err := r.docker(ctx, "volume", "ls", "--quiet", "--filter", "dangling=true")
	if err != nil {
		return plan, err
	}
	names := uniqueFields(b)
	if len(names) == 0 {
		return plan, nil
	}
	inUse, err := r.containerVolumeUse(ctx)
	if err != nil {
		return plan, err
	}
	vols, err := r.inspectVolumes(ctx, names)
	if err != nil {
		return plan, err
	}
	for _, v := range vols {
		switch {
		case !v.anonymous():
			plan.named++
			continue
		case inUse[v.Name]:
			plan.inUse++
			continue
		case !v.plainLocal():
			plan.special++
			continue
		}
		since, ok := seen[v.Name]
		if !ok || since.After(now) {
			since = now
		}
		plan.unused[v.Name] = since
		created, parseErr := time.Parse(time.RFC3339Nano, v.CreatedAt)
		if parseErr != nil || now.Sub(created) < floor || now.Sub(since) < floor {
			plan.young++
			continue
		}
		plan.targets = append(plan.targets, AnonymousVolume{Name: v.Name, Created: created, UnusedSince: since})
	}
	sort.SliceStable(plan.targets, func(i, j int) bool { return plan.targets[i].UnusedSince.Before(plan.targets[j].UnusedSince) })
	return plan, nil
}

// containerVolumeUse is the name of every volume any container, running or
// stopped, mounts.
func (r Runner) containerVolumeUse(ctx context.Context) (map[string]bool, error) {
	used := map[string]bool{}
	b, err := r.docker(ctx, "ps", "-aq")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(b))
	if len(ids) == 0 {
		return used, nil
	}
	chunks, err := r.inspectContainers(ctx, ids, "--format", `{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}`)
	if err != nil {
		return nil, err
	}
	for _, b := range chunks {
		for _, name := range strings.Fields(string(b)) {
			used[name] = true
		}
	}
	return used, nil
}

func (r Runner) inspectVolumes(ctx context.Context, names []string) ([]volumeFacts, error) {
	b, err := r.docker(ctx, append([]string{"volume", "inspect"}, names...)...)
	if err != nil {
		return nil, err
	}
	var vols []volumeFacts
	if err := json.Unmarshal(b, &vols); err != nil {
		return nil, fmt.Errorf("parse docker volume inspect: %w", err)
	}
	return vols, nil
}

// AnonymousVolumes previews or applies the anonymous-volume rules. Only an
// explicit pass (Runner.Explicit) that applies removes anything; every other
// applying pass records what it observed and retains.
func (r Runner) AnonymousVolumes(ctx context.Context, apply bool) error {
	floor, err := r.Policy.anonymousVolumeUnused()
	if err != nil {
		return err
	}
	if floor == 0 {
		r.print("anonymous volumes: off (anonymous_volume_unused is %q)\n", anonymousVolumesOff)
		return nil
	}
	d := r.deletes()
	plan, err := r.planAnonymousVolumes(ctx, time.Now(), floor, r.readVolumeRecord())
	if err != nil {
		return dockerLayerErr(d, err)
	}
	var failed []string
	if apply {
		if err := r.writeVolumeRecord(plan.unused); err != nil {
			failed = append(failed, fmt.Sprintf("record unused volumes: %v", err))
		}
	}
	remove := apply && r.Explicit
	var (
		removed int
		stop    error
	)
	for _, v := range plan.targets {
		if !remove {
			if r.Explicit {
				r.print("remove anonymous volume %s\n", v.describe())
			} else {
				r.print("anonymous volume %s: retained, only an explicit `forge storage gc --apply` removes volumes\n", v.describe())
			}
			continue
		}
		outcome, err := r.removeDockerObject(ctx, d, "docker volume rm "+v.Name, "volume", "rm", v.Name)
		if stopsPass(err) {
			stop = err
			break
		}
		switch outcome {
		case dockerRemoved:
			removed++
			r.print("removed anonymous volume %s\n", v.describe())
		case dockerRefused:
			r.print("kept anonymous volume %s: %v\n", v.Name, err)
		case dockerFailed:
			failed = append(failed, fmt.Sprintf("%s: %v", v.Name, err))
		}
	}
	verdict := fmt.Sprintf("%d would be removed", len(plan.targets))
	switch {
	case remove:
		verdict = fmt.Sprintf("removed %d of %d", removed, len(plan.targets))
	case !r.Explicit:
		verdict = fmt.Sprintf("%d eligible, removed only by an explicit `forge storage gc --apply`", len(plan.targets))
	}
	r.print("anonymous volumes: %s (unused for %s or more); retained %d unused for less, %d named (never removed), %d in use by a container, %d with a non-local driver or driver options\n",
		verdict, floor, plan.young, plan.named, plan.inUse, plan.special)
	if stop != nil {
		return stop
	}
	if len(failed) > 0 {
		return fmt.Errorf("anonymous volumes: %s", strings.Join(failed, "; "))
	}
	return nil
}

// AnonymousVolumeRecordPath is where forge records, beside the policy, when it
// first saw each anonymous volume unused.
func AnonymousVolumeRecordPath(policyPath string) string {
	return filepath.Join(filepath.Dir(policyPath), "anonymous-volumes.json")
}

type volumeRecord struct {
	UnusedSince map[string]time.Time `json:"unused_since"`
}

// readVolumeRecord returns the observation record. A runner with no policy
// path, or a record that is missing, unreadable or corrupt, reads as empty:
// every clock restarts, which only ever retains a volume longer.
func (r Runner) readVolumeRecord() map[string]time.Time {
	if r.PolicyPath == "" {
		return nil
	}
	b, err := os.ReadFile(AnonymousVolumeRecordPath(r.PolicyPath))
	if err != nil {
		return nil
	}
	var rec volumeRecord
	if json.Unmarshal(b, &rec) != nil {
		return nil
	}
	return rec.UnusedSince
}

// writeVolumeRecord replaces the observation record with what this pass saw.
// Without a policy path there is nowhere to keep it, so no clock can ever
// reach the floor and nothing is removed.
func (r Runner) writeVolumeRecord(unused map[string]time.Time) error {
	if r.PolicyPath == "" {
		return nil
	}
	if maps.EqualFunc(unused, r.readVolumeRecord(), time.Time.Equal) {
		return nil
	}
	return writeStateFile(r.PolicyPath, AnonymousVolumeRecordPath(r.PolicyPath), volumeRecord{UnusedSince: unused})
}
