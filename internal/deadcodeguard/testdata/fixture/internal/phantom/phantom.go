// Package phantom plants one instance of every shape the phantom-field rule
// must judge, and one of every shape it must leave alone.
package phantom

import "sync"

// Component reproduces the real defect: config.ComponentConfig.Ports was read
// by PrimaryPort and by the deploy-data emitter, and written by nothing but
// tests, so every consumer branched on a hard zero.
type Component struct {
	Name string
	// WANT phantom-field: read below, written only from the test file.
	Ports map[string]int
	// WANT phantom-field: read below, written nowhere at all.
	Schedule string
	// OK: written by NewComponent.
	Kind string
}

// PrimaryPort is the real shape of the defect — a lookup whose answer is
// pinned to zero because nothing ever fills the map it reads.
func (c Component) PrimaryPort() int {
	if c.Ports == nil {
		return 0
	}
	return c.Ports["http"]
}

// Describe reads Schedule, which nothing writes.
func (c Component) Describe() string {
	if c.Schedule != "" {
		return c.Name + " @ " + c.Schedule
	}
	return c.Name
}

// NewComponent writes Name and Kind and nothing else.
func NewComponent(name string) *Component {
	c := &Component{Name: name}
	c.Kind = "server"
	return c
}

// Tagged is a serialization target. Decoders fill Label by reflection, which
// no call graph can see, so no field of this struct may be judged.
type Tagged struct {
	Label string `json:"label"`
	Other int    `json:"other"`
}

// Label is read and never written in Go — the json decoder is the writer.
func (t Tagged) Describe() string { return t.Label }

// Seams holds the shapes that look phantom but are not.
type Seams struct {
	// OK: a func-typed field is an injection seam; nil means "default".
	Runner func() error
	// OK: an interface-typed field is the same.
	Sink interface{ Write([]byte) (int, error) }
	// OK: a mutex is used, never assigned — Lock() has a pointer receiver.
	mu sync.Mutex
	// OK: written positionally by NewSeams' unkeyed literal.
	Positional string
	guarded    int
}

// Use exercises the seam fields so they are read at least once.
func (s *Seams) Use() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Runner != nil {
		_ = s.Runner()
	}
	if s.Sink != nil {
		_, _ = s.Sink.Write(nil)
	}
	return s.Positional, s.guarded
}

// NewSeams writes Positional and guarded through an UNKEYED literal, which the
// rule must count as a write exactly like a keyed one.
func NewSeams() *Seams {
	return &Seams{nil, nil, sync.Mutex{}, "positional", 7}
}

// ── Look-alike: written only THROUGH a subfield ──────────────────────────────
//
// This is the real ProjectGenerator.Features shape, and it must NOT be
// reported. Nothing assigns Flags wholesale; production mutates it by writing
// one of its subfields. Assigning through a chain mutates every struct along
// that chain, so `g.Flags.Verbose = &b` is a WRITE of Flags, not a read of it.
//
// Getting this wrong reported a field that dozens of production sites read and
// that production really does write, which would have led to deleting working
// code — and a rule that cries wolf gets weakened until it protects nothing.
type Flags struct {
	Verbose *bool
	Quiet   *bool
}

type Runner struct {
	Name string
	// OK: never assigned wholesale, but written through in applyDefaults below.
	Flags Flags
}

// applyDefaults is the production writer — through the field, not to it.
func (r *Runner) applyDefaults() {
	off := func() *bool { b := false; return &b }
	if r.Flags.Verbose == nil {
		r.Flags.Verbose = off()
	}
	if r.Flags.Quiet == nil {
		r.Flags.Quiet = off()
	}
}

// Loud reads Flags, the same way production reads it in dozens of places.
func (r Runner) Loud() bool {
	r2 := r
	r2.applyDefaults()
	return r2.Flags.Verbose != nil && *r2.Flags.Verbose
}

// ── Look-alike: a value-typed cache, mutated only in place ───────────────────
//
// The real cloud.Client.elevated shape. Nothing assigns `cached` wholesale: its
// zero value is ready to use, the mutex is locked through it and the map is
// filled through it. Every one of those is a WRITE of `cached`.
//
// It was reported anyway, because a use was deduplicated by its file offset
// alone. `c.cached.mu` and `c.cached` start at the same offset (the `c`), so
// whichever selector was recorded first swallowed the other: the write of
// `entries` hid the write of `cached`, and the read left over was all the
// guard saw.
type entryCache struct {
	mu      sync.Mutex
	entries map[string]string
}

type Client struct {
	// OK: written through `c.cached.entries = …` and mutated by mu.Lock().
	cached entryCache
}

// Lookup is the only production code that touches cached.
func (c *Client) Lookup(key string) string {
	c.cached.mu.Lock()
	defer c.cached.mu.Unlock()
	if c.cached.entries == nil {
		c.cached.entries = map[string]string{}
	}
	if v, ok := c.cached.entries[key]; ok {
		return v
	}
	c.cached.entries[key] = key
	return key
}

// ── Planted: a field read only THROUGH a chain, written nowhere ──────────────
//
// The same offset collision in the other direction. `o.Inner.Value` records the
// read of Value and then dropped the read of Inner as a duplicate, so a field
// whose every read goes through a subfield was never seen read at all — a
// phantom the rule could not report.
type Leaf struct {
	// OK: written by NewLeaf's keyed literal.
	Value string
}

// NewLeaf writes Value.
func NewLeaf(v string) Leaf { return Leaf{Value: v} }

type Outer struct {
	// WANT phantom-field: read below through .Value, written nowhere.
	Inner Leaf
}

// Show reads Inner — and only through its subfield.
func (o Outer) Show() string { return o.Inner.Value }
