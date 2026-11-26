package servicecatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/token"
	"io"
	"path"
	"strings"
)

// Decode validates catalog bytes before a browser/hosted ingest consumes them.
// It has no size bound of its own: a catalog grows with the project it
// describes, and the bytes are already in memory, so the bound belongs to the
// reader that fetched them (a bundle layer's total, a registry blob read).
// It authenticates no provenance: callers must verify the containing OCI
// bundle digest and apply their existing project/environment authorization.
//
// It accepts either Source form: the committed one (path + symbol) and the
// located one a bundle carries (path + line). DecodeLocated accepts only the
// latter.
func Decode(raw []byte) (Catalog, error) {
	var c Catalog
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("service catalog: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("service catalog: trailing data")
	}
	if c.Schema != Schema {
		return c, fmt.Errorf("unsupported service catalog schema %q", c.Schema)
	}
	var bad string
	c.eachSource(func(kind string, s *Source) {
		if bad == "" && !validSource(*s) {
			bad = kind
		}
	})
	if bad != "" {
		return c, fmt.Errorf("invalid %s source", bad)
	}
	for _, v := range c.Clients {
		if v.Resolution == "declared" && (!protoRef(v.Service) || !protoRef(v.ConfigField)) {
			return c, fmt.Errorf("invalid client metadata")
		}
	}
	return c, nil
}

// DecodeLocated is Decode for a bundle's copy: every source must be located
// (path + line) or unknown, never symbolic. A bundle's readers decode the v1
// Source as {path, line} and refuse any other key, so a symbolic source that
// reached one would void the whole catalog.
func DecodeLocated(raw []byte) (Catalog, error) {
	c, err := Decode(raw)
	if err != nil {
		return c, err
	}
	var bad string
	c.eachSource(func(kind string, s *Source) {
		if bad == "" && s.Symbol != "" {
			bad = kind
		}
	})
	if bad != "" {
		return c, fmt.Errorf("unlocated %s source: a bundle's catalog carries lines, not symbols (servicecatalog.Locate)", bad)
	}
	return c, nil
}

// validSource reports whether s is the unknown Source, a committed one (a safe
// module-relative path naming a symbol), or a located one (that path and a
// positive line).
func validSource(s Source) bool {
	if s.Path == "" {
		return s.Symbol == "" && s.Line == 0
	}
	if strings.ContainsAny(s.Path, "\\:\x00") || strings.HasPrefix(s.Path, "/") || path.Clean(s.Path) != s.Path || s.Path == "." || s.Path == ".." || strings.HasPrefix(s.Path, "../") {
		return false
	}
	if s.Symbol != "" {
		return s.Line == 0 && validSymbol(s.Symbol)
	}
	return s.Line > 0
}

// validSymbol accepts "Name" and "Type.Name", each part a Go identifier —
// Unicode letters included, as Go allows (protoRef stays ASCII: protobuf
// names are).
func validSymbol(sym string) bool {
	parts := strings.Split(sym, ".")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if !token.IsIdentifier(p) {
			return false
		}
	}
	return true
}

func identifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// eachSource calls fn with every Source in c, and what holds it.
func (c *Catalog) eachSource(fn func(kind string, s *Source)) {
	methods := func(kind string, ms []CodeMethod) {
		for i := range ms {
			fn(kind, &ms[i].Source)
		}
	}
	for i := range c.Processes {
		fn("process", &c.Processes[i].Source)
	}
	for i := range c.Components {
		v := &c.Components[i]
		fn("component", &v.Source)
		methods("method", v.Methods)
		for j := range v.ImplementationCandidates {
			impl := &v.ImplementationCandidates[j]
			fn("implementation", &impl.Source)
			methods("implementation method", impl.Methods)
		}
	}
	for i := range c.Bindings {
		methods("interface", c.Bindings[i].Methods)
	}
	for i := range c.Clients {
		fn("client", &c.Clients[i].Source)
	}
	for i := range c.Diagnostics {
		fn("diagnostic", &c.Diagnostics[i].Source)
	}
}

func protoRef(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !identifier(p) {
			return false
		}
	}
	return true
}
