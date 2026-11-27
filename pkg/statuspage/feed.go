package statuspage

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The Atom feed (RFC 4287): one entry per incident and per maintenance
// window, updated whenever one of its updates is published, so a feed reader
// shows a notice again when it changes. Content is plain text (type="text"),
// so nothing in a body is markup to a reader either.

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Author  atomAuthor  `xml:"author"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
	Href string `xml:"href,attr"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

type atomEntry struct {
	ID        string   `xml:"id"`
	Title     string   `xml:"title"`
	Updated   string   `xml:"updated"`
	Published string   `xml:"published"`
	Link      atomLink `xml:"link"`
	Content   atomText `xml:"content"`
}

// feedBase is what the feed's links resolve against: the page URL with a
// trailing slash, or "" for a page with none (a local preview links
// relatively).
func feedBase(p Page) string {
	if p.URL == "" {
		return ""
	}
	return strings.TrimSuffix(p.URL, "/") + "/"
}

// feedID is a stable IRI for the feed or one of its entries: under the page
// URL when there is one, else a urn that names only the anchor.
func feedID(p Page, anchor string) string {
	if base := feedBase(p); base != "" {
		if anchor == "" {
			return base + FileFeed
		}
		return base + "#" + anchor
	}
	if anchor == "" {
		return "urn:forge-status:feed"
	}
	return "urn:forge-status:" + anchor
}

func atomTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func renderFeed(d Document) ([]byte, error) {
	base := feedBase(d.Page)
	f := atomFeed{
		ID:     feedID(d.Page, ""),
		Title:  d.Page.Title,
		Author: atomAuthor{Name: d.Page.Title},
		Links: []atomLink{
			{Rel: "self", Type: "application/atom+xml", Href: base + FileFeed},
			{Rel: "alternate", Type: "text/html", Href: base + "./"},
		},
	}
	if base != "" {
		f.Links[1].Href = base
	}
	var newest time.Time
	for _, in := range d.Incidents {
		e, at := incidentEntry(d.Page, in)
		f.Entries = append(f.Entries, e)
		if at.After(newest) {
			newest = at
		}
	}
	for _, m := range d.Maintenance {
		e, at := maintenanceEntry(d.Page, m)
		f.Entries = append(f.Entries, e)
		if at.After(newest) {
			newest = at
		}
	}
	// Newest change first, as feed readers list them.
	slices.SortStableFunc(f.Entries, func(a, b atomEntry) int {
		if c := strings.Compare(b.Updated, a.Updated); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	// The feed changes when an entry does. With no entries it carries the
	// document's time.
	if newest.IsZero() {
		newest = d.GeneratedAt
	}
	f.Updated = atomTime(newest)
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("render feed.atom: %w", err)
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func entryLink(p Page, anchor string) atomLink {
	return atomLink{Rel: "alternate", Type: "text/html", Href: feedBase(p) + "#" + anchor}
}

func incidentEntry(p Page, in Incident) (atomEntry, time.Time) {
	anchor := "incident-" + in.ID
	updated := in.StartedAt
	var b strings.Builder
	fmt.Fprintf(&b, "%s impact. Started %s", capitalized(string(in.Impact)), in.StartedAt.Format(clock))
	if in.ResolvedAt != nil {
		fmt.Fprintf(&b, ", resolved %s", in.ResolvedAt.Format(clock))
	}
	b.WriteString(".")
	if len(in.Components) > 0 {
		fmt.Fprintf(&b, " Affects %s.", strings.Join(in.Components, ", "))
	}
	for i := len(in.Updates) - 1; i >= 0; i-- {
		u := in.Updates[i]
		fmt.Fprintf(&b, "\n\n%s (%s)\n%s", capitalized(string(u.Status)), u.At.Format(clock), strings.TrimSpace(u.Body))
		if u.At.After(updated) {
			updated = u.At
		}
	}
	title := in.Title
	if in.Status == IncidentResolved {
		title = "Resolved: " + title
	}
	return atomEntry{
		ID: feedID(p, anchor), Title: title, Updated: atomTime(updated), Published: atomTime(in.StartedAt),
		Link: entryLink(p, anchor), Content: atomText{Type: "text", Body: b.String()},
	}, updated
}

func maintenanceEntry(p Page, m Maintenance) (atomEntry, time.Time) {
	anchor := "maintenance-" + m.ID
	var b strings.Builder
	fmt.Fprintf(&b, "%s maintenance, %s to %s.", capitalized(words(string(m.Status))), m.StartsAt.Format(clock), m.EndsAt.Format(clock))
	if len(m.Components) > 0 {
		fmt.Fprintf(&b, " Affects %s.", strings.Join(m.Components, ", "))
	}
	// A window is announced with its first update (Validate requires one).
	published := m.Updates[0].At
	updated := published
	for i := len(m.Updates) - 1; i >= 0; i-- {
		u := m.Updates[i]
		fmt.Fprintf(&b, "\n\n%s (%s)\n%s", capitalized(words(string(u.Status))), u.At.Format(clock), strings.TrimSpace(u.Body))
		if u.At.After(updated) {
			updated = u.At
		}
	}
	title := "Maintenance: " + m.Title
	return atomEntry{
		ID: feedID(p, anchor), Title: title, Updated: atomTime(updated), Published: atomTime(published),
		Link: entryLink(p, anchor), Content: atomText{Type: "text", Body: b.String()},
	}, updated
}
