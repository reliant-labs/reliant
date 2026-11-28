// Copyright (c) 2025 Reliant Labs
package serveraction

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// Spec declares one server action: what a workflow names and what it may
// pass. It carries no implementation, so every process that validates a
// workflow can hold it.
type Spec struct {
	// Name is the canonical reference, "reliant/<ns>.<verb>@<major>".
	Name string
	// Description is one line for the people reading a workflow and the
	// activation disclosure.
	Description string
	// Params is the JSON Schema of the node's `with:`, an object schema; nil
	// accepts any params. Validation checks literal values against it
	// statically, and the dispatcher checks the rendered values before the
	// handler runs.
	Params map[string]any
	// Mutates is true for an action that changes state (a verdict, an
	// enqueue), false for one that only reads or decides (pr.next). The
	// activation disclosure says which.
	Mutates bool
}

// ErrUnknown is a well-formed reliant/ name that no Spec declares.
var ErrUnknown = errors.New("unknown server action")

// Catalog is a set of Specs by name.
type Catalog struct {
	specs   map[string]Spec
	schemas map[string]*jsonschema.Resolved
}

// NewCatalog builds a catalog, refusing a malformed or duplicate name and a
// params schema that does not compile.
func NewCatalog(specs ...Spec) (*Catalog, error) {
	c := &Catalog{specs: make(map[string]Spec, len(specs)), schemas: make(map[string]*jsonschema.Resolved, len(specs))}
	for _, spec := range specs {
		name, err := ParseName(spec.Name)
		if err != nil {
			return nil, err
		}
		if name.String() != spec.Name {
			return nil, fmt.Errorf("server action %q must be declared by its canonical name %q", spec.Name, name)
		}
		if _, dup := c.specs[spec.Name]; dup {
			return nil, fmt.Errorf("server action %q is declared twice", spec.Name)
		}
		if spec.Params != nil {
			schema, err := compileSchema(spec.Params)
			if err != nil {
				return nil, fmt.Errorf("server action %q: params schema: %w", spec.Name, err)
			}
			c.schemas[spec.Name] = schema
		}
		c.specs[spec.Name] = spec
	}
	return c, nil
}

var builtin = sync.OnceValues(func() (*Catalog, error) { return NewCatalog(builtinSpecs()...) })

// Builtin is the catalog of every server action Reliant ships. It is static,
// so validation in any process resolves a name the way the worker will.
func Builtin() *Catalog {
	c, err := builtin()
	if err != nil {
		// builtinSpecs is code, and TestBuiltinCatalogCompiles holds it valid.
		panic(fmt.Sprintf("serveraction: the builtin catalog is invalid: %v", err))
	}
	return c
}

// Resolve returns the Spec a `uses:` names: an error for a name outside the
// reliant/ namespace, a malformed one, or one no Spec declares (ErrUnknown).
func (c *Catalog) Resolve(uses string) (Spec, error) {
	name, err := ParseName(uses)
	if err != nil {
		return Spec{}, err
	}
	if c != nil {
		if spec, ok := c.specs[name.String()]; ok {
			return spec, nil
		}
	}
	return Spec{}, fmt.Errorf("%w %q", ErrUnknown, name.String())
}

// Names lists the declared names, sorted.
func (c *Catalog) Names() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.specs))
	for name := range c.specs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// CheckParams validates rendered params against the action's schema. A nil
// error for an action that declares none.
func (c *Catalog) CheckParams(name string, params map[string]any) error {
	if c == nil {
		return nil
	}
	schema, ok := c.schemas[name]
	if !ok {
		return nil
	}
	if params == nil {
		params = map[string]any{}
	}
	if err := schema.Validate(params); err != nil {
		return fmt.Errorf("params of %s: %s", name, strings.TrimSpace(err.Error()))
	}
	return nil
}

func compileSchema(schema map[string]any) (*jsonschema.Resolved, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return s.Resolve(nil)
}
