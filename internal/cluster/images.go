package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Reading what a cluster is ACTUALLY running.
//
// Every other read in this package serves the apply pipeline — does this
// rollout exist, did it become ready. This one serves verification, and the
// difference in purpose is a difference in trust: a verifier may not consult
// anything forge itself wrote. It asks the API server what image each workload
// carries and reports that, whatever it says.
//
// WHY WORKLOAD SPECS AND NOT PODS. A workload's pod template is what the
// cluster was told to run, and it is what `kubectl apply` sets — so it is the
// thing a deploy changes and therefore the thing drift shows up in. Pod
// `status.containerStatuses[].imageID` is a strictly later fact (what a kubelet
// actually pulled) and is attractive, but it is absent for a CronJob between
// schedules and multi-valued across a rolling update, so it cannot answer the
// question uniformly. Specs can. If a future check wants "declared vs actually
// pulled" as a second axis, it belongs beside this, not instead of it.

// workloadKinds is the set queried for running images.
//
// Deployments are not sufficient and assuming they were would be a silent
// hole: forge's workload expansion (kcl/workloads/expand.k) renders CronJobs
// for `kind = "cron"` and Jobs for `kind = "job"`, both carrying real
// application images. A verifier blind to those reports a clean environment
// while a drifted cron runs old bytes. StatefulSets and DaemonSets are
// included for the same reason — nothing about drift is specific to the
// controller type.
var workloadKinds = []string{"deployments", "statefulsets", "daemonsets", "cronjobs", "jobs"}

// WorkloadImage is one image reference declared by one workload.
//
// Container is carried because a workload may run several (a sidecar, an init
// container), and a drift report that names only the workload leaves the
// reader to find which container moved.
type WorkloadImage struct {
	// Kind is the Kubernetes kind, as the API server reports it
	// ("Deployment", "CronJob").
	Kind string
	// Name is metadata.name.
	Name string
	// Container is the container's name within the pod template. Init
	// containers are included and are not distinguished here — the image is
	// what matters, and forge's `job` expansion puts real application images
	// in init containers.
	Container string
	// Init distinguishes a named init slot from a main/sidecar slot.
	Init bool
	// Image is the reference exactly as declared, unparsed:
	// "ghcr.io/acme/api@sha256:..." or "ghcr.io/acme/api:v1.2.3".
	Image string
	// EnvVar names the environment variable this reference came from, empty
	// when the reference is the container's own `image` field.
	//
	// WHY A SECOND SOURCE EXISTS. Not every image a release ships runs as a
	// workload. An operator that launches pods on demand carries its pod
	// image as a config value — control-plane's workspace-controller pins
	// the workspace base image in DAEMON_IMAGE — so the image is deployed,
	// digest-pinned, and part of the release, while running nowhere at the
	// moment anyone looks. Reading only container images reports that as
	// MISSING, which is a false failure on a correct environment; ignoring
	// it entirely would leave a genuinely drifted operator image invisible.
	// Both are avoided by treating it as what it is: a declared reference
	// from a different field of the same pod spec.
	EnvVar string
}

// podTemplateJSON is the sliver of a pod template this read needs.
type podTemplateJSON struct {
	Spec struct {
		Containers     []containerJSON `json:"containers"`
		InitContainers []containerJSON `json:"initContainers"`
	} `json:"spec"`
}

type containerJSON struct {
	Name  string    `json:"name"`
	Image string    `json:"image"`
	Env   []envJSON `json:"env"`
}

// envJSON is one environment variable. Only the literal Value is read — a
// valueFrom reference (secret, configMap, field) resolves at pod start and is
// not visible in the spec, so there is nothing to compare and it is skipped
// rather than guessed at.
type envJSON struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// workloadItemJSON is one item of the multi-kind `kubectl get` List.
//
// Two template locations, because CronJob nests one extra level: its pod
// template lives under spec.jobTemplate.spec.template while every other kind
// puts it at spec.template. Modelling both here is what keeps CronJob drift
// visible; a jsonpath expression cannot express the alternation, which is the
// reason this parses JSON rather than shelling a jsonpath template.
type workloadItemJSON struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template    *podTemplateJSON `json:"template"`
		JobTemplate *struct {
			Spec struct {
				Template *podTemplateJSON `json:"template"`
			} `json:"spec"`
		} `json:"jobTemplate"`
	} `json:"spec"`
	// Status is read for ONE purpose: telling a Job that is still running
	// from one that has already finished. See finished().
	Status struct {
		Active         int    `json:"active"`
		Succeeded      int    `json:"succeeded"`
		Failed         int    `json:"failed"`
		CompletionTime string `json:"completionTime"`
	} `json:"status"`
}

// finished reports whether this item is a Job that has already run to a
// conclusion, and whose image is therefore HISTORY rather than something the
// namespace is running.
//
// WHY THIS EXISTS. A Job is not replaced on redeploy the way a Deployment is —
// each release creates a new one and the old, completed Jobs remain as a
// record. Reading their images made `forge env status` report a fully and
// correctly deployed production namespace as drifted across five digests,
// because ten finished `control-plane-migrate` / `-idp-provision` Jobs still
// carried the digests of the releases that created them. That drift could
// never clear: the evidence is immutable and grows with every deploy, so the
// command was permanently red on a healthy env.
//
// ONLY JOBS. A CronJob's template is the NEXT run's spec and is always live. A
// Deployment, StatefulSet or DaemonSet has no terminal state to check.
//
// An ACTIVE Job is deliberately still counted: a migration in flight really is
// running, and that is precisely when someone needs to know which build it is.
func (w workloadItemJSON) finished() bool {
	if w.Kind != "Job" {
		return false
	}
	if w.Status.Active > 0 {
		return false
	}
	return w.Status.Succeeded > 0 || w.Status.Failed > 0 || w.Status.CompletionTime != ""
}

// ListWorkloadImages returns every container image declared by every workload
// in a namespace, across all of workloadKinds.
//
// An error means the read did not COMPLETE — kubectl missing, no such context,
// credentials refused, namespace absent. It never means "nothing is running":
// a reachable but empty namespace returns an empty slice and a nil error, and
// keeping those two outcomes distinct is what lets a caller avoid reporting a
// network failure as an environment defect.
//
// kctx is required. An empty context would send the read to whatever context
// happens to be active, which for a command whose entire output is a claim
// about a specific environment would make that claim silently wrong.
func ListWorkloadImages(ctx context.Context, kctx, namespace string) ([]WorkloadImage, error) {
	if strings.TrimSpace(kctx) == "" {
		return nil, fmt.Errorf("no kubectl context given: refusing to read whatever context is currently active")
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("no namespace given")
	}

	cmd := kubectlCmd(ctx, kctx,
		"get", strings.Join(workloadKinds, ","),
		"-n", namespace,
		"-o", "json",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl get workloads in namespace %s (context %s): %w", namespace, kctx, annotateKubectlErr(err))
	}

	return parseWorkloadImages(out)
}

// parseWorkloadImages extracts every image reference from a `kubectl get -o
// json` List.
//
// Split from ListWorkloadImages so the extraction is testable WITHOUT a
// cluster. That is not a stylistic preference: the conditions worth testing —
// a CronJob's doubly-nested pod template, an operator's config-pinned image —
// are properties of the JSON shape, and a test that needed a live cluster
// carrying them would never run in CI.
func parseWorkloadImages(raw []byte) ([]WorkloadImage, error) {
	var list struct {
		Items []workloadItemJSON `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse kubectl output: %w", err)
	}

	var images []WorkloadImage
	for _, item := range list.Items {
		// A completed Job's image is a record of what ran once, not of what
		// this namespace runs now.
		if item.finished() {
			continue
		}
		tmpl := item.Spec.Template
		if tmpl == nil && item.Spec.JobTemplate != nil {
			tmpl = item.Spec.JobTemplate.Spec.Template
		}
		if tmpl == nil {
			continue
		}
		all := append(append([]containerJSON{}, tmpl.Spec.InitContainers...), tmpl.Spec.Containers...)
		for i, c := range all {
			if strings.TrimSpace(c.Image) != "" {
				images = append(images, WorkloadImage{
					Kind:      item.Kind,
					Name:      item.Metadata.Name,
					Container: c.Name,
					Init:      i < len(tmpl.Spec.InitContainers),
					Image:     c.Image,
				})
			}
			for _, e := range c.Env {
				if !looksLikeImageRef(e.Value) {
					continue
				}
				images = append(images, WorkloadImage{
					Kind:      item.Kind,
					Name:      item.Metadata.Name,
					Container: c.Name,
					Init:      i < len(tmpl.Spec.InitContainers),
					Image:     e.Value,
					EnvVar:    e.Name,
				})
			}
		}
	}

	// Stable order so a report reads the same on every run; an unstable
	// report makes a diff between two verify runs unreadable.
	sort.Slice(images, func(i, j int) bool {
		if images[i].Kind != images[j].Kind {
			return images[i].Kind < images[j].Kind
		}
		if images[i].Name != images[j].Name {
			return images[i].Name < images[j].Name
		}
		if images[i].Container != images[j].Container {
			return images[i].Container < images[j].Container
		}
		return images[i].EnvVar < images[j].EnvVar
	})
	return images, nil
}

// looksLikeImageRef reports whether an environment variable's value is a
// container image reference.
//
// The bar is DELIBERATELY HIGH: a digest pin, or a registry-qualified tag.
// Env vars are free-form, and a loose test (anything containing a slash, any
// "name:version" string) would sweep in database URLs, log levels and version
// strings, then report them as MISSING images — turning the command red with
// noise, which is worse than the false MISSING this replaced. Requiring a
// digest or a dotted/ported registry host means a match is an image reference
// essentially by construction.
//
// The cost of the high bar is a bare "postgres:16" in an env var going
// unnoticed. That is the right trade: a release's own images are digest-pinned
// by forge, so the references this must catch always clear the bar.
func looksLikeImageRef(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 512 || strings.ContainsAny(v, " \t\n") {
		return false
	}
	// Anything carrying a digest is an image reference.
	if name, digest, found := strings.Cut(v, "@"); found {
		return strings.HasPrefix(digest, "sha256:") && name != ""
	}
	// Otherwise require a registry-qualified tag: a host with a dot or a
	// port, then a path, then a tag.
	slash := strings.Index(v, "/")
	if slash <= 0 {
		return false
	}
	host := v[:slash]
	if !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return false
	}
	rest := v[slash+1:]
	if strings.Contains(rest, "://") {
		return false
	}
	return strings.LastIndex(rest, ":") > 0
}

// annotateKubectlErr folds kubectl's stderr into the error. Without it an
// *exec.ExitError renders as the useless "exit status 1", discarding the one
// line that says whether this was a missing namespace, an expired credential,
// or an unknown context.
func annotateKubectlErr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if msg := strings.TrimSpace(string(exitErr.Stderr)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}
