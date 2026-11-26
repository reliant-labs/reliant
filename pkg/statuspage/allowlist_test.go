package statuspage

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// publicFields is every JSON path of a forge.status/v1 document and its Go
// type: the allowlist. Every one is classified PUBLIC: a visitor of a public
// page reads it.
//
// Adding a field to a Document type fails TestDocumentFieldsArePinned until
// it is listed here. Before you list it, check it against what is NEVER
// public (control-plane#3227, forge docs/proposals/status-page.md, "What is
// public, and what never is"):
//
//   - uptime check names, URLs, paths and hostnames;
//   - HTTP statuses and error types;
//   - latency and certificate expiry;
//   - environment, organization and project ids and names;
//   - alert names and issue titles;
//   - who published an update, and whether an agent drafted it;
//   - any unpublished draft.
//
// A field that carries any of those does not belong in the document: the
// private view gets it some other way. Changing or removing a listed field
// is a break of the forge.status/v1 contract, which only a new schema may do.
var publicFields = []string{
	"schema string",
	"generated_at time.Time",
	"page.title string",
	"page.url string", // the page's own public address, never a check's
	"page.logo_url string",
	"page.accent_color string",
	"page.links[].label string",
	"page.links[].url string",
	"components[].name string",
	"components[].description string",
	"components[].state statuspage.State",
	"components[].overridden bool",
	"components[].days[].date string",
	"components[].days[].availability *float64",
	"components[].days[].maintenance bool",
	"incidents[].id string", // an opaque public id for anchors and feed entries
	"incidents[].title string",
	"incidents[].impact statuspage.Impact",
	"incidents[].status statuspage.IncidentStatus",
	"incidents[].components []string",
	"incidents[].started_at time.Time",
	"incidents[].resolved_at *time.Time",
	"incidents[].updates[].status statuspage.IncidentStatus",
	"incidents[].updates[].body string",
	"incidents[].updates[].at time.Time",
	"maintenance[].id string",
	"maintenance[].title string",
	"maintenance[].status statuspage.MaintenanceStatus",
	"maintenance[].components []string",
	"maintenance[].starts_at time.Time",
	"maintenance[].ends_at time.Time",
	"maintenance[].updates[].status statuspage.MaintenanceStatus",
	"maintenance[].updates[].body string",
	"maintenance[].updates[].at time.Time",
}

// documentFields walks t's exported fields to their leaves (a non-struct, or
// time.Time), as "json.path type".
func documentFields(t reflect.Type, prefix string) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		path := prefix + name
		ft := f.Type
		switch {
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			out = append(out, documentFields(ft.Elem(), path+"[].")...)
		case ft.Kind() == reflect.Struct && ft != reflect.TypeFor[time.Time]():
			out = append(out, documentFields(ft, path+".")...)
		default:
			out = append(out, path+" "+ft.String())
		}
	}
	return out
}

// TestDocumentFieldsArePinned: the document carries exactly the classified
// fields, so nothing becomes public by being added to a struct.
func TestDocumentFieldsArePinned(t *testing.T) {
	got := documentFields(reflect.TypeFor[Document](), "")
	for _, f := range got {
		if !slices.Contains(publicFields, f) {
			t.Errorf("Document field %q is not classified. Check it against the never-public list in allowlist_test.go, then add it to publicFields", f)
		}
	}
	for _, f := range publicFields {
		if !slices.Contains(got, f) {
			t.Errorf("publicFields lists %q, which the Document no longer has: changing a forge.status/v1 field breaks every reader of status.json", f)
		}
	}
}
