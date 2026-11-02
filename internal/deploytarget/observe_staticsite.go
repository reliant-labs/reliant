package deploytarget

import (
	"context"
	"fmt"
	"time"
)

// Observe reports which release digest each frontend's live prefix is
// currently serving.
//
// # Why the recorded state, checked against the bucket
//
// A static site has no process to ask, so "what is running" is "what does
// the live prefix hold". The bucket cannot answer that directly: live/ is
// a SYNCED COPY of a release prefix, not a pointer at one, so listing it
// tells you the files and not which release they came from. The deploy
// path already records the answer (WriteDeployState after every deploy),
// so the digest comes from there.
//
// A recorded value alone would be a CLAIM, not an observation — it says
// what forge did last, which stops being true the moment retention, a
// bucket lifecycle rule or a human `gcloud storage rm` removes the
// archive underneath it. So the recorded digest is CONFIRMED against the
// bucket's actual release listing, and a digest whose archive is gone
// reports degraded: live/ is still serving those bytes today, but the
// archive behind it has evaporated, so no environment can be re-pointed at
// that release again.
//
// That check is also the only thing here that costs a network call, and
// it is the reason this is an observation rather than a state-file read.
func (p StaticSiteProvider) Observe(ctx context.Context, group ServiceGroup) (Observed, error) {
	out := Observed{
		ProviderID: p.Name(),
		ObservedAt: time.Now().UTC(),
		Items:      make([]ObservedItem, 0, len(group.StaticSites)),
	}
	for _, fe := range group.StaticSites {
		out.Items = append(out.Items, p.observeOne(ctx, fe, group.Env))
	}
	return out, nil
}

// observeOne resolves one frontend's live release digest and confirms the
// archive behind it still exists.
func (p StaticSiteProvider) observeOne(ctx context.Context, fe StaticSiteFrontend, env string) ObservedItem {
	st, err := ReadDeployState(p.projectDir(), p.Name(), env, fe.Name)
	if err != nil {
		return ObservedItem{
			Name:   fe.Name,
			Health: HealthUnknown,
			Detail: fmt.Sprintf("could not read recorded live release: %v", err),
		}
	}
	if st == nil || st.Tag == "" {
		return ObservedItem{
			Name:   fe.Name,
			Health: HealthAbsent,
			Detail: fmt.Sprintf("no release recorded for environment %q; this frontend has never been deployed by forge", env),
		}
	}

	archived, lerr := p.listReleaseDigests(ctx, fe.Spec)
	if lerr != nil {
		// The recorded digest is real information, so it is reported even
		// though the confirmation failed — but health stays UNKNOWN, not
		// healthy. A caller must be able to tell "live is serving X and
		// the archive is intact" from "forge believes live is serving X
		// and could not check", and the digest field alone cannot carry
		// that difference.
		return ObservedItem{
			Name:   fe.Name,
			Digest: st.Tag,
			Health: HealthUnknown,
			Detail: fmt.Sprintf("recorded live release %s, but the bucket could not be listed to confirm it: %v", st.Tag, lerr),
		}
	}

	for _, d := range archived {
		if d == st.Tag {
			return ObservedItem{
				Name:   fe.Name,
				Digest: st.Tag,
				Health: HealthHealthy,
			}
		}
	}
	return ObservedItem{
		Name:   fe.Name,
		Digest: st.Tag,
		Health: HealthDegraded,
		Detail: fmt.Sprintf(
			"live is serving release %s but no archive for it remains at %s — promoting an env to it would 404",
			st.Tag, fe.Spec.releaseURI(st.Tag)),
	}
}
