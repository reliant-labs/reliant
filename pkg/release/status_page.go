package release

import (
	"fmt"
	"slices"

	"github.com/reliant-labs/forge/pkg/statuspage"
)

// ShapeStatusPage is the env's declared status page (forge.StatusPage). It is
// opt-in only: an env that declares none has a nil [Shape.StatusPage] and no
// status_page key, and nothing is rendered or published for it. It says what
// a public page of this env shows, and which of the env's uptime checks each
// component is made of. The control plane reads it from the recorded shape
// and builds the page's forge.status/v1 document (pkg/statuspage) from the
// checks' probes.
//
// Declaring a page does not publish it. Going public is a separate, audited
// act by a person in the control plane, so a rollback can neither publish nor
// unpublish a page.
// Check names are here because the control plane needs them to read the
// probes; they never reach the public document, which names components only.
type ShapeStatusPage struct {
	// Title names the page.
	Title string `json:"title"`
	// LogoURL is an https image shown beside the title.
	LogoURL string `json:"logo_url,omitempty"`
	// AccentColor is a #rrggbb colour for the page's decoration.
	AccentColor string `json:"accent_color,omitempty"`
	// Links are shown in the page header, in declaration order.
	Links []ShapeStatusLink `json:"links,omitempty"`
	// Components are the page's rows, in display order.
	Components []ShapeStatusComponent `json:"components"`
}

// ShapeStatusLink is one header link of a status page.
type ShapeStatusLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ShapeStatusComponent is one row of a status page: a public name for one or
// more of the env's uptime checks. Its state is the worst of theirs.
type ShapeStatusComponent struct {
	// Name is the public name, unique within the page.
	Name string `json:"name"`
	// Description is a short line under the name.
	Description string `json:"description,omitempty"`
	// Checks name [ShapeUptimeCheck]s of the same shape, sorted. At least one.
	Checks []string `json:"checks"`
}

// page is the presentation part, in the document's own type, so the shape
// and the document are held to one set of rules.
func (p ShapeStatusPage) page() statuspage.Page {
	out := statuspage.Page{Title: p.Title, LogoURL: p.LogoURL, AccentColor: p.AccentColor}
	for _, l := range p.Links {
		out.Links = append(out.Links, statuspage.Link{Label: l.Label, URL: l.URL})
	}
	return out
}

// validateStatusPage checks a declared page against the forge.status/v1
// rules and against the shape's own checks: every check a component names
// must be one of them.
func validateStatusPage(p *ShapeStatusPage, checks []ShapeUptimeCheck) error {
	if p == nil {
		return nil
	}
	if err := p.page().Validate(); err != nil {
		return fmt.Errorf("%w: status page: %v", ErrInvalid, err)
	}
	if len(p.Components) == 0 {
		return fmt.Errorf("%w: status page has no components", ErrInvalid)
	}
	if len(p.Components) > statuspage.MaxComponents {
		return fmt.Errorf("%w: status page has %d components, more than %d", ErrInvalid, len(p.Components), statuspage.MaxComponents)
	}
	have := map[string]bool{}
	for _, c := range checks {
		have[c.Name] = true
	}
	names := map[string]bool{}
	for _, c := range p.Components {
		if err := statuspage.ValidateComponentName(c.Name); err != nil {
			return fmt.Errorf("%w: status page: %v", ErrInvalid, err)
		}
		if err := statuspage.ValidateDescription(c.Description); err != nil {
			return fmt.Errorf("%w: status page component %q: %v", ErrInvalid, c.Name, err)
		}
		if names[c.Name] {
			return fmt.Errorf("%w: status page names component %q twice", ErrInvalid, c.Name)
		}
		names[c.Name] = true
		if len(c.Checks) == 0 {
			return fmt.Errorf("%w: status page component %q names no uptime check", ErrInvalid, c.Name)
		}
		seen := map[string]bool{}
		for _, n := range c.Checks {
			if seen[n] {
				return fmt.Errorf("%w: status page component %q names check %q twice", ErrInvalid, c.Name, n)
			}
			seen[n] = true
			if !have[n] {
				return fmt.Errorf("%w: status page component %q names uptime check %q, which the shape does not have", ErrInvalid, c.Name, n)
			}
		}
	}
	return nil
}

// canonicalStatusPage is p with each component's checks sorted and empty
// lists omitted. Components and links keep their order: it is the page's.
// A copy: p is not modified.
func canonicalStatusPage(p *ShapeStatusPage) *ShapeStatusPage {
	if p == nil {
		return nil
	}
	out := *p
	out.Links = slices.Clone(p.Links)
	if len(out.Links) == 0 {
		out.Links = nil
	}
	out.Components = make([]ShapeStatusComponent, len(p.Components))
	for i, c := range p.Components {
		c.Checks = sortedStrings(c.Checks)
		out.Components[i] = c
	}
	return &out
}
