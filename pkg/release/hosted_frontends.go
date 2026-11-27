package release

import (
	"fmt"
	"sort"
	"strings"
)

// ShapeHostedFrontend is what an env declares about one of its frontends on
// platform static hosting (forge.OnHosted) beyond the StaticSite the release
// applies: serving policy the control plane reads from the env's shape, not
// from the site's spec, because it is the env's decision rather than the
// build's.
//
// An entry is recorded only when it differs from the default, so a frontend
// that declares nothing has none, and every shape recorded before the field
// encodes and hashes exactly as it did.
type ShapeHostedFrontend struct {
	// Frontend is the frontend's name, which is also its StaticSite's.
	Frontend string `json:"frontend"`
	// ReleaseURLs is forge.Frontend.release_urls: whether the platform serves
	// each release the env still retains at a URL of its own, on the preview
	// domain (noindex, unguessable, gone once the release is collected). Nil
	// is the default, on; false opts the env's frontend out.
	ReleaseURLs *bool `json:"release_urls,omitempty"`
}

// IsDefault reports whether f declares nothing a reader would act on.
func (f ShapeHostedFrontend) IsDefault() bool {
	return f.ReleaseURLs == nil || *f.ReleaseURLs
}

// ReleaseURLsDisabled names the hosted frontends whose per-release URLs the env
// turned off (forge.Frontend.release_urls = False), sorted. Every other hosted
// frontend gets them.
func (s Shape) ReleaseURLsDisabled() []string {
	var out []string
	for _, f := range s.HostedFrontends {
		if f.ReleaseURLs != nil && !*f.ReleaseURLs {
			out = append(out, f.Frontend)
		}
	}
	sort.Strings(out)
	return out
}

// canonicalHostedFrontends drops the entries that declare only defaults and
// sorts the rest by frontend, so a producer that spells the default out hashes
// the same as one that declares nothing.
func canonicalHostedFrontends(in []ShapeHostedFrontend) []ShapeHostedFrontend {
	var out []ShapeHostedFrontend
	for _, f := range in {
		if f.IsDefault() {
			continue
		}
		off := false
		out = append(out, ShapeHostedFrontend{Frontend: f.Frontend, ReleaseURLs: &off})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Frontend < out[j].Frontend })
	return out
}

func validateHostedFrontends(in []ShapeHostedFrontend) error {
	seen := map[string]bool{}
	for _, f := range in {
		if strings.TrimSpace(f.Frontend) == "" {
			return fmt.Errorf("%w: shape hosted_frontends has an entry with no frontend", ErrInvalid)
		}
		if seen[f.Frontend] {
			return fmt.Errorf("%w: shape hosted_frontends names frontend %q twice", ErrInvalid, f.Frontend)
		}
		seen[f.Frontend] = true
	}
	return nil
}

// CheckReleaseURLs refuses a shape that turns per-release URLs off to a reader
// that serves them but cannot read the opt-out: one that advertises
// [CapabilityServesReleaseURLs] without [BundleFeatureReleaseURLs] would be
// sent the shape without Shape.HostedFrontends (LimitShape) and serve every
// retained release of a frontend whose env said not to. A reader that serves
// no release URLs has nothing to opt out of, and is sent the shape without the
// key. No opt-out, and the nil reader (this forge's own machine ledger),
// always pass. Callers run it before anything is written.
func (r *BundleReader) CheckReleaseURLs(s Shape) error {
	off := s.ReleaseURLsDisabled()
	if len(off) == 0 || r.Supports(BundleFeatureReleaseURLs) || !r.Supports(CapabilityServesReleaseURLs) {
		return nil
	}
	return fmt.Errorf("frontend %s sets release_urls = False, and this control plane serves per-release URLs "+
		"(it advertises %q) but cannot read the opt-out (it does not advertise %q), so it would serve them anyway. "+
		"Upgrade the control plane before deploying this env",
		strings.Join(off, ", "), CapabilityServesReleaseURLs, BundleFeatureReleaseURLs)
}
