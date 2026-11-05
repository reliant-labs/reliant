package cluster

// Superseded-Job prune — keeping the last few finished Jobs of each one-shot
// and deleting the rest.
//
// A standalone one-shot Job (a migration, a provisioning step) is named by the
// hash of its spec, `<name>-<hash>`: a Job's spec.template is immutable, so a
// changed spec has to be a NEW object, and an unchanged one a true no-op. The
// price is that every release which changes the spec leaves the previous Job
// behind. They carried `ttlSecondsAfterFinished` as the cleanup, until a
// reconciler made that wrong (the TTL controller deletes the finished Job, the
// next reconcile creates it again: the migration re-runs every period), so the
// TTL is refused for any env a reconciler may apply (pkg/release/fluxsafe.go).
// control-plane-prod had collected ~250 completed Jobs by the time that landed,
// growing by about five per deploy.
//
// A DIRECT apply never deletes what the render stopped producing, so the prune
// lives here, after the apply. A Job is deleted only when ALL hold:
//
//  1. forge made it: `app.kubernetes.io/managed-by=forge` and the stable
//     `forge.dev/job-name` handle. A Job without the handle (a CronJob's child,
//     a Job a chart or a person created) is never even a candidate.
//  2. it is in the namespace this apply targets and, when both name an env, in
//     THIS env (`forge.dev/env`).
//  3. the render this apply just sent still defines a Job of that base name in
//     that namespace. A base the render no longer carries proves nothing (a
//     --target subset, a KCL conditional that went false), so it is left alone.
//  4. it has FINISHED (condition Complete or Failed). A running Job is never
//     touched, however old.
//  5. it is not named by the current render: the release being applied still
//     references it (and a rollback re-applies an older hash that must survive).
//  6. it is not among the newest [KeepFinishedJobs] finished Jobs of its base
//     name, current release included, kept for debugging ("why did the
//     migration of three releases ago do that?").
//
// It runs only on a FULL apply, like the CRD prune, and a failure is a warning,
// never a failed deploy: it is housekeeping.
//
// THE FLUX PATH needs none of this and gets the same result by construction.
// Flux prunes by INVENTORY: an object an earlier revision applied and this one
// no longer renders is deleted. A new hash is a new name, so the previous Job
// leaves the render and is pruned on the next reconcile; nothing stamps a Job
// prune-exempt (internal/bundle/prune_guard.go keeps only data-bearing kinds out
// of the inventory's reach, and TestBuild_HashNamedJobsStayPrunable pins that a
// Job stays inside it). The difference is retention: Flux keeps none past the
// current release, direct apply keeps the last [KeepFinishedJobs]. What Flux
// cannot reach is a Job it never applied, which is exactly the backlog a direct
// apply left; this prune clears that on the next direct apply, and a one-time
// kubectl command clears it for an env that has already moved.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/deploy"
)

// KeepFinishedJobs is how many finished Jobs of one base name an apply leaves
// behind, the current release's included. It is fixed, not a knob: the plan's
// object_removed detail and the deploy skills state this number, so a per-env
// override would have to reach them too, and nothing has needed one.
const KeepFinishedJobs = 3

// liveJob is the part of a batch/v1 Job the prune reads.
type liveJob struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		Labels            map[string]string `json:"labels"`
		CreationTimestamp string            `json:"creationTimestamp"`
		DeletionTimestamp *string           `json:"deletionTimestamp"`
	} `json:"metadata"`
	Status struct {
		Active     int `json:"active"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

func (j liveJob) base() string { return j.Metadata.Labels[deploy.LabelJobName] }

// finished reports whether the Job reached a terminal condition. A Job with a
// pod still running, or no terminal condition yet, is not finished.
func (j liveJob) finished() bool {
	if j.Status.Active > 0 {
		return false
	}
	for _, c := range j.Status.Conditions {
		if (c.Type == "Complete" || c.Type == "Failed") && c.Status == "True" {
			return true
		}
	}
	return false
}

func (j liveJob) created() time.Time {
	t, _ := time.Parse(time.RFC3339, j.Metadata.CreationTimestamp)
	return t
}

// renderedJobs is what the apply's render says about one-shot Jobs: the names
// it carries and, per (namespace, base name), that it defines the base at all.
type renderedJobs struct {
	names map[string]struct{}
	bases map[string]struct{}
}

func jobBaseKey(ns, base string) string { return ns + "/" + base }

// renderedOneShotJobs reads the forge-rendered Jobs out of a manifest stream. A
// Job with no `forge.dev/job-name` label is not a hash-named forge Job (a raw
// additional manifest) and takes no part in the prune. A document naming no
// namespace is in defaultNS, the namespace the apply passes with `-n`.
func renderedOneShotJobs(manifests, defaultNS string) renderedJobs {
	out := renderedJobs{names: map[string]struct{}{}, bases: map[string]struct{}{}}
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok || m.Kind != "Job" || m.Metadata.Name == "" {
			continue
		}
		base := m.Metadata.Labels[deploy.LabelJobName]
		if base == "" {
			continue
		}
		ns := m.Metadata.Namespace
		if ns == "" {
			ns = defaultNS
		}
		out.names[jobBaseKey(ns, m.Metadata.Name)] = struct{}{}
		out.bases[jobBaseKey(ns, base)] = struct{}{}
	}
	return out
}

// supersededJobs picks, from the Jobs live in namespace ns, the ones to delete.
// Pure: every guard in the file comment is a line here, so a test can pin each.
// env may be empty (no env guard).
func supersededJobs(live []liveJob, rendered renderedJobs, ns, env string) []string {
	byBase := map[string][]liveJob{}
	for _, j := range live {
		if j.Kind != "" && j.Kind != "Job" {
			continue
		}
		if j.Metadata.Labels[AppManagedByLabel] != "forge" || j.base() == "" {
			continue
		}
		if j.Metadata.Namespace != ns {
			continue
		}
		if e := j.Metadata.Labels[deploy.LabelEnv]; env != "" && e != "" && e != env {
			continue
		}
		if _, defined := rendered.bases[jobBaseKey(ns, j.base())]; !defined {
			continue
		}
		byBase[j.base()] = append(byBase[j.base()], j)
	}

	var doomed []string
	for _, jobs := range byBase {
		var finished []liveJob
		for _, j := range jobs {
			if j.finished() {
				finished = append(finished, j)
			}
		}
		// Newest first; the name breaks a timestamp tie so the choice is stable.
		sort.Slice(finished, func(a, b int) bool {
			if ta, tb := finished[a].created(), finished[b].created(); !ta.Equal(tb) {
				return ta.After(tb)
			}
			return finished[a].Metadata.Name > finished[b].Metadata.Name
		})
		for i, j := range finished {
			if i < KeepFinishedJobs || j.Metadata.DeletionTimestamp != nil {
				continue
			}
			if _, current := rendered.names[jobBaseKey(ns, j.Metadata.Name)]; current {
				continue
			}
			doomed = append(doomed, j.Metadata.Name)
		}
	}
	sort.Strings(doomed)
	return doomed
}

// PruneSupersededJobs deletes the finished, superseded Jobs described in the
// file comment, keeping the newest [KeepFinishedJobs] finished Jobs of each base
// name. It returns the names it deleted.
func PruneSupersededJobs(ctx context.Context, kctx, manifests, namespace, env string) ([]string, error) {
	if strings.TrimSpace(namespace) == "" {
		return nil, nil
	}
	rendered := renderedOneShotJobs(manifests, namespace)
	if len(rendered.bases) == 0 {
		return nil, nil
	}
	out, err := kubectlCmd(ctx, kctx, "get", "jobs", "-n", namespace,
		"-l", AppManagedByLabel+"=forge", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	var list struct {
		Items []liveJob `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("parse jobs: %w", err)
	}
	doomed := supersededJobs(list.Items, rendered, namespace, env)
	if len(doomed) == 0 {
		return nil, nil
	}
	fmt.Printf("Pruning %d superseded finished Job(s) in %s (keeping the newest %d of each): %s\n",
		len(doomed), namespace, KeepFinishedJobs, summarizeNames(doomed, 8))
	var deleted []string
	var firstErr error
	for _, name := range doomed {
		del := kubectlCmd(ctx, kctx, "delete", "job", name, "-n", namespace,
			"--ignore-not-found=true", "--cascade=background")
		del.Stdout, del.Stderr = os.Stdout, os.Stderr
		if err := del.Run(); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("delete job %s: %w", name, err)
			}
			continue
		}
		deleted = append(deleted, name)
	}
	return deleted, firstErr
}

// summarizeNames lists up to max names and counts the rest.
func summarizeNames(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(", … and %d more", len(names)-max)
}
