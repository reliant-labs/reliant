package metrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
)

// SchemaVersion identifies the shape of Schema. Consumers refuse a version
// they do not know.
const SchemaVersion = "forge.metrics/v1"

// Schema is the machine-readable declaration of every metric a service emits.
// It is what `<bin> metrics schema --json`, GET /debug/metrics/schema, the
// release bundle's observability/metrics-schema.json and
// `forge obs metrics declared` print.
type Schema struct {
	SchemaVersion string `json:"schema_version"`
	// Service is the service.name the metrics are reported under.
	Service string `json:"service"`
	// Version is the service.version of the build; "dev" for an unstamped one.
	Version string `json:"version"`
	Metrics []Spec `json:"metrics"`
}

// Schema returns the registry's schema for a service and build version. Metrics
// are sorted by name and every attribute list is non-nil, so the encoding is
// deterministic and a consumer can range over it without nil checks.
func (r *Registry) Schema(service, version string) Schema {
	specs := r.Declared()
	for i := range specs {
		if specs[i].Attributes == nil {
			specs[i].Attributes = []Attribute{}
		}
	}
	return Schema{SchemaVersion: SchemaVersion, Service: service, Version: version, Metrics: specs}
}

// MarshalJSON encodes a Spec exactly as the schema spells it: no Buckets, and
// `values` only on an attribute that enumerates them.
func (s Spec) MarshalJSON() ([]byte, error) {
	type wire struct {
		Name        string      `json:"name"`
		Type        Type        `json:"type"`
		Unit        string      `json:"unit"`
		Description string      `json:"description"`
		Attributes  []Attribute `json:"attributes"`
		Stability   Stability   `json:"stability"`
		Since       string      `json:"since"`
	}
	attrs := s.Attributes
	if attrs == nil {
		attrs = []Attribute{}
	}
	return json.Marshal(wire{s.Name, s.Type, s.Unit, s.Description, attrs, s.Stability, s.Since})
}

// WriteJSON writes the schema as indented JSON followed by a newline.
func (s Schema) WriteJSON(w io.Writer) error {
	b, err := s.JSON()
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// JSON is the schema's canonical encoding: indented, newline-terminated, no
// HTML escaping.
func (s Schema) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseSchema decodes and checks a schema document.
func ParseSchema(b []byte) (Schema, error) {
	var s Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return Schema{}, fmt.Errorf("decode metrics schema: %w", err)
	}
	if s.SchemaVersion != SchemaVersion {
		return Schema{}, fmt.Errorf("metrics schema version %q is not %q", s.SchemaVersion, SchemaVersion)
	}
	return s, nil
}

// SchemaPath is where the schema is served on the debug listener.
const SchemaPath = "/debug/metrics/schema"

// SchemaHandler serves the registry's schema as JSON. It reads the registry on
// every request, so a metric declared after startup is listed.
func SchemaHandler(r *Registry, service, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b, err := r.Schema(service, version).JSON()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
}

// WriteTable writes the schema as a human-readable table, for `<bin> metrics
// schema` without --json.
func (s Schema) WriteTable(w io.Writer) error {
	fmt.Fprintf(w, "%s %s (%s)\n\n", s.Service, s.Version, s.SchemaVersion)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tUNIT\tSTABILITY\tSINCE\tATTRIBUTES")
	for _, m := range s.Metrics {
		keys := make([]string, 0, len(m.Attributes))
		for _, a := range m.Attributes {
			k := a.Key
			if len(a.Values) > 0 {
				k += "=" + strings.Join(a.Values, "|")
			}
			keys = append(keys, k)
		}
		attrs := "-"
		if len(keys) > 0 {
			attrs = strings.Join(keys, ", ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, m.Type, m.Unit, m.Stability, m.Since, attrs)
	}
	return tw.Flush()
}
