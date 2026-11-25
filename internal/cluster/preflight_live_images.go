package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// RunningImageLister reports the images a cluster's containers are RUNNING
// right now, keyed by normalizeImageRef, each mapped to one "namespace/pod"
// that runs it (for the message).
//
// Only containers in the running state count. A pod stuck in
// ImagePullBackOff names the image in its spec too, and counting it would turn
// "the image is missing" into "the cluster is running it" — the exact lie this
// exists to avoid.
type RunningImageLister interface {
	RunningImages(ctx context.Context, kctx string) (map[string]string, error)
}

// KubectlRunningImages is the live RunningImageLister: one
// `kubectl get pods --all-namespaces -o json` against the context.
type KubectlRunningImages struct{}

// RunningImages lists the images running containers in kctx use.
func (KubectlRunningImages) RunningImages(ctx context.Context, kctx string) (map[string]string, error) {
	out, err := kubectlCmd(ctx, kctx, "get", "pods", "--all-namespaces", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl get pods: %w", err)
	}
	return runningImagesFromPodList(out)
}

// runningImagesFromPodList reads a PodList and indexes, for every container in
// the running state, both the image its spec names and the image its status
// reports (runtimes normalize the latter, e.g. docker.io/library/…).
func runningImagesFromPodList(raw []byte) (map[string]string, error) {
	type container struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	}
	type status struct {
		Name    string `json:"name"`
		Image   string `json:"image"`
		ImageID string `json:"imageID"`
		State   struct {
			Running *struct{} `json:"running"`
		} `json:"state"`
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Containers     []container `json:"containers"`
				InitContainers []container `json:"initContainers"`
			} `json:"spec"`
			Status struct {
				ContainerStatuses     []status `json:"containerStatuses"`
				InitContainerStatuses []status `json:"initContainerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse pod list: %w", err)
	}
	out := map[string]string{}
	add := func(image, where string) {
		if image = strings.TrimSpace(image); image == "" {
			return
		}
		key := normalizeImageRef(image)
		if _, seen := out[key]; !seen {
			out[key] = where
		}
	}
	for _, pod := range list.Items {
		where := pod.Metadata.Namespace + "/" + pod.Metadata.Name
		specImage := map[string]string{}
		for _, c := range append(pod.Spec.Containers, pod.Spec.InitContainers...) {
			specImage[c.Name] = c.Image
		}
		for _, st := range append(pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses...) {
			if st.State.Running == nil {
				continue
			}
			add(specImage[st.Name], where)
			add(st.Image, where)
			// A digest the runtime resolved: "<repo>@sha256:…". Indexed so a
			// digest-pinned ref matches whatever tag the spec spelled.
			if _, digest, ok := strings.Cut(st.ImageID, "@"); ok {
				add("@"+digest, where)
			}
		}
	}
	return out, nil
}

// normalizeImageRef spells an image ref the one way a registry resolves it:
// an explicit registry (docker.io when none is named), library/ for a
// single-component Docker Hub name, and :latest when neither a tag nor a
// digest is given. "nats:2.10" and "docker.io/library/nats:2.10" are one image.
// A bare "@sha256:…" (a runtime-resolved digest) is returned unchanged.
func normalizeImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "@") {
		return ref
	}
	host, repo, tag, digest := splitImageRef(ref)
	out := host + "/" + repo
	if tag != "" {
		out += ":" + tag
	}
	if digest != "" {
		out += "@" + digest
	}
	if tag == "" && digest == "" {
		out += ":latest"
	}
	return out
}

// splitImageRef splits an image ref into its registry host, repository, tag
// and digest, applying Docker Hub's defaults (docker.io, library/).
func splitImageRef(ref string) (host, repository, tag, digest string) {
	name := ref
	if before, after, ok := strings.Cut(name, "@"); ok {
		name, digest = before, after
	}
	// A tag is a ':' after the last '/', so a registry port is not one.
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	}
	first, rest, hasSlash := strings.Cut(name, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, repository = first, rest
	} else {
		host, repository = dockerHub, name
	}
	if host == "index.docker.io" || host == "registry-1.docker.io" {
		host = dockerHub
	}
	if host == dockerHub && !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}
	return host, repository, tag, digest
}

// dockerHub is the registry an image ref names when it names none.
const dockerHub = "docker.io"

// liveImages answers "is the live target running this image?" for one
// preflight run. The cluster is asked at most once, and only when an image is
// about to block — the happy path never pays for the listing.
type liveImages struct {
	lister RunningImageLister
	kctx   string
	ctx    context.Context

	once   sync.Once
	images map[string]string
}

func newLiveImages(ctx context.Context, opts PreflightOpts) *liveImages {
	if opts.RunningImages == nil || strings.TrimSpace(opts.Context) == "" {
		return nil
	}
	return &liveImages{lister: opts.RunningImages, kctx: opts.Context, ctx: ctx}
}

// running reports where the live target runs ref, if it does. A listing that
// fails is no evidence either way: the verdict then stands as the registry gave
// it.
func (l *liveImages) running(ref string) (string, bool) {
	if l == nil {
		return "", false
	}
	l.once.Do(func() {
		images, err := l.lister.RunningImages(l.ctx, l.kctx)
		if err == nil {
			l.images = images
		}
	})
	if where, ok := l.images[normalizeImageRef(ref)]; ok {
		return where, true
	}
	if _, digest, ok := strings.Cut(ref, "@"); ok {
		if where, ok := l.images["@"+digest]; ok {
			return where, true
		}
	}
	return "", false
}
