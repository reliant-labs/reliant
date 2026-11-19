package cli

// `forge env status <env>`'s release half for forge.OnExternal workloads.
//
// The release half verifies every image the binding pins against what is
// RUNNING. A cluster env is read off the cluster; an external workload runs
// wherever the author's tooling put it, so the only witness is the binding's
// `status` hook. Its images are split off before the cluster read and judged
// by the hook — and an env whose workloads are all external never touches a
// cluster at all.
//
// A workload with no status hook is UNREACHABLE (exit 2): forge could not
// look, and "could not look" is never folded into a match.

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// envExternalResolver is the optional resolver seam that names the env's
// forge.OnExternal workloads. A resolver without it has none (every test
// resolver predating the runtime), so their envs read exactly as before.
type envExternalResolver interface {
	ExternalWorkloads(ctx context.Context, projectDir, envName string) []WorkloadEntity
}

// envStatusRenderMemo shares one render of an env between the resolver's methods,
// so adding the external split costs no second evaluation of the KCL.
type envStatusRenderMemo struct {
	mu   sync.Mutex
	done map[string]*KCLEntities
}

func (m *envStatusRenderMemo) render(ctx context.Context, projectDir, envName string) *KCLEntities {
	if m == nil {
		e, err := RenderKCL(ctx, projectDir, envName)
		if err != nil {
			return nil
		}
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := projectDir + "\x00" + envName
	if e, ok := m.done[key]; ok {
		return e
	}
	e, err := RenderKCL(ctx, projectDir, envName)
	if err != nil {
		e = nil
	}
	if m.done == nil {
		m.done = map[string]*KCLEntities{}
	}
	m.done[key] = e
	return e
}

// ExternalWorkloads implements envExternalResolver. A render that fails names
// none: the cluster path then reports what it always did.
func (r kclTargetResolver) ExternalWorkloads(ctx context.Context, projectDir, envName string) []WorkloadEntity {
	e := r.memo.render(ctx, projectDir, envName)
	if e == nil {
		return nil
	}
	return e.WorkloadsOn(RuntimeExternal)
}

// splitExternalImages takes the binding's images that belong to external
// workloads out of resolved, verifies each through its workload's status
// hook, and returns those verifications with the images left for the cluster.
func splitExternalImages(ctx context.Context, projectDir, envName string, resolved map[string]string, opts envStatusOptions) (external []imageVerification, rest map[string]string) {
	er, ok := opts.Resolver.(envExternalResolver)
	if !ok {
		return nil, resolved
	}
	workloads := er.ExternalWorkloads(ctx, projectDir, envName)
	if len(workloads) == 0 {
		return nil, resolved
	}
	rest = map[string]string{}
	keys := make([]string, 0, len(resolved))
	for k := range resolved {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	hooks := opts.ExternalHooks
	for _, key := range keys {
		w, found := externalWorkloadFor(workloads, key)
		if !found {
			rest[key] = resolved[key]
			continue
		}
		external = append(external, verifyExternalImage(ctx, hooks, envName, w, key, resolved[key]))
	}
	return external, rest
}

// externalWorkloadFor is the external workload whose image is the binding's
// key: the same repository, or (a legacy bare-name key) the same last segment.
func externalWorkloadFor(workloads []WorkloadEntity, key string) (WorkloadEntity, bool) {
	k := parseImageRef(key)
	for _, w := range workloads {
		ref := parseImageRef(w.Spec.Image)
		if ref.Repository == "" {
			ref = parseImageRef(w.Image)
		}
		if ref.Repository == "" {
			continue
		}
		if ref.Repository == k.Repository || (k.Repository == k.Name && ref.Name == k.Name) {
			return w, true
		}
	}
	return WorkloadEntity{}, false
}

func verifyExternalImage(ctx context.Context, hooks deploytarget.ExternalHooks, envName string, w WorkloadEntity, image, declared string) imageVerification {
	v := imageVerification{Image: image, Declared: declared, Workloads: []string{"external/" + w.Name}}
	if w.Runtime.External == nil || w.Runtime.External.Status == nil {
		v.State = imageUnreachable
		v.Detail = "forge.OnExternal declares no status hook, so forge cannot read what " + w.Name + " runs"
		return v
	}
	spec := externalSpecFor(envName, w, nil, nil, nil)
	spec.Image = parseImageRef(image).Repository + "@" + declared
	st, err := hooks.Status(ctx, *spec)
	switch {
	case err != nil:
		v.State, v.Detail = imageUnreachable, err.Error()
	case st.Digest == "":
		v.State, v.Detail = imageUnreachable, "the status hook reported no digest"
	case st.Digest == declared:
		v.State, v.Running = imageMatch, st.Digest
	default:
		v.State, v.Running = imageDrift, st.Digest
		v.Detail = fmt.Sprintf("the status hook reports %s running", shortDigest(st.Digest))
	}
	return v
}
