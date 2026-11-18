package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/stsresize"
)

// VolumeResizeChecker judges one rendered StatefulSet against the live one on
// the cluster named by kctx. It returns nil when the live volumes can be
// converged to the declared ones (or are already), and a stsresize refusal
// (errors.Is ErrShrink, ErrNotExpandable, ErrTemplateChanged) when they
// cannot. Any other error means the live state could not be read.
type VolumeResizeChecker interface {
	Judge(ctx context.Context, kctx string, desired *appsv1.StatefulSet) error
}

// KubectlVolumeResize is the VolumeResizeChecker over kubectl: it reads the
// live StatefulSet, its claims and the StorageClasses, and asks
// stsresize.Decide, the decision the resize Job's script and the direct-apply
// recovery make, so the three cannot disagree about what is refusable.
type KubectlVolumeResize struct{}

// Judge implements VolumeResizeChecker.
func (KubectlVolumeResize) Judge(ctx context.Context, kctx string, desired *appsv1.StatefulSet) error {
	k := kubectlStatefulSets{kctx: kctx}
	live, err := k.StatefulSet(ctx, desired.Namespace, desired.Name)
	if err != nil || live == nil {
		return err
	}
	claims, err := k.PersistentVolumeClaims(ctx, desired.Namespace)
	if err != nil {
		return err
	}
	classes, err := k.StorageClasses(ctx)
	if err != nil {
		return err
	}
	_, err = stsresize.Decide(desired, live, claims, classes)
	return err
}

func isVolumeRefusal(err error) bool {
	return errors.Is(err, stsresize.ErrShrink) || errors.Is(err, stsresize.ErrNotExpandable) ||
		errors.Is(err, stsresize.ErrTemplateChanged) || errors.Is(err, stsresize.ErrOtherImmutable)
}

// checkVolumeResize judges every rendered StatefulSet that has
// volumeClaimTemplates, on the cluster(s) its document is applied to.
func checkVolumeResize(ctx context.Context, opts PreflightOpts, wg *sync.WaitGroup, sink preflightSink) {
	for _, doc := range splitDocs(opts.Manifests) {
		var sts appsv1.StatefulSet
		if err := sigsyaml.Unmarshal([]byte(doc), &sts); err != nil || sts.Kind != "StatefulSet" || len(sts.Spec.VolumeClaimTemplates) == 0 {
			continue
		}
		if sts.Namespace == "" {
			sts.Namespace = opts.Namespace
		}
		contexts := []string{opts.Context}
		if opts.DocumentContexts != nil {
			contexts = opts.DocumentContexts(doc)
		}
		// A document routed to several clusters is judged on each, and a
		// finding must say which, whatever the rest of the run spans.
		spans := len(contexts) > 1
		for _, kctx := range contexts {
			wg.Add(1)
			go func(kctx string, desired *appsv1.StatefulSet, spans bool) {
				defer wg.Done()
				subject := desired.Namespace + "/" + desired.Name
				if sink.multiCluster || spans {
					subject = kctx + ":" + subject
				}
				err := opts.VolumeResize.Judge(ctx, kctx, desired)
				if err == nil {
					return
				}
				sink.mu.Lock()
				defer sink.mu.Unlock()
				if isVolumeRefusal(err) {
					sink.result.VolumeResizeRefusals = append(sink.result.VolumeResizeRefusals, fmt.Sprintf("%s: %v", subject, err))
					return
				}
				sink.result.VolumeResizeWarnings = append(sink.result.VolumeResizeWarnings,
					fmt.Sprintf("could not read StatefulSet %s's live volumes (%v); its volume sizes were not checked", subject, err))
			}(kctx, sts.DeepCopy(), spans)
		}
	}
}
