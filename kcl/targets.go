package kcl

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Deploy-target reflection over the EMBEDDED KCL module.
//
// ── Why this is derived, not listed ──────────────────────────────────────
//
// The set of deploy targets is a fact about kcl/schema.k, and schema.k is
// already in the binary (embed.go). Anything that restates the set — a
// []string{"OnHost", "OnCluster"}, a doc comment, a help string — is a SECOND
// copy that no compiler and no test keeps honest, and it rots on the first
// commit that adds a runtime.
//
// So nothing here names a target. The set is defined structurally:
//
//	a deploy target is a union member of a `runtime` field
//	on some schema in the embedded module.
//
// DeployTargets finds every `runtime?:` field wherever it is declared (through
// a `type X = A | B` alias too), splits its union, and resolves each member
// back to its own schema — picking up the member's docstring and fields on
// the way. Add a runtime to a union in the module and it appears in `forge
// project shapes --kind deploy-target` and in the deploy warning's hint text
// with no Go change at all. Remove one and it disappears. That is the whole
// design.
//
// The OWNING schema is discovered the same way rather than assumed: Workload,
// Frontend and ManagedDatabase each accept a different set (OnBucket is
// frontend-only, OnCluster is not a frontend runtime), and a member shared by
// several (OnHost, OnHosted) lists every owner.
//
// ── Why a line parser and not the kcl binary ─────────────────────────────
//
// `forge project shapes` is recon and must stay fast and dependency-free —
// shelling out to `kcl` would make it fail on a machine that has no kcl
// installed, which is most machines running a `go install`'d forge. The
// parser is a line scan over an in-memory FS: microseconds, no I/O, and it
// cannot be stale because there is no cache between it and the source.

// DeployTarget is one schema a `runtime` field may be set to,
// resolved from the embedded module.
type DeployTarget struct {
	// Name is the schema name, e.g. "OnBucket".
	Name string
	// File and Line locate the schema declaration inside the module
	// (e.g. "schema.k", 1851), so a reader's next step is a targeted read.
	File string
	Line int
	// Doc is the first line of the schema's """docstring""", empty when it
	// has none.
	Doc string
	// Fields are the schema's own declared fields, in source order.
	Fields []SchemaField
	// Workloads are the schemas whose `runtime` union names this target,
	// sorted. This is the load-bearing distinction between the unions: a
	// Frontend and a Workload accept different runtime sets.
	Workloads []string
}

// SchemaField is one field of a KCL schema declaration.
type SchemaField struct {
	Name string
	// Type is the declared type verbatim, including a union
	// ("GoBuild | DockerBuild | ShellBuild") or a container
	// ("[FirebaseBundleDir]", "[{str: any}]").
	Type string
	// Optional is true for `name?: T` — the field may be omitted.
	Optional bool
	// Default is the literal after `=`, empty when the field has none.
	// A field with a default is also omittable, but differently: it takes
	// the default rather than staying unset.
	Default string
}

// Required reports whether the field must be supplied — neither optional
// nor defaulted.
func (f SchemaField) Required() bool { return !f.Optional && f.Default == "" }

var (
	reSchemaDecl = regexp.MustCompile(`^schema\s+(\w+)\s*(?:\([\w\s,.]*\))?\s*:`)
	// A field declared at the schema's own indent level. Nested/indented
	// content (check bodies, continuation lines, dict literals) sits deeper
	// and is deliberately not matched.
	reSchemaField = regexp.MustCompile(`^    (\w+)(\??):\s*(.*)$`)
	reCheckBlock  = regexp.MustCompile(`^    check:\s*$`)
	reBareSchema  = regexp.MustCompile(`^\w+$`)
	// A top-level type alias: `type Runtime = OnHost | OnCompose | ...`.
	reTypeAlias = regexp.MustCompile(`^type\s+(\w+)\s*=\s*(.+?)\s*$`)
)

// targetFields are the fields whose union type is a set of deploy targets:
// every schema's `runtime` — a workload's (the `Runtime` alias), a frontend's
// (the `FrontendRuntime` alias) and a database's.
var targetFields = map[string]bool{"runtime": true}

// schemaDecl is one parsed `schema X:` block.
type schemaDecl struct {
	name   string
	file   string
	line   int
	doc    string
	fields []SchemaField
}

// DeployTargets returns every deploy target declared in the embedded module,
// sorted by name. See the package-level note above for why the set is derived
// structurally rather than listed.
func DeployTargets() ([]DeployTarget, error) {
	return deployTargetsFrom(Module)
}

// deployTargetsFrom is DeployTargets over an arbitrary FS. It exists so the
// forward-compatibility test can point the SAME parser at another branch's
// schema.k and show that new targets are picked up with no code change.
func deployTargetsFrom(fsys fs.FS) ([]DeployTarget, error) {
	schemas, unions, err := parseModule(fsys)
	if err != nil {
		return nil, err
	}

	// owners[target] = the schemas whose deploy union names it.
	owners := map[string]map[string]bool{}
	for owner, members := range unions {
		for _, m := range members {
			if owners[m] == nil {
				owners[m] = map[string]bool{}
			}
			owners[m][owner] = true
		}
	}

	out := make([]DeployTarget, 0, len(owners))
	for name, ownerSet := range owners {
		t := DeployTarget{Name: name}
		// A union member with no resolvable schema still gets reported,
		// with an empty location. Dropping it would make the listing lie
		// about what the union accepts, which is the one thing this must
		// not do.
		if s, ok := schemas[name]; ok {
			t.File, t.Line, t.Doc, t.Fields = s.file, s.line, s.doc, s.fields
		}
		for o := range ownerSet {
			t.Workloads = append(t.Workloads, o)
		}
		sort.Strings(t.Workloads)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DeployTargetsFor returns the names of the targets valid on the named
// workload schema (e.g. "Frontend"), sorted. Empty when that schema declares
// no deploy union.
//
// This is what the deploy warning's hint text is built from, so the hint
// names exactly the targets the schema actually accepts for that workload
// kind — never a literal, and never the other union's members.
func DeployTargetsFor(workloadSchema string) ([]string, error) {
	targets, err := DeployTargets()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range targets {
		for _, w := range t.Workloads {
			if w == workloadSchema {
				out = append(out, t.Name)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// parseModule scans every .k file in fsys, returning the schema declarations
// by name and the `runtime` unions by owning schema name.
func parseModule(fsys fs.FS) (map[string]schemaDecl, map[string][]string, error) {
	schemas := map[string]schemaDecl{}
	unions := map[string][]string{}
	aliases := map[string][]string{}

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".k" {
			return nil
		}
		// tiers/ is the generated wire package. Its schemas share names
		// with the root package's (StaticSite, ManagedDatabase, Workload)
		// but are never what a union here references, and a bare-name map
		// would let whichever file walked last win.
		if strings.HasPrefix(p, "tiers/") {
			return nil
		}
		src, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return readErr
		}
		parseFile(p, string(src), schemas, unions, aliases)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	// A field typed by an alias (`runtime?: Runtime`) names its members
	// through it. Aliases may be declared in any file, so they resolve
	// after the walk.
	for owner, members := range unions {
		if len(members) == 1 {
			if expanded, ok := aliases[members[0]]; ok {
				unions[owner] = expanded
				continue
			}
			delete(unions, owner)
		}
	}
	return schemas, unions, nil
}

// parseFile accumulates one .k file's schemas and runtime unions.
//
// The state machine is small but every branch is load-bearing against the
// real schema.k, which has 2,849 lines of multi-line docstrings containing
// indented prose and KCL EXAMPLES that look exactly like field declarations,
// `check:` blocks whose conditions wrap across backslash continuations, and
// comment blocks between fields.
func parseFile(file, src string, schemas map[string]schemaDecl, unions map[string][]string, aliases map[string][]string) {
	var cur *schemaDecl
	inDoc := false   // inside a """...""" docstring
	inCheck := false // past the `check:` line of the current schema
	docWanted := false

	flush := func() {
		if cur != nil {
			schemas[cur.name] = *cur
		}
		cur = nil
	}

	for i, line := range strings.Split(src, "\n") {
		lineNo := i + 1

		// A docstring body is prose, not code. It must be consumed before
		// anything else looks at the line: schema.k's docstrings embed
		// indented `forge.Service { ... }` examples and bulleted field
		// lists that the field regex would otherwise match.
		if inDoc {
			if strings.Contains(line, `"""`) {
				inDoc = false
			} else if docWanted && cur != nil && cur.doc == "" {
				if t := strings.TrimSpace(line); t != "" {
					cur.doc = t
				}
			}
			continue
		}

		if m := reTypeAlias.FindStringSubmatch(line); m != nil {
			flush()
			if members := unionMembers(m[2]); len(members) > 0 {
				aliases[m[1]] = members
			}
			continue
		}
		if m := reSchemaDecl.FindStringSubmatch(line); m != nil {
			flush()
			cur = &schemaDecl{name: m[1], file: file, line: lineNo}
			inCheck, docWanted = false, true
			continue
		}
		if cur == nil {
			continue
		}

		// A non-indented, non-blank line has left the schema body.
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(line, " ") {
			flush()
			continue
		}

		if idx := strings.Index(line, `"""`); idx >= 0 {
			rest := line[idx+3:]
			if strings.Contains(rest, `"""`) {
				// Single-line docstring: `"""One sentence."""`.
				if docWanted && cur.doc == "" {
					cur.doc = strings.TrimSpace(strings.SplitN(rest, `"""`, 2)[0])
				}
			} else {
				inDoc = true
				if docWanted && cur.doc == "" {
					cur.doc = strings.TrimSpace(rest)
				}
			}
			continue
		}
		// Only the docstring that opens the schema body counts; a later
		// `"""` (there are none today, but a field-level one would parse)
		// is not the schema's doc.
		if strings.TrimSpace(line) != "" {
			docWanted = false
		}

		if reCheckBlock.MatchString(line) {
			inCheck = true
			continue
		}
		if inCheck {
			// check bodies are conditions, not fields. They are indented
			// deeper than a field, but a wrapped continuation can land at
			// any indent, so the block is skipped wholesale.
			continue
		}
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}

		m := reSchemaField.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, optional, rest := m[1], m[2] == "?", m[3]
		typ, def := splitDefault(rest)
		cur.fields = append(cur.fields, SchemaField{
			Name:     name,
			Type:     strings.TrimSpace(typ),
			Optional: optional,
			Default:  strings.TrimSpace(def),
		})

		if targetFields[name] {
			typ = strings.TrimSpace(typ)
			if members := unionMembers(typ); len(members) > 0 {
				unions[cur.name] = members
			} else if reBareSchema.MatchString(typ) {
				// A single name: an alias to expand after the walk, or
				// dropped there if it is not one.
				unions[cur.name] = []string{typ}
			}
		}
	}
	flush()
}

// splitDefault splits `[{str: any}] = []` into type and default at the
// top-level `=`. Scanning for bracket depth matters: `[{str: any}]` and
// `{str: str}` both nest, and a naive strings.Index on "=" would also trip
// over a `==` inside a defaulted expression.
func splitDefault(rest string) (typ, def string) {
	depth := 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case '=':
			if depth != 0 {
				continue
			}
			if i+1 < len(rest) && rest[i+1] == '=' {
				i++
				continue
			}
			if i > 0 && (rest[i-1] == '!' || rest[i-1] == '<' || rest[i-1] == '>') {
				continue
			}
			return rest[:i], rest[i+1:]
		case '#':
			if depth == 0 {
				return rest[:i], ""
			}
		}
	}
	return rest, ""
}

// unionMembers splits `OnHost | OnCluster` into its member schema
// names. It returns nil for a type that is not a union of bare schema names
// — `str`, `[EnvVar]`, `{str: any}` — so a `deploy` field that is not
// polymorphic contributes no targets rather than a garbage one.
func unionMembers(typ string) []string {
	parts := strings.Split(typ, "|")
	if len(parts) < 2 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !reBareSchema.MatchString(p) {
			return nil
		}
		out = append(out, p)
	}
	return out
}
